package stepanel

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

// apiTokenRateLimiter applies independent token, account, and source limits.
// All keys are digests so credentials and client addresses are not retained.
type apiTokenRateLimiter struct {
	mu      sync.Mutex
	byToken bucketCache
	byUser  bucketCache
	byIP    bucketCache
	lastGC  time.Time
}

type bucketPolicy struct {
	ratePerMinute float64
	burst         float64
	maxKeys       int
}

type bucketEntry struct {
	key      [sha256.Size]byte
	bucket   tokenBucket
	lastSeen time.Time
}

type bucketCache struct {
	policy bucketPolicy
	items  map[[sha256.Size]byte]*list.Element
	lru    list.List
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

const (
	tokensPerMinute    = 600.0
	tokenBurst         = 60.0
	identityPerMinute  = 6_000.0
	identityBurst      = 600.0
	sourcePerMinute    = 12_000.0
	sourceBurst        = 1_200.0
	maxTokenKeys       = 10_000
	maxIdentityKeys    = 10_000
	maxSourceKeys      = 10_000
	bucketInactiveTime = 5 * time.Minute
)

func newAPITokenRateLimiter() *apiTokenRateLimiter {
	now := time.Now()
	return &apiTokenRateLimiter{
		byToken: newBucketCache(bucketPolicy{tokensPerMinute, tokenBurst, maxTokenKeys}),
		byUser:  newBucketCache(bucketPolicy{identityPerMinute, identityBurst, maxIdentityKeys}),
		byIP:    newBucketCache(bucketPolicy{sourcePerMinute, sourceBurst, maxSourceKeys}),
		lastGC:  now,
	}
}

func newBucketCache(policy bucketPolicy) bucketCache {
	return bucketCache{policy: policy, items: make(map[[sha256.Size]byte]*list.Element)}
}

func scopedLimitKey(scope, value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(scope + "\x00" + value))
}

// allow enforces a per-token sustained rate with a smaller initial burst, plus
// aggregate limits for the authenticated account and trusted client address.
// Empty identity/source values skip that dimension rather than sharing a
// global anonymous bucket.
func (l *apiTokenRateLimiter) allow(tokenID, username, source string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) >= time.Minute {
		l.garbageCollect(now)
		l.lastGC = now
	}
	allowed := l.byToken.consume(scopedLimitKey("token", tokenID), now)
	if username != "" {
		allowed = l.byUser.consume(scopedLimitKey("user", username), now) && allowed
	}
	if source != "" {
		allowed = l.byIP.consume(scopedLimitKey("source", source), now) && allowed
	}
	return allowed
}

func (c *bucketCache) consume(key [sha256.Size]byte, now time.Time) bool {
	element := c.items[key]
	var entry *bucketEntry
	if element == nil {
		if len(c.items) >= c.policy.maxKeys {
			// Capacity pressure must not deny a valid new token. LRU eviction
			// bounds memory; account/source buckets also limit token rotation.
			oldest := c.lru.Back()
			if oldest != nil {
				delete(c.items, oldest.Value.(*bucketEntry).key)
				c.lru.Remove(oldest)
			}
		}
		entry = &bucketEntry{key: key, bucket: tokenBucket{tokens: c.policy.burst, lastRefill: now}, lastSeen: now}
		element = c.lru.PushFront(entry)
		c.items[key] = element
	} else {
		entry = element.Value.(*bucketEntry)
		entry.lastSeen = now
		c.lru.MoveToFront(element)
	}
	elapsed := now.Sub(entry.bucket.lastRefill).Seconds()
	if elapsed > 0 {
		entry.bucket.tokens = min(c.policy.burst, entry.bucket.tokens+c.policy.ratePerMinute*elapsed/60)
		entry.bucket.lastRefill = now
	}
	if entry.bucket.tokens < 1 {
		return false
	}
	entry.bucket.tokens--
	return true
}

func (l *apiTokenRateLimiter) garbageCollect(now time.Time) {
	for _, cache := range []*bucketCache{&l.byToken, &l.byUser, &l.byIP} {
		for element := cache.lru.Back(); element != nil; {
			previous := element.Prev()
			entry := element.Value.(*bucketEntry)
			if now.Sub(entry.lastSeen) > bucketInactiveTime {
				delete(cache.items, entry.key)
				cache.lru.Remove(element)
			}
			element = previous
		}
	}
}

// reset clears per-token state after revocation or another credential change.
func (l *apiTokenRateLimiter) reset(tokenID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	key := scopedLimitKey("token", tokenID)
	if element := l.byToken.items[key]; element != nil {
		delete(l.byToken.items, key)
		l.byToken.lru.Remove(element)
	}
	l.mu.Unlock()
}

// stats returns current per-token bucket count and its capacity.
func (l *apiTokenRateLimiter) stats() (int, int) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.byToken.items), l.byToken.policy.maxKeys
}
