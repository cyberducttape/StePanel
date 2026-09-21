package main

import (
	"testing"
	"time"
)

func TestAPITokenRateLimiter(t *testing.T) {
	limiter := newAPITokenRateLimiter()

	t.Run("allows requests within limit", func(t *testing.T) {
		token := "test-token-1"

		// Should allow 600 requests in one minute
		for i := 0; i < 100; i++ {
			if !limiter.allow(token) {
				t.Fatalf("request %d should be allowed", i)
			}
		}
	})

	t.Run("rate limits after quota exceeded", func(t *testing.T) {
		token := "test-token-2"

		// Consume all tokens
		for i := 0; i < 600; i++ {
			limiter.allow(token)
		}

		// Next request should be rate limited
		if limiter.allow(token) {
			t.Fatal("should be rate limited after quota exceeded")
		}
	})

	t.Run("refills tokens over time", func(t *testing.T) {
		token := "test-token-3"

		// Consume all tokens
		for i := 0; i < 600; i++ {
			limiter.allow(token)
		}

		// Wait for tokens to refill (600 per minute = 10 per second = 1 per 100ms)
		time.Sleep(150 * time.Millisecond)

		// Should have at least 1-2 new tokens after 150ms (need ~60ms per token)
		allowed := 0
		for i := 0; i < 5; i++ {
			if limiter.allow(token) {
				allowed++
			}
		}

		if allowed < 1 {
			t.Fatalf("expected at least 1 refilled token after 150ms, got %d", allowed)
		}
	})

	t.Run("tracks multiple tokens", func(t *testing.T) {
		token1 := "token-one"
		token2 := "token-two"

		// Consume tokens for token1
		for i := 0; i < 100; i++ {
			limiter.allow(token1)
		}

		// token2 should still have full quota
		for i := 0; i < 100; i++ {
			if !limiter.allow(token2) {
				t.Fatalf("token-two should have independent quota")
			}
		}
	})

	t.Run("garbage collects unused tokens", func(t *testing.T) {
		limiter2 := newAPITokenRateLimiter()

		// Manually populate state with old tokens by direct access
		limiter2.mu.Lock()
		now := time.Now()
		oldTime := now.Add(-10 * time.Minute) // Older than 5-minute threshold
		for i := 0; i < 100; i++ {
			limiter2.state["token-"+string(rune(i))] = &tokenBucket{
				tokens:     100.0,
				lastRefill: oldTime,
			}
		}
		limiter2.lastGC = now.Add(-2 * time.Minute) // Trigger GC on next allow()
		limiter2.mu.Unlock()

		// Verify we have 100 tokens
		tracked, _ := limiter2.stats()
		if tracked != 100 {
			t.Fatalf("expected 100 tracked tokens, got %d", tracked)
		}

		// Add a new token - this should trigger GC
		limiter2.allow("new-token")

		// GC should have cleaned up old tokens (all the old ones should be gone)
		// We should have just the new-token and possibly 1-2 other recently added
		tracked, _ = limiter2.stats()
		if tracked > 10 {
			t.Fatalf("GC should have removed old tokens, still have %d (expected <10)", tracked)
		}
	})

	t.Run("rejects new tokens when at capacity", func(t *testing.T) {
		limiter3 := newAPITokenRateLimiter()
		limiter3.maxKeys = 10 // Small limit for testing

		// Fill up to capacity
		for i := 0; i < 10; i++ {
			limiter3.allow("token-" + string(rune(i)))
		}

		// Try to add one more token when at capacity
		if limiter3.allow("token-new") {
			t.Fatal("should reject new token when at capacity")
		}
	})

	t.Run("reset clears token state", func(t *testing.T) {
		token := "test-token-reset"
		limiter4 := newAPITokenRateLimiter()

		// Consume all tokens
		for i := 0; i < 600; i++ {
			limiter4.allow(token)
		}

		// Should be rate limited
		if limiter4.allow(token) {
			t.Fatal("should be rate limited before reset")
		}

		// Reset token
		limiter4.reset(token)

		// Should have full quota again
		if !limiter4.allow(token) {
			t.Fatal("should have full quota after reset")
		}
	})

	t.Run("handles nil limiter gracefully", func(t *testing.T) {
		var nilLimiter *apiTokenRateLimiter

		// Should allow when limiter is nil (disabled)
		if !nilLimiter.allow("any-token") {
			t.Fatal("nil limiter should allow requests")
		}

		nilLimiter.reset("any-token") // Should not panic
	})
}

func TestAPITokenLimiterStats(t *testing.T) {
	limiter := newAPITokenRateLimiter()

	tracked, max := limiter.stats()
	if tracked != 0 || max != maxTokenKeys {
		t.Fatalf("expected (0, %d), got (%d, %d)", maxTokenKeys, tracked, max)
	}

	// Create some tokens
	for i := 0; i < 5; i++ {
		limiter.allow("token-" + string(rune(i)))
	}

	tracked, max = limiter.stats()
	if tracked != 5 {
		t.Fatalf("expected 5 tracked tokens, got %d", tracked)
	}
}
