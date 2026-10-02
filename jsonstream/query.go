package jsonstream

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type segKind byte

const (
	segField segKind = iota
	segIndex
	segWildcard
)

type segment struct {
	kind segKind
	name string
	idx  int
}

// Query is a compiled path expression.
//
// Supported syntax:
//
//	$              root
//	$.field        object field access
//	$["field"]     object field access (arbitrary name)
//	$[*]           wildcard array element
//	$[0]           array element at index 0
//
// Examples:
//
//	$.store.book[*].author
//	$.items[0].name
type Query struct {
	raw  string
	segs []segment
}

// ParseQuery compiles a path expression.
func ParseQuery(s string) (*Query, error) {
	q := &Query{raw: s}
	p := &queryParser{s: s}
	if err := p.parse(q); err != nil {
		return nil, err
	}
	return q, nil
}

// String returns the original query text.
func (q *Query) String() string { return q.raw }

type queryParser struct {
	s string
	i int
}

func (p *queryParser) parse(q *Query) error {
	if len(p.s) == 0 {
		return errors.New("jsonstream: empty query")
	}
	if p.s[0] == '$' {
		p.i = 1
	}
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '.':
			p.i++
			if p.i >= len(p.s) {
				return fmt.Errorf("jsonstream: unexpected end of query %q after '.'", p.s)
			}
			if p.s[p.i] == '.' {
				return fmt.Errorf("jsonstream: recursive descent is not supported in %q", p.s)
			}
			name, err := p.readName()
			if err != nil {
				return err
			}
			q.segs = append(q.segs, segment{kind: segField, name: name})
		case '[':
			p.i++
			if p.i >= len(p.s) {
				return fmt.Errorf("jsonstream: unterminated bracket in %q", p.s)
			}
			switch p.s[p.i] {
			case '*':
				p.i++
				if p.i >= len(p.s) || p.s[p.i] != ']' {
					return fmt.Errorf("jsonstream: expected ']' after '*' in %q", p.s)
				}
				p.i++
				q.segs = append(q.segs, segment{kind: segWildcard})
			case '"':
				name, err := p.readBracketString()
				if err != nil {
					return err
				}
				q.segs = append(q.segs, segment{kind: segField, name: name})
			default:
				start := p.i
				for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
					p.i++
				}
				if start == p.i {
					return fmt.Errorf("jsonstream: expected array index or '*' at position %d in %q", start, p.s)
				}
				idx, err := strconv.Atoi(p.s[start:p.i])
				if err != nil {
					return fmt.Errorf("jsonstream: invalid array index in %q: %w", p.s, err)
				}
				if p.i >= len(p.s) || p.s[p.i] != ']' {
					return fmt.Errorf("jsonstream: expected ']' after index in %q", p.s)
				}
				p.i++
				q.segs = append(q.segs, segment{kind: segIndex, idx: idx})
			}
		default:
			return fmt.Errorf("jsonstream: unexpected character %q at position %d in %q", p.s[p.i], p.i, p.s)
		}
	}
	return nil
}

func (p *queryParser) readName() (string, error) {
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '.' || c == '[' || c == ']' {
			break
		}
		p.i++
	}
	if start == p.i {
		return "", fmt.Errorf("jsonstream: empty field name at position %d in %q", start, p.s)
	}
	return p.s[start:p.i], nil
}

func (p *queryParser) readBracketString() (string, error) {
	// p.s[p.i] == '"'
	start := p.i
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' {
			p.i++
			if p.i >= len(p.s) || p.s[p.i] != ']' {
				return "", fmt.Errorf("jsonstream: expected ']' after string in %q", p.s)
			}
			p.i++
			return b.String(), nil
		}
		if c == '\\' {
			p.i++
			if p.i >= len(p.s) {
				return "", fmt.Errorf("jsonstream: unterminated escape in %q", p.s)
			}
			esc := p.s[p.i]
			p.i++
			switch esc {
			case '"', '\\', '/':
				b.WriteByte(esc)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				return "", fmt.Errorf("jsonstream: unsupported escape \\%c in query string", esc)
			}
			continue
		}
		b.WriteByte(c)
		p.i++
	}
	_ = start
	return "", fmt.Errorf("jsonstream: unterminated string in %q", p.s)
}
