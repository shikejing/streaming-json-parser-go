package streamjson

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// EventKind identifies the type of a parse event.
type EventKind int

const (
	EventStartObject EventKind = iota
	EventEndObject
	EventStartArray
	EventEndArray
	EventKey
	EventString
	EventNumber
	EventBool
	EventNull
	// EventEndDocument marks the end of one complete top-level document.
	EventEndDocument
)

// Event is a single streaming parse event. Offset is the absolute byte
// offset of the token in the input stream.
type Event struct {
	Kind   EventKind
	Offset int64
	String string // decoded string for EventString and EventKey
	Number string // raw literal for EventNumber
	Bool   bool   // value for EventBool
}

// frameState is the sub-state of an open container.
type frameState int

const (
	fKey   frameState = iota // object: expect key (or '}' if empty)
	fColon                   // object: expect ':'
	fValue                   // expect value (or ']' for empty array)
	fComma                   // expect ',' or closing bracket
)

type frame struct {
	isObj bool
	n     int // completed children
	st    frameState
}

// Parser incrementally parses a stream of one or more concatenated JSON
// documents from an io.Reader without loading the whole input into memory.
//
// Memory use is bounded by nesting depth plus the longest single token,
// never by document size.
type Parser struct {
	br       *bufio.Reader
	offset   int64 // bytes consumed so far
	docIndex int
	stack    []frame
	inDoc    bool // a top-level value is in progress

	capArmed  bool // StartCapture called; tee begins at next value token
	capturing bool
	capBuf    bytes.Buffer
}

// NewParser creates a Parser reading from r.
func NewParser(r io.Reader) *Parser {
	return &Parser{br: bufio.NewReaderSize(r, 32*1024)}
}

// DocIndex returns the index of the document currently being parsed.
func (p *Parser) DocIndex() int { return p.docIndex }

// Offset returns the absolute offset of the next unread byte.
func (p *Parser) Offset() int64 { return p.offset }

func (p *Parser) readByte() (byte, error) {
	b, err := p.br.ReadByte()
	if err != nil {
		return 0, err
	}
	p.offset++
	if p.capturing {
		p.capBuf.WriteByte(b)
	}
	return b, nil
}

func (p *Parser) unreadByte() {
	_ = p.br.UnreadByte()
	p.offset--
	if p.capturing {
		p.capBuf.Truncate(p.capBuf.Len() - 1)
	}
}

// nextNonWS returns the next non-whitespace byte.
func (p *Parser) nextNonWS() (byte, error) {
	for {
		c, err := p.readByte()
		if err != nil {
			return 0, err
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return c, nil
		}
	}
}

func (p *Parser) syntaxf(msg string) error {
	return &Error{Kind: ErrKindSyntax, DocIndex: p.docIndex, Offset: p.offset, Msg: msg}
}

func (p *Parser) truncated(context string) error {
	return &Error{Kind: ErrKindTruncated, DocIndex: p.docIndex, Offset: p.offset, Msg: "unexpected end of input while " + context}
}

// Next returns the next parse event. It returns io.EOF when the input is
// exhausted. After a top-level document completes, an EventEndDocument
// event is emitted and DocIndex advances. On a parse error the caller may
// call Resync to continue with the following document.
func (p *Parser) Next() (Event, error) {
	for {
		if len(p.stack) == 0 {
			if p.inDoc {
				p.inDoc = false
				ev := Event{Kind: EventEndDocument, Offset: p.offset}
				p.docIndex++
				return ev, nil
			}
			c, err := p.nextNonWS()
			if err != nil {
				if err == io.EOF {
					return Event{}, io.EOF
				}
				return Event{}, err
			}
			p.inDoc = true
			return p.startValue(c)
		}
		f := &p.stack[len(p.stack)-1]
		switch f.st {
		case fKey:
			c, err := p.nextNonWS()
			if err != nil {
				return Event{}, p.eofErr(err, "reading object key")
			}
			if c == '}' && f.n == 0 {
				return p.endContainer(EventEndObject)
			}
			if c != '"' {
				return Event{}, p.syntaxf("expected object key")
			}
			off := p.offset - 1
			s, err := p.readString()
			if err != nil {
				return Event{}, err
			}
			f.st = fColon
			return Event{Kind: EventKey, Offset: off, String: s}, nil
		case fColon:
			c, err := p.nextNonWS()
			if err != nil {
				return Event{}, p.eofErr(err, "reading ':'")
			}
			if c != ':' {
				return Event{}, p.syntaxf("expected ':' after object key")
			}
			f.st = fValue
		case fValue:
			c, err := p.nextNonWS()
			if err != nil {
				return Event{}, p.eofErr(err, "reading value")
			}
			if !f.isObj && f.n == 0 && c == ']' {
				return p.endContainer(EventEndArray)
			}
			return p.startValue(c)
		case fComma:
			c, err := p.nextNonWS()
			if err != nil {
				return Event{}, p.eofErr(err, "reading ',' or closing bracket")
			}
			if f.isObj {
				if c == ',' {
					f.st = fKey
					continue
				}
				if c == '}' {
					return p.endContainer(EventEndObject)
				}
			} else {
				if c == ',' {
					f.st = fValue
					continue
				}
				if c == ']' {
					return p.endContainer(EventEndArray)
				}
			}
			return Event{}, p.syntaxf("expected ',' or closing bracket")
		}
	}
}

func (p *Parser) eofErr(err error, context string) error {
	if err == io.EOF {
		return p.truncated(context)
	}
	return err
}

// valueComplete records that a value ended inside the current container.
func (p *Parser) valueComplete() {
	if len(p.stack) > 0 {
		f := &p.stack[len(p.stack)-1]
		f.n++
		f.st = fComma
	}
}

func (p *Parser) endContainer(kind EventKind) (Event, error) {
	off := p.offset - 1
	p.stack = p.stack[:len(p.stack)-1]
	p.valueComplete()
	return Event{Kind: kind, Offset: off}, nil
}

func (p *Parser) startValue(c byte) (Event, error) {
	off := p.offset - 1
	if p.capArmed && !p.capturing {
		// The value token starts here; begin the raw-byte tee with the
		// already-consumed first byte.
		p.capturing = true
		p.capBuf.WriteByte(c)
	}
	switch c {
	case '{':
		p.stack = append(p.stack, frame{isObj: true, st: fKey})
		return Event{Kind: EventStartObject, Offset: off}, nil
	case '[':
		p.stack = append(p.stack, frame{st: fValue})
		return Event{Kind: EventStartArray, Offset: off}, nil
	case '"':
		s, err := p.readString()
		if err != nil {
			return Event{}, err
		}
		p.valueComplete()
		return Event{Kind: EventString, Offset: off, String: s}, nil
	case 't':
		if err := p.expectLit("rue", "reading literal"); err != nil {
			return Event{}, err
		}
		p.valueComplete()
		return Event{Kind: EventBool, Offset: off, Bool: true}, nil
	case 'f':
		if err := p.expectLit("alse", "reading literal"); err != nil {
			return Event{}, err
		}
		p.valueComplete()
		return Event{Kind: EventBool, Offset: off}, nil
	case 'n':
		if err := p.expectLit("ull", "reading literal"); err != nil {
			return Event{}, err
		}
		p.valueComplete()
		return Event{Kind: EventNull, Offset: off}, nil
	default:
		if c == '-' || (c >= '0' && c <= '9') {
			num, err := p.readNumber(c)
			if err != nil {
				return Event{}, err
			}
			p.valueComplete()
			return Event{Kind: EventNumber, Offset: off, Number: num}, nil
		}
		return Event{}, p.syntaxf("unexpected character " + quoteByte(c))
	}
}

func quoteByte(c byte) string {
	if c >= 0x20 && c < 0x7f {
		return "'" + string(c) + "'"
	}
	return fmt.Sprintf("byte 0x%02x", c)
}

func (p *Parser) expectLit(rest, context string) error {
	for i := 0; i < len(rest); i++ {
		c, err := p.readByte()
		if err != nil {
			return p.eofErr(err, context)
		}
		if c != rest[i] {
			return p.syntaxf("invalid literal")
		}
	}
	return nil
}

// readString reads a JSON string body (the opening quote is already
// consumed) and returns the decoded value.
func (p *Parser) readString() (string, error) {
	var buf []byte
	for {
		c, err := p.readByte()
		if err != nil {
			return "", p.eofErr(err, "reading string")
		}
		switch c {
		case '"':
			return string(buf), nil
		case '\\':
			esc, err := p.readByte()
			if err != nil {
				return "", p.eofErr(err, "reading escape sequence")
			}
			switch esc {
			case '"', '\\', '/':
				buf = append(buf, esc)
			case 'b':
				buf = append(buf, '\b')
			case 'f':
				buf = append(buf, '\f')
			case 'n':
				buf = append(buf, '\n')
			case 'r':
				buf = append(buf, '\r')
			case 't':
				buf = append(buf, '\t')
			case 'u':
				r, err := p.readHex4()
				if err != nil {
					return "", err
				}
				buf = append(buf, string(r)...)
			default:
				return "", p.syntaxf("invalid escape sequence")
			}
		default:
			if c < 0x20 {
				return "", p.syntaxf("unescaped control character in string")
			}
			buf = append(buf, c)
		}
	}
}

func (p *Parser) readHex4() (rune, error) {
	var v rune
	for i := 0; i < 4; i++ {
		c, err := p.readByte()
		if err != nil {
			return 0, p.eofErr(err, "reading \\u escape")
		}
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, p.syntaxf("invalid \\u escape")
		}
		v = v*16 + rune(d)
	}
	return v, nil
}

// readNumber reads a number literal; the first byte is already consumed.
func (p *Parser) readNumber(first byte) (string, error) {
	buf := []byte{first}
	for {
		c, err := p.readByte()
		if err != nil {
			if err == io.EOF {
				// Number terminated by end of input; valid only at top
				// level, but the enclosing state machine will treat a
				// missing closer as truncation on the next call.
				return string(buf), nil
			}
			return "", err
		}
		switch {
		case c >= '0' && c <= '9', c == '.', c == 'e', c == 'E', c == '+', c == '-':
			buf = append(buf, c)
		default:
			p.unreadByte()
			return string(buf), nil
		}
	}
}

// Resync abandons the current (failed) document and skips input up to the
// next line boundary (or EOF) so parsing can resume at the next document.
// Concatenated documents should therefore be separated by newlines when
// error recovery is desired.
func (p *Parser) Resync() {
	p.stack = p.stack[:0]
	p.inDoc = false
	p.capArmed = false
	p.capturing = false
	p.capBuf.Reset()
	p.docIndex++
	for {
		c, err := p.br.ReadByte()
		if err != nil {
			return
		}
		p.offset++
		if c == '\n' {
			return
		}
	}
}

// StartCapture begins recording raw input bytes of the next value token.
// It must be called before the value is read, i.e. right after the
// preceding EventKey or container-start/comma event.
func (p *Parser) StartCapture() {
	p.capArmed = true
	p.capBuf.Reset()
}

// StopCapture stops recording and returns the captured raw bytes with
// surrounding whitespace trimmed.
func (p *Parser) StopCapture() []byte {
	p.capArmed = false
	p.capturing = false
	out := bytes.TrimSpace(p.capBuf.Bytes())
	cp := make([]byte, len(out))
	copy(cp, out)
	p.capBuf.Reset()
	return cp
}
