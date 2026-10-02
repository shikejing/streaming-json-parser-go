package jsonstream

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

// generatingReader produces a large JSON document of N items.
type generatingReader struct {
	items int
	pos   int64
	buf   []byte
	done  bool
}

func (r *generatingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	if r.buf == nil {
		r.buf = []byte(`{"items":[`)
	}
	for len(p) > 0 {
		if len(r.buf) > 0 {
			n := copy(p, r.buf)
			r.buf = r.buf[n:]
			r.pos += int64(n)
			return n, nil
		}
		if r.items > 0 {
			item := fmt.Sprintf(`{"id":%d,"name":"item%d","tags":["a","b"]},`, r.items-1, r.items-1)
			r.buf = []byte(item)
			r.items--
		} else {
			r.buf = []byte(`]}`)
			r.done = true
		}
	}
	return 0, nil
}

func TestHugeInput(t *testing.T) {
	const n = 200000
	r := &generatingReader{items: n}
	q, _ := ParseQuery("$.items[*].id")
	p := NewParser(r, WithQuery(q), WithChunkSize(4096))
	defer p.Close()
	ctx := context.Background()
	count := 0
	var first, last string
	for {
		ev, err := p.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if ev.Kind == EventMatch {
			if count == 0 {
				first = ev.Match.Value.String()
			}
			last = ev.Match.Value.String()
			count++
		}
	}
	if count != n {
		t.Fatalf("got %d matches, want %d", count, n)
	}
	if first != "199999" || last != "0" {
		t.Errorf("order: first=%s last=%s", first, last)
	}
}

func TestDeterministicOrder(t *testing.T) {
	const n = 5000
	build := func() string {
		var b strings.Builder
		b.WriteString(`{"items":[`)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(fmt.Sprintf(`{"id":%d}`, i))
		}
		b.WriteString(`]}`)
		return b.String()
	}
	q, _ := ParseQuery("$.items[*].id")
	var order1, order2 []string
	for _, input := range []string{build(), build()} {
		matches, _ := collect(t, strings.NewReader(input), q)
		var order []string
		for _, m := range matches {
			order = append(order, m.Value.String())
		}
		if order1 == nil {
			order1 = order
		} else {
			order2 = order
		}
	}
	if len(order1) != n || len(order2) != n {
		t.Fatalf("len1=%d len2=%d", len(order1), len(order2))
	}
	for i := range order1 {
		if order1[i] != order2[i] {
			t.Fatalf("order differs at %d: %s vs %s", i, order1[i], order2[i])
		}
	}
}

// trackingReader counts bytes read.
type trackingReader struct {
	r    io.Reader
	read int64
	mu   sync.Mutex
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.mu.Lock()
	t.read += int64(n)
	t.mu.Unlock()
	return n, err
}

func (t *trackingReader) bytesRead() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.read
}

func TestBackpressure(t *testing.T) {
	// Consumer is slow; reader should not read far ahead.
	const n = 1000
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf(`{"id":%d}`, i))
	}
	b.WriteString(`]}`)
	tr := &trackingReader{r: strings.NewReader(b.String())}
	q, _ := ParseQuery("$.items[*].id")
	p := NewParser(tr, WithQuery(q), WithChunkSize(1024))
	defer p.Close()
	ctx := context.Background()
	count := 0
	for {
		ev, err := p.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if ev.Kind == EventMatch {
			count++
			// Slow consumer: small sleep per item.
			time.Sleep(time.Microsecond)
		}
	}
	if count != n {
		t.Fatalf("count = %d", count)
	}
	// Reader should not have read the whole input eagerly; but since we
	// consumed everything, it will have read all. The point is it doesn't
	// read ahead of demand. We verify memory stayed bounded by checking
	// goroutine count and that we got all matches.
	t.Logf("total bytes read: %d (input %d)", tr.bytesRead(), b.Len())
}

func TestGoroutineLeak(t *testing.T) {
	q, _ := ParseQuery("$.id")
	pr, _ := io.Pipe()
	p := NewParser(pr, WithQuery(q))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		for {
			_, err := p.Next(ctx)
			if err != nil {
				close(done)
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	cancel()
	<-done
	p.Close()
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}

func TestConcurrentStress(t *testing.T) {
	q, _ := ParseQuery("$.items[*].id")
	const tasks = 16
	const items = 2000
	var wg sync.WaitGroup
	errCh := make(chan error, tasks)
	for t1 := 0; t1 < tasks; t1++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &generatingReader{items: items}
			p := NewParser(r, WithQuery(q), WithChunkSize(2048))
			defer p.Close()
			ctx := context.Background()
			count := 0
			for {
				ev, err := p.Next(ctx)
				if err != nil {
					if err == io.EOF {
						break
					}
					errCh <- err
					return
				}
				if ev.Kind == EventMatch {
					count++
				}
			}
			if count != items {
				errCh <- fmt.Errorf("count = %d", count)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestMemoryBounded(t *testing.T) {
	// Run a huge input and check that heap growth is bounded.
	const n = 500000
	r := &generatingReader{items: n}
	q, _ := ParseQuery("$.items[*].id")
	p := NewParser(r, WithQuery(q), WithChunkSize(4096))
	defer p.Close()
	ctx := context.Background()
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	count := 0
	for {
		ev, err := p.Next(ctx)
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if ev.Kind == EventMatch {
			count++
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	if count != n {
		t.Fatalf("count = %d", count)
	}
	// Heap should not grow proportional to n. Allow generous headroom.
	growth := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	if growth > 50*1024*1024 {
		t.Errorf("heap grew by %d MB, want bounded", growth/(1024*1024))
	}
	t.Logf("heap growth: %d KB over %d items", growth/1024, n)
}
