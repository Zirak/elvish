package eval

import "errors"

// Map is a map from string to Value.
type Map struct {
	inner *map[Value]Value
}

type MapLike interface {
	Lener
	IndexOneer
}

// NewMap creates a new Map.
func NewMap(inner map[Value]Value) Map {
	return Map{&inner}
}

func (Map) Kind() string {
	return "map"
}

func (m Map) Repr(indent int) string {
	var builder MapReprBuilder
	builder.Indent = indent
	for k, v := range *m.inner {
		builder.WritePair(k.Repr(indent+1), v.Repr(indent+1))
	}
	return builder.String()
}

func (m Map) Len() int {
	return len(*m.inner)
}

func (m Map) IndexOne(idx Value) Value {
	v, ok := (*m.inner)[idx]
	if !ok {
		throw(errors.New("no such key: " + idx.Repr(NoPretty)))
	}
	return v
}

func (m Map) IndexSet(idx Value, v Value) {
	(*m.inner)[idx] = v
}

// MapReprBuilder helps building the Repr of a Map. It is also useful for
// implementing other Map-like values. The zero value of a MapReprBuilder is
// ready to use.
type MapReprBuilder struct {
	ListReprBuilder
}

func (b *MapReprBuilder) WritePair(k, v string) {
	b.WriteElem("&" + k + "=" + v)
}

func (b *MapReprBuilder) String() string {
	if b.buf.Len() == 0 {
		return "[&]"
	}
	return b.ListReprBuilder.String()
}
