package streamjson

import (
	"context"
	"io"
	"strconv"
	"sync/atomic"
)

// Match is a single query result produced during scanning.
type Match struct {
	DocIndex int    // which concatenated document produced the match
	Offset   int64  // absolute byte offset of the matched value in the input
	Path     string // concrete path of the match (wildcards resolved)
	Raw      []byte // raw JSON bytes of the matched value
}

// DocError records a per-document parse failure; other documents are
// unaffected.
type DocError struct {
	DocIndex int
	Err      error
}

// Miss describes why a document produced no match for the query.
type Miss struct {
	DocIndex int
	Reason   MissReason
	Path     string // concrete path prefix where matching failed
}

// mframe mirrors one open container in the parser.
type mframe struct {
	isObj        bool
	depth        int // depth of the container value (root value = 0)
	matched      bool
	childMatched bool
	path         string // concrete path to this container
	key          string // pending object key
	keyPending   bool
	idx          int // next array element index
}

// Scanner drives a Parser with a compiled Query, yielding matches one at a
// time. It is pull-based: parsing only advances when Next is called, which
// provides natural backpressure and leaves no background goroutines.
//
// A Scanner is not safe for concurrent use; run one Scanner per goroutine.
type Scanner struct {
	p *Parser
	q *Query

	stack      []mframe
	armed      bool   // capture armed for the upcoming value
	capturing  bool   // capturing a matched container value
	capDepth   int    // depth of the container being captured
	matchPath  string // concrete path of the armed/in-progress match
	matchOff   int64  // offset of the matched value
	docMatched bool   // current document produced at least one match

	hasMiss   bool
	missDepth int
	miss      Miss

	misses  []Miss
	docErrs []DocError
	closed  atomic.Bool
}

// NewScanner creates a Scanner over r for the given query.
func NewScanner(r io.Reader, q *Query) *Scanner {
	return &Scanner{p: NewParser(r), q: q}
}

func matchKey(seg Segment, key string) bool {
	return seg.Kind == SegWildcard || (seg.Kind == SegField && seg.Field == key)
}

func matchIdx(seg Segment, idx int) bool {
	return seg.Kind == SegWildcard || (seg.Kind == SegIndex && seg.Index == idx)
}

func childPathKey(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

func childPathIdx(parent string, idx int) string {
	return parent + "[" + strconv.Itoa(idx) + "]"
}

func (s *Scanner) top() *mframe { return &s.stack[len(s.stack)-1] }

// disarm cancels a speculative capture that did not match a value.
func (s *Scanner) disarm() {
	if s.armed {
		s.armed = false
		s.p.StopCapture()
	}
}

// arm speculatively enables raw-byte capture when the value the parser is
// about to read would be a full match. Called before every parser step.
func (s *Scanner) arm() {
	if s.armed || s.capturing {
		return
	}
	nseg := len(s.q.Segments)
	if len(s.stack) == 0 {
		if nseg == 0 {
			s.armed = true
			s.matchPath = ""
			s.p.StartCapture()
		}
		return
	}
	top := s.top()
	d := top.depth
	if !top.matched || d+1 != nseg {
		return
	}
	seg := s.q.Segments[d]
	if top.isObj {
		if !top.keyPending || !matchKey(seg, top.key) {
			return
		}
		s.matchPath = childPathKey(top.path, top.key)
	} else {
		if !matchIdx(seg, top.idx) {
			return
		}
		s.matchPath = childPathIdx(top.path, top.idx)
	}
	s.armed = true
	s.p.StartCapture()
}

// beginValue accounts for a value starting in the current context and
// reports whether its path prefix matches the query.
func (s *Scanner) beginValue() (childMatch bool, path string) {
	nseg := len(s.q.Segments)
	if len(s.stack) == 0 {
		// The empty path prefix always matches the root value.
		return true, ""
	}
	top := s.top()
	d := top.depth
	if top.isObj {
		top.keyPending = false
		if top.matched && d < nseg && matchKey(s.q.Segments[d], top.key) {
			top.childMatched = true
			return true, childPathKey(top.path, top.key)
		}
		return false, childPathKey(top.path, top.key)
	}
	idx := top.idx
	top.idx++
	if top.matched && d < nseg && matchIdx(s.q.Segments[d], idx) {
		top.childMatched = true
		return true, childPathIdx(top.path, idx)
	}
	return false, childPathIdx(top.path, idx)
}

func (s *Scanner) addMiss(reason MissReason, depth int, path string) {
	if s.hasMiss && depth <= s.missDepth {
		return
	}
	s.hasMiss = true
	s.missDepth = depth
	s.miss = Miss{DocIndex: s.p.DocIndex(), Reason: reason, Path: path}
}

func (s *Scanner) resetDoc() {
	s.stack = s.stack[:0]
	s.armed = false
	s.capturing = false
	s.docMatched = false
	s.hasMiss = false
}

// handle processes one parser event; it returns a non-nil Match when a
// match completed.
func (s *Scanner) handle(ev Event) *Match {
	nseg := len(s.q.Segments)
	switch ev.Kind {
	case EventKey:
		top := s.top()
		top.key = ev.String
		top.keyPending = true
	case EventString, EventNumber, EventBool, EventNull:
		childMatch, path := s.beginValue()
		if s.armed {
			raw := s.p.StopCapture()
			s.armed = false
			s.docMatched = true
			return &Match{DocIndex: s.p.DocIndex(), Offset: ev.Offset, Path: s.matchPath, Raw: raw}
		}
		if childMatch {
			vdepth := len(s.stack)
			if vdepth < nseg {
				// Query continues past a scalar value.
				s.addMiss(MissTypeMismatch, vdepth, path)
			}
		}
	case EventStartObject, EventStartArray:
		childMatch, path := s.beginValue()
		vdepth := len(s.stack)
		s.stack = append(s.stack, mframe{
			isObj:   ev.Kind == EventStartObject,
			depth:   vdepth,
			matched: childMatch,
			path:    path,
		})
		if s.armed {
			s.armed = false
			s.capturing = true
			s.capDepth = vdepth
			s.matchOff = ev.Offset
		}
	case EventEndObject, EventEndArray:
		s.disarm() // a speculative array-element arm may not have hit a value
		f := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		if s.capturing && f.depth == s.capDepth {
			raw := s.p.StopCapture()
			s.capturing = false
			s.docMatched = true
			return &Match{DocIndex: s.p.DocIndex(), Offset: s.matchOff, Path: s.matchPath, Raw: raw}
		}
		if f.matched && !f.childMatched && f.depth < nseg {
			seg := s.q.Segments[f.depth]
			var reason MissReason
			switch {
			case f.isObj && seg.Kind == SegIndex:
				reason = MissTypeMismatch
			case f.isObj:
				reason = MissMissingField
			case !f.isObj && seg.Kind == SegField:
				reason = MissTypeMismatch
			default:
				reason = MissIndexOutOfRange
			}
			s.addMiss(reason, f.depth, f.path)
		}
	case EventEndDocument:
		s.disarm()
		if !s.docMatched {
			if !s.hasMiss {
				s.addMiss(MissTypeMismatch, 0, "")
			}
			s.misses = append(s.misses, s.miss)
		}
		s.resetDoc()
	}
	return nil
}

// Next advances to the next match. It returns io.EOF when the input is
// exhausted. Parse failures of individual documents are recorded in
// DocErrors and scanning resumes at the next document; cancellation via
// ctx stops parsing promptly and returns the context error.
func (s *Scanner) Next(ctx context.Context) (*Match, error) {
	if s.closed.Load() {
		return nil, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.arm()
		ev, err := s.p.Next()
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			if ctx.Err() != nil {
				// The read failed because the caller cancelled or the
				// deadline passed; report that rather than a parse error.
				return nil, ctx.Err()
			}
			s.docErrs = append(s.docErrs, DocError{DocIndex: s.p.DocIndex(), Err: err})
			s.resetDoc()
			s.p.Resync()
			continue
		}
		if m := s.handle(ev); m != nil {
			return m, nil
		}
	}
}

// Misses returns per-document no-match explanations collected so far.
// Field absence (MissMissingField) is reported distinctly from type
// mismatches and out-of-range indexes, and never participates as an empty
// value.
func (s *Scanner) Misses() []Miss { return s.misses }

// DocErrors returns per-document parse errors collected so far.
func (s *Scanner) DocErrors() []DocError { return s.docErrs }

// Close releases resources. It is safe to call at any time, including
// before the input is exhausted (early termination). Because the Scanner
// is pull-based, Close never leaves background work behind.
func (s *Scanner) Close() error {
	s.closed.Store(true)
	return nil
}

// Result is one item delivered on the channel returned by Stream.
type Result struct {
	Match *Match
	Err   error
}

// Stream is a convenience wrapper around Next that delivers results on a
// bounded channel from a single background goroutine. The goroutine exits
// when the input is exhausted, when ctx is cancelled, or when Close is
// called; callers that stop consuming early must cancel ctx (or close the
// scanner) to let the goroutine finish.
func (s *Scanner) Stream(ctx context.Context, bufSize int) <-chan Result {
	if bufSize < 0 {
		bufSize = 0
	}
	ch := make(chan Result, bufSize)
	go func() {
		defer close(ch)
		for {
			m, err := s.Next(ctx)
			if err != nil {
				if err != io.EOF {
					select {
					case ch <- Result{Err: err}:
					case <-ctx.Done():
					}
				}
				return
			}
			select {
			case ch <- Result{Match: m}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
