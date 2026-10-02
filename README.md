# jsonstream

A streaming JSON parser and query engine for Go, designed for very large
documents that do not fit in memory.

## Features

- **Streaming, bounded memory.** Input is read in fixed-size chunks (default
  32 KiB). The whole document is never loaded into memory. Matches are
  delivered one at a time as parsing proceeds.
- **Path queries.** Nested fields, array indices, and wildcards. Missing
  fields are distinguished from null values; missing fields and type
  mismatches are reported separately.
- **Multi-document input.** Sequentially concatenated documents (NDJSON or
  whitespace-separated JSON values) are parsed independently. Each result
  identifies its document index and byte offset.
- **Error recovery.** Truncated and illegal documents are reported with their
  position. Parsing resynchronizes to the next document boundary and
  continues; one bad document does not affect the others.
- **Cancellation and timeout.** `Next` takes a `context.Context`. Cancellation
  is prompt and leaves no background tasks or half-parsed state.
- **Backpressure.** The pull-based API reads only as fast as the consumer
  processes matches, so memory cannot grow without bound.
- **Concurrency.** Create one `Parser` per task; each maintains independent
  progress and no shared state.
- **Early termination.** Stop consuming at any time; `Close` releases
  resources without deadlock.

## Installation

```sh
go get github.com/shikejing/streaming-json-parser-go
```

## Quick start

```go
q, err := jsonstream.ParseQuery("$.store.book[*].author")
if err != nil {
    log.Fatal(err)
}

p := jsonstream.NewParser(reader, jsonstream.WithQuery(q))
defer p.Close()

ctx := context.Background()
for {
    ev, err := p.Next(ctx)
    if err == io.EOF {
        break
    }
    if err != nil {
        log.Fatal(err)
    }
    switch ev.Kind {
    case jsonstream.EventMatch:
        fmt.Printf("match %s = %s\n", ev.Match.Path, ev.Match.Value.String())
    case jsonstream.EventDocEnd:
        doc := ev.Doc
        if doc.Status != jsonstream.DocOK {
            log.Printf("document %d error: %v", doc.DocIndex, doc.Err)
        }
    }
}
```

## Query syntax

A query is a path expression compiled with `ParseQuery`.

| Syntax              | Meaning                              |
|---------------------|--------------------------------------|
| `$`                 | Root (matches a root scalar)         |
| `$.field`           | Object field access                  |
| `$["field"]`        | Object field with an arbitrary name  |
| `$[*]`              | Wildcard array element               |
| `$[0]`              | Array element at index 0             |

Examples:

```
$.store.book[*].author
$.items[0].name
$["a b"].value
```

Only scalar values (strings, numbers, booleans, null) are reported as
matches. A query that resolves to an object or array consumes the subtree
without emitting a match, which keeps per-result memory bounded.

### Missing vs. mismatch

For each document, `DocInfo` reports:

- `Matched` — whether any scalar matched.
- `Missing` / `MissingPaths` — branches where a required field or array index
  was absent.
- `Mismatch` / `MismatchPaths` — branches where the path existed but the
  value had the wrong type (e.g. `[*]` applied to an object, or `.field`
  applied to a scalar).

A field with value `null` is a match (with `Value.IsNull() == true`), not a
missing field.

## Configuration

Options passed to `NewParser`:

| Option                    | Default   | Description                                    |
|---------------------------|-----------|------------------------------------------------|
| `WithQuery(q)`            | root only | The compiled query expression                  |
| `WithChunkSize(n)`        | 32768     | Read chunk size in bytes                       |
| `WithMaxSamples(n)`       | 10        | Max sample paths recorded per document         |

## Multi-document input

Documents may be concatenated with any whitespace between them, including
newlines:

```
{"id":1}
{"id":2}
{"id":3}
```

Each document produces exactly one `EventDocEnd`. Matches carry `DocIndex`
(zero-based) and `DocOffset` (byte offset where the document started).

### Error recovery

When a document is truncated or contains illegal syntax, `Next` returns an
`EventDocEnd` with `Status` set to `DocTruncated` or `DocIllegal` and an
`Err` describing the position and reason. The parser then resynchronizes to
the next newline (the document boundary) and continues with the next
document.

For example, with input:

```
{"id":}
{"id":2}
```

the first document is reported as illegal and the second document parses
normally.

Truncation (incomplete input at EOF) and illegal syntax are distinguished:
`DocTruncated` means the input ended too soon; `DocIllegal` means the content
was malformed.

## Cancellation and timeout

`Next` accepts a `context.Context`. Cancelling the context (or exceeding its
deadline) causes `Next` to return `ctx.Err()` promptly. No background
goroutines are left running: the parser uses a single in-flight read that is
abandoned on cancellation and exits when the underlying read returns.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
ev, err := p.Next(ctx)
```

## Concurrency

Each `Parser` is independent and safe for single-goroutine use. To process
multiple streams concurrently, create one `Parser` per task:

```go
var wg sync.WaitGroup
for _, r := range readers {
    wg.Add(1)
    go func(r io.Reader) {
        defer wg.Done()
        p := jsonstream.NewParser(r, jsonstream.WithQuery(q))
        defer p.Close()
        // ...
    }(r)
}
wg.Wait()
```

There is no shared mutable state between parsers, so a slow task cannot block
or starve others.

## Memory characteristics

- The input is read in fixed-size chunks; the document is never loaded whole.
- A single string value is buffered while decoding (proportional to the
  value, not the document).
- Matches are delivered one at a time; no result set is accumulated.
- Sample paths for missing/mismatch outcomes are capped by `WithMaxSamples`.

## API

See the [package documentation](https://pkg.go.dev/github.com/shikejing/streaming-json-parser-go)
for the full API reference.
