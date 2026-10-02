package jsonstream

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
)

type tokenKind byte

const (
	tokEOF tokenKind = iota
	tokLBrace
	tokRBrace
	tokLBracket
	tokRBracket
	tokColon
	tokComma
	tokString
	tokNumber
	tokTrue
	tokFalse
	tokNull
)

type token struct {
	kind tokenKind
	off  int64
	str  string
}

type readResult struct {
	buf []byte
	n   int
	err error
}

// tokenizer reads JSON tokens from a stream in bounded-size chunks.
type tokenizer struct {
	r     io.Reader
	chunk int

	buf  []byte
	pos  int
	n    int
	base int64 // byte offset of buf[0]

	readCh chan readResult
	closed bool
}

func newTokenizer(r io.Reader, chunk int) *tokenizer {
	if chunk <= 0 {
		chunk = 32 * 1024
	}
	return &tokenizer{r: r, chunk: chunk}
}

// fill ensures at least one byte is available in buf. It uses a single
// in-flight read goroutine so that context cancellation can unblock a
// consumer waiting on a slow reader.
func (t *tokenizer) fill(ctx context.Context) error {
	if t.pos < t.n {
		return nil
	}
	if t.closed {
		return io.ErrClosedPipe
	}
	if t.readCh == nil {
		t.readCh = make(chan readResult, 1)
		go func(r io.Reader, ch chan<- readResult, size int) {
			buf := make([]byte, size)
			n, err := r.Read(buf)
			ch <- readResult{buf: buf, n: n, err: err}
		}(t.r, t.readCh, t.chunk)
	}
	select {
	case <-ctx.Done():
		t.closed = true
		return ctx.Err()
	case res := <-t.readCh:
		t.readCh = nil
		t.base += int64(t.n)
		t.buf = res.buf
		t.pos = 0
		t.n = res.n
		if res.err != nil {
			if errors.Is(res.err, io.EOF) && res.n > 0 {
				return nil
			}
			if errors.Is(res.err, io.EOF) {
				return io.EOF
			}
			return res.err
		}
		if res.n == 0 {
			return t.fill(ctx)
		}
		return nil
	}
}

// next returns the next token.
func (t *tokenizer) next(ctx context.Context) (token, error) {
	for {
		if err := t.fill(ctx); err != nil {
			return token{}, err
		}
		c := t.buf[t.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			t.pos++
		case c == '{':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokLBrace, off: off}, nil
		case c == '}':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokRBrace, off: off}, nil
		case c == '[':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokLBracket, off: off}, nil
		case c == ']':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokRBracket, off: off}, nil
		case c == ':':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokColon, off: off}, nil
		case c == ',':
			off := t.base + int64(t.pos)
			t.pos++
			return token{kind: tokComma, off: off}, nil
		case c == '"':
			return t.scanString(ctx)
		case c == '-' || (c >= '0' && c <= '9'):
			return t.scanNumber(ctx)
		case c == 't':
			return t.scanLiteral(ctx, "true", tokTrue)
		case c == 'f':
			return t.scanLiteral(ctx, "false", tokFalse)
		case c == 'n':
			return t.scanLiteral(ctx, "null", tokNull)
		default:
			off := t.base + int64(t.pos)
			return token{}, &tokError{off: off, msg: "unexpected character " + strconv.QuoteRune(rune(c))}
		}
	}
}

func (t *tokenizer) scanString(ctx context.Context) (token, error) {
	start := t.base + int64(t.pos)
	t.pos++ // consume opening quote
	var raw strings.Builder
	for {
		if t.pos >= t.n {
			if err := t.fill(ctx); err != nil {
				if errors.Is(err, io.EOF) {
					return token{}, &tokError{off: start, trunc: true, msg: "unterminated string"}
				}
				return token{}, err
			}
		}
		c := t.buf[t.pos]
		if c == '"' {
			t.pos++
			s, err := decodeJSONString(raw.String())
			if err != nil {
				return token{}, &tokError{off: start, msg: err.Error()}
			}
			return token{kind: tokString, off: start, str: s}, nil
		}
		if c == '\\' {
			raw.WriteByte(c)
			t.pos++
			if t.pos >= t.n {
				if err := t.fill(ctx); err != nil {
					if errors.Is(err, io.EOF) {
						return token{}, &tokError{off: start, trunc: true, msg: "unterminated escape in string"}
					}
					return token{}, err
				}
			}
			raw.WriteByte(t.buf[t.pos])
			t.pos++
			continue
		}
		if c < 0x20 {
			return token{}, &tokError{off: t.base + int64(t.pos), msg: "unescaped control character in string"}
		}
		raw.WriteByte(c)
		t.pos++
	}
}

func (t *tokenizer) scanNumber(ctx context.Context) (token, error) {
	start := t.base + int64(t.pos)
	var raw strings.Builder
	for {
		if t.pos >= t.n {
			if err := t.fill(ctx); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return token{}, err
			}
		}
		c := t.buf[t.pos]
		if isNumberByte(c) {
			raw.WriteByte(c)
			t.pos++
			continue
		}
		break
	}
	s := raw.String()
	if !validJSONNumber(s) {
		return token{}, &tokError{off: start, msg: "invalid number " + strconv.Quote(s)}
	}
	return token{kind: tokNumber, off: start, str: s}, nil
}

func (t *tokenizer) scanLiteral(ctx context.Context, lit string, kind tokenKind) (token, error) {
	start := t.base + int64(t.pos)
	var raw strings.Builder
	for raw.Len() < len(lit) {
		if t.pos >= t.n {
			if err := t.fill(ctx); err != nil {
				if errors.Is(err, io.EOF) {
					return token{}, &tokError{off: start, trunc: true, msg: "unterminated literal " + lit}
				}
				return token{}, err
			}
		}
		raw.WriteByte(t.buf[t.pos])
		t.pos++
	}
	if raw.String() == lit {
		return token{kind: kind, off: start}, nil
	}
	return token{}, &tokError{off: start, msg: "invalid literal " + strconv.Quote(raw.String()) + " (want " + lit + ")"}
}

// resync scans forward to the next newline and consumes it.
func (t *tokenizer) resync(ctx context.Context) error {
	for {
		if t.pos >= t.n {
			if err := t.fill(ctx); err != nil {
				return err
			}
		}
		c := t.buf[t.pos]
		t.pos++
		if c == '\n' {
			return nil
		}
	}
}

// currentOffset returns the absolute byte offset of the current read position.
func (t *tokenizer) currentOffset() int64 {
	return t.base + int64(t.pos)
}

// Close releases resources and unblocks any pending read.
func (t *tokenizer) Close() error {
	t.closed = true
	if c, ok := t.r.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func isNumberByte(c byte) bool {
	return c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' ||
		(c >= '0' && c <= '9')
}

func validJSONNumber(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[i] == '-' {
		i++
	}
	if i >= len(s) {
		return false
	}
	if s[i] == '0' {
		i++
	} else if s[i] >= '1' && s[i] <= '9' {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return false
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return false
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	return i == len(s)
}
