package balancer

import (
	"sync"
	"testing"
)

// TestNextSequentialOrder checks strict round-robin sequence and wrap-around.
func TestNextSequentialOrder(t *testing.T) {
	var b Balancer
	n := 3

	want := []int{0, 1, 2, 0, 1, 2, 0}
	for i, w := range want {
		if got := b.Next(n); got != w {
			t.Errorf("call %d: Next() = %d, want %d", i, got, w)
		}
	}
}

func TestNextSingleBackend(t *testing.T) {
	var b Balancer
	for i := 0; i < 5; i++ {
		if got := b.Next(1); got != 0 {
			t.Errorf("call %d: Next(1) = %d, want 0", i, got)
		}
	}
}

// TestNextPanicsOnZeroBackend guards the call-site programming error.
func TestNextPanicsOnZeroBackend(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Next(0) did not panic")
		}
	}()
	var b Balancer
	b.Next(0)
}

// TestNextConcurrentDistribution hammers Next from many goroutines and checks
// that every backend is picked and the counts stay balanced. With N goroutines
// doing N requests each over K backends, per-backend counts must be within 1
// of the perfect split — a lost update would drift the counter visibly.
func TestNextConcurrentDistribution(t *testing.T) {
	var b Balancer
	const backends = 5
	const goroutines = 50
	const perGoroutine = 200 // 10_000 requests total

	counts := make([]int, backends)

	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]int, backends)
			for i := 0; i < perGoroutine; i++ {
				local[b.Next(backends)]++
			}
			mu.Lock()
			defer mu.Unlock()
			for k, v := range local {
				counts[k] += v
			}
		}()
	}
	wg.Wait()

	total := goroutines * perGoroutine
	sum := 0
	for _, c := range counts {
		sum += c
	}
	if sum != total {
		t.Fatalf("total picks = %d, want %d", sum, total)
	}
	per := total / backends // 2000
	for k, c := range counts {
		if c < per || c > per+1 {
			t.Errorf("backend %d picked %d times; want %d..%d", k, c, per, per+1)
		}
	}
}

// TestNextConcurrentWrap exercises steady-state wrap-around concurrently:
// every picked index must stay inside [0, n) as the counter keeps rolling.
func TestNextConcurrentWrap(t *testing.T) {
	var b Balancer
	const backends = 4
	const goroutines = 20
	const perGoroutine = 1000

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				if got := b.Next(backends); got < 0 || got >= backends {
					t.Errorf("Next() = %d, out of range [0,%d)", got, backends)
					return
				}
			}
		}()
	}
	wg.Wait()
}
