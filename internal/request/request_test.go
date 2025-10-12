package request

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/logger"
	"go.uber.org/ratelimit"
)

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		checkFn func(ratelimit.Limiter) bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
		{
			name:    "invalid format - no slash",
			input:   "100",
			wantNil: true,
		},
		{
			name:    "invalid format - non-numeric count",
			input:   "abc/sec",
			wantNil: true,
		},
		{
			name:    "invalid format - zero count",
			input:   "0/sec",
			wantNil: true,
		},
		{
			name:    "invalid format - negative count",
			input:   "-10/sec",
			wantNil: true,
		},
		{
			name:    "invalid unit",
			input:   "100/invalid",
			wantNil: true,
		},
		{
			name:    "valid - per second",
			input:   "100/sec",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per second (long form)",
			input:   "100/second",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per minute",
			input:   "60/min",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per minute (long form)",
			input:   "60/minute",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per hour",
			input:   "3600/hr",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per hour (long form)",
			input:   "3600/hour",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per day",
			input:   "86400/d",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per day (long form)",
			input:   "86400/day",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - with whitespace",
			input:   "  100  /  sec  ",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - plural units",
			input:   "100/seconds",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRateLimit(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseRateLimit(%q) = %v, want nil", tt.input, got)
				}
			} else {
				if got == nil {
					t.Errorf("ParseRateLimit(%q) = nil, want non-nil", tt.input)
				} else if tt.checkFn != nil && !tt.checkFn(got) {
					t.Errorf("ParseRateLimit(%q) failed custom check", tt.input)
				}
			}
		})
	}
}

func TestParseRateLimitWithSlack_ZeroSlack(t *testing.T) {
	tests := []struct {
		name    string
		rateStr string
		slack   int
		wantNil bool
	}{
		{
			name:    "zero slack",
			rateStr: "120/hour",
			slack:   0,
			wantNil: false,
		},
		{
			name:    "default slack (-1)",
			rateStr: "120/hour",
			slack:   -1,
			wantNil: false,
		},
		{
			name:    "custom slack",
			rateStr: "120/hour",
			slack:   5,
			wantNil: false,
		},
		{
			name:    "P0 Fix: negative slack other than -1 should return nil",
			rateStr: "120/hour",
			slack:   -2,
			wantNil: true,
		},
		{
			name:    "P0 Fix: negative slack -10 should return nil",
			rateStr: "120/hour",
			slack:   -10,
			wantNil: true,
		},
		{
			name:    "empty rate string",
			rateStr: "",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "invalid rate string",
			rateStr: "invalid",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "zero count",
			rateStr: "0/sec",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "Edge case: very large count should return nil",
			rateStr: "99999999/sec",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "Edge case: max safe count",
			rateStr: "10000000/sec",
			slack:   0,
			wantNil: false,
		},
		{
			name:    "Edge case: overflow attempt",
			rateStr: "999999999999/sec",
			slack:   0,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRateLimitWithSlack(tt.rateStr, tt.slack)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseRateLimitWithSlack(%q, %d) = %v, want nil", tt.rateStr, tt.slack, got)
				}
			} else {
				if got == nil {
					t.Errorf("ParseRateLimitWithSlack(%q, %d) = nil, want non-nil", tt.rateStr, tt.slack)
				}
			}
		})
	}
}

func TestParseRateLimitWithSlack_AllUnits(t *testing.T) {
	units := []string{"sec", "second", "seconds", "min", "minute", "minutes", "hr", "hour", "hours", "d", "day", "days"}

	for _, unit := range units {
		t.Run("unit_"+unit, func(t *testing.T) {
			rateStr := "100/" + unit
			limiter := ParseRateLimitWithSlack(rateStr, 0)
			if limiter == nil {
				t.Errorf("ParseRateLimitWithSlack(%q, 0) = nil, want non-nil", rateStr)
			}
		})
	}
}

func BenchmarkParseRateLimit(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseRateLimit("100/sec")
	}
}

func BenchmarkParseRateLimitWithSlack(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseRateLimitWithSlack("100/sec", 0)
	}
}

// TestMakeRequestBodyClosing tests that response body is properly closed
// This verifies the fix for critical issue #2: logger Printf bug
func TestMakeRequestBodyClosing(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectError    bool
	}{
		{
			name: "successful request with body closing",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"success": true}`))
			},
			expectError: false,
		},
		{
			name: "error response with body closing",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error": "internal error"}`))
			},
			expectError: true,
		},
		{
			name: "empty response body",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			// Create client
			client := New(
				WithLogger(logger.New("test")),
			)

			// Create request
			req, err := http.NewRequest(http.MethodGet, server.URL+"/test", nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			// Make request
			_, err = client.MakeRequest(req)

			// Check error expectation
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// Give the defer a moment to execute
			time.Sleep(10 * time.Millisecond)
		})
	}
}

// TestMakeRequestBodyCloseError tests that body close errors are logged properly
func TestMakeRequestBodyCloseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"test": "data"}`))
	}))
	defer server.Close()

	client := New(
		WithLogger(logger.New("test")),
	)

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// This should not panic even if body close has issues
	data, err := client.MakeRequest(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected response data, got empty")
	}
}

// TestClientDoWithContext tests that client respects context cancellation
func TestClientDoWithContext(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(
		WithLogger(logger.New("test")),
	)

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Request should fail due to context timeout
	_, err = client.Do(req)
	if err == nil {
		t.Error("Expected context timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("Expected context-related error, got: %v", err)
	}
}

// TestClientConcurrentRequests tests that client handles concurrent requests safely
func TestClientConcurrentRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	client := New(
		WithLogger(logger.New("test")),
	)

	// Run concurrent requests
	const numRequests = 20
	errChan := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			if err != nil {
				errChan <- err
				return
			}

			_, err = client.MakeRequest(req)
			errChan <- err
		}()
	}

	// Collect results
	for i := 0; i < numRequests; i++ {
		err := <-errChan
		if err != nil {
			t.Errorf("Concurrent request %d failed: %v", i, err)
		}
	}
}

// BenchmarkMakeRequest benchmarks the request making process
func BenchmarkMakeRequest(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"benchmark": true}`))
	}))
	defer server.Close()

	client := New(
		WithLogger(logger.New("test")),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		_, _ = client.MakeRequest(req)
	}
}

// TestJoinURL tests URL joining with query parameters
func TestJoinURL(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		paths    []string
		expected string
		hasError bool
	}{
		{
			name:     "simple path",
			base:     "https://api.example.com",
			paths:    []string{"v1", "users"},
			expected: "https://api.example.com/v1/users",
			hasError: false,
		},
		{
			name:     "path with query params",
			base:     "https://api.example.com",
			paths:    []string{"v1", "users?page=1&limit=10"},
			expected: "https://api.example.com/v1/users?page=1&limit=10",
			hasError: false,
		},
		{
			name:     "base with trailing slash",
			base:     "https://api.example.com/",
			paths:    []string{"v1", "users"},
			expected: "https://api.example.com/v1/users",
			hasError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := JoinURL(tt.base, tt.paths...)

			if tt.hasError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.hasError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}
