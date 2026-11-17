package diagnostics

import (
	"sync"
	"time"
)

// Dedupe remembers successfully queued batches for a bounded window. The key
// contains the real actor ref plus client session and batch ref; raw usernames
// and payloads are never stored.
type Dedupe struct {
	mu      sync.Mutex
	entries map[string]time.Time
	max     int
	ttl     time.Duration
	now     func() time.Time
}

// NewDedupe creates a bounded dedupe table (1024 entries, 10 minutes).
func NewDedupe(max int, ttl time.Duration, now func() time.Time) *Dedupe {
	if max <= 0 {
		max = 1024
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &Dedupe{entries: make(map[string]time.Time), max: max, ttl: ttl, now: now}
}

func dedupeKey(actorRef, session, batchRef string) string {
	return actorRef + "|" + session + "|" + batchRef
}

// Seen reports whether this exact batch was already queued.
func (d *Dedupe) Seen(actorRef, session, batchRef string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.cleanupLocked(now)
	expiry, ok := d.entries[dedupeKey(actorRef, session, batchRef)]
	return ok && now.Before(expiry)
}

// Mark records a batch only after it was accepted into the writer queue.
func (d *Dedupe) Mark(actorRef, session, batchRef string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.cleanupLocked(now)
	if len(d.entries) >= d.max {
		var oldestKey string
		var oldest time.Time
		for key, expiry := range d.entries {
			if oldestKey == "" || expiry.Before(oldest) {
				oldestKey = key
				oldest = expiry
			}
		}
		if oldestKey != "" {
			delete(d.entries, oldestKey)
		}
	}
	d.entries[dedupeKey(actorRef, session, batchRef)] = now.Add(d.ttl)
}

func (d *Dedupe) cleanupLocked(now time.Time) {
	for key, expiry := range d.entries {
		if !now.Before(expiry) {
			delete(d.entries, key)
		}
	}
}

func (d *Dedupe) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.entries)
}
