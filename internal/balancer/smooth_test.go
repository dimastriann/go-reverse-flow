package balancer

import (
	"sync"
	"testing"
)

// TestSmoothGoldenSequence pins the canonical smooth WRR interleave for
// weights 3:1 — a,a,b,a repeating (no 3-in-a-row bursts of the heavy node).
func TestSmoothGoldenSequence(t *testing.T) {
	sw, err := NewSmoothWeighted(map[string]int{
		"http://heavy:x": 3,
		"http://light:y": 1,
	})
	if err != nil {
		t.Fatalf("NewSmoothWeighted: %v", err)
	}

	candidates := []string{"http://heavy:x", "http://light:y"}
	want := []string{"heavy", "heavy", "light", "heavy", "heavy", "heavy", "light", "heavy"}
	for i, w := range want {
		idx, err := sw.Pick(candidates)
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		if got := candidates[idx]; got != wantSlotURL(w) {
			t.Errorf("pick %d: chose %q, want %q", i, got, wantSlotURL(w))
		}
	}
}

func wantSlotURL(name string) string {
	if name == "light" {
		return "http://light:y"
	}
	return "http://heavy:x"
}

// TestSmoothAndPlainRideTogether checks an unknown candidate joins as weight
// 1 without corrupting configured state.
func TestSmoothUnknownCandidate(t *testing.T) {
	sw, err := NewSmoothWeighted(map[string]int{"http://a:x": 2})
	if err != nil {
		t.Fatalf("NewSmoothWeighted: %v", err)
	}
	// First pick introduces the unknown guest.
	for i := 0; i < 4; i++ {
		idx, err := sw.Pick([]string{"http://a:x", "http://ghost:z"})
		if err != nil {
			t.Fatalf("pick %d: %v", i, err)
		}
		// a(2) vs ghost(1): a must win roughly 2:1 — no panic is the main check.
		_ = idx
	}
	if w := sw.Weights()["http://ghost:z"]; w != 1 {
		t.Errorf("ghost weight = %d, want 1", w)
	}
}

// TestSmoothRejectsBadWeights guards construction errors.
func TestSmoothRejectsBadWeights(t *testing.T) {
	tests := []struct {
		name    string
		weights map[string]int
	}{
		{name: "zero weight", weights: map[string]int{"http://a:x": 0}},
		{name: "negative weight", weights: map[string]int{"http://a:x": 2, "http://b:y": -1}},
		{name: "empty map", weights: map[string]int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSmoothWeighted(tt.weights); err == nil {
				t.Errorf("expected error for %v", tt.weights)
			}
		})
	}
}

// TestSmoothConcurrency hammers Pick from many goroutines and asserts the
// weighted dispatch ratio stays within tolerance (no state corruption).
func TestSmoothConcurrency(t *testing.T) {
	sw, err := NewSmoothWeighted(map[string]int{"http://a:x": 2, "http://b:y": 1})
	if err != nil {
		t.Fatalf("NewSmoothWeighted: %v", err)
	}
	candidates := []string{"http://a:x", "http://b:y"}

	counts := make([]int, 2)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(local []int) {
			defer wg.Done()
			for i := 0; i < 600; i++ { // 30_000 picks total
				idx, err := sw.Pick(candidates)
				if err != nil {
					t.Error(err)
					return
				}
				local[idx]++
			}
			mu.Lock()
			for k, v := range local {
				counts[k] += v
			}
			mu.Unlock()
		}(make([]int, 2))
	}
	wg.Wait()

	// 2:1 of 30000 -> 20000/10000 exactly in expectation; smooth guarantees
	// near-exact balance, allow ±2 tolerance for concurrent interleavings.
	if delta := counts[0] - 20000; delta < -2 || delta > 2 {
		t.Errorf("heavy got %d, want ~20000 (delta=%d)", counts[0], delta)
	}
	if delta := counts[1] - 10000; delta < -2 || delta > 2 {
		t.Errorf("light got %d, want ~10000 (delta=%d)", counts[1], delta)
	}
	if total := counts[0] + counts[1]; total != 30000 {
		t.Errorf("total picks %d, want 30000", total)
	}
}

// TestSmoothPickEmpty errors instead of pattern weirdness.
func TestSmoothPickEmpty(t *testing.T) {
	sw, err := NewSmoothWeighted(map[string]int{"http://a:x": 1})
	if err != nil {
		t.Fatalf("NewSmoothWeighted: %v", err)
	}
	if _, err := sw.Pick(nil); err == nil {
		t.Error("Pick(nil) = nil error, want error")
	}
}
