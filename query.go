package streamjson

import (
	"fmt"
	"strconv"
)

// SegKind identifies a query path segment kind.
type SegKind int

const (
	SegField    SegKind = iota // object field: .name
	SegIndex                   // array index: [n]
	SegWildcard                // wildcard: .* or [*]
)

// Segment is one step of a query path.
type Segment struct {
	Kind  SegKind
	Field string
	Index int
}

// Query is a compiled path expression such as "a.b[0].c" or "items[*].id".
type Query struct {
	Segments []Segment
	Raw      string
}

// ParseQuery compiles a path expression.
//
// Grammar:
//
//	expr    := segment*
//	segment := '.' field | '.' '*' | '[' number ']' | '[' '*' ']' | field
//
// Fields are dot-separated names, [n] selects an array element, and '*'
// (or '[*]') matches any object field or array element. An empty
// expression matches the whole document.
func ParseQuery(expr string) (*Query, error) {
	q := &Query{Raw: expr}
	i := 0
	for i < len(expr) {
		switch c := expr[i]; {
		case c == '.':
			i++
			if i < len(expr) && expr[i] == '*' {
				q.Segments = append(q.Segments, Segment{Kind: SegWildcard})
				i++
				continue
			}
			start := i
			for i < len(expr) && expr[i] != '.' && expr[i] != '[' {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("streamjson: query %q: empty field at position %d", expr, start)
			}
			q.Segments = append(q.Segments, Segment{Kind: SegField, Field: expr[start:i]})
		case c == '[':
			i++
			if i < len(expr) && expr[i] == '*' {
				i++
				if i >= len(expr) || expr[i] != ']' {
					return nil, fmt.Errorf("streamjson: query %q: expected ']' at position %d", expr, i)
				}
				i++
				q.Segments = append(q.Segments, Segment{Kind: SegWildcard})
				continue
			}
			start := i
			for i < len(expr) && expr[i] != ']' {
				i++
			}
			if i >= len(expr) {
				return nil, fmt.Errorf("streamjson: query %q: unterminated '['", expr)
			}
			n, err := strconv.Atoi(expr[start:i])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("streamjson: query %q: invalid array index %q", expr, expr[start:i])
			}
			i++
			q.Segments = append(q.Segments, Segment{Kind: SegIndex, Index: n})
		default:
			if c == '*' {
				q.Segments = append(q.Segments, Segment{Kind: SegWildcard})
				i++
				continue
			}
			start := i
			for i < len(expr) && expr[i] != '.' && expr[i] != '[' {
				i++
			}
			q.Segments = append(q.Segments, Segment{Kind: SegField, Field: expr[start:i]})
		}
	}
	return q, nil
}

// MissReason explains why a query produced no match for a document.
type MissReason int

const (
	MissNone MissReason = iota
	// MissMissingField: an object did not contain the required field.
	MissMissingField
	// MissTypeMismatch: a segment was applied to an incompatible value type.
	MissTypeMismatch
	// MissIndexOutOfRange: an array index exceeded the array length.
	MissIndexOutOfRange
)

func (r MissReason) String() string {
	switch r {
	case MissMissingField:
		return "missing field"
	case MissTypeMismatch:
		return "type mismatch"
	case MissIndexOutOfRange:
		return "index out of range"
	default:
		return "none"
	}
}
