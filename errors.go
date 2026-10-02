package streamjson

import "fmt"

// ErrorKind distinguishes the two failure classes the parser can report:
// truncated (input ended unexpectedly) vs invalid (content is malformed).
type ErrorKind int

const (
	// ErrKindSyntax means the input contains invalid JSON content.
	ErrKindSyntax ErrorKind = iota
	// ErrKindTruncated means the input ended before a document was complete.
	ErrKindTruncated
)

// Error describes a parse failure with its absolute input position.
type Error struct {
	Kind     ErrorKind
	DocIndex int   // index of the document being parsed (0-based)
	Offset   int64 // absolute byte offset in the input stream
	Msg      string
}

func (e *Error) Error() string {
	kind := "syntax"
	if e.Kind == ErrKindTruncated {
		kind = "truncated"
	}
	return fmt.Sprintf("streamjson: %s error in document %d at offset %d: %s", kind, e.DocIndex, e.Offset, e.Msg)
}

// IsTruncated reports whether err is a truncation error (input incomplete),
// as opposed to invalid content.
func IsTruncated(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Kind == ErrKindTruncated
}
