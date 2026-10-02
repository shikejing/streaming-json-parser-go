package streamjson

import (
	"io"
	"strings"
	"testing"
)

func collectEvents(t *testing.T, input string) ([]Event, error) {
	t.Helper()
	p := NewParser(strings.NewReader(input))
	var evs []Event
	for {
		ev, err := p.Next()
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

func kinds(evs []Event) []EventKind {
	var ks []EventKind
	for _, e := range evs {
		ks = append(ks, e.Kind)
	}
	return ks
}

func TestParserSingleDoc(t *testing.T) {
	evs, err := collectEvents(t, `{"a": [1, "x", true, null], "b": {}}`)
	if err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	want := []EventKind{
		EventStartObject,
		EventKey, EventStartArray, EventNumber, EventString, EventBool, EventNull, EventEndArray,
		EventKey, EventStartObject, EventEndObject,
		EventEndObject,
		EventEndDocument,
	}
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %v want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	// check scalar payloads
	if evs[1].String != "a" || evs[3].Number != "1" || evs[4].String != "x" || !evs[5].Bool {
		t.Fatalf("bad payloads: %+v", evs)
	}
}

func TestParserMultiDoc(t *testing.T) {
	evs, err := collectEvents(t, "1\n{\"a\":2}\n[3]\n")
	if err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	var endDocs int
	for _, e := range evs {
		if e.Kind == EventEndDocument {
			endDocs++
		}
	}
	if endDocs != 3 {
		t.Fatalf("want 3 documents, got %d (%v)", endDocs, kinds(evs))
	}
}

func TestParserTruncated(t *testing.T) {
	_, err := collectEvents(t, `{"a": [1, 2`)
	pe, ok := err.(*Error)
	if !ok || pe.Kind != ErrKindTruncated {
		t.Fatalf("want truncated error, got %v", err)
	}
	if !IsTruncated(err) {
		t.Fatal("IsTruncated should be true")
	}
	if pe.Offset <= 0 {
		t.Fatalf("error should carry offset, got %+v", pe)
	}
}

func TestParserSyntaxError(t *testing.T) {
	_, err := collectEvents(t, `{"a": }`)
	pe, ok := err.(*Error)
	if !ok || pe.Kind != ErrKindSyntax {
		t.Fatalf("want syntax error, got %v", err)
	}
	if IsTruncated(err) {
		t.Fatal("IsTruncated should be false")
	}
}

func TestParserResync(t *testing.T) {
	p := NewParser(strings.NewReader("{\"bad\": }\n{\"good\": 1}\n"))
	for {
		_, err := p.Next()
		if err != nil {
			if pe, ok := err.(*Error); ok && pe.Kind == ErrKindSyntax {
				if pe.DocIndex != 0 {
					t.Fatalf("error should be in doc 0, got %d", pe.DocIndex)
				}
				p.Resync()
				continue
			}
			if err == io.EOF {
				return
			}
			t.Fatalf("unexpected err %v", err)
		}
	}
}

func TestParserCapture(t *testing.T) {
	p := NewParser(strings.NewReader(`{"a": {"x": [1,2]}, "b": 2}`))
	for {
		ev, err := p.Next()
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if ev.Kind == EventKey && ev.String == "a" {
			p.StartCapture()
		}
		if ev.Kind == EventEndObject && p.capturing {
			got := string(p.StopCapture())
			if got != `{"x": [1,2]}` {
				t.Fatalf("captured %q", got)
			}
			return
		}
	}
}
