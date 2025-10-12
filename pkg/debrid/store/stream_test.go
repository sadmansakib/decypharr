package store

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

// newTestCache creates a minimal Cache instance for testing
func newTestCache(httpClient *http.Client) *Cache {
	logger := zerolog.New(io.Discard)
	return &Cache{
		logger:                logger,
		streamClient:          httpClient,
		linkValidationRetry:   xsync.NewMapOf[string, *validationRetry](),
		linkRetryTracker:      xsync.NewMapOf[string, *linkRetryInfo](),
		invalidDownloadLinks:  xsync.NewMapOf[string, string](),
		failedLinksCounter:    xsync.NewMapOf[string, atomic.Int32](),
	}
}

// TestStream_InvalidPresignedToken tests HTTP 400 handling for TorBox invalid presigned token errors
func TestStream_InvalidPresignedToken(t *testing.T) {
	tests := []struct {
		name           string
		responseBody   string
		expectLinkErr  bool
		expectRetryVal bool
		description    string
	}{
		{
			name: "torbox_invalid_presigned_token_html",
			responseBody: `<!doctype html>
<html>
<head>
  <title>TorBox Satellite</title>
</head>
<body>
  <h1>Invalid Presigned Token</h1>
  <p>This link is invalid. Please get a new download link from TorBox.</p>
</body>
</html>`,
			expectLinkErr:  true,
			expectRetryVal: true,
			description:    "Should detect 'Invalid Presigned Token' in HTML response",
		},
		{
			name:           "presigned_lowercase",
			responseBody:   `{"error": "invalid presigned token"}`,
			expectLinkErr:  true,
			expectRetryVal: true,
			description:    "Should detect lowercase 'invalid presigned token'",
		},
		{
			name:           "presigned_uppercase",
			responseBody:   `{"error": "Invalid Presigned Token"}`,
			expectLinkErr:  true,
			expectRetryVal: true,
			description:    "Should detect uppercase 'Invalid Presigned Token'",
		},
		{
			name:           "presigned_keyword_only",
			responseBody:   `{"error": "presigned URL has expired"}`,
			expectLinkErr:  true,
			expectRetryVal: true,
			description:    "Should detect 'presigned' keyword alone",
		},
		{
			name:           "other_400_error",
			responseBody:   `{"error": "bad request"}`,
			expectLinkErr:  false,
			expectRetryVal: false,
			description:    "Should not treat other 400 errors as link errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server that returns 400 with the response body
			linkAttempt := 0
			attemptCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attemptCount++
				// Check which link this is based on query param
				currentLink := r.URL.Query().Get("link")

				if currentLink == "link1" {
					// First link: always return 400 (for validation retries)
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(tt.responseBody))
				} else if currentLink == "link2" {
					// Second link: return success
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("success"))
				} else {
					// Shouldn't happen, but return error
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("unexpected"))
				}
			}))
			defer server.Close()

			// Create cache instance
			cache := newTestCache(server.Client())

			// Create link function that returns different URLs per call
			linkCallCount := 0
			linkFunc := func() (types.DownloadLink, error) {
				linkCallCount++
				linkAttempt++
				return types.DownloadLink{
					DownloadLink: server.URL + "?link=link" + string(rune('0'+linkAttempt)),
					Link:         "test-link",
				}, nil
			}

			// Execute stream with context
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			resp, err := cache.Stream(ctx, 0, 0, linkFunc)

			if tt.expectLinkErr {
				// For presigned token errors:
				// - Should perform 3 validation retries on first link
				// - Then fetch a new link (linkCallCount becomes 2)
				// - New link should succeed
				if err != nil {
					t.Errorf("%s: Expected eventual success after retries, got error: %v", tt.description, err)
				}
				if resp == nil {
					t.Errorf("%s: Expected response after retries, got nil", tt.description)
				}
				if linkCallCount != 2 {
					t.Errorf("%s: Expected 2 link calls (initial + after validation retries), got %d", tt.description, linkCallCount)
				}
				// Verify at least 4 HTTP attempts (3 validation + 1 success on new link)
				if attemptCount < 4 {
					t.Errorf("%s: Expected at least 4 HTTP attempts (3 validation + success), got %d", tt.description, attemptCount)
				}
			} else {
				// For other 400 errors, should fail immediately without retries
				if err == nil {
					t.Errorf("%s: Expected error for non-retryable 400, got nil", tt.description)
				}
				if linkCallCount != 1 {
					t.Errorf("%s: Expected 1 link call (no retry), got %d", tt.description, linkCallCount)
				}
			}

			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

// TestHandleHTTPError_PresignedToken tests the handleHTTPError function directly
func TestHandleHTTPError_PresignedToken(t *testing.T) {
	cache := newTestCache(nil)

	testLink := types.DownloadLink{
		DownloadLink: "https://example.com/test",
		Link:         "test-link",
	}

	tests := []struct {
		name           string
		statusCode     int
		responseBody   string
		expectRetry    bool
		expectLinkErr  bool
		validateBefore bool
		description    string
	}{
		{
			name:           "presigned_token_first_attempt",
			statusCode:     http.StatusBadRequest,
			responseBody:   "Invalid Presigned Token",
			expectRetry:    true,
			expectLinkErr:  false, // First attempt should validate
			validateBefore: false,
			description:    "First 400 with presigned token should trigger validation retry",
		},
		{
			name:           "presigned_token_after_validation",
			statusCode:     http.StatusBadRequest,
			responseBody:   "Invalid Presigned Token",
			expectRetry:    true,
			expectLinkErr:  true, // After max retries should fetch new link
			validateBefore: true,
			description:    "After max validation retries, should mark as link error",
		},
		{
			name:           "404_first_attempt",
			statusCode:     http.StatusNotFound,
			responseBody:   "Not Found",
			expectRetry:    true,
			expectLinkErr:  false,
			validateBefore: false,
			description:    "404 should trigger validation retry on first attempt",
		},
		// Note: bandwidth test removed because it requires a mock client for AccountManager
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset validation retries for clean test
			cache.linkValidationRetry = xsync.NewMapOf[string, *validationRetry]()

			if tt.validateBefore {
				// Simulate max validation retries reached
				cache.linkValidationRetry.Store(testLink.DownloadLink, &validationRetry{
					attemptCount: MaxValidationRetries,
					firstAttempt: time.Now(),
				})
			}

			// Create mock response
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Body:       io.NopCloser(bytes.NewBufferString(tt.responseBody)),
			}

			// Call handleHTTPError
			streamErr := cache.handleHTTPError(resp, testLink)

			// Verify expectations
			if streamErr.Retryable != tt.expectRetry {
				t.Errorf("%s: Expected Retryable=%v, got %v", tt.description, tt.expectRetry, streamErr.Retryable)
			}

			if streamErr.LinkError != tt.expectLinkErr {
				t.Errorf("%s: Expected LinkError=%v, got %v", tt.description, tt.expectLinkErr, streamErr.LinkError)
			}
		})
	}
}

// TestStream_PresignedTokenValidationFlow tests the full validation retry flow
func TestStream_PresignedTokenValidationFlow(t *testing.T) {
	requestLog := []string{}

	// Create test server that simulates presigned token expiry -> recovery
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestLog = append(requestLog, r.URL.Query().Get("attempt"))

		attemptNum := len(requestLog)
		if attemptNum <= 3 {
			// First 3 attempts: invalid presigned token
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Invalid Presigned Token"))
		} else {
			// After fetching new link: success
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("success"))
		}
	}))
	defer server.Close()

	cache := newTestCache(server.Client())

	linkCallCount := 0
	linkFunc := func() (types.DownloadLink, error) {
		linkCallCount++
		return types.DownloadLink{
			DownloadLink: server.URL + "?attempt=" + string(rune('0'+linkCallCount)),
			Link:         "test-link",
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := cache.Stream(ctx, 0, 0, linkFunc)

	// Should succeed after validation retries + new link
	if err != nil {
		t.Errorf("Expected success after validation flow, got error: %v", err)
	}
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}
	defer resp.Body.Close()

	// Verify the flow:
	// 1. Initial link called
	// 2. 3 validation retries on same link
	// 3. New link called after max retries
	// 4. Success
	if linkCallCount != 2 {
		t.Errorf("Expected 2 link calls (initial + retry), got %d", linkCallCount)
	}

	if len(requestLog) < 4 {
		t.Errorf("Expected at least 4 requests (3 validation + 1 success), got %d", len(requestLog))
	}

	// Read response to verify success
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "success" {
		t.Errorf("Expected 'success' response body, got: %s", string(body))
	}
}

// TestStream_PresignedTokenRateLimiting tests that presigned token errors respect rate limiting
func TestStream_PresignedTokenRateLimiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid Presigned Token"))
	}))
	defer server.Close()

	cache := newTestCache(server.Client())

	linkCallCount := 0
	linkFunc := func() (types.DownloadLink, error) {
		linkCallCount++
		return types.DownloadLink{
			DownloadLink: server.URL,
			Link:         "test-link",
		}, nil
	}

	// First call: should go through validation retries
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	_, err1 := cache.Stream(ctx1, 0, 0, linkFunc)
	firstCallLinkCount := linkCallCount

	// Second call immediately after: should be blocked by exponential backoff
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	_, err2 := cache.Stream(ctx2, 0, 0, linkFunc)
	secondCallLinkCount := linkCallCount - firstCallLinkCount

	// First call should have tried validation retries + new link
	if firstCallLinkCount < 2 {
		t.Errorf("Expected at least 2 link calls in first stream, got %d", firstCallLinkCount)
	}

	// Second call should be blocked by backoff (minimal link calls)
	if secondCallLinkCount > 1 {
		t.Errorf("Expected backoff to prevent retries, but got %d link calls", secondCallLinkCount)
	}

	// Both calls should eventually fail or be blocked
	if err1 == nil && err2 == nil {
		t.Error("Expected at least one error due to persistent failures")
	}
}
