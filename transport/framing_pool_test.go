package transport

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

// Pooled scratch buffers are only safe if every borrower fully copies out
// of them before returning them to the pool. Run many distinct encode/
// decode cycles back to back (so buffers actually get reused) and check
// each result is exactly right -- this is what would catch a buffer
// returned to the pool (and overwritten by the next borrower) too early.
func TestBatchPoolReuseDoesNotCorruptData(t *testing.T) {
	for i := 0; i < 500; i++ {
		pkts := make([][]byte, 3)
		for j := range pkts {
			pkts[j] = bytes.Repeat([]byte{byte(i), byte(j)}, 10+i%50)
		}
		wire := encodeBatch(pkts)
		got, err := decodeBatch(wire)
		if err != nil {
			t.Fatalf("iteration %d: decodeBatch: %v", i, err)
		}
		if len(got) != len(pkts) {
			t.Fatalf("iteration %d: got %d packets, want %d", i, len(got), len(pkts))
		}
		for j := range pkts {
			if !bytes.Equal(got[j], pkts[j]) {
				t.Fatalf("iteration %d packet %d: corrupted (pool reuse bug)", i, j)
			}
		}
	}
}

// Same idea, but concurrent: sync.Pool itself is safe for concurrent use,
// but this also has to prove the surrounding get/use/put logic doesn't
// hand out an in-flight buffer to two goroutines at once.
func TestBatchPoolReuseConcurrentSafe(t *testing.T) {
	var wg sync.WaitGroup
	errCh := make(chan error, 50)
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				want := bytes.Repeat([]byte{byte(g)}, 20+i)
				wire := encodeBatch([][]byte{want})
				got, err := decodeBatch(wire)
				if err != nil {
					errCh <- fmt.Errorf("goroutine %d iter %d: %v", g, i, err)
					return
				}
				if len(got) != 1 || !bytes.Equal(got[0], want) {
					errCh <- fmt.Errorf("goroutine %d iter %d: corrupted", g, i)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func BenchmarkEncodeDecodeBatch(b *testing.B) {
	pkts := make([][]byte, 16)
	for i := range pkts {
		pkts[i] = bytes.Repeat([]byte{byte(i)}, 200)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wire := encodeBatch(pkts)
		if _, err := decodeBatch(wire); err != nil {
			b.Fatal(err)
		}
	}
}
