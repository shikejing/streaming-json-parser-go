package streamjson

import "testing"

func TestParseQuery(t *testing.T) {
	cases := []struct {
		expr string
		want []Segment
	}{
		{"a.b.c", []Segment{{Kind: SegField, Field: "a"}, {Kind: SegField, Field: "b"}, {Kind: SegField, Field: "c"}}},
		{"a[0].b", []Segment{{Kind: SegField, Field: "a"}, {Kind: SegIndex, Index: 0}, {Kind: SegField, Field: "b"}}},
		{"items[*].id", []Segment{{Kind: SegField, Field: "items"}, {Kind: SegWildcard}, {Kind: SegField, Field: "id"}}},
		{"a.*", []Segment{{Kind: SegField, Field: "a"}, {Kind: SegWildcard}}},
		{"*", []Segment{{Kind: SegWildcard}}},
		{"[2]", []Segment{{Kind: SegIndex, Index: 2}}},
		{"", nil},
	}
	for _, c := range cases {
		q, err := ParseQuery(c.expr)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		if len(q.Segments) != len(c.want) {
			t.Fatalf("%q: got %+v want %+v", c.expr, q.Segments, c.want)
		}
		for i, s := range q.Segments {
			if s != c.want[i] {
				t.Fatalf("%q seg %d: got %+v want %+v", c.expr, i, s, c.want[i])
			}
		}
	}
}

func TestParseQueryErrors(t *testing.T) {
	for _, expr := range []string{"a..b", "a[", "a[x]", "a[-1]", "a.", "a[*"} {
		if _, err := ParseQuery(expr); err == nil {
			t.Fatalf("%q: want error", expr)
		}
	}
}
