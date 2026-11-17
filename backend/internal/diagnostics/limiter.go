package diagnostics

import (
	"math"
	"sync"
	"time"
)

// RateLimits are independent from the business API limits.
type RateLimits struct {
	ActorPerMinute   float64
	ActorBurst       float64
	GlobalPerMinute  float64
	GlobalBurst      float64
	MaxActors        int
	ActorIdleTimeout time.Duration
}

// DefaultRateLimits matches the v1 contract: 30 batches/minute per user with a
// burst of 10, 120 batches/minute per process with a burst of 20.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		ActorPerMinute:   30,
		ActorBurst:       10,
		GlobalPerMinute:  120,
		GlobalBurst:      20,
		MaxActors:        256,
		ActorIdleTimeout: 15 * time.Minute,
	}
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

func newBucket(now time.Time, burst float64) *bucket {
	return &bucket{tokens: burst, last: now, lastSeen: now}
}

func (b *bucket) refill(now time.Time, perMinute, burst float64) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(burst, b.tokens+elapsed*(perMinute/60))
		b.last = now
	}
	b.lastSeen = now
}

func (b *bucket) take(perMinute float64) (bool, int) {
	if b.tokens >= 1 {
		b.tokens -= 1
		return true, 0
	}
	missing := 1 - b.tokens
	seconds := int(math.Ceil(missing * 60 / perMinute))
	if seconds < 1 {
		seconds = 1
	}
	return false, seconds
}

// Limiter is a bounded token-bucket table with idle eviction.
type Limiter struct {
	mu     sync.Mutex
	cfg    RateLimits
	actors map[string]*bucket
	global *bucket
	now    func() time.Time
}

// NewLimiter creates an independent diagnostics limiter.
func NewLimiter(cfg RateLimits, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if cfg.MaxActors <= 0 {
		cfg = DefaultRateLimits()
	}
	return &Limiter{
		cfg:    cfg,
		actors: make(map[string]*bucket),
		global: newBucket(now(), cfg.GlobalBurst),
		now:    now,
	}
}

// Allow consumes one batch from both buckets. A denied request does not consume
// the other bucket's token.
func (l *Limiter) Allow(actor string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.global.refill(now, l.cfg.GlobalPerMinute, l.cfg.GlobalBurst)
	if l.global.tokens < 1 {
		missing := 1 - l.global.tokens
		seconds := int(math.Ceil(missing * 60 / 120))
		if seconds < 1 {
			seconds = 1
		}
		return false, seconds
	}
	l.cleanupLocked(now)
	entry, ok := l.actors[actor]
	if !ok {
		if len(l.actors) >= l.cfg.MaxActors {
			// Bounded denial: never grow the table just to admit a new actor.
			return false, 60
		}
		entry = newBucket(now, l.cfg.ActorBurst)
		l.actors[actor] = entry
	}
	entry.refill(now, l.cfg.ActorPerMinute, l.cfg.ActorBurst)
	allowed, retry := entry.take(l.cfg.ActorPerMinute)
	if !allowed {
		return false, retry
	}
	l.global.take(l.cfg.GlobalPerMinute)
	return true, 0
}

func (l *Limiter) cleanupLocked(now time.Time) {
	for actor, entry := range l.actors {
		if now.Sub(entry.lastSeen) > l.cfg.ActorIdleTimeout {
			delete(l.actors, actor)
		}
	}
}
