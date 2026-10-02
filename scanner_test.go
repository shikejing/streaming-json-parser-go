package streamjson

import (
	"context"
	"io"
	"strings"
	"testing"
)

func scanAll(t *testing.T, input, query string) (*Scanner, []Match) {
	t.Helper()
	q, err := ParseQuery(query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	s := NewScanner(strings.NewReader(input), q)
	defer s.Close()
	var ms []Match
	for {
		m, err := s.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		ms = append(ms, *m)
	}
	return s, ms
}

func raws(ms []Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Raw))
	}
	return out
}

func TestScanNestedField(t *testing.T) {
	_, ms := scanAll(t, `{"a": {"b": {"c": 42}}}`, "a.b.c")
	if len(ms) != 1 || string(ms[0].Raw) != "42" || ms[0].Path != "a.b.c" {
		t.Fatalf("got %+v", ms)
	}
}

func TestScanWildcardAndIndex(t *testing.T) {
	input := `{"items": [{"id": 1}, {"id": 2}, {"id": 3}]}`
	_, ms := scanAll(t, input, "items[*].id")
	got := raws(ms)
	if len(got) != 3 || got[0] != "1" || got[1] != "2" || got[2] != "3" {
		t.Fatalf("got %v", got)
	}
	_, ms = scanAll(t, input, "items[1].id")
	if len(ms) != 1 || string(ms[0].Raw) != "2" || ms[0].Path != "items[1].id" {
		t.Fatalf("got %+v", ms)
	}
	// wildcard over object fields
	_, ms = scanAll(t, `{"a": 1, "b": 2}`, "*")
	got = raws(ms)
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("got %v", got)
	}
}

func TestScanMatchOrderDeterministic(t *testing.T) {
	input := `{"x": [{"v": 1}, {"v": 2}, {"v": 3}, {"v": 4}]}`
	_, first := scanAll(t, input, "x[*].v")
	for i := 0; i < 5; i++ {
		_, again := scanAll(t, input, "x[*].v")
		if len(again) != len(first) {
			t.Fatalf("run %d: length differs", i)
		}
		for j := range first {
			if string(again[j].Raw) != string(first[j].Raw) || again[j].Offset != first[j].Offset {
				t.Fatalf("run %d match %d differs", i, j)
			}
		}
	}
}

func TestScanMultiDoc(t *testing.T) {
	input := "{\"id\": 1}\n{\"id\": 2}\n{\"id\": 3}\n"
	_, ms := scanAll(t, input, "id")
	if len(ms) != 3 {
		t.Fatalf("got %+v", ms)
	}
	for i, m := range ms {
		if m.DocIndex != i {
			t.Fatalf("match %d: doc index %d", i, m.DocIndex)
		}
	}
	// offsets must be increasing and point at the raw value
	if !(ms[0].Offset < ms[1].Offset && ms[1].Offset < ms[2].Offset) {
		t.Fatalf("offsets not increasing: %+v", ms)
	}
	if input[ms[0].Offset] != '1' {
		t.Fatalf("offset %d does not point at value", ms[0].Offset)
	}
}

func TestScanDocErrorIsolation(t *testing.T) {
	input := "{\"id\": 1}\n{bad json}\n{\"id\": 3}\n"
	s, ms := scanAll(t, input, "id")
	if len(ms) != 2 || string(ms[0].Raw) != "1" || string(ms[1].Raw) != "3" {
		t.Fatalf("got %v", raws(ms))
	}
	if ms[1].DocIndex != 2 {
		t.Fatalf("third doc should have index 2, got %d", ms[1].DocIndex)
	}
	errs := s.DocErrors()
	if len(errs) != 1 || errs[0].DocIndex != 1 {
		t.Fatalf("doc errors: %+v", errs)
	}
	if IsTruncated(errs[0].Err) {
		t.Fatal("bad content should be a syntax error, not truncation")
	}
}

func TestScanTruncatedDoc(t *testing.T) {
	input := "{\"id\": 1}\n{\"id\": 2"
	s, ms := scanAll(t, input, "id")
	// The second document's value was fully read before truncation, so it
	// is still emitted; the document itself is then reported truncated.
	if len(ms) != 2 || ms[1].DocIndex != 1 {
		t.Fatalf("got %v", raws(ms))
	}
	errs := s.DocErrors()
	if len(errs) != 1 || !IsTruncated(errs[0].Err) {
		t.Fatalf("want truncation error, got %+v", errs)
	}
	if errs[0].DocIndex != 1 {
		t.Fatalf("truncation should be in doc 1, got %d", errs[0].DocIndex)
	}
	pe := errs[0].Err.(*Error)
	if pe.Offset != int64(len(input)) {
		t.Fatalf("truncation offset %d, want %d", pe.Offset, len(input))
	}
}

func TestScanMissReasons(t *testing.T) {
	// missing field
	s, ms := scanAll(t, `{"a": {"x": 1}}`, "a.b")
	if len(ms) != 0 {
		t.Fatalf("unexpected matches %v", raws(ms))
	}
	misses := s.Misses()
	if len(misses) != 1 || misses[0].Reason != MissMissingField || misses[0].Path != "a" {
		t.Fatalf("misses: %+v", misses)
	}
	// type mismatch: field segment applied to array
	_, _ = scanAll(t, `{"a": [1,2]}`, "a.b")
	// index out of range
	s, _ = scanAll(t, `{"a": [1,2]}`, "a[5]")
	misses = s.Misses()
	if len(misses) != 1 || misses[0].Reason != MissIndexOutOfRange {
		t.Fatalf("misses: %+v", misses)
	}
	// type mismatch: descend into scalar
	s, _ = scanAll(t, `{"a": 5}`, "a.b")
	misses = s.Misses()
	if len(misses) != 1 || misses[0].Reason != MissTypeMismatch || misses[0].Path != "a" {
		t.Fatalf("misses: %+v", misses)
	}
}

func TestScanWholeDocQuery(t *testing.T) {
	_, ms := scanAll(t, `{"a": 1}`, "")
	if len(ms) != 1 || string(ms[0].Raw) != `{"a": 1}` {
		t.Fatalf("got %+v", ms)
	}
}

func TestScanContainerMatch(t *testing.T) {
	_, ms := scanAll(t, `{"a": {"b": [1, 2]}, "c": 3}`, "a.b")
	if len(ms) != 1 || string(ms[0].Raw) != "[1, 2]" {
		t.Fatalf("got %+v", ms)
	}
}
