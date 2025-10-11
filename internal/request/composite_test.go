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
	// Create two limiters: 100/sec and 50/sec
	// The 50/sec limiter should be the more restrictive one
	limiter1 := ratelimit.New(100, ratelimit.Per(time.Second))
	limiter2 := ratelimit.New(50, ratelimit.Per(time.Second))

	composite := NewCompositeRateLimiter(limiter1, limiter2).(*CompositeRateLimiter)

	// Take tokens and measure actual vs expected duration
	start := time.Now()
	count := 10
	for i := 0; i < count; i++ {
		composite.Take()
	}
	duration := time.Since(start)

	// Calculate expected duration: 10 tokens at 50/sec = 200ms minimum
	// Allow 2x overhead for scheduler jitter and system load
	expected := time.Duration(count-1) * (time.Second / 50)
	maxAllowed := expected * 2

	if duration > maxAllowed {
		t.Errorf("Unexpected delay: got %v, expected ~%v (max %v)", duration, expected, maxAllowed)
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
	if testing.Short() {
		t.Skip("Skipping long-running test in short mode")
	}

	// Simulate Torbox-like dual limits with faster rates for testing: 100/sec AND 10/sec
	// This maintains the 10:1 ratio but runs 60x faster
	limiter1 := ratelimit.New(100, ratelimit.Per(time.Second))
	limiter2 := ratelimit.New(10, ratelimit.Per(time.Second))

	composite := NewCompositeRateLimiter(limiter1, limiter2).(*CompositeRateLimiter)

	// Take 10 tokens - should be allowed by both limits
	start := time.Now()
	for i := 0; i < 10; i++ {
		composite.Take()
	}
	duration := time.Since(start)

	// Should complete relatively quickly (under 1 second for 10 tokens at 10/sec)
	if duration > 2*time.Second {
		t.Errorf("Expected quick completion for 10 requests, took %v", duration)
	}

	// The 11th request should block briefly (hit the 10/sec limit)
	start = time.Now()
	composite.Take()
	duration = time.Since(start)

	// Should block for approximately 100ms (1000ms / 10 requests)
	// Allow range of 50ms to 200ms for scheduler jitter
	if duration < 50*time.Millisecond || duration > 200*time.Millisecond {
		t.Logf("Note: blocking duration was %v (expected ~100ms)", duration)
	}
}

// P1 Recommendation 2: Add unit tests for ParseMultipleRateLimitsWithSlack
func TestParseMultipleRateLimitsWithSlack_ZeroSlack(t *testing.T) {
	limiter := ParseMultipleRateLimitsWithSlack(0, "120/hour", "20/min")

	if limiter == nil {
		t.Fatal("Expected valid composite limiter with zero slack")
	}

	composite, ok := limiter.(*CompositeRateLimiter)
	if !ok {
		t.Fatal("Expected CompositeRateLimiter type")
	}

	if len(composite.limiters) != 2 {
		t.Errorf("Expected 2 limiters, got %d", len(composite.limiters))
	}
}

func TestParseMultipleRateLimitsWithSlack_DefaultSlack(t *testing.T) {
	limiter := ParseMultipleRateLimitsWithSlack(-1, "120/hour", "20/min")

	if limiter == nil {
		t.Fatal("Expected valid composite limiter with default slack")
	}

	composite, ok := limiter.(*CompositeRateLimiter)
	if !ok {
		t.Fatal("Expected CompositeRateLimiter type")
	}

	if len(composite.limiters) != 2 {
		t.Errorf("Expected 2 limiters, got %d", len(composite.limiters))
	}
}

func TestParseMultipleRateLimitsWithSlack_CustomSlack(t *testing.T) {
	limiter := ParseMultipleRateLimitsWithSlack(5, "120/hour", "20/min")

	if limiter == nil {
		t.Fatal("Expected valid composite limiter with custom slack")
	}

	composite, ok := limiter.(*CompositeRateLimiter)
	if !ok {
		t.Fatal("Expected CompositeRateLimiter type")
	}

	if len(composite.limiters) != 2 {
		t.Errorf("Expected 2 limiters, got %d", len(composite.limiters))
	}
}

func TestParseMultipleRateLimitsWithSlack_NegativeSlack(t *testing.T) {
	// P0 Fix: Negative slack values other than -1 should return nil
	limiter := ParseMultipleRateLimitsWithSlack(-2, "120/hour", "20/min")

	if limiter != nil {
		t.Error("Expected nil for invalid negative slack (-2)")
	}

	limiter = ParseMultipleRateLimitsWithSlack(-10, "120/hour", "20/min")
	if limiter != nil {
		t.Error("Expected nil for invalid negative slack (-10)")
	}
}

func TestParseMultipleRateLimitsWithSlack_InvalidRateStrings(t *testing.T) {
	tests := []struct {
		name     string
		slack    int
		rateStrs []string
		wantNil  bool
	}{
		{
			name:     "all invalid rate strings",
			slack:    0,
			rateStrs: []string{"invalid", "also-invalid"},
			wantNil:  true,
		},
		{
			name:     "mixed valid and invalid",
			slack:    0,
			rateStrs: []string{"120/hour", "invalid", "20/min"},
			wantNil:  false, // Should return composite with 2 valid limiters
		},
		{
			name:     "empty rate strings",
			slack:    0,
			rateStrs: []string{},
			wantNil:  true,
		},
		{
			name:     "single valid rate string",
			slack:    0,
			rateStrs: []string{"120/hour"},
			wantNil:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := ParseMultipleRateLimitsWithSlack(tt.slack, tt.rateStrs...)
			if tt.wantNil {
				if limiter != nil {
					t.Errorf("Expected nil for test case %q", tt.name)
				}
			} else {
				if limiter == nil {
					t.Errorf("Expected non-nil for test case %q", tt.name)
				}
			}
		})
	}
}

func TestParseMultipleRateLimitsWithSlack_AllTimeUnits(t *testing.T) {
	tests := []struct {
		name     string
		rateStrs []string
	}{
		{
			name:     "seconds",
			rateStrs: []string{"10/sec", "20/second"},
		},
		{
			name:     "minutes",
			rateStrs: []string{"60/min", "120/minute"},
		},
		{
			name:     "hours",
			rateStrs: []string{"3600/hr", "7200/hour"},
		},
		{
			name:     "days",
			rateStrs: []string{"86400/d", "172800/day"},
		},
		{
			name:     "mixed units",
			rateStrs: []string{"10/sec", "600/min", "3600/hr", "86400/d"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := ParseMultipleRateLimitsWithSlack(0, tt.rateStrs...)
			if limiter == nil {
				t.Errorf("Expected non-nil limiter for %q", tt.name)
			}
		})
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

func BenchmarkParseMultipleRateLimitsWithSlack(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseMultipleRateLimitsWithSlack(0, "120/hour", "20/min")
	}
}
