package eval

//go:generate ./boilerplate.py

import (
	"fmt"

	"github.com/elves/elvish/parse"
	"github.com/elves/elvish/util"
)

type scope map[string]bool

// compiler maintains the set of states needed when compiling a single source
// file.
type compiler struct {
	// Lexical scopes.
	scopes []scope
	// Variables captured from outer scopes.
	capture scope
	// Position of what is being compiled.
	begin, end int
}

func compile(sc scope, n *parse.Chunk) (op Op, err error) {
	cp := &compiler{[]scope{sc}, scope{}, 0, 0}
	defer util.Catch(&err)
	return cp.chunkOp(n), nil
}

func (cp *compiler) compiling(n parse.Node) {
	cp.begin, cp.end = n.Begin(), n.End()
}

func (cp *compiler) errorpf(begin, end int, format string, args ...interface{}) {
	throw(&util.PosError{begin, end, fmt.Errorf(format, args...)})
}

func (cp *compiler) errorf(format string, args ...interface{}) {
	cp.errorpf(cp.begin, cp.end, format, args...)
}

func (cp *compiler) thisScope() scope {
	return cp.scopes[len(cp.scopes)-1]
}

func (cp *compiler) pushScope() scope {
	sc := scope{}
	cp.scopes = append(cp.scopes, sc)
	return sc
}

func (cp *compiler) popScope() {
	cp.scopes[len(cp.scopes)-1] = nil
	cp.scopes = cp.scopes[:len(cp.scopes)-1]
}

func (cp *compiler) registerVariableGet(qname string) bool {
	_, ns, name := parseVariable(qname)
	if ns != "" && ns != "local" && ns != "up" {
		// Variable in another mod, do nothing
		return true
	}
	// Find in local scope
	if ns == "" || ns == "local" {
		if cp.thisScope()[name] {
			return true
		} else if ns == "local" {
			return false
		}
	}
	// Find in upper scopes
	for i := len(cp.scopes) - 2; i >= 0; i-- {
		if cp.scopes[i][name] {
			// Existing name: record capture and return.
			cp.capture[name] = true
			return true
		}
	}
	return false
}

func (cp *compiler) registerVariableSet(qname string) bool {
	_, ns, name := parseVariable(qname)
	switch ns {
	case "local":
		cp.thisScope()[name] = true
		return true
	case "up":
		for i := len(cp.scopes) - 2; i >= 0; i-- {
			if cp.scopes[i][name] {
				// Existing name: record capture and return.
				cp.capture[name] = true
				return true
			}
		}
		return false
	case "":
		if cp.thisScope()[name] {
			// A name on current scope. Do nothing.
			return true
		}
		// Walk up the upper scopes
		for i := len(cp.scopes) - 2; i >= 0; i-- {
			if cp.scopes[i][name] {
				// Existing name. Do nothing
				cp.capture[name] = true
				return true
			}
		}
		// New name. Register on this scope!
		cp.thisScope()[name] = true
		return true
	default:
		// Variable in another mod, do nothing
		return true
	}
}
