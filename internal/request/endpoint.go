package request

import (
	"net/http"
	"regexp"
	"strings"

	"go.uber.org/ratelimit"
)

// EndpointLimiter associates a rate limiter with a specific HTTP endpoint pattern.
type EndpointLimiter struct {
	method  string         // HTTP method (GET, POST, etc.) or "*" for any
	pattern *regexp.Regexp // Compiled regex pattern for URL path
	limiter ratelimit.Limiter
}

// NewEndpointLimiter creates a new endpoint-specific rate limiter.
//
// Parameters:
//   - method: HTTP method ("GET", "POST", etc.) or "*" for any method
//   - pattern: Regex pattern to match against request URL path
//   - limiter: Rate limiter to apply to matching requests
//
// Example:
//   el := NewEndpointLimiter("POST", `^/api/torrents/createtorrent`, compositeRateLimiter)
func NewEndpointLimiter(method, pattern string, limiter ratelimit.Limiter) *EndpointLimiter {
	// Compile the pattern
	regex, err := regexp.Compile(pattern)
	if err != nil {
		// If pattern is invalid, return nil
		return nil
	}

	return &EndpointLimiter{
		method:  strings.ToUpper(method),
		pattern: regex,
		limiter: limiter,
	}
}

// Matches checks if a request matches this endpoint limiter's criteria.
func (e *EndpointLimiter) Matches(req *http.Request) bool {
	// Check method match
	if e.method != "*" && req.Method != e.method {
		return false
	}

	// Check path match
	return e.pattern.MatchString(req.URL.Path)
}

// GetLimiter returns the rate limiter for this endpoint.
func (e *EndpointLimiter) GetLimiter() ratelimit.Limiter {
	return e.limiter
}

// EndpointLimiterRegistry manages multiple endpoint-specific rate limiters.
type EndpointLimiterRegistry struct {
	limiters []*EndpointLimiter
}

// NewEndpointLimiterRegistry creates a new registry for endpoint-specific rate limiters.
func NewEndpointLimiterRegistry() *EndpointLimiterRegistry {
	return &EndpointLimiterRegistry{
		limiters: make([]*EndpointLimiter, 0),
	}
}

// Register adds a new endpoint limiter to the registry.
func (r *EndpointLimiterRegistry) Register(method, pattern string, limiter ratelimit.Limiter) error {
	el := NewEndpointLimiter(method, pattern, limiter)
	if el == nil {
		return nil // Skip invalid patterns
	}

	r.limiters = append(r.limiters, el)
	return nil
}

// GetLimiter finds and returns the rate limiter for a specific request.
// It returns the first matching endpoint limiter, or nil if no match is found.
func (r *EndpointLimiterRegistry) GetLimiter(req *http.Request) ratelimit.Limiter {
	for _, el := range r.limiters {
		if el.Matches(req) {
			return el.GetLimiter()
		}
	}
	return nil
}

// Size returns the number of registered endpoint limiters.
func (r *EndpointLimiterRegistry) Size() int {
	return len(r.limiters)
}
