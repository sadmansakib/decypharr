package request

import (
	"testing"
	"time"

	"go.uber.org/ratelimit"
)

func TestNewCompositeRateLimiter_SingleLimiter(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	composite := NewCompositeRateLimiter(limiter)

	// Should return the limiter directly, not wrapped
	if composite != limiter {
		t.Error("Expected single limiter to be returned directly")
	}
}

func TestNewCompositeRateLimiter_MultipleLimiters(t *testing.T) {
	limiter1 := ParseRateLimit("10/sec")
	limiter2 := ParseRateLimit("60/min")

	composite := NewCompositeRateLimiter(limiter1, limiter2)

	if composite == nil {
		t.Fatal("Expected composite limiter, got nil")
	}

	// Type assertion to check it's a composite limiter
	_, ok := composite.(*CompositeRateLimiter)
	if !ok {
		t.Error("Expected CompositeRateLimiter type")
	}
}

func TestNewCompositeRateLimiter_NilLimiters(t *testing.T) {
	composite := NewCompositeRateLimiter(nil, nil)

	if composite != nil {
		t.Error("Expected nil when all limiters are nil")
	}
}

func TestNewCompositeRateLimiter_MixedNilLimiters(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	composite := NewCompositeRateLimiter(nil, limiter, nil)

	// Should filter out nils and return the single valid limiter
	if composite != limiter {
		t.Error("Expected single valid limiter to be returned directly")
	}
}

func TestCompositeRateLimiter_EnforcesBothLimits(t *testing.T) {
	// Create two limiters: 5/sec and 10/min
	// In 1 second, we should be limited by the more restrictive one
	limiter1 := ratelimit.New(5, ratelimit.Per(time.Second))
	limiter2 := ratelimit.New(10, ratelimit.Per(time.Minute))

	composite := NewCompositeRateLimiter(limiter1, limiter2).(*CompositeRateLimiter)

	// Take 5 tokens quickly - should be allowed by both limits
	start := time.Now()
	for i := 0; i < 5; i++ {
		composite.Take()
	}
	duration := time.Since(start)

	// Should complete quickly (within 100ms) as we're under both limits
	if duration > 100*time.Millisecond {
		t.Errorf("Expected quick completion, took %v", duration)
	}
}

func TestCompositeRateLimiter_BlocksOnMostRestrictive(t *testing.T) {
	// Create a very restrictive limiter: 1/sec
	// And a less restrictive one: 100/min
	limiter1 := ratelimit.New(1, ratelimit.Per(time.Second))
	limiter2 := ratelimit.New(100, ratelimit.Per(time.Minute))

	composite := NewCompositeRateLimiter(limiter1, limiter2).(*CompositeRateLimiter)

	// Take first token
	composite.Take()

	// Take second token - should block for about 1 second
	start := time.Now()
	composite.Take()
	duration := time.Since(start)

	// Should have blocked for approximately 1 second (allow some tolerance)
	if duration < 900*time.Millisecond || duration > 1100*time.Millisecond {
		t.Errorf("Expected ~1s block, got %v", duration)
	}
}

func TestParseMultipleRateLimits_ValidInputs(t *testing.T) {
	limiter := ParseMultipleRateLimits("60/hour", "10/min")

	if limiter == nil {
		t.Fatal("Expected valid composite limiter")
	}

	composite, ok := limiter.(*CompositeRateLimiter)
	if !ok {
		t.Fatal("Expected CompositeRateLimiter type")
	}

	if len(composite.limiters) != 2 {
		t.Errorf("Expected 2 limiters, got %d", len(composite.limiters))
	}
}

func TestParseMultipleRateLimits_SingleValid(t *testing.T) {
	limiter := ParseMultipleRateLimits("60/hour")

	if limiter == nil {
		t.Fatal("Expected valid limiter")
	}

	// Should return single limiter directly, not wrapped
	_, ok := limiter.(*CompositeRateLimiter)
	if ok {
		t.Error("Expected single limiter, not composite")
	}
}

func TestParseMultipleRateLimits_InvalidInputs(t *testing.T) {
	limiter := ParseMultipleRateLimits("invalid", "also-invalid")

	if limiter != nil {
		t.Error("Expected nil for invalid inputs")
	}
}

func TestParseMultipleRateLimits_MixedValid(t *testing.T) {
	limiter := ParseMultipleRateLimits("60/hour", "invalid", "10/min")

	if limiter == nil {
		t.Fatal("Expected valid composite limiter")
	}

	composite, ok := limiter.(*CompositeRateLimiter)
	if !ok {
		t.Fatal("Expected CompositeRateLimiter type")
	}

	// Should filter out invalid and keep 2 valid limiters
	if len(composite.limiters) != 2 {
		t.Errorf("Expected 2 valid limiters, got %d", len(composite.limiters))
	}
}

func TestCompositeRateLimiter_TorboxScenario(t *testing.T) {
	// Simulate Torbox dual limits: 60/hour AND 10/min
	hourly := ratelimit.New(60, ratelimit.Per(time.Hour))
	perMinute := ratelimit.New(10, ratelimit.Per(time.Minute))

	composite := NewCompositeRateLimiter(hourly, perMinute).(*CompositeRateLimiter)

	// Take 10 tokens quickly - should be allowed
	start := time.Now()
	for i := 0; i < 10; i++ {
		composite.Take()
	}
	duration := time.Since(start)

	// Should complete quickly (well under 1 minute)
	if duration > 500*time.Millisecond {
		t.Errorf("Expected quick completion for 10 requests, took %v", duration)
	}

	// The 11th request should block (hit the per-minute limit)
	start = time.Now()
	composite.Take()
	duration = time.Since(start)

	// Should block for approximately 6 seconds (60s / 10 requests)
	if duration < 5*time.Second {
		t.Errorf("Expected blocking for ~6s, got %v", duration)
	}
}

func BenchmarkCompositeRateLimiter_Single(b *testing.B) {
	limiter := ParseRateLimit("1000000/sec") // Very high limit to measure overhead
	composite := NewCompositeRateLimiter(limiter)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		composite.Take()
	}
}

func BenchmarkCompositeRateLimiter_Dual(b *testing.B) {
	limiter1 := ratelimit.New(1000000, ratelimit.Per(time.Second))
	limiter2 := ratelimit.New(10000000, ratelimit.Per(time.Minute))
	composite := NewCompositeRateLimiter(limiter1, limiter2)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		composite.Take()
	}
}

func BenchmarkRawRateLimiter(b *testing.B) {
	limiter := ratelimit.New(1000000, ratelimit.Per(time.Second))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		limiter.Take()
	}
}
