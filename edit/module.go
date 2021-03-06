package edit

import (
	"errors"
	"fmt"

	"github.com/elves/elvish/eval"
	"github.com/elves/elvish/parse"
	"github.com/elves/elvish/util"
)

// Exposing editor functionalities as an elvish module.

// Errors thrown to Evaler.
var (
	ErrTakeNoArg      = errors.New("editor builtins take no arguments")
	ErrEditorInactive = errors.New("editor inactive")
)

// makeModule builds a module from an Editor.
func makeModule(ed *Editor) eval.Namespace {
	ns := eval.Namespace{}
	// Populate builtins.
	for _, b := range builtins {
		ns[eval.FnPrefix+b.name] = eval.NewPtrVariable(&EditBuiltin{b, ed})
	}
	// Populate binding tables in the variable $binding.
	// TODO Make binding specific to the Editor.
	binding := &eval.Struct{
		[]string{"insert", "command", "completion", "navigation", "history"},
		[]eval.Variable{
			eval.NewRoVariable(BindingTable{keyBindings[modeInsert]}),
			eval.NewRoVariable(BindingTable{keyBindings[modeCommand]}),
			eval.NewRoVariable(BindingTable{keyBindings[modeCompletion]}),
			eval.NewRoVariable(BindingTable{keyBindings[modeNavigation]}),
			eval.NewRoVariable(BindingTable{keyBindings[modeHistory]}),
		},
	}

	ns["binding"] = eval.NewRoVariable(binding)

	return ns
}

// BindingTable adapts a binding table to eval.IndexSetter.
type BindingTable struct {
	inner map[Key]Caller
}

func (BindingTable) Kind() string {
	return "map"
}

func (bt BindingTable) Repr(indent int) string {
	var builder eval.MapReprBuilder
	builder.Indent = indent
	for k, v := range bt.inner {
		builder.WritePair(parse.Quote(k.String()), v.Repr(eval.IncIndent(indent, 1)))
	}
	return builder.String()
}

func (bt BindingTable) IndexOne(idx eval.Value) eval.Value {
	key := keyIndex(idx)
	switch f := bt.inner[key].(type) {
	case Builtin:
		return eval.String(f.name)
	case EvalCaller:
		return f.Caller
	}
	throw(errors.New("bug"))
	panic("unreachable")
}

func (bt BindingTable) IndexSet(idx, v eval.Value) {
	key := keyIndex(idx)

	var f Caller
	switch v := v.(type) {
	case eval.String:
		builtin, ok := builtinMap[string(v)]
		if !ok {
			throw(fmt.Errorf("no builtin named %s", v.Repr(eval.NoPretty)))
		}
		f = builtin
	case eval.CallerValue:
		f = EvalCaller{v}
	default:
		throw(fmt.Errorf("bad function type %s", v.Kind()))
	}

	bt.inner[key] = f
}

func keyIndex(idx eval.Value) Key {
	skey, ok := idx.(eval.String)
	if !ok {
		throw(errKeyMustBeString)
	}
	key, err := parseKey(string(skey))
	if err != nil {
		throw(err)
	}
	return key
}

// EditBuiltin adapts a Builtin to satisfy eval.Value and eval.Caller.
type EditBuiltin struct {
	b  Builtin
	ed *Editor
}

func (*EditBuiltin) Kind() string {
	return "fn"
}

func (eb *EditBuiltin) Repr(int) string {
	return "<editor builtin " + eb.b.name + ">"
}

func (eb *EditBuiltin) Call(ec *eval.EvalCtx, args []eval.Value) {
	if len(args) > 0 {
		throw(ErrTakeNoArg)
	}
	if !eb.ed.active {
		throw(ErrEditorInactive)
	}
	eb.b.impl(eb.ed)
}

func throw(e error) {
	util.Throw(e)
}
