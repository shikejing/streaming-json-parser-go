package jsonstream

import (
	"context"
	"errors"
	"io"
	"strconv"
)

// EventKind identifies the kind of event returned by Parser.Next.
type EventKind int

const (
	// EventNone is the zero value and means no event is ready yet.
	EventNone EventKind = iota
	// EventMatch is emitted for every scalar value that satisfies the query.
	EventMatch
	// EventDocEnd is emitted when a document finishes (successfully or not).
	EventDocEnd
)

// Match describes one matched scalar value.
type Match struct {
	DocIndex  int64  // zero-based index of the document
	DocOffset int64  // byte offset where the document started
	Offset    int64  // byte offset of the matched value
	Path      string // resolved path, e.g. "$.store.book[2].author"
	Value     Value  // decoded scalar value
}

// DocInfo describes the outcome of one document.
type DocInfo struct {
	DocIndex      int64
	StartOffset   int64
	EndOffset     int64
	Status        DocStatus
	Err           *DocError
	Matched       bool
	Missing       int      // number of branches that ended with a missing field/index
	Mismatch      int      // number of branches that ended with a type mismatch
	MissingPaths  []string // sample paths of missing branches (capped)
	MismatchPaths []string // sample paths of mismatch branches (capped)
}

// Event is either a Match or a DocInfo.
type Event struct {
	Kind  EventKind
	Match Match
	Doc   DocInfo
}

// Option configures a Parser.
type Option func(*Parser)

// WithQuery sets the query expression.
func WithQuery(q *Query) Option {
	return func(p *Parser) { p.query = q }
}

// WithChunkSize sets the read chunk size in bytes (default 32768).
func WithChunkSize(n int) Option {
	return func(p *Parser) {
		if n > 0 {
			p.chunk = n
		}
	}
}

// WithMaxSamples sets the maximum number of sample paths recorded per
// document for missing/mismatch outcomes (default 10).
func WithMaxSamples(n int) Option {
	return func(p *Parser) {
		if n > 0 {
			p.maxSamples = n
		}
	}
}

// Parser performs streaming JSON document parsing and query matching.
// It is safe for single-goroutine use; create one Parser per concurrent task.
type Parser struct {
	r          io.Reader
	query      *Query
	chunk      int
	maxSamples int

	tok      *tokenizer
	doc      *docParser
	docIndex int64
	done     bool
}

// NewParser creates a Parser over r.
func NewParser(r io.Reader, opts ...Option) *Parser {
	p := &Parser{
		r:          r,
		chunk:      32 * 1024,
		maxSamples: 10,
		query:      &Query{}, // match root scalar only
	}
	for _, o := range opts {
		o(p)
	}
	p.tok = newTokenizer(r, p.chunk)
	return p
}

// Next returns the next event. It returns io.EOF when all input has been
// consumed. A document-level error (truncation or illegal syntax) is reported
// via an EventDocEnd; parsing continues with the next document. Context
// cancellation returns ctx.Err() and stops parsing.
func (p *Parser) Next(ctx context.Context) (Event, error) {
	if p.done {
		return Event{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	for {
		if p.doc == nil {
			d := &docParser{p: p, start: p.tok.currentOffset()}
			p.doc = d
			tok, err := p.tok.next(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					p.done = true
					return Event{}, io.EOF
				}
				return p.handleTokErr(ctx, err)
			}
			d.start = tok.off
			switch tok.kind {
			case tokString, tokNumber, tokTrue, tokFalse, tokNull, tokLBrace, tokLBracket:
				if err := d.handleValue(ctx, tok, 0, "$"); err != nil {
					if errors.Is(err, io.EOF) {
						return d.finish(ctx, DocTruncated, &DocError{
							Status: DocTruncated, Offset: d.p.tok.currentOffset(), Msg: "unexpected end of input",
						})
					}
					return Event{}, err
				}
				if d.match != nil {
					return Event{Kind: EventMatch, Match: *d.match}, nil
				}
				if len(d.stack) == 0 {
					return d.finish(ctx, DocOK, nil)
				}
			default:
				return d.finish(ctx, DocIllegal, &DocError{Status: DocIllegal, Offset: tok.off, Msg: "unexpected character"})
			}
		}
		tok, err := p.tok.next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				var ev Event
				if len(p.doc.stack) == 0 {
					ev, err = p.doc.finish(ctx, DocOK, nil)
				} else {
					ev, err = p.doc.finish(ctx, DocTruncated, &DocError{
						Status: DocTruncated, Offset: p.tok.currentOffset(), Msg: "unexpected end of input",
					})
				}
				p.doc = nil
				return ev, err
			}
			return p.handleTokErr(ctx, err)
		}
		ev, err := p.doc.dispatch(ctx, tok)
		if err != nil {
			return Event{}, err
		}
		if ev.Kind == EventNone {
			continue
		}
		if ev.Kind == EventDocEnd {
			p.doc = nil
		}
		return ev, nil
	}
}

// Close releases parser resources.
func (p *Parser) Close() error {
	if p.tok != nil {
		return p.tok.Close()
	}
	return nil
}

// ---- document state machine ----

const (
	stObjKey byte = iota
	stObjColon
	stObjValue
	stObjSep
	stArrValue
	stArrSep
)

type frame struct {
	typ         byte // 'O' object, 'A' array
	state       byte
	seg         int
	field       string
	seen        bool
	keyMatched  bool
	keyRequired bool
	idx         int
	wc          bool
	tgt         int
	path        string
}

type docParser struct {
	p           *Parser
	stack       []*frame
	start       int64
	match       *Match
	matched     bool
	missing     int
	mismatch    int
	missSamples []string
	mismSamples []string
}

func (d *docParser) pushContainer(typ byte, seg int, path string) {
	f := &frame{typ: typ, seg: seg, path: path}
	if typ == 'O' {
		f.state = stObjKey
		if seg < len(d.p.query.segs) && d.p.query.segs[seg].kind == segField {
			f.field = d.p.query.segs[seg].name
		}
	} else {
		f.state = stArrValue
		if seg < len(d.p.query.segs) {
			s := d.p.query.segs[seg]
			switch s.kind {
			case segWildcard:
				f.wc = true
			case segIndex:
				f.tgt = s.idx
			}
		}
	}
	d.stack = append(d.stack, f)
}

func (d *docParser) pop() {
	d.stack = d.stack[:len(d.stack)-1]
}

func (d *docParser) handleValue(ctx context.Context, tok token, seg int, path string) error {
	d.match = nil
	switch tok.kind {
	case tokString, tokNumber, tokTrue, tokFalse, tokNull:
		if seg >= len(d.p.query.segs) {
			d.emitMatch(tok, path)
		} else {
			d.recordMismatch(path)
		}
	case tokLBrace:
		if seg >= len(d.p.query.segs) {
			return d.skipContainer(ctx)
		} else if d.p.query.segs[seg].kind == segField {
			d.pushContainer('O', seg, path)
		} else {
			d.recordMismatch(path)
			return d.skipContainer(ctx)
		}
	case tokLBracket:
		if seg >= len(d.p.query.segs) {
			return d.skipContainer(ctx)
		} else if d.p.query.segs[seg].kind != segField {
			d.pushContainer('A', seg, path)
		} else {
			d.recordMismatch(path)
			return d.skipContainer(ctx)
		}
	}
	return nil
}

func (d *docParser) emitMatch(tok token, path string) {
	d.matched = true
	m := Match{
		DocIndex:  d.p.docIndex,
		DocOffset: d.start,
		Offset:    tok.off,
		Path:      path,
	}
	switch tok.kind {
	case tokString:
		m.Value = Value{Kind: VString, Str: tok.str}
	case tokNumber:
		m.Value = parseNumber(tok.str)
	case tokTrue:
		m.Value = Value{Kind: VBool, Bool: true}
	case tokFalse:
		m.Value = Value{Kind: VBool, Bool: false}
	case tokNull:
		m.Value = Value{Kind: VNull}
	}
	d.match = &m
}

func (d *docParser) recordMissing(path string) {
	d.missing++
	if len(d.missSamples) < d.p.maxSamples {
		d.missSamples = append(d.missSamples, path)
	}
}

func (d *docParser) recordMismatch(path string) {
	d.mismatch++
	if len(d.mismSamples) < d.p.maxSamples {
		d.mismSamples = append(d.mismSamples, path)
	}
}

func (d *docParser) skipValue(ctx context.Context, tok token) error {
	switch tok.kind {
	case tokLBrace, tokLBracket:
		return d.skipContainer(ctx)
	}
	return nil
}

func (d *docParser) skipContainer(ctx context.Context) error {
	depth := 1
	for depth > 0 {
		tok, err := d.p.tok.next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return err
		}
		switch tok.kind {
		case tokLBrace, tokLBracket:
			depth++
		case tokRBrace, tokRBracket:
			depth--
		}
	}
	return nil
}

func (d *docParser) dispatch(ctx context.Context, tok token) (Event, error) {
	top := d.stack[len(d.stack)-1]
	d.match = nil
	switch top.state {
	case stObjKey:
		switch tok.kind {
		case tokRBrace:
			if top.keyRequired {
				return d.illegal(ctx, tok, "expected object key, got '}'")
			}
			if !top.seen {
				d.recordMissing(top.path + "." + top.field)
			}
			d.pop()
		case tokString:
			top.keyMatched = top.field != "" && tok.str == top.field
			if top.keyMatched {
				top.seen = true
			}
			top.keyRequired = false
			top.state = stObjColon
		default:
			return d.illegal(ctx, tok, "expected object key or '}'")
		}
	case stObjColon:
		if tok.kind == tokColon {
			top.state = stObjValue
		} else {
			return d.illegal(ctx, tok, "expected ':'")
		}
	case stObjValue:
		if top.keyMatched {
			if !isValueStart(tok.kind) {
				return d.illegal(ctx, tok, "expected value")
			}
			if err := d.handleValue(ctx, tok, top.seg+1, top.path+"."+top.field); err != nil {
				return d.handleErr(ctx, err)
			}
		} else {
			if err := d.skipValue(ctx, tok); err != nil {
				return d.handleErr(ctx, err)
			}
		}
		top.keyMatched = false
		top.state = stObjSep
	case stObjSep:
		if tok.kind == tokRBrace {
			if !top.seen {
				d.recordMissing(top.path + "." + top.field)
			}
			d.pop()
		} else if tok.kind == tokComma {
			top.keyRequired = true
			top.state = stObjKey
		} else {
			return d.illegal(ctx, tok, "expected ',' or '}'")
		}
	case stArrValue:
		if tok.kind == tokRBracket {
			if !top.wc && top.idx <= top.tgt {
				d.recordMissing(top.path + "[" + strconv.Itoa(top.tgt) + "]")
			}
			d.pop()
		} else if isValueStart(tok.kind) {
			if top.wc || top.idx == top.tgt {
				if err := d.handleValue(ctx, tok, top.seg+1, top.path+"["+strconv.Itoa(top.idx)+"]"); err != nil {
					return d.handleErr(ctx, err)
				}
			} else {
				if err := d.skipValue(ctx, tok); err != nil {
					return d.handleErr(ctx, err)
				}
			}
			top.idx++
			top.state = stArrSep
		} else {
			return d.illegal(ctx, tok, "expected array element or ']'")
		}
	case stArrSep:
		if tok.kind == tokRBracket {
			if !top.wc && top.idx <= top.tgt {
				d.recordMissing(top.path + "[" + strconv.Itoa(top.tgt) + "]")
			}
			d.pop()
		} else if tok.kind == tokComma {
			top.state = stArrValue
		} else {
			return d.illegal(ctx, tok, "expected ',' or ']'")
		}
	}
	if d.match != nil {
		return Event{Kind: EventMatch, Match: *d.match}, nil
	}
	if len(d.stack) == 0 {
		return d.finish(ctx, DocOK, nil)
	}
	return Event{}, nil
}

func (d *docParser) handleErr(ctx context.Context, err error) (Event, error) {
	if errors.Is(err, io.EOF) {
		return d.finish(ctx, DocTruncated, &DocError{
			Status: DocTruncated, Offset: d.p.tok.currentOffset(), Msg: "unexpected end of input",
		})
	}
	return Event{}, err
}

// handleTokErr converts a tokenizer syntax error into a document error.
func (p *Parser) handleTokErr(ctx context.Context, err error) (Event, error) {
	var te *tokError
	if errors.As(err, &te) {
		status := DocIllegal
		if te.trunc {
			status = DocTruncated
		}
		if p.doc == nil {
			p.doc = &docParser{p: p, start: te.off}
		}
		ev, e := p.doc.finish(ctx, status, &DocError{Status: status, Offset: te.off, Msg: te.msg})
		p.doc = nil
		return ev, e
	}
	return Event{}, err
}

func (d *docParser) illegal(ctx context.Context, tok token, msg string) (Event, error) {
	return d.finish(ctx, DocIllegal, &DocError{Status: DocIllegal, Offset: tok.off, Msg: msg})
}

func (d *docParser) finish(ctx context.Context, status DocStatus, derr *DocError) (Event, error) {
	doc := DocInfo{
		DocIndex:      d.p.docIndex,
		StartOffset:   d.start,
		EndOffset:     d.p.tok.currentOffset(),
		Status:        status,
		Matched:       d.matched,
		Missing:       d.missing,
		Mismatch:      d.mismatch,
		MissingPaths:  d.missSamples,
		MismatchPaths: d.mismSamples,
	}
	if derr != nil {
		derr.DocIndex = d.p.docIndex
		derr.StartOffset = d.start
		doc.Err = derr
	}
	if status != DocOK {
		if err := d.p.tok.resync(ctx); err != nil {
			if errors.Is(err, io.EOF) {
				d.p.done = true
			} else {
				return Event{}, err
			}
		}
	}
	d.p.docIndex++
	return Event{Kind: EventDocEnd, Doc: doc}, nil
}

func isValueStart(k tokenKind) bool {
	switch k {
	case tokLBrace, tokLBracket, tokString, tokNumber, tokTrue, tokFalse, tokNull:
		return true
	}
	return false
}

func parseNumber(s string) Value {
	v := Value{Kind: VNumber, Str: s}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		v.i = i
		v.hasI = true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		v.f = f
		v.hasF = true
	}
	return v
}
