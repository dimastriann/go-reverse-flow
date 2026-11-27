package logging

import (
	"sync"
	"time"
)

// Entry is one recorded access-log line for the admin tail view.
type Entry struct {
	Time     time.Time     `json:"time"`
	Remote   string        `json:"remote"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Backend  string        `json:"backend"`
	Status   int           `json:"status"`
	Duration time.Duration `json:"duration"`
}

// Ring is a bounded in-memory buffer of recent access entries: newest at the
// end, oldest dropped beyond the cap. All methods are safe for concurrent
// use; readers receive copies, so callers can never alias ring internals.
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	max     int
}

// NewRing creates a buffer holding at most max entries.
func NewRing(max int) *Ring {
	if max < 1 {
		max = 1
	}
	return &Ring{max: max}
}

// add records one entry, dropping the oldest beyond the cap.
func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	if len(r.entries) > r.max {
		r.entries = r.entries[len(r.entries)-r.max:]
	}
}

// Tail returns up to n newest entries in chronological order (oldest of the
// returned window first). n <= 0 returns nothing.
func (r *Ring) Tail(n int) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if n <= 0 || len(r.entries) == 0 {
		return nil
	}
	if n > len(r.entries) {
		n = len(r.entries)
	}
	out := make([]Entry, n)
	copy(out, r.entries[len(r.entries)-n:])
	return out
}
