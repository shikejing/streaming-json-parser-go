package streamjson

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// genReader streams a JSON array of n objects like {"id":K,"pad":"..."}
// without materializing the document in memory.
type genReader struct {
	n    int
	i    int
	buf  []byte
	done bool
}

func (g *genReader) Read(p []byte) (int, error) {
	for len(g.buf) == 0 {
		if g.done {
			return 0, io.EOF
		}
		switch {
		case g.i == 0:
			g.buf = []byte(`{"items":[`)
		case g.i <= g.n:
			if g.i > 1 {
				g.buf = append(g.buf, ',')
			}
			g.buf = append(g.buf, []byte(fmt.Sprintf(`{"id":%d,"pad":"%s"}`, g.i, strings.Repeat("x", 100)))...)
		default:
			g.buf = []byte(`]}`)
			g.done = true
		}
		g.i++
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

func TestScanLargeInput(t *testing.T) {
	const n = 200_000 // ~25MB of JSON
	q, _ := ParseQuery("items[*].id")
	s := NewScanner(&genReader{n: n}, q)
	defer s.Close()
	var count, maxRaw int
	for {
		m, err := s.Next(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		count++
		if len(m.Raw) > maxRaw {
			maxRaw = len(m.Raw)
		}
	}
	if count != n {
		t.Fatalf("got %d matches, want %d", count, n)
	}
	if maxRaw > 16 {
		t.Fatalf("match size should be tiny and constant, got %d", maxRaw)
	}
	if errs := s.DocErrors(); len(errs) != 0 {
		t.Fatalf("unexpected doc errors: %v", errs)
	}
}

// blockingReader returns one chunk then blocks until released.
type blockingReader struct {
	chunk   []byte
	sent    bool
	release chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.chunk), nil
	}
	<-b.release
	return 0, io.EOF
}

func TestCancelStopsScan(t *testing.T) {
	r := &blockingReader{chunk: []byte(`{"a": 1}`), release: make(chan struct{})}
	defer close(r.release)
	q, _ := ParseQuery("a")
	s := NewScanner(r, q)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	m, err := s.Next(ctx)
	if err != nil || string(m.Raw) != "1" {
		t.Fatalf("first match: %v %v", m, err)
	}
	cancel()
	_, err = s.Next(ctx)
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// ctxReader returns one chunk, then blocks until ctx is done.
type ctxReader struct {
	ctx   context.Context
	chunk []byte
	sent  bool
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if !c.sent {
		c.sent = true
		return copy(p, c.chunk), nil
	}
	<-c.ctx.Done()
	return 0, c.ctx.Err()
}

func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r := &ctxReader{ctx: ctx, chunk: []byte(`{"a": `)}
	q, _ := ParseQuery("a")
	s := NewScanner(r, q)
	defer s.Close()
	start := time.Now()
	_, err := s.Next(ctx)
	if err != context.DeadlineExceeded {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Next did not return promptly after deadline")
	}
}

// countingReader wraps a reader and counts bytes consumed.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestBackpressure(t *testing.T) {
	// 4MB input; the first match is near the start. A pull-based scanner
	// must not read much beyond it.
	var sb strings.Builder
	sb.WriteString(`{"items":[{"id":1}`)
	for i := 0; i < 20_000; i++ {
		sb.WriteString(`,{"id":` + fmt.Sprint(i) + `,"pad":"` + strings.Repeat("y", 150) + `"}`)
	}
	sb.WriteString(`]}`)
	cr := &countingReader{r: strings.NewReader(sb.String())}
	q, _ := ParseQuery("items[*].id")
	s := NewScanner(cr, q)
	defer s.Close()
	m, err := s.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if string(m.Raw) != "1" {
		t.Fatalf("got %q", m.Raw)
	}
	if cr.n > 128*1024 {
		t.Fatalf("scanner read %d bytes before first match was consumed; want bounded read-ahead", cr.n)
	}
}

func TestConcurrentScanners(t *testing.T) {
	const workers = 32
	const perWorker = 5_000
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			q, _ := ParseQuery("items[*].id")
			s := NewScanner(&genReader{n: perWorker}, q)
			defer s.Close()
			var count int
			for {
				_, err := s.Next(context.Background())
				if err == io.EOF {
					break
				}
				if err != nil {
					errs <- err
					return
				}
				count++
			}
			if count != perWorker {
				errs <- fmt.Errorf("worker %d: got %d matches", w, count)
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestStreamEarlyCancelNoLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		q, _ := ParseQuery("items[*].id")
		s := NewScanner(&genReader{n: 10_000}, q)
		ch := s.Stream(ctx, 4)
		// Consume a few results then cancel without draining.
		for j := 0; j < 3; j++ {
			if _, ok := <-ch; !ok {
				break
			}
		}
		cancel()
		s.Close()
		// drain until closed
		for range ch {
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("goroutine leak: before=%d after=%d", before, got)
	}
}

func TestEarlyClose(t *testing.T) {
	q, _ := ParseQuery("items[*].id")
	s := NewScanner(&genReader{n: 100_000}, q)
	m, err := s.Next(context.Background())
	if err != nil || m == nil {
		t.Fatalf("first match: %v %v", m, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.Next(context.Background()); err != io.EOF {
		t.Fatalf("Next after Close: %v", err)
	}
}

func TestMultiDocConcatenatedNoNewline(t *testing.T) {
	// Documents concatenated with only whitespace.
	s, ms := scanAll(t, `{"a":1} {"a":2} [3] "str" 42`, "a")
	if len(ms) != 2 || ms[0].DocIndex != 0 || ms[1].DocIndex != 1 {
		t.Fatalf("got %+v", ms)
	}
	if errs := s.DocErrors(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}
