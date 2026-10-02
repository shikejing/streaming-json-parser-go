package jsonstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// collect drains all events and returns matches and doc ends.
func collect(t *testing.T, r io.Reader, q *Query, opts ...Option) ([]Match, []DocInfo) {
	t.Helper()
	p := NewParser(r, append([]Option{WithQuery(q)}, opts...)...)
	defer p.Close()
	var matches []Match
	var docs []DocInfo
	ctx := context.Background()
	for {
		ev, err := p.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("Next error: %v", err)
		}
		switch ev.Kind {
		case EventMatch:
			matches = append(matches, ev.Match)
		case EventDocEnd:
			docs = append(docs, ev.Doc)
		default:
			t.Fatalf("unexpected event kind %d", ev.Kind)
		}
	}
	return matches, docs
}

func TestBasicNested(t *testing.T) {
	q, err := ParseQuery("$.store.book[*].author")
	if err != nil {
		t.Fatal(err)
	}
	input := `{"store":{"book":[{"author":"Alice"},{"author":"Bob"}]}}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	want := []string{"Alice", "Bob"}
	for i, m := range matches {
		if m.Value.String() != want[i] {
			t.Errorf("match %d = %q, want %q", i, m.Value.String(), want[i])
		}
		if m.Path != fmt.Sprintf("$.store.book[%d].author", i) {
			t.Errorf("match %d path = %q", i, m.Path)
		}
	}
	if len(docs) != 1 || docs[0].Status != DocOK || !docs[0].Matched {
		t.Errorf("doc = %+v", docs)
	}
}

func TestArrayIndex(t *testing.T) {
	q, _ := ParseQuery("$.items[1].name")
	input := `{"items":[{"name":"a"},{"name":"b"},{"name":"c"}]}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "b" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Path != "$.items[1].name" {
		t.Errorf("path = %q", matches[0].Path)
	}
}

func TestMissingIndex(t *testing.T) {
	q, _ := ParseQuery("$.items[5].name")
	input := `{"items":[{"name":"a"}]}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 0 {
		t.Fatalf("unexpected matches: %+v", matches)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	d := docs[0]
	if d.Matched {
		t.Error("doc should not be matched")
	}
	if d.Missing != 1 {
		t.Errorf("missing = %d, want 1", d.Missing)
	}
	if len(d.MissingPaths) != 1 || d.MissingPaths[0] != "$.items[5]" {
		t.Errorf("missing paths = %+v", d.MissingPaths)
	}
}

func TestMissingField(t *testing.T) {
	q, _ := ParseQuery("$.a.b")
	input := `{"a":{"c":1}}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 0 {
		t.Fatalf("matches = %+v", matches)
	}
	d := docs[0]
	if d.Missing != 1 || d.Mismatch != 0 {
		t.Errorf("missing=%d mismatch=%d", d.Missing, d.Mismatch)
	}
	if len(d.MissingPaths) != 1 || d.MissingPaths[0] != "$.a.b" {
		t.Errorf("missing paths = %+v", d.MissingPaths)
	}
}

func TestNullIsMatch(t *testing.T) {
	q, _ := ParseQuery("$.a")
	input := `{"a":null}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if !matches[0].Value.IsNull() {
		t.Errorf("value = %+v, want null", matches[0].Value)
	}
}

func TestMismatch(t *testing.T) {
	q, _ := ParseQuery("$.a[*].b")
	input := `{"a":[1,2,3]}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 0 {
		t.Fatalf("matches = %+v", matches)
	}
	d := docs[0]
	if d.Matched {
		t.Error("should not match")
	}
	if d.Mismatch != 3 {
		t.Errorf("mismatch = %d, want 3", d.Mismatch)
	}
	if len(d.MismatchPaths) != 3 {
		t.Errorf("mismatch paths = %+v", d.MismatchPaths)
	}
}

func TestMismatchRoot(t *testing.T) {
	q, _ := ParseQuery("$.a")
	input := `[1,2]`
	_, docs := collect(t, strings.NewReader(input), q)
	if docs[0].Mismatch != 1 {
		t.Errorf("mismatch = %d", docs[0].Mismatch)
	}
	if len(docs[0].MismatchPaths) != 1 || docs[0].MismatchPaths[0] != "$" {
		t.Errorf("mismatch paths = %+v", docs[0].MismatchPaths)
	}
}

func TestMultiDoc(t *testing.T) {
	q, _ := ParseQuery("$.id")
	input := "{\"id\":1}\n{\"id\":2}\n{\"id\":3}\n"
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 3 {
		t.Fatalf("matches = %+v", matches)
	}
	for i, m := range matches {
		if m.DocIndex != int64(i) {
			t.Errorf("match %d docIndex = %d", i, m.DocIndex)
		}
		if m.Value.String() != fmt.Sprintf("%d", i+1) {
			t.Errorf("match %d value = %q", i, m.Value.String())
		}
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %d", len(docs))
	}
	for i, d := range docs {
		if d.Status != DocOK {
			t.Errorf("doc %d status = %v", i, d.Status)
		}
		if d.DocIndex != int64(i) {
			t.Errorf("doc %d index = %d", i, d.DocIndex)
		}
	}
}

func TestMultiDocConcatenated(t *testing.T) {
	q, _ := ParseQuery("$.id")
	input := `{"id":1}{"id":2}{"id":3}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 3 {
		t.Fatalf("matches = %+v", matches)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %d", len(docs))
	}
}

func TestTruncated(t *testing.T) {
	q, _ := ParseQuery("$.a")
	input := `{"a":1`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	d := docs[0]
	if d.Status != DocTruncated {
		t.Errorf("status = %v, want truncated", d.Status)
	}
	if d.Err == nil {
		t.Fatal("expected error")
	}
	if d.Err.Offset < 5 {
		t.Errorf("offset = %d", d.Err.Offset)
	}
}

func TestTruncatedThenRecover(t *testing.T) {
	q, _ := ParseQuery("$.id")
	// first doc OK, second doc truncated at EOF
	input := "{\"id\":1}\n{\"id\":2"
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Status != DocOK {
		t.Errorf("doc0 status = %v", docs[0].Status)
	}
	if docs[1].Status != DocTruncated {
		t.Errorf("doc1 status = %v, want truncated", docs[1].Status)
	}
}

func TestIllegal(t *testing.T) {
	q, _ := ParseQuery("$.a")
	input := `{"a":}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	d := docs[0]
	if d.Status != DocIllegal {
		t.Errorf("status = %v, want illegal", d.Status)
	}
	if d.Err == nil {
		t.Fatal("expected error")
	}
}

func TestIllegalThenRecover(t *testing.T) {
	q, _ := ParseQuery("$.id")
	input := "{\"id\":}\n{\"id\":2}\n"
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "2" {
		t.Fatalf("matches = %+v", matches)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Status != DocIllegal {
		t.Errorf("doc0 = %v", docs[0].Status)
	}
	if docs[1].Status != DocOK {
		t.Errorf("doc1 = %v", docs[1].Status)
	}
}

func TestNumbers(t *testing.T) {
	q, _ := ParseQuery("$.n")
	cases := []struct {
		in   string
		want string
	}{
		{`{"n":42}`, "42"},
		{`{"n":-3}`, "-3"},
		{`{"n":1.5}`, "1.5"},
		{`{"n":1e3}`, "1e3"},
		{`{"n":2.5E-1}`, "2.5E-1"},
	}
	for _, c := range cases {
		matches, _ := collect(t, strings.NewReader(c.in), q)
		if len(matches) != 1 || matches[0].Value.String() != c.want {
			t.Errorf("input %s: matches = %+v, want %q", c.in, matches, c.want)
		}
	}
}

func TestStringEscapes(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"a\"b\\c\nd\tx"}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	want := "a\"b\\c\nd\tx"
	if matches[0].Value.String() != want {
		t.Errorf("got %q, want %q", matches[0].Value.String(), want)
	}
}

func TestUnicodeEscape(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"é中"}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Value.String() != "é中" {
		t.Errorf("got %q", matches[0].Value.String())
	}
}

func TestSurrogatePair(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"😀"}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Value.String() != "😀" {
		t.Errorf("got %q", matches[0].Value.String())
	}
}

func TestRootScalar(t *testing.T) {
	q, _ := ParseQuery("$")
	input := `42`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "42" {
		t.Fatalf("matches = %+v", matches)
	}
	if len(docs) != 1 || docs[0].Status != DocOK {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestQueryErrors(t *testing.T) {
	bad := []string{
		"",
		"a.b",
		"$.",
		"$..a",
		"$.a[",
		"$.a[*",
		"$.a[]",
		"$.a[abc]",
		"$.a[99999999999999999999999]",
	}
	for _, s := range bad {
		if _, err := ParseQuery(s); err == nil {
			t.Errorf("query %q should fail", s)
		}
	}
}

func TestBracketField(t *testing.T) {
	q, _ := ParseQuery(`$["a b"]`)
	input := `{"a b":1}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "1" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestBoolMatch(t *testing.T) {
	q, _ := ParseQuery("$.flag")
	input := `{"flag":true}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	b, err := matches[0].Value.BoolValue()
	if err != nil || !b {
		t.Errorf("bool = %v, err = %v", b, err)
	}
}

func TestInt64(t *testing.T) {
	q, _ := ParseQuery("$.n")
	input := `{"n":42}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	i, err := matches[0].Value.Int64()
	if err != nil || i != 42 {
		t.Errorf("int = %d, err = %v", i, err)
	}
}

func TestFloat64(t *testing.T) {
	q, _ := ParseQuery("$.n")
	input := `{"n":1.5}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	f, err := matches[0].Value.Float64()
	if err != nil || f != 1.5 {
		t.Errorf("float = %f, err = %v", f, err)
	}
}

func TestEmptyInput(t *testing.T) {
	q, _ := ParseQuery("$.a")
	matches, docs := collect(t, strings.NewReader(""), q)
	if len(matches) != 0 || len(docs) != 0 {
		t.Fatalf("matches=%+v docs=%+v", matches, docs)
	}
}

func TestWhitespaceOnly(t *testing.T) {
	q, _ := ParseQuery("$.a")
	matches, docs := collect(t, strings.NewReader("   \n\t  "), q)
	if len(matches) != 0 || len(docs) != 0 {
		t.Fatalf("matches=%+v docs=%+v", matches, docs)
	}
}

func TestDocOffsets(t *testing.T) {
	q, _ := ParseQuery("$.id")
	input := "{\"id\":1}\n{\"id\":2}\n"
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].StartOffset != 0 {
		t.Errorf("doc0 start = %d", docs[0].StartOffset)
	}
	if docs[1].StartOffset != 9 {
		t.Errorf("doc1 start = %d, want 9", docs[1].StartOffset)
	}
	if matches[1].DocOffset != 9 {
		t.Errorf("match1 docOffset = %d", matches[1].DocOffset)
	}
}

func TestMaxSamples(t *testing.T) {
	q, _ := ParseQuery("$.a[*].b")
	input := `{"a":[1,2,3,4,5]}`
	_, docs := collect(t, strings.NewReader(input), q, WithMaxSamples(2))
	if len(docs[0].MismatchPaths) != 2 {
		t.Errorf("mismatch paths = %+v", docs[0].MismatchPaths)
	}
	if docs[0].Mismatch != 5 {
		t.Errorf("mismatch count = %d", docs[0].Mismatch)
	}
}

func TestDeeplyNested(t *testing.T) {
	q, _ := ParseQuery("$.a.b.c.d.e")
	input := `{"a":{"b":{"c":{"d":{"e":42}}}}}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "42" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestWildcardNested(t *testing.T) {
	q, _ := ParseQuery("$.a[*][*]")
	input := `{"a":[[1,2],[3,4]]}`
	matches, _ := collect(t, strings.NewReader(input), q)
	if len(matches) != 4 {
		t.Fatalf("matches = %+v", matches)
	}
	want := []string{"1", "2", "3", "4"}
	for i, m := range matches {
		if m.Value.String() != want[i] {
			t.Errorf("match %d = %q", i, m.Value.String())
		}
	}
}

func TestCancellation(t *testing.T) {
	q, _ := ParseQuery("$.id")
	pr, pw := io.Pipe()
	p := NewParser(pr, WithQuery(q))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		for {
			_, err := p.Next(ctx)
			if err != nil {
				done <- err
				return
			}
		}
	}()
	// write one doc then block
	pw.Write([]byte(`{"id":1}` + "\n"))
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not return after cancellation")
	}
	p.Close()
}

func TestTimeout(t *testing.T) {
	q, _ := ParseQuery("$.id")
	pr, _ := io.Pipe()
	p := NewParser(pr, WithQuery(q))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	p.Close()
}

func TestEarlyClose(t *testing.T) {
	q, _ := ParseQuery("$.id")
	pr, pw := io.Pipe()
	p := NewParser(pr, WithQuery(q))
	ctx := context.Background()
	// produce many docs
	go func() {
		for i := 0; i < 1000; i++ {
			pw.Write([]byte(fmt.Sprintf("{\"id\":%d}\n", i)))
		}
	}()
	got := 0
	for got < 3 {
		ev, err := p.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == EventMatch {
			got++
		}
	}
	p.Close()
	// after close, Next should not deadlock
	done := make(chan struct{})
	go func() {
		p.Next(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Next deadlocked after Close")
	}
}

func TestConcurrentParsers(t *testing.T) {
	q, _ := ParseQuery("$.id")
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			input := fmt.Sprintf("{\"id\":%d}\n", i)
			matches, docs := collect(t, strings.NewReader(input), q)
			if len(matches) != 1 || matches[0].Value.String() != fmt.Sprintf("%d", i) {
				errs <- fmt.Errorf("task %d: matches=%+v", i, matches)
				return
			}
			if len(docs) != 1 || docs[0].Status != DocOK {
				errs <- fmt.Errorf("task %d: docs=%+v", i, docs)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestTruncatedContainerEmptyQuery(t *testing.T) {
	q, _ := ParseQuery("$")
	input := `[1,2`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Status != DocTruncated {
		t.Errorf("status = %v, want truncated", docs[0].Status)
	}
}

func TestTruncatedObjectEmptyQuery(t *testing.T) {
	q, _ := ParseQuery("$")
	input := `{"a":1`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Status != DocTruncated {
		t.Errorf("status = %v, want truncated", docs[0].Status)
	}
}

func TestIllegalStringEscape(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"a\x"}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 || docs[0].Status != DocIllegal {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestIllegalNumber(t *testing.T) {
	q, _ := ParseQuery("$.n")
	input := `{"n":1.2.3}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 || docs[0].Status != DocIllegal {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestTrailingComma(t *testing.T) {
	q, _ := ParseQuery("$.a")
	input := `{"a":1,}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 || docs[0].Status != DocIllegal {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestUnpairedSurrogate(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"\uD800"}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 || docs[0].Status != DocIllegal {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestLowSurrogate(t *testing.T) {
	q, _ := ParseQuery("$.s")
	input := `{"s":"\uDC00"}`
	_, docs := collect(t, strings.NewReader(input), q)
	if len(docs) != 1 || docs[0].Status != DocIllegal {
		t.Fatalf("docs = %+v", docs)
	}
}

func TestNestedWildcardMissing(t *testing.T) {
	q, _ := ParseQuery("$.a[*].b[*].c")
	input := `{"a":[{"b":[{"c":1}]},{"b":[{}]}]}`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 1 || matches[0].Value.String() != "1" {
		t.Fatalf("matches = %+v", matches)
	}
	d := docs[0]
	if d.Missing != 1 {
		t.Errorf("missing = %d, want 1", d.Missing)
	}
}

func TestWildcardOnObject(t *testing.T) {
	q, _ := ParseQuery("$[*]")
	input := `{"a":1}`
	_, docs := collect(t, strings.NewReader(input), q)
	if docs[0].Mismatch != 1 {
		t.Errorf("mismatch = %d", docs[0].Mismatch)
	}
}

func TestFieldOnScalar(t *testing.T) {
	q, _ := ParseQuery("$.a.b")
	input := `{"a":1}`
	_, docs := collect(t, strings.NewReader(input), q)
	if docs[0].Mismatch != 1 {
		t.Errorf("mismatch = %d", docs[0].Mismatch)
	}
}

func TestEmptyArrayWildcard(t *testing.T) {
	q, _ := ParseQuery("$[*]")
	input := `[]`
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 0 {
		t.Fatalf("matches = %+v", matches)
	}
	if docs[0].Matched || docs[0].Missing != 0 || docs[0].Mismatch != 0 {
		t.Errorf("doc = %+v", docs[0])
	}
}

func TestMultipleDocsWithErrors(t *testing.T) {
	q, _ := ParseQuery("$.id")
	input := "{\"id\":1}\nbad\n{\"id\":3}\n"
	matches, docs := collect(t, strings.NewReader(input), q)
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %+v", docs)
	}
	if docs[0].Status != DocOK || docs[1].Status != DocIllegal || docs[2].Status != DocOK {
		t.Errorf("statuses = %v, %v, %v", docs[0].Status, docs[1].Status, docs[2].Status)
	}
}
