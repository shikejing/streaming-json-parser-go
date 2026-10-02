package streamjson_test

import (
	"context"
	"fmt"
	"io"
	"strings"

	streamjson "github.com/shikejing/streaming-json-parser-go"
)

func ExampleScanner() {
	input := "{\"items\":[{\"id\":1},{\"id\":2}]}\n{\"items\":[{\"id\":3}]}\n"
	q, _ := streamjson.ParseQuery("items[*].id")
	s := streamjson.NewScanner(strings.NewReader(input), q)
	defer s.Close()
	for {
		m, err := s.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		fmt.Printf("doc=%d path=%s raw=%s\n", m.DocIndex, m.Path, m.Raw)
	}
	// Output:
	// doc=0 path=items[0].id raw=1
	// doc=0 path=items[1].id raw=2
	// doc=1 path=items[0].id raw=3
}
