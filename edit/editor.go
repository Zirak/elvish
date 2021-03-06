// Package edit implements a command line editor.
package edit

import (
	"fmt"
	"os"
	"syscall"

	"github.com/elves/elvish/eval"
	"github.com/elves/elvish/parse"
	"github.com/elves/elvish/store"
	"github.com/elves/elvish/sys"
	"github.com/elves/elvish/util"
)

var Logger = util.GetLogger("[edit] ")

const (
	lackEOLRune = '\u23ce'
	lackEOL     = "\033[7m" + string(lackEOLRune) + "\033[m"
)

// Editor keeps the status of the line editor.
type Editor struct {
	file   *os.File
	writer *writer
	reader *Reader
	sigs   chan os.Signal
	store  *store.Store
	evaler *eval.Evaler
	cmdSeq int
	editorState
}

type editorState struct {
	// States used during ReadLine. Reset at the beginning of ReadLine.
	active                bool
	savedTermios          *sys.Termios
	tokens                []Token
	prompt, rprompt, line string
	dot                   int
	notifications         []string
	tips                  []string
	mode                  bufferMode
	completion            *completion
	completionLines       int
	navigation            *navigation
	history               historyState
	historyListing        *historyListing
	isExternal            map[string]bool
	// Used for builtins.
	lastKey    Key
	nextAction action
}

type bufferMode int

const (
	modeInsert bufferMode = iota
	modeCommand
	modeCompletion
	modeNavigation
	modeHistory
	modeHistoryListing
)

type actionType int

const (
	noAction actionType = iota
	reprocessKey
	exitReadLine
)

// LineRead is the result of ReadLine. Exactly one member is non-zero, making
// it effectively a tagged union.
type LineRead struct {
	Line string
	EOF  bool
	Err  error
}

// NewEditor creates an Editor.
func NewEditor(file *os.File, sigs chan os.Signal, ev *eval.Evaler, st *store.Store) *Editor {
	seq := -1
	if st != nil {
		var err error
		seq, err = st.NextCmdSeq()
		if err != nil {
			// TODO(xiaq): Also report the error
			seq = -1
		}
	}

	ed := &Editor{
		file:   file,
		writer: newWriter(file),
		reader: NewReader(file),
		sigs:   sigs,
		store:  st,
		evaler: ev,
		cmdSeq: seq,
	}
	ev.AddModule("le", makeModule(ed))
	return ed
}

func (ed *Editor) flash() {
	// TODO implement fish-like flash effect
}

func (ed *Editor) addTip(format string, args ...interface{}) {
	ed.tips = append(ed.tips, fmt.Sprintf(format, args...))
}

func (ed *Editor) notify(format string, args ...interface{}) {
	ed.notifications = append(ed.notifications, fmt.Sprintf(format, args...))
}

func (ed *Editor) refresh(fullRefresh bool, tips bool) error {
	// Re-lex the line, unless we are in modeCompletion
	src := ed.line
	if ed.mode != modeCompletion {
		n, err := parse.Parse(src)
		if err != nil {
			// If all the errors happen at the end, it is liekly complaining about missing texts that will eventually be inserted. Don't show such errors.
			// XXX We may need a more reliable criteria.
			if tips && !atEnd(err, len(src)) {
				ed.addTip("parser error: %s", err)
			}
		}
		if n == nil {
			ed.tokens = []Token{{ParserError, src, nil, ""}}
		} else {
			ed.tokens = tokenize(src, n)
			_, err := ed.evaler.Compile(n)
			if err != nil {
				if tips && !atEnd(err, len(src)) {
					ed.addTip("compiler error: %s", err)
				}
				if err, ok := err.(*util.PosError); ok {
					p := err.Begin
					for i, token := range ed.tokens {
						if token.Node.Begin() <= p && p < token.Node.End() {
							ed.tokens[i].MoreStyle += styleForCompilerError
							break
						}
					}
				}
			}
		}
		for i, t := range ed.tokens {
			for _, stylist := range stylists {
				ed.tokens[i].MoreStyle += stylist(t.Node, ed)
			}
		}
	}
	return ed.writer.refresh(&ed.editorState, fullRefresh)
}

func atEnd(e error, n int) bool {
	switch e := e.(type) {
	case *util.PosError:
		return e.Begin == n
	case *util.Errors:
		for _, child := range e.Errors {
			if !atEnd(child, n) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// acceptCompletion accepts currently selected completion candidate.
func (ed *Editor) acceptCompletion() {
	c := ed.completion
	if 0 <= c.current && c.current < len(c.candidates) {
		accepted := c.candidates[c.current].source.text
		ed.insertAtDot(accepted)
	}
	ed.completion = nil
	ed.mode = modeInsert
}

// insertAtDot inserts text at the dot and moves the dot after it.
func (ed *Editor) insertAtDot(text string) {
	ed.line = ed.line[:ed.dot] + text + ed.line[ed.dot:]
	ed.dot += len(text)
}

func setupTerminal(file *os.File) (*sys.Termios, error) {
	fd := int(file.Fd())
	term, err := sys.NewTermiosFromFd(fd)
	if err != nil {
		return nil, fmt.Errorf("can't get terminal attribute: %s", err)
	}

	savedTermios := term.Copy()

	term.SetICanon(false)
	term.SetEcho(false)
	term.SetVMin(1)
	term.SetVTime(0)

	err = term.ApplyToFd(fd)
	if err != nil {
		return nil, fmt.Errorf("can't set up terminal attribute: %s", err)
	}

	/*
		err = sys.FlushInput(fd)
		if err != nil {
			return nil, fmt.Errorf("can't flush input: %s", err)
		}
	*/

	return savedTermios, nil
}

// startReadLine prepares the terminal for the editor.
func (ed *Editor) startReadLine() error {
	savedTermios, err := setupTerminal(ed.file)
	if err != nil {
		return err
	}
	ed.savedTermios = savedTermios

	_, width := sys.GetWinsize(int(ed.file.Fd()))
	// Turn on autowrap, write lackEOL along with enough padding to fill the
	// whole screen. If the cursor was in the first column, we end up in the
	// same line (just off the line boundary); otherwise we are now in the next
	// line. We now rewind to the first column and erase anything there. The
	// final effect is that a lackEOL gets written if and only if the cursor
	// was not in the first column.
	fmt.Fprintf(ed.file, "\033[?7h%s%*s\r \r", lackEOL, width-WcWidth(lackEOLRune), "")

	// Turn off autowrap. The edito has its own wrapping mechanism. Doing
	// wrapping manually means that when the actual width of some characters
	// are greater than what our wcwidth implementation tells us, characters at
	// the end of that line gets hidden -- compared to pushed to the next line,
	// which is more disastrous.
	ed.file.WriteString("\033[?7l")
	// Turn on SGR-style mouse tracking.
	//ed.file.WriteString("\033[?1000;1006h")
	return nil
}

// finishReadLine puts the terminal in a state suitable for other programs to
// use.
func (ed *Editor) finishReadLine(addError func(error)) {
	ed.mode = modeInsert
	ed.tips = nil
	ed.completion = nil
	ed.navigation = nil
	ed.dot = len(ed.line)
	// TODO Perhaps make it optional to NOT clear the rprompt
	ed.rprompt = ""
	addError(ed.refresh(false, false))
	ed.file.WriteString("\n")

	// ed.reader.Stop()
	ed.reader.Quit()

	// Turn on autowrap.
	ed.file.WriteString("\033[?7h")
	// Turn off mouse tracking.
	//ed.file.WriteString("\033[?1000;1006l")

	// restore termios
	err := ed.savedTermios.ApplyToFd(int(ed.file.Fd()))

	if err != nil {
		addError(fmt.Errorf("can't restore terminal attribute: %s", err))
	}
	ed.savedTermios = nil
	ed.editorState = editorState{}
}

// ReadLine reads a line interactively.
func (ed *Editor) ReadLine(prompt, rprompt func() string) (lr LineRead) {
	ed.editorState = editorState{active: true}
	isExternalCh := make(chan map[string]bool, 1)
	go getIsExternal(ed.evaler, isExternalCh)

	ed.writer.resetOldBuf()
	go ed.reader.Run()

	err := ed.startReadLine()
	if err != nil {
		return LineRead{Err: err}
	}
	defer ed.finishReadLine(func(err error) {
		if err != nil {
			lr.Err = util.CatError(lr.Err, err)
		}
	})

MainLoop:
	for {
		ed.prompt = prompt()
		ed.rprompt = rprompt()

		err := ed.refresh(false, true)
		if err != nil {
			return LineRead{Err: err}
		}

		ed.tips = nil

		select {
		case m := <-isExternalCh:
			ed.isExternal = m
		case sig := <-ed.sigs:
			// TODO(xiaq): Maybe support customizable handling of signals
			switch sig {
			case syscall.SIGINT:
				// Start over
				ed.editorState = editorState{
					savedTermios: ed.savedTermios,
					isExternal:   ed.isExternal,
				}
				goto MainLoop
			case syscall.SIGWINCH:
				continue MainLoop
			case syscall.SIGCHLD:
				// ignore
			default:
				ed.addTip("ignored signal %s", sig)
			}
		case err := <-ed.reader.ErrorChan():
			ed.notify("reader error: %s", err.Error())
		case mouse := <-ed.reader.MouseChan():
			ed.addTip("mouse: %+v", mouse)
		case <-ed.reader.CPRChan():
			// Ignore CPR
		case k := <-ed.reader.KeyChan():
		lookupKey:
			keyBinding, ok := keyBindings[ed.mode]
			if !ok {
				ed.addTip("No binding for current mode")
				continue
			}

			fn, bound := keyBinding[k]
			if !bound {
				fn = keyBinding[Default]
			}

			ed.lastKey = k
			fn.Call(ed)
			act := ed.nextAction
			ed.nextAction = action{}

			switch act.actionType {
			case noAction:
				continue
			case reprocessKey:
				err = ed.refresh(false, true)
				if err != nil {
					return LineRead{Err: err}
				}
				goto lookupKey
			case exitReadLine:
				lr = act.returnValue
				if lr.EOF == false && lr.Err == nil && lr.Line != "" {
					ed.appendHistory(lr.Line)
				}

				return lr
			}
		}
	}
}
