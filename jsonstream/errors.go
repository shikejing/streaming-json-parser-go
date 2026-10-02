package jsonstream

import "fmt"

// DocStatus describes how a document ended.
type DocStatus int

const (
	// DocOK means the document was complete and syntactically valid.
	DocOK DocStatus = iota
	// DocTruncated means the input ended before the document finished.
	DocTruncated
	// DocIllegal means the document contained invalid JSON syntax.
	DocIllegal
)

// DocError describes a document-level failure (truncation or illegal syntax).
// It is carried inside DocInfo and does not stop the parser: the parser
// resynchronizes to the next document boundary and continues.
type DocError struct {
	DocIndex    int64 // zero-based index of the document
	StartOffset int64 // byte offset where the document started
	Offset      int64 // byte offset where the error was detected
	Status      DocStatus
	Msg         string
}

func (e *DocError) Error() string {
	kind := "illegal syntax"
	if e.Status == DocTruncated {
		kind = "truncated input"
	}
	return fmt.Sprintf("jsonstream: document %d (%s) at offset %d: %s",
		e.DocIndex, kind, e.Offset, e.Msg)
}
