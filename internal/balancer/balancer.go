// Package balancer picks the next backend in round-robin order.
//
// Next is safe for concurrent use: it relies on a monotonically increasing
// atomic counter instead of a lock, so hot request paths stay lock-free.
package balancer

import (
	"fmt"
	"sync/atomic"
)

// Balancer hands out backend indexes in round-robin order.
type Balancer struct {
	counter atomic.Uint64
}

// Next returns the index of the backend to use for the current request.
// n is the number of backends; Next panics if n <= 0, which indicates a
// programming error at a call site that has no backends to offer.
func (b *Balancer) Next(n int) int {
	if n <= 0 {
		panic(fmt.Sprintf("balancer: Next called with %d backends", n))
	}
	return int(b.counter.Add(1)-1) % n
}
