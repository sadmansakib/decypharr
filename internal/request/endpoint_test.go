package request

import (
	"net/http"
	"testing"

	"go.uber.org/ratelimit"
)

func TestNewEndpointLimiter_ValidPattern(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	el := NewEndpointLimiter("POST", `^/api/test`, limiter)

	if el == nil {
		t.Fatal("Expected valid endpoint limiter")
	}

	if el.method != "POST" {
		t.Errorf("Expected method POST, got %s", el.method)
	}

	if el.limiter != limiter {
		t.Error("Limiter not set correctly")
	}
}

func TestNewEndpointLimiter_InvalidPattern(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	el := NewEndpointLimiter("POST", `[invalid(regex`, limiter)

	if el != nil {
		t.Error("Expected nil for invalid regex pattern")
	}
}

func TestEndpointLimiter_MatchesMethod(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	el := NewEndpointLimiter("POST", `^/api/test`, limiter)

	tests := []struct {
		method   string
		path     string
		expected bool
	}{
		{"POST", "/api/test", true},
		{"POST", "/api/test/123", true},
		{"GET", "/api/test", false},
		{"PUT", "/api/test", false},
		{"POST", "/other/path", false},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, "http://example.com"+tt.path, nil)
		result := el.Matches(req)

		if result != tt.expected {
			t.Errorf("Method %s, Path %s: expected %v, got %v",
				tt.method, tt.path, tt.expected, result)
		}
	}
}

func TestEndpointLimiter_MatchesAnyMethod(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	el := NewEndpointLimiter("*", `^/api/test`, limiter)

	tests := []struct {
		method   string
		path     string
		expected bool
	}{
		{"POST", "/api/test", true},
		{"GET", "/api/test", true},
		{"PUT", "/api/test", true},
		{"DELETE", "/api/test", true},
		{"POST", "/other/path", false},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, "http://example.com"+tt.path, nil)
		result := el.Matches(req)

		if result != tt.expected {
			t.Errorf("Method %s, Path %s: expected %v, got %v",
				tt.method, tt.path, tt.expected, result)
		}
	}
}

func TestEndpointLimiter_ComplexPattern(t *testing.T) {
	limiter := ParseRateLimit("10/sec")
	// Pattern matches /api/v1/torrents/createtorrent exactly
	el := NewEndpointLimiter("POST", `^/v1/api/torrents/createtorrent$`, limiter)

	tests := []struct {
		path     string
		expected bool
	}{
		{"/v1/api/torrents/createtorrent", true},
		{"/v1/api/torrents/createtorrent/", false},  // trailing slash
		{"/v1/api/torrents/createtorrent/123", false}, // extra path
		{"/v1/api/torrents/list", false},
		{"/api/torrents/createtorrent", false}, // missing v1
	}

	for _, tt := range tests {
		req, _ := http.NewRequest("POST", "http://example.com"+tt.path, nil)
		result := el.Matches(req)

		if result != tt.expected {
			t.Errorf("Path %s: expected %v, got %v", tt.path, tt.expected, result)
		}
	}
}

func TestEndpointLimiterRegistry_Register(t *testing.T) {
	registry := NewEndpointLimiterRegistry()
	limiter := ParseRateLimit("10/sec")

	err := registry.Register("POST", `^/api/test`, limiter)
	if err != nil {
		t.Errorf("Expected successful registration, got error: %v", err)
	}

	if registry.Size() != 1 {
		t.Errorf("Expected 1 registered limiter, got %d", registry.Size())
	}
}

func TestEndpointLimiterRegistry_RegisterInvalidPattern(t *testing.T) {
	registry := NewEndpointLimiterRegistry()
	limiter := ParseRateLimit("10/sec")

	// Invalid regex should not cause error, just skip
	err := registry.Register("POST", `[invalid(regex`, limiter)
	if err != nil {
		t.Errorf("Expected no error for invalid pattern, got: %v", err)
	}

	if registry.Size() != 0 {
		t.Errorf("Expected 0 registered limiters, got %d", registry.Size())
	}
}

func TestEndpointLimiterRegistry_GetLimiter_FirstMatch(t *testing.T) {
	registry := NewEndpointLimiterRegistry()

	limiter1 := ParseRateLimit("10/sec")
	limiter2 := ParseRateLimit("20/sec")

	// Register two patterns that could both match
	registry.Register("POST", `^/api/.*`, limiter1)
	registry.Register("POST", `^/api/test`, limiter2)

	req, _ := http.NewRequest("POST", "http://example.com/api/test", nil)
	result := registry.GetLimiter(req)

	// Should return the first match (limiter1)
	if result != limiter1 {
		t.Error("Expected first matching limiter to be returned")
	}
}

func TestEndpointLimiterRegistry_GetLimiter_NoMatch(t *testing.T) {
	registry := NewEndpointLimiterRegistry()
	limiter := ParseRateLimit("10/sec")

	registry.Register("POST", `^/api/test`, limiter)

	req, _ := http.NewRequest("GET", "http://example.com/other/path", nil)
	result := registry.GetLimiter(req)

	if result != nil {
		t.Error("Expected nil for no matching pattern")
	}
}

func TestEndpointLimiterRegistry_TorboxScenario(t *testing.T) {
	registry := NewEndpointLimiterRegistry()

	// Simulate Torbox endpoint-specific limiters
	createTorrentLimiter := ParseMultipleRateLimits("60/hour", "10/min")

	// Register endpoint-specific limiter
	registry.Register("POST", `^/v1/api/torrents/createtorrent`, createTorrentLimiter)

	tests := []struct {
		method       string
		path         string
		shouldMatch  bool
		expectedType string
	}{
		{
			"POST",
			"/v1/api/torrents/createtorrent",
			true,
			"composite",
		},
		{
			"GET",
			"/v1/api/torrents/list",
			false,
			"none",
		},
		{
			"POST",
			"/v1/api/torrents/other",
			false,
			"none",
		},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest(tt.method, "http://example.com"+tt.path, nil)
		result := registry.GetLimiter(req)

		if tt.shouldMatch {
			if result == nil {
				t.Errorf("Path %s: expected limiter, got nil", tt.path)
				continue
			}

			if tt.expectedType == "composite" {
				_, ok := result.(*CompositeRateLimiter)
				if !ok {
					t.Errorf("Path %s: expected composite limiter", tt.path)
				}
			}
		} else {
			if result != nil {
				t.Errorf("Path %s: expected nil, got limiter", tt.path)
			}
		}
	}
}

func TestEndpointLimiterRegistry_MultipleEndpoints(t *testing.T) {
	registry := NewEndpointLimiterRegistry()

	limiter1 := ParseMultipleRateLimits("60/hour", "10/min")
	limiter2 := ParseMultipleRateLimits("60/hour", "10/min")
	limiter3 := ParseMultipleRateLimits("60/hour", "10/min")

	// Register all three Torbox special endpoints
	registry.Register("POST", `^/v1/api/torrents/createtorrent`, limiter1)
	registry.Register("POST", `^/v1/api/usenet/createusenetdownload`, limiter2)
	registry.Register("POST", `^/v1/api/webdl/createwebdownload`, limiter3)

	if registry.Size() != 3 {
		t.Errorf("Expected 3 registered limiters, got %d", registry.Size())
	}

	// Test that each endpoint gets its specific limiter
	tests := []struct {
		path            string
		expectedLimiter ratelimit.Limiter
	}{
		{"/v1/api/torrents/createtorrent", limiter1},
		{"/v1/api/usenet/createusenetdownload", limiter2},
		{"/v1/api/webdl/createwebdownload", limiter3},
	}

	for _, tt := range tests {
		req, _ := http.NewRequest("POST", "http://example.com"+tt.path, nil)
		result := registry.GetLimiter(req)

		if result != tt.expectedLimiter {
			t.Errorf("Path %s: got wrong limiter", tt.path)
		}
	}
}

func BenchmarkEndpointLimiterRegistry_GetLimiter_NoMatch(b *testing.B) {
	registry := NewEndpointLimiterRegistry()
	limiter := ParseRateLimit("10/sec")

	// Register 3 endpoints
	registry.Register("POST", `^/v1/api/torrents/createtorrent`, limiter)
	registry.Register("POST", `^/v1/api/usenet/create`, limiter)
	registry.Register("POST", `^/v1/api/webdl/create`, limiter)

	req, _ := http.NewRequest("GET", "http://example.com/other/path", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.GetLimiter(req)
	}
}

func BenchmarkEndpointLimiterRegistry_GetLimiter_Match(b *testing.B) {
	registry := NewEndpointLimiterRegistry()
	limiter := ParseRateLimit("10/sec")

	registry.Register("POST", `^/v1/api/torrents/createtorrent`, limiter)

	req, _ := http.NewRequest("POST", "http://example.com/v1/api/torrents/createtorrent", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.GetLimiter(req)
	}
}
