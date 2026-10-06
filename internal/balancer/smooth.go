// SmoothWeighted implements the nginx/HAProxy smooth weighted round-robin
// algorithm: each pick bumps every candidate's current weight by its static
// weight, chooses the largest, then subtracts the total — which interleaves
// high- and low-weight backends instead of grouping them into bursts.
//
// State is keyed by backend URL rather than slot position, so a demotion
// shrinking the candidate list never corrupts the weights. All public methods
// are safe for concurrent use.
package balancer

import (
	"fmt"
	"sync"
)

type cw struct {
	current int
	weight  int
}

// SmoothWeighted picks backends proportionally to configured weights.
type SmoothWeighted struct {
	mu    sync.Mutex
	state map[string]*cw
}

// NewSmoothWeighted builds a picker from URL->weight. Weights must be >= 1.
func NewSmoothWeighted(weights map[string]int) (*SmoothWeighted, error) {
	s := &SmoothWeighted{state: make(map[string]*cw, len(weights))}
	for url, w := range weights {
		if w < 1 {
			return nil, fmt.Errorf("balancer: weight %d for %s must be >= 1", w, url)
		}
		s.state[url] = &cw{weight: w}
	}
	if len(s.state) == 0 {
		return nil, fmt.Errorf("balancer: smooth weighted picker needs at least one backend")
	}
	return s, nil
}

// Pick chooses among the given candidate URLs and returns its index in the
// slice. Candidates unknown to the configured weights count as weight 1 (a
// checker demotion that temporarily forgets a URL stays functional).
func (s *SmoothWeighted) Pick(candidates []string) (int, error) {
	if len(candidates) == 0 {
		return -1, fmt.Errorf("balancer: pick needs at least one candidate")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	total := 0
	var bestIdx, bestWeight int
	for i, url := range candidates {
		c, ok := s.state[url]
		if !ok {
			c = &cw{weight: 1}
			s.state[url] = c
		}
		c.current += c.weight
		total += c.weight
		if c.current > bestWeight {
			bestIdx, bestWeight = i, c.current
		}
	}
	state := s.state[candidates[bestIdx]]
	state.current -= total
	return bestIdx, nil
}

// Weights exposes a copy of the configured weights for diagnostics.
func (s *SmoothWeighted) Weights() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.state))
	for url, c := range s.state {
		out[url] = c.weight
	}
	return out
}
