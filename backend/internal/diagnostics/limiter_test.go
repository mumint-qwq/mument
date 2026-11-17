package diagnostics

import (
	"testing"
	"time"
)

func TestLimiterPerActorAndGlobal(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewLimiter(RateLimits{
		ActorPerMinute: 2, ActorBurst: 2, GlobalPerMinute: 100, GlobalBurst: 10,
		MaxActors: 16, ActorIdleTimeout: time.Minute,
	}, func() time.Time { return now })

	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("first allow rejected")
	}
	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("second allow rejected")
	}
	if ok, retry := limiter.Allow("a"); ok || retry < 1 {
		t.Fatalf("third allow = %v retry=%d", ok, retry)
	}
	now = now.Add(61 * time.Second)
	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("refilled allow rejected")
	}
}

func TestLimiterGlobalBurstAndBound(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewLimiter(RateLimits{
		ActorPerMinute: 30, ActorBurst: 10, GlobalPerMinute: 60, GlobalBurst: 2,
		MaxActors: 3, ActorIdleTimeout: time.Minute,
	}, func() time.Time { return now })
	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("a rejected")
	}
	if ok, _ := limiter.Allow("b"); !ok {
		t.Fatal("b rejected")
	}
	if ok, retry := limiter.Allow("c"); ok || retry < 1 {
		t.Fatalf("global burst not enforced: %v %d", ok, retry)
	}
	now = now.Add(2 * time.Second)
	// Global refilled; a new actor gets a fresh bucket while the table has space.
	if ok, _ := limiter.Allow("c"); !ok {
		t.Fatal("new actor after refill rejected")
	}
}

func TestLimiterActorTableIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewLimiter(RateLimits{
		ActorPerMinute: 30, ActorBurst: 10, GlobalPerMinute: 300, GlobalBurst: 100,
		MaxActors: 2, ActorIdleTimeout: time.Minute,
	}, func() time.Time { return now })
	if ok, _ := limiter.Allow("a"); !ok {
		t.Fatal("a rejected")
	}
	if ok, _ := limiter.Allow("b"); !ok {
		t.Fatal("b rejected")
	}
	if ok, retry := limiter.Allow("c"); ok || retry < 1 {
		t.Fatalf("actor table oversubscribed: %v %d", ok, retry)
	}
	now = now.Add(61 * time.Second)
	if ok, _ := limiter.Allow("c"); !ok {
		t.Fatal("idle cleanup did not free a slot")
	}
}

func TestDedupeExpiryAndBound(t *testing.T) {
	now := time.Unix(1000, 0)
	dedupe := NewDedupe(2, time.Minute, func() time.Time { return now })
	if dedupe.Seen("a", "s", "b") {
		t.Fatal("unmarked batch reported duplicate")
	}
	dedupe.Mark("a", "s", "b")
	if !dedupe.Seen("a", "s", "b") {
		t.Fatal("marked batch not found")
	}
	now = now.Add(61 * time.Second)
	if dedupe.Seen("a", "s", "b") {
		t.Fatal("expired batch reported duplicate")
	}
	dedupe.Mark("a", "s", "b1")
	dedupe.Mark("a", "s", "b2")
	dedupe.Mark("a", "s", "b3")
	if dedupe.Len() > 2 {
		t.Fatalf("dedupe table grew to %d", dedupe.Len())
	}
}
