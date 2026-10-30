package stepanel

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"
)

func testLimiter(tokenRate, tokenBurst, userRate, userBurst, sourceRate, sourceBurst float64, capacity int) *apiTokenRateLimiter {
	return &apiTokenRateLimiter{
		byToken: newBucketCache(bucketPolicy{tokenRate, tokenBurst, capacity}),
		byUser:  newBucketCache(bucketPolicy{userRate, userBurst, capacity}),
		byIP:    newBucketCache(bucketPolicy{sourceRate, sourceBurst, capacity}),
		lastGC:  time.Now(),
	}
}

func TestAPITokenRateLimiterUsesSeparateBurstAndSustainedRate(t *testing.T) {
	limiter := testLimiter(60, 3, 600, 10, 1200, 20, 10)
	for i := 0; i < 3; i++ {
		if !limiter.allow("token", "user", "192.0.2.1") {
			t.Fatalf("initial burst request %d rejected", i+1)
		}
	}
	if limiter.allow("token", "user", "192.0.2.1") {
		t.Fatal("request beyond initial burst was allowed")
	}
	// The token refills at 60/minute, independently of its burst capacity.
	limiter.mu.Lock()
	entry := limiter.byToken.items[scopedLimitKey("token", "token")].Value.(*bucketEntry)
	entry.bucket.lastRefill = time.Now().Add(-2 * time.Second)
	limiter.mu.Unlock()
	if !limiter.allow("token", "user", "192.0.2.1") {
		t.Fatal("request was not admitted after sustained-rate refill")
	}
}

func TestAPITokenLimiterAdmitsNewIdentityWhenTokenMapFull(t *testing.T) {
	limiter := testLimiter(600, 2, 6000, 10, 12000, 20, 2)
	if !limiter.allow("old-1", "user-1", "192.0.2.1") || !limiter.allow("old-2", "user-2", "192.0.2.2") {
		t.Fatal("failed to populate limiter")
	}
	if !limiter.allow("new-valid-token", "user-3", "192.0.2.3") {
		t.Fatal("valid new token was rejected at capacity")
	}
	if tracked, max := limiter.stats(); tracked != max || max != 2 {
		t.Fatalf("token buckets = (%d, %d), want bounded full cache", tracked, max)
	}
}

func TestAPITokenLimiterAdmitsConcurrentValidTokensAtCapacity(t *testing.T) {
	limiter := testLimiter(600, 2, 6000, 10, 12000, 20, 2)
	limiter.allow("fill-a", "fill-user-a", "192.0.2.1")
	limiter.allow("fill-b", "fill-user-b", "192.0.2.2")
	const count = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	var rejected []string
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if !limiter.allow(fmt.Sprintf("valid-%d", i), fmt.Sprintf("user-%d", i), fmt.Sprintf("192.0.2.%d", i+10)) {
				mu.Lock()
				rejected = append(rejected, fmt.Sprintf("valid-%d", i))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(rejected) != 0 {
		t.Fatalf("valid tokens rejected under bounded-cache churn: %v", rejected)
	}
	for _, cache := range []*bucketCache{&limiter.byToken, &limiter.byUser, &limiter.byIP} {
		if len(cache.items) > cache.policy.maxKeys {
			t.Fatalf("bucket cache grew past limit: %d > %d", len(cache.items), cache.policy.maxKeys)
		}
	}
}

func TestAPITokenLimiterEvictsLeastRecentlyUsedBucket(t *testing.T) {
	limiter := testLimiter(60, 2, 600, 10, 1200, 20, 2)
	limiter.allow("oldest", "user-old", "192.0.2.1")
	limiter.allow("recent", "user-recent", "192.0.2.2")
	// Touch the recent bucket, then ensure the oldest entry is the eviction.
	limiter.allow("recent", "user-recent", "192.0.2.2")
	limiter.allow("new", "user-new", "192.0.2.3")
	limiter.mu.Lock()
	_, oldestExists := limiter.byToken.items[scopedLimitKey("token", "oldest")]
	_, recentExists := limiter.byToken.items[scopedLimitKey("token", "recent")]
	_, newExists := limiter.byToken.items[scopedLimitKey("token", "new")]
	limiter.mu.Unlock()
	if oldestExists || !recentExists || !newExists {
		t.Fatalf("unexpected LRU membership: oldest=%v recent=%v new=%v", oldestExists, recentExists, newExists)
	}
}

func TestAPITokenLimiterAggregatesByUserAndSource(t *testing.T) {
	t.Run("account", func(t *testing.T) {
		limiter := testLimiter(600, 10, 60, 2, 1200, 20, 10)
		if !limiter.allow("token-a", "same-user", "192.0.2.1") || !limiter.allow("token-b", "same-user", "192.0.2.2") {
			t.Fatal("account burst unexpectedly rejected")
		}
		if limiter.allow("token-c", "same-user", "192.0.2.3") {
			t.Fatal("account aggregate limit was bypassed with another token")
		}
	})
	t.Run("source", func(t *testing.T) {
		limiter := testLimiter(600, 10, 6000, 20, 60, 2, 10)
		if !limiter.allow("token-a", "user-a", "192.0.2.1") || !limiter.allow("token-b", "user-b", "192.0.2.1") {
			t.Fatal("source burst unexpectedly rejected")
		}
		if limiter.allow("token-c", "user-c", "192.0.2.1") {
			t.Fatal("source aggregate limit was bypassed with another token")
		}
	})
}

func TestAPITokenLimiterGarbageCollectsInactiveEntries(t *testing.T) {
	limiter := testLimiter(600, 10, 6000, 20, 12000, 40, 100)
	limiter.allow("old-token", "old-user", "192.0.2.1")
	limiter.mu.Lock()
	for _, cache := range []*bucketCache{&limiter.byToken, &limiter.byUser, &limiter.byIP} {
		for element := cache.lru.Front(); element != nil; element = element.Next() {
			element.Value.(*bucketEntry).lastSeen = time.Now().Add(-10 * time.Minute)
		}
	}
	limiter.lastGC = time.Now().Add(-2 * time.Minute)
	limiter.mu.Unlock()
	limiter.allow("new-token", "new-user", "192.0.2.2")
	if tracked, _ := limiter.stats(); tracked != 1 {
		t.Fatalf("inactive token buckets remain: %d", tracked)
	}
}

func TestAPITokenLimiterResetAndNil(t *testing.T) {
	limiter := testLimiter(60, 1, 600, 10, 1200, 20, 10)
	if !limiter.allow("reset-me", "user", "192.0.2.1") || limiter.allow("reset-me", "user", "192.0.2.1") {
		t.Fatal("token burst behavior incorrect")
	}
	limiter.reset("reset-me")
	if !limiter.allow("reset-me", "user", "192.0.2.1") {
		t.Fatal("reset did not replenish token bucket")
	}
	var nilLimiter *apiTokenRateLimiter
	if !nilLimiter.allow("token", "user", "source") {
		t.Fatal("nil limiter should permit requests")
	}
	nilLimiter.reset("token")
}

func TestAPITokenLimiterStoresOnlyScopedDigests(t *testing.T) {
	limiter := newAPITokenRateLimiter()
	secret := "bearer-secret-that-must-not-be-a-map-key"
	if !limiter.allow(secret, "private-user", "192.0.2.10") {
		t.Fatal("initial request rejected")
	}
	key := scopedLimitKey("token", secret)
	if key == sha256.Sum256([]byte(secret)) {
		t.Fatal("limiter key is not domain-separated")
	}
	limiter.mu.Lock()
	_, found := limiter.byToken.items[key]
	limiter.mu.Unlock()
	if !found {
		t.Fatal("scoped token digest not used as cache key")
	}
}

func TestAPITokenLimiterStats(t *testing.T) {
	limiter := newAPITokenRateLimiter()
	if tracked, max := limiter.stats(); tracked != 0 || max != maxTokenKeys {
		t.Fatalf("initial stats = (%d, %d)", tracked, max)
	}
	for i := 0; i < 5; i++ {
		limiter.allow("token", "user", string(rune('a'+i)))
	}
	if tracked, max := limiter.stats(); tracked != 1 || max != maxTokenKeys {
		t.Fatalf("token stats = (%d, %d)", tracked, max)
	}
}
