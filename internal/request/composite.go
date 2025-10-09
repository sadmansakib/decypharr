package request

import (
	"time"

	"go.uber.org/ratelimit"
)

// CompositeRateLimiter enforces multiple rate limits simultaneously.
// It takes a token from each limiter sequentially, ensuring all limits are respected.
// The actual blocking time is determined by the most restrictive limiter.
type CompositeRateLimiter struct {
	limiters []ratelimit.Limiter
}

// NewCompositeRateLimiter creates a new composite rate limiter from multiple limiters.
// If no limiters are provided or all are nil, it returns nil.
func NewCompositeRateLimiter(limiters ...ratelimit.Limiter) ratelimit.Limiter {
	// Filter out nil limiters
	validLimiters := make([]ratelimit.Limiter, 0, len(limiters))
	for _, limiter := range limiters {
		if limiter != nil {
			validLimiters = append(validLimiters, limiter)
		}
	}

	// If no valid limiters, return nil
	if len(validLimiters) == 0 {
		return nil
	}

	// If only one limiter, return it directly (optimization)
	if len(validLimiters) == 1 {
		return validLimiters[0]
	}

	return &CompositeRateLimiter{
		limiters: validLimiters,
	}
}

// Take blocks until all rate limiters allow a request.
// It enforces all limits sequentially, returning the latest timestamp.
// This ensures that BOTH (or all) rate limits are respected.
func (c *CompositeRateLimiter) Take() time.Time {
	var latest time.Time

	// Take a token from each limiter
	// The most restrictive limiter will determine the actual wait time
	for _, limiter := range c.limiters {
		t := limiter.Take()
		if t.After(latest) {
			latest = t
		}
	}

	return latest
}

// ParseMultipleRateLimits parses multiple rate limit strings and returns a composite limiter.
// This is useful for endpoints with multiple simultaneous rate limits.
//
// Example:
//   limiter := ParseMultipleRateLimits("60/hour", "10/min")
//   // This creates a limiter that enforces BOTH limits
func ParseMultipleRateLimits(rateStrs ...string) ratelimit.Limiter {
	limiters := make([]ratelimit.Limiter, 0, len(rateStrs))

	for _, rateStr := range rateStrs {
		limiter := ParseRateLimit(rateStr)
		if limiter != nil {
			limiters = append(limiters, limiter)
		}
	}

	return NewCompositeRateLimiter(limiters...)
}
