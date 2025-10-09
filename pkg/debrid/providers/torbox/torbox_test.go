package torbox

import (
	"net/http"
	"testing"

	"github.com/sirrobot01/decypharr/internal/request"
)

// Removed TestTorboxRateLimitingConfiguration due to global config dependency

func TestTorboxEndpointLimiterMatching(t *testing.T) {
	// Test that endpoint patterns match correctly
	tests := []struct {
		method   string
		path     string
		expected bool
		endpoint string
	}{
		{
			method:   "POST",
			path:     "/v1/api/torrents/createtorrent",
			expected: true,
			endpoint: "createtorrent",
		},
		{
			method:   "GET",
			path:     "/v1/api/torrents/requestdl",
			expected: true,
			endpoint: "requestdl",
		},
		{
			method:   "GET",
			path:     "/v1/api/torrents/mylist",
			expected: true,
			endpoint: "mylist",
		},
		{
			method:   "GET",
			path:     "/v1/api/torrents/checkcached",
			expected: true,
			endpoint: "checkcached",
		},
		{
			method:   "DELETE",
			path:     "/v1/api/torrents/controltorrent/123",
			expected: true,
			endpoint: "controltorrent",
		},
		{
			method:   "GET",
			path:     "/v1/api/other/endpoint",
			expected: false,
			endpoint: "none",
		},
	}

	// Create endpoint limiter registry
	registry := request.NewEndpointLimiterRegistry()

	// Register Torbox endpoint limiters
	registry.Register("POST", `^/v1/api/torrents/createtorrent`, request.ParseMultipleRateLimits("60/hour", "10/min"))
	registry.Register("GET", `^/v1/api/torrents/requestdl`, request.ParseMultipleRateLimits("120/hour", "20/min"))
	registry.Register("GET", `^/v1/api/torrents/mylist`, request.ParseMultipleRateLimits("300/hour", "60/min"))
	registry.Register("GET", `^/v1/api/torrents/checkcached`, request.ParseMultipleRateLimits("600/hour", "120/min"))
	registry.Register("DELETE", `^/v1/api/torrents/controltorrent`, request.ParseMultipleRateLimits("60/hour", "10/min"))

	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, "https://api.torbox.app"+tt.path, nil)
		limiter := registry.GetLimiter(req)

		hasLimiter := limiter != nil
		if hasLimiter != tt.expected {
			t.Errorf("Endpoint %s %s: expected limiter=%v, got limiter=%v",
				tt.method, tt.path, tt.expected, hasLimiter)
		}

		if tt.expected && limiter != nil {
			// Verify it's a composite limiter for dual rate limits
			_, isComposite := limiter.(*request.CompositeRateLimiter)
			if !isComposite {
				t.Errorf("Endpoint %s %s: expected CompositeRateLimiter, got %T",
					tt.method, tt.path, limiter)
			}
		}
	}
}

func TestTorboxRateLimitValues(t *testing.T) {
	// Test that rate limit parsing works correctly
	tests := []struct {
		rateStr  string
		expected bool
	}{
		{"60/hour", true},
		{"10/min", true},
		{"120/hour", true},
		{"20/min", true},
		{"5/sec", true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		limiter := request.ParseRateLimit(tt.rateStr)
		hasLimiter := limiter != nil

		if hasLimiter != tt.expected {
			t.Errorf("Rate limit '%s': expected valid=%v, got valid=%v",
				tt.rateStr, tt.expected, hasLimiter)
		}
	}
}

func TestTorboxCompositeRateLimits(t *testing.T) {
	// Test composite rate limiter creation
	limiter := request.ParseMultipleRateLimits("60/hour", "10/min")

	if limiter == nil {
		t.Fatal("Expected non-nil composite rate limiter")
	}

	// Verify it's a composite limiter
	_, isComposite := limiter.(*request.CompositeRateLimiter)
	if !isComposite {
		t.Errorf("Expected CompositeRateLimiter, got %T", limiter)
	}
}
