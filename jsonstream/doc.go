// Package jsonstream provides a streaming JSON parser and query engine for
// very large documents. Input is read in bounded-size chunks; matches are
// delivered one at a time as the document is parsed, so a single match never
// requires loading the whole document into memory.
//
// Documents may be concatenated sequentially (NDJSON or whitespace-separated
// JSON values). Each document is parsed independently and reports its index
// and byte offset. Truncated and illegal documents are reported as document
// errors and parsing continues with the next document.
//
// Use NewParser with a Query to start parsing, then call Next in a loop until
// it returns io.EOF.
package jsonstream
