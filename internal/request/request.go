package request

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"go.uber.org/ratelimit"
	"golang.org/x/net/proxy"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func JoinURL(base string, paths ...string) (string, error) {
	// Split the last path component to separate query parameters
	lastPath := paths[len(paths)-1]
	parts := strings.Split(lastPath, "?")
	paths[len(paths)-1] = parts[0]

	joined, err := url.JoinPath(base, paths...)
	if err != nil {
		return "", err
	}

	// Add back query parameters if they exist
	if len(parts) > 1 {
		return joined + "?" + parts[1], nil
	}

	return joined, nil
}

var (
	once     sync.Once
	instance *Client
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int32

const (
	CircuitClosed CircuitBreakerState = iota
	CircuitOpen
	CircuitHalfOpen
)

// Helper functions for combined state management
// encodeCombinedState combines circuit state and half-open requests into a single int64
func encodeCombinedState(state CircuitBreakerState, halfOpenRequests int32) int64 {
	return int64(state)<<16 | int64(halfOpenRequests&0xFFFF)
}

// decodeCombinedState extracts circuit state and half-open requests from combined state
func decodeCombinedState(combined int64) (CircuitBreakerState, int32) {
	state := CircuitBreakerState((combined >> 16) & 0xFFFF)
	halfOpenRequests := int32(combined & 0xFFFF)
	return state, halfOpenRequests
}

// CircuitBreakerConfig defines configuration for circuit breaker
type CircuitBreakerConfig struct {
	FailureThreshold    int           // Number of consecutive 429s to open circuit
	RecoveryTimeout     time.Duration // Time before attempting half-open
	MaxHalfOpenRequests int           // Max requests allowed in half-open state
}

// CircuitBreaker manages the circuit breaker state for rate limiting
type CircuitBreaker struct {
	config           CircuitBreakerConfig
	// Combined state: lower 16 bits = half-open requests, upper 16 bits = circuit state
	combinedState    int64 // Atomic combined state (state<<16 | halfOpenRequests)
	failureCount     int32 // Consecutive failures (atomic)
	lastFailureTime  int64 // Unix timestamp of last failure (atomic)
	logger           zerolog.Logger
	mu               sync.RWMutex // For critical state transitions
	// Lifecycle management
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	closed           int32 // Atomic flag to indicate if closed
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig, logger zerolog.Logger) *CircuitBreaker {
	ctx, cancel := context.WithCancel(context.Background())
	cb := &CircuitBreaker{
		config:        config,
		combinedState: encodeCombinedState(CircuitClosed, 0),
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	// Start background cleanup goroutine
	go cb.cleanupRoutine()

	return cb
}

// Allow checks if a request should be allowed through the circuit breaker
func (cb *CircuitBreaker) Allow() error {
	// Check if circuit breaker is closed/shutdown
	if atomic.LoadInt32(&cb.closed) == 1 {
		return fmt.Errorf("circuit breaker is closed")
	}

	// Limit iterations to prevent potential infinite loops
	maxIterations := 100
	for i := 0; i < maxIterations; i++ {
		combined := atomic.LoadInt64(&cb.combinedState)
		state, halfOpenRequests := decodeCombinedState(combined)

		switch state {
		case CircuitClosed:
			return nil
		case CircuitOpen:
			// Atomically read the last failure time to avoid races
			lastFailureTime := atomic.LoadInt64(&cb.lastFailureTime)
			if lastFailureTime == 0 {
				// No failure recorded yet, shouldn't be in open state
				continue
			}

			lastFailure := time.Unix(lastFailureTime, 0)
			if time.Since(lastFailure) >= cb.config.RecoveryTimeout {
				// Attempt atomic transition to half-open with first request slot reserved
				newCombined := encodeCombinedState(CircuitHalfOpen, 1)
				if atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined) {
					cb.logger.Info().Msg("Circuit breaker transitioning to half-open state")
					return nil
				}
				// CAS failed, someone else changed state, retry
				continue
			}
			return fmt.Errorf("circuit breaker is open - rate limit protection active")
		case CircuitHalfOpen:
			// Check if we can increment half-open requests
			if halfOpenRequests >= int32(cb.config.MaxHalfOpenRequests) {
				return fmt.Errorf("circuit breaker half-open request limit exceeded")
			}

			// Attempt atomic increment of half-open requests
			newCombined := encodeCombinedState(CircuitHalfOpen, halfOpenRequests+1)
			if atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined) {
				return nil
			}
			// CAS failed, retry with updated state
			continue
		default:
			return nil
		}
	}

	// If we reach here, we've hit the iteration limit
	return fmt.Errorf("circuit breaker state transition limit exceeded")
}

// RecordSuccess records a successful response
func (cb *CircuitBreaker) RecordSuccess() {
	// Check if circuit breaker is closed
	if atomic.LoadInt32(&cb.closed) == 1 {
		return
	}

	// Limit iterations to prevent infinite loops
	maxIterations := 50
	for i := 0; i < maxIterations; i++ {
		combined := atomic.LoadInt64(&cb.combinedState)
		state, _ := decodeCombinedState(combined)

		switch state {
		case CircuitHalfOpen:
			// Atomically transition from half-open to closed
			newCombined := encodeCombinedState(CircuitClosed, 0)
			if atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined) {
				// Reset failure count atomically after successful state transition
				atomic.StoreInt32(&cb.failureCount, 0)
				cb.logger.Info().Msg("Circuit breaker closed - service recovered")
				return
			}
			// CAS failed, retry with updated state
			continue
		case CircuitClosed:
			// Reset failure count on success in closed state
			atomic.StoreInt32(&cb.failureCount, 0)
			return
		case CircuitOpen:
			// No action needed in open state
			return
		default:
			return
		}
	}

	// If we reach here, log a warning about iteration limit
	cb.logger.Warn().Msg("RecordSuccess: state transition limit exceeded")
}

// RecordFailure records a 429 response failure
func (cb *CircuitBreaker) RecordFailure() {
	// Check if circuit breaker is closed
	if atomic.LoadInt32(&cb.closed) == 1 {
		return
	}

	// Record failure time and increment failure count atomically
	atomic.StoreInt64(&cb.lastFailureTime, time.Now().Unix())
	failures := atomic.AddInt32(&cb.failureCount, 1)

	// Limit iterations to prevent infinite loops
	maxIterations := 50
	for i := 0; i < maxIterations; i++ {
		combined := atomic.LoadInt64(&cb.combinedState)
		state, _ := decodeCombinedState(combined)

		switch state {
		case CircuitClosed:
			// Check if we should transition to open state
			if failures >= int32(cb.config.FailureThreshold) {
				newCombined := encodeCombinedState(CircuitOpen, 0)
				if atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined) {
					cb.logger.Warn().Int32("failures", failures).Msg("Circuit breaker opened due to rate limit violations")
					return
				}
				// CAS failed, retry with updated state
				continue
			}
			return
		case CircuitHalfOpen:
			// Immediately return to open state on failure during half-open
			newCombined := encodeCombinedState(CircuitOpen, 0)
			if atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined) {
				cb.logger.Warn().Msg("Circuit breaker reopened - service still rate limiting")
				return
			}
			// CAS failed, retry with updated state
			continue
		case CircuitOpen:
			// Already open, nothing to do
			return
		default:
			return
		}
	}

	// If we reach here, log a warning about iteration limit
	cb.logger.Warn().Msg("RecordFailure: state transition limit exceeded")
}

// validateStateConsistency checks for state consistency issues (for debugging)
func (cb *CircuitBreaker) validateStateConsistency() bool {
	combined := atomic.LoadInt64(&cb.combinedState)
	state, halfOpenRequests := decodeCombinedState(combined)
	failureCount := atomic.LoadInt32(&cb.failureCount)
	closed := atomic.LoadInt32(&cb.closed)

	// Basic consistency checks
	if closed == 1 {
		return true // Don't validate when circuit breaker is shut down
	}

	// If state is closed, half-open requests should be zero
	if state == CircuitClosed && halfOpenRequests != 0 {
		cb.logger.Warn().Int32("state", int32(state)).Int32("halfOpenRequests", halfOpenRequests).
			Msg("State consistency violation: closed state with non-zero half-open requests")
		return false
	}

	// If state is open, half-open requests should be zero
	if state == CircuitOpen && halfOpenRequests != 0 {
		cb.logger.Warn().Int32("state", int32(state)).Int32("halfOpenRequests", halfOpenRequests).
			Msg("State consistency violation: open state with non-zero half-open requests")
		return false
	}

	// Handle negative half-open requests (rollback race condition)
	if halfOpenRequests < 0 {
		cb.logger.Warn().Int32("halfOpenRequests", halfOpenRequests).
			Msg("State consistency violation: negative half-open requests (rollback race)")
		return false
	}

	// Failure count should never be negative
	if failureCount < 0 {
		cb.logger.Warn().Int32("failureCount", failureCount).
			Msg("State consistency violation: negative failure count")
		return false
	}

	// Half-open requests should never be negative
	if halfOpenRequests < 0 {
		cb.logger.Warn().Int32("halfOpenRequests", halfOpenRequests).
			Msg("State consistency violation: negative half-open requests")
		return false
	}

	return true
}

// Close gracefully shuts down the circuit breaker and cleans up resources
func (cb *CircuitBreaker) Close() error {
	// Set closed flag atomically
	if !atomic.CompareAndSwapInt32(&cb.closed, 0, 1) {
		// Already closed
		return nil
	}

	cb.logger.Debug().Msg("Closing circuit breaker")

	// Cancel context to signal cleanup goroutine
	if cb.cancel != nil {
		cb.cancel()
	}

	// Wait for cleanup goroutine to finish
	select {
	case <-cb.done:
		cb.logger.Debug().Msg("Circuit breaker cleanup completed")
	case <-time.After(5 * time.Second):
		cb.logger.Warn().Msg("Circuit breaker cleanup timeout - forcing close")
	}

	return nil
}

// cleanupRoutine runs in background to handle periodic cleanup and state management
func (cb *CircuitBreaker) cleanupRoutine() {
	defer close(cb.done)

	// Cleanup ticker for periodic maintenance (e.g., reset old failure counts)
	ticker := time.NewTicker(cb.config.RecoveryTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-cb.ctx.Done():
			cb.logger.Debug().Msg("Circuit breaker cleanup routine shutting down")
			return
		case <-ticker.C:
			// Periodic maintenance - clean up old state if needed
			cb.periodicMaintenance()
		}
	}
}

// periodicMaintenance performs periodic cleanup of circuit breaker state
func (cb *CircuitBreaker) periodicMaintenance() {
	// Check if circuit breaker is closed
	if atomic.LoadInt32(&cb.closed) == 1 {
		return
	}

	combined := atomic.LoadInt64(&cb.combinedState)
	state, halfOpenRequests := decodeCombinedState(combined)
	lastFailure := time.Unix(atomic.LoadInt64(&cb.lastFailureTime), 0)

	// Clean up inconsistent state: reset half-open requests if not in half-open state
	if state != CircuitHalfOpen && halfOpenRequests != 0 {
		cb.logger.Debug().Int32("state", int32(state)).Int32("halfOpenRequests", halfOpenRequests).
			Msg("Cleaning up inconsistent half-open requests")
		newCombined := encodeCombinedState(state, 0)
		atomic.CompareAndSwapInt64(&cb.combinedState, combined, newCombined)
	}

	// If circuit has been open for a very long time, log for monitoring
	if state == CircuitOpen && time.Since(lastFailure) > cb.config.RecoveryTimeout*5 {
		cb.logger.Info().Dur("duration", time.Since(lastFailure)).Msg("Circuit breaker has been open for extended period")
	}

	// Reset failure count if circuit has been closed for a long time without failures
	if state == CircuitClosed && atomic.LoadInt32(&cb.failureCount) > 0 && time.Since(lastFailure) > cb.config.RecoveryTimeout*2 {
		atomic.StoreInt32(&cb.failureCount, 0)
		cb.logger.Debug().Msg("Reset circuit breaker failure count after extended success period")
	}
}

// RateLimiter interface for different types of rate limiters
type RateLimiter interface {
	Take() time.Time
}

// EndpointRateLimiter represents a rate limiter for a specific endpoint pattern
type EndpointRateLimiter struct {
	Pattern    string              // URL pattern to match (supports simple string matching or regex)
	Limiters   []ratelimit.Limiter // Multiple rate limiters (e.g., hourly + per-minute)
	IsRegex    bool                // Whether pattern is a regex
	CompiledRx *regexp.Regexp      // Compiled regex if IsRegex is true
}

type ClientOption func(*Client)

// Client represents an HTTP client with additional capabilities
type Client struct {
	client               *http.Client
	rateLimiter          ratelimit.Limiter
	endpointRateLimiters []EndpointRateLimiter // Per-endpoint rate limiters
	circuitBreaker       *CircuitBreaker       // Circuit breaker for 429 responses
	headers              map[string]string
	headersMu            sync.RWMutex
	maxRetries           int
	timeout              time.Duration
	skipTLSVerify        bool
	retryableStatus      map[int]struct{}
	retryableStatusMu    sync.RWMutex // Protects retryableStatus map from concurrent access
	logger               zerolog.Logger
	proxy                string
	// Resource tracking for debugging (optional)
	openConnections      int64  // Atomic counter for tracking open connections
	responseLeakTracker  bool   // Enable response body leak tracking
}

// WithMaxRetries sets the maximum number of retry attempts
func WithMaxRetries(maxRetries int) ClientOption {
	return func(c *Client) {
		c.maxRetries = maxRetries
	}
}

// WithTimeout sets the request timeout
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.timeout = timeout
	}
}

func WithRedirectPolicy(policy func(req *http.Request, via []*http.Request) error) ClientOption {
	return func(c *Client) {
		c.client.CheckRedirect = policy
	}
}

// WithRateLimiter sets a rate limiter
func WithRateLimiter(rl ratelimit.Limiter) ClientOption {
	return func(c *Client) {
		c.rateLimiter = rl
	}
}

// WithEndpointRateLimiters sets per-endpoint rate limiters
// This allows different rate limiters for specific URL patterns
func WithEndpointRateLimiters(limiters []EndpointRateLimiter) ClientOption {
	return func(c *Client) {
		// Compile regex patterns
		for i := range limiters {
			if limiters[i].IsRegex {
				compiled, err := regexp.Compile(limiters[i].Pattern)
				if err != nil {
					c.logger.Error().Err(err).Msgf("Failed to compile regex pattern: %s", limiters[i].Pattern)
					continue
				}
				limiters[i].CompiledRx = compiled
			}
		}
		c.endpointRateLimiters = limiters
	}
}

// WithCircuitBreaker sets a circuit breaker for 429 response handling
func WithCircuitBreaker(config CircuitBreakerConfig) ClientOption {
	return func(c *Client) {
		c.circuitBreaker = NewCircuitBreaker(config, c.logger)
	}
}

// WithHeaders sets default headers
func WithHeaders(headers map[string]string) ClientOption {
	return func(c *Client) {
		c.headersMu.Lock()
		c.headers = headers
		c.headersMu.Unlock()
	}
}

func (c *Client) SetHeader(key, value string) {
	c.headersMu.Lock()
	c.headers[key] = value
	c.headersMu.Unlock()
}

func WithLogger(logger zerolog.Logger) ClientOption {
	return func(c *Client) {
		c.logger = logger
	}
}

func WithTransport(transport *http.Transport) ClientOption {
	return func(c *Client) {
		c.client.Transport = transport
	}
}

// WithRetryableStatus adds status codes that should trigger a retry
func WithRetryableStatus(statusCodes ...int) ClientOption {
	return func(c *Client) {
		c.retryableStatusMu.Lock()
		c.retryableStatus = make(map[int]struct{}) // reset the map
		for _, code := range statusCodes {
			c.retryableStatus[code] = struct{}{}
		}
		c.retryableStatusMu.Unlock()
	}
}

func WithProxy(proxyURL string) ClientOption {
	return func(c *Client) {
		c.proxy = proxyURL
	}
}

// WithResponseLeakTracker enables response body leak tracking for debugging
func WithResponseLeakTracker(enabled bool) ClientOption {
	return func(c *Client) {
		c.responseLeakTracker = enabled
	}
}

// doRequest performs a single HTTP request with rate limiting and circuit breaker
// Note: Caller is responsible for closing the response body
func (c *Client) doRequest(req *http.Request) (*http.Response, error) {
	// Check circuit breaker first
	if c.circuitBreaker != nil {
		if err := c.circuitBreaker.Allow(); err != nil {
			return nil, err
		}
	}

	// Apply endpoint-specific rate limiting first
	if len(c.endpointRateLimiters) > 0 {
		c.applyEndpointRateLimit(req)
	}

	// Apply general rate limiting
	if c.rateLimiter != nil {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		default:
			c.rateLimiter.Take()
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// Track open connections if debugging is enabled
	if c.responseLeakTracker {
		atomic.AddInt64(&c.openConnections, 1)
		c.logger.Debug().Int64("open_connections", atomic.LoadInt64(&c.openConnections)).Str("url", req.URL.String()).Msg("HTTP response created")
	}

	return resp, nil
}

// applyEndpointRateLimit applies rate limiting based on the request URL
func (c *Client) applyEndpointRateLimit(req *http.Request) {
	requestURL := req.URL.String()

	for _, limiter := range c.endpointRateLimiters {
		matched := false

		if limiter.IsRegex && limiter.CompiledRx != nil {
			matched = limiter.CompiledRx.MatchString(requestURL)
		} else {
			// Simple string matching
			matched = strings.Contains(requestURL, limiter.Pattern)
		}

		if matched {
			// Apply all rate limiters for this endpoint (e.g., hourly + per-minute)
			for _, rl := range limiter.Limiters {
				if rl != nil {
					select {
					case <-req.Context().Done():
						return
					default:
						rl.Take()
					}
				}
			}
			// Only apply the first matching pattern
			return
		}
	}
}

// Do performs an HTTP request with retries for certain status codes
// Note: Caller is responsible for closing the response body of successful responses
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	// Save the request body for reuse in retries
	var bodyBytes []byte
	var err error

	if req.Body != nil {
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		req.Body.Close()
	}

	backoff := time.Millisecond * 500
	var resp *http.Response

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		// Reset the request body if it exists
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		// Apply headers
		c.headersMu.RLock()
		if c.headers != nil {
			for key, value := range c.headers {
				req.Header.Set(key, value)
			}
		}
		c.headersMu.RUnlock()

		resp, err = c.doRequest(req)
		if err != nil {
			// Check if this is a network error that might be worth retrying
			if isRetryableError(err) && attempt < c.maxRetries {
				// Apply backoff with jitter
				jitter := time.Duration(rand.Int63n(int64(backoff / 4)))
				sleepTime := backoff + jitter

				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(sleepTime):
					// Continue to next retry attempt
				}

				// Exponential backoff
				backoff *= 2
				continue
			}
			return nil, err
		}

		// Record circuit breaker state based on response
		if c.circuitBreaker != nil {
			if resp.StatusCode == http.StatusTooManyRequests {
				c.circuitBreaker.RecordFailure()
				c.logger.Warn().Int("status", resp.StatusCode).Str("url", req.URL.String()).Msg("Rate limit violation detected")
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				c.circuitBreaker.RecordSuccess()
			}
		}

		// Check if the status code is retryable
		c.retryableStatusMu.RLock()
		_, isRetryable := c.retryableStatus[resp.StatusCode]
		c.retryableStatusMu.RUnlock()
		if !isRetryable || attempt == c.maxRetries {
			// Success path - caller is responsible for closing response body
			return resp, nil
		}

		// Close the response body before retrying to prevent file handle leaks
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Debug().Err(closeErr).Msg("Failed to close response body before retry")
		}
		// Track connection closure if debugging is enabled
		if c.responseLeakTracker {
			atomic.AddInt64(&c.openConnections, -1)
			c.logger.Debug().Int64("open_connections", atomic.LoadInt64(&c.openConnections)).Msg("HTTP response closed before retry")
		}

		// Apply backoff with jitter
		jitter := time.Duration(rand.Int63n(int64(backoff / 4)))
		sleepTime := backoff + jitter

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(sleepTime):
			// Continue to next retry attempt
		}

		// Exponential backoff
		backoff *= 2
	}

	return nil, fmt.Errorf("max retries exceeded")
}

// MakeRequest performs an HTTP request and returns the response body as bytes
func (c *Client) MakeRequest(req *http.Request) ([]byte, error) {
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err := res.Body.Close(); err != nil {
			c.logger.Printf("Failed to close response body: %v", err)
		}
		// Track connection closure if debugging is enabled
		if c.responseLeakTracker {
			atomic.AddInt64(&c.openConnections, -1)
			c.logger.Debug().Int64("open_connections", atomic.LoadInt64(&c.openConnections)).Str("url", req.URL.String()).Msg("HTTP response closed in MakeRequest")
		}
	}()

	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP error %d: %s", res.StatusCode, string(bodyBytes))
	}

	return bodyBytes, nil
}

// Get performs a GET request and returns the response
// Note: Caller is responsible for closing the response body
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}

	return c.Do(req)
}

// GetWithAutoClose performs a GET request and automatically handles response body closure
// Returns the response body bytes and any error encountered
func (c *Client) GetWithAutoClose(url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Debug().Err(closeErr).Str("url", url).Msg("Failed to close response body")
		}
		// Track connection closure if debugging is enabled
		if c.responseLeakTracker {
			atomic.AddInt64(&c.openConnections, -1)
			c.logger.Debug().Int64("open_connections", atomic.LoadInt64(&c.openConnections)).Str("url", url).Msg("HTTP response auto-closed")
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	return body, nil
}

// DoWithAutoClose performs an HTTP request and automatically handles response body closure
// Returns the response body bytes and status code
func (c *Client) DoWithAutoClose(req *http.Request) ([]byte, int, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Debug().Err(closeErr).Str("url", req.URL.String()).Msg("Failed to close response body")
		}
		// Track connection closure if debugging is enabled
		if c.responseLeakTracker {
			atomic.AddInt64(&c.openConnections, -1)
			c.logger.Debug().Int64("open_connections", atomic.LoadInt64(&c.openConnections)).Str("url", req.URL.String()).Msg("HTTP response auto-closed")
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", err)
	}

	return body, resp.StatusCode, nil
}

// GetOpenConnectionCount returns the current number of open HTTP connections for debugging
func (c *Client) GetOpenConnectionCount() int64 {
	return atomic.LoadInt64(&c.openConnections)
}

// Close gracefully shuts down the client and cleans up resources
func (c *Client) Close() error {
	if c.circuitBreaker != nil {
		if err := c.circuitBreaker.Close(); err != nil {
			c.logger.Warn().Err(err).Msg("Failed to close circuit breaker")
			return err
		}
	}

	// Log potential resource leaks if tracking is enabled
	if c.responseLeakTracker {
		openConns := atomic.LoadInt64(&c.openConnections)
		if openConns > 0 {
			c.logger.Warn().Int64("open_connections", openConns).Msg("Potential response body leaks detected on client close")
		} else {
			c.logger.Debug().Msg("All HTTP response bodies properly closed")
		}
	}

	return nil
}

// New creates a new HTTP client with the specified options
func New(options ...ClientOption) *Client {
	client := &Client{
		maxRetries:    3,
		skipTLSVerify: true,
		retryableStatus: map[int]struct{}{
			http.StatusTooManyRequests:     struct{}{},
			http.StatusInternalServerError: struct{}{},
			http.StatusBadGateway:          struct{}{},
			http.StatusServiceUnavailable:  struct{}{},
			http.StatusGatewayTimeout:      struct{}{},
		},
		logger:  logger.New("request"),
		timeout: 60 * time.Second,
		proxy:   "",
		headers: make(map[string]string),
	}

	// default http client
	client.client = &http.Client{
		Timeout: client.timeout,
	}

	// Apply options before configuring transport
	for _, option := range options {
		option(client)
	}

	// Check if transport was set by WithTransport option
	if client.client.Transport == nil {
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: client.skipTLSVerify,
			},
			DisableKeepAlives: false,
		}

		// Configure proxy if needed
		SetProxy(transport, client.proxy)

		// Set the transport to the client
		client.client.Transport = transport
	}

	return client
}

// MultiRateLimiter combines multiple rate limiters that must all be satisfied
type MultiRateLimiter struct {
	limiters []ratelimit.Limiter
}

// Ensure MultiRateLimiter implements RateLimiter interface
var _ RateLimiter = (*MultiRateLimiter)(nil)

// NewMultiRateLimiter creates a new multi-rate limiter with the given rate limit strings
func NewMultiRateLimiter(rateLimitStrs ...string) *MultiRateLimiter {
	var limiters []ratelimit.Limiter
	for _, rateStr := range rateLimitStrs {
		if limiter := ParseRateLimit(rateStr); limiter != nil {
			limiters = append(limiters, limiter)
		}
	}
	if len(limiters) == 0 {
		return nil
	}
	return &MultiRateLimiter{limiters: limiters}
}

// Take blocks until all rate limiters allow a request
func (m *MultiRateLimiter) Take() time.Time {
	if m == nil || len(m.limiters) == 0 {
		return time.Now()
	}

	var latestTime time.Time
	for _, limiter := range m.limiters {
		t := limiter.Take()
		if t.After(latestTime) {
			latestTime = t
		}
	}
	return latestTime
}

func ParseRateLimit(rateStr string) ratelimit.Limiter {
	if rateStr == "" {
		return nil
	}
	parts := strings.SplitN(rateStr, "/", 2)
	if len(parts) != 2 {
		return nil
	}

	// parse count
	count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || count <= 0 {
		return nil
	}

	// Set slack size to 10%
	slackSize := count / 10

	// normalize unit
	unit := strings.ToLower(strings.TrimSpace(parts[1]))
	unit = strings.TrimSuffix(unit, "s")
	switch unit {
	case "minute", "min":
		return ratelimit.New(count, ratelimit.Per(time.Minute), ratelimit.WithSlack(slackSize))
	case "second", "sec":
		return ratelimit.New(count, ratelimit.Per(time.Second), ratelimit.WithSlack(slackSize))
	case "hour", "hr":
		return ratelimit.New(count, ratelimit.Per(time.Hour), ratelimit.WithSlack(slackSize))
	case "day", "d":
		return ratelimit.New(count, ratelimit.Per(24*time.Hour), ratelimit.WithSlack(slackSize))
	default:
		return nil
	}
}

func JSONResponse(w http.ResponseWriter, data interface{}, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	err := json.NewEncoder(w).Encode(data)
	if err != nil {
		return
	}
}

func Default() *Client {
	once.Do(func() {
		instance = New()
	})
	return instance
}

func isRetryableError(err error) bool {
	errString := err.Error()

	// Connection reset and other network errors
	if strings.Contains(errString, "connection reset by peer") ||
		strings.Contains(errString, "read: connection reset") ||
		strings.Contains(errString, "connection refused") ||
		strings.Contains(errString, "network is unreachable") ||
		strings.Contains(errString, "connection timed out") ||
		strings.Contains(errString, "no such host") ||
		strings.Contains(errString, "i/o timeout") ||
		strings.Contains(errString, "unexpected EOF") ||
		strings.Contains(errString, "TLS handshake timeout") {
		return true
	}

	// Check for net.Error type which can provide more information
	var netErr net.Error
	if errors.As(err, &netErr) {
		// Retry on timeout errors and temporary errors
		return netErr.Timeout()
	}

	// Not a retryable error
	return false
}

func SetProxy(transport *http.Transport, proxyURL string) {
	if proxyURL != "" {
		if strings.HasPrefix(proxyURL, "socks5://") {
			// Handle SOCKS5 proxy
			socksURL, err := url.Parse(proxyURL)
			if err != nil {
				return
			} else {
				auth := &proxy.Auth{}
				if socksURL.User != nil {
					auth.User = socksURL.User.Username()
					password, _ := socksURL.User.Password()
					auth.Password = password
				}

				dialer, err := proxy.SOCKS5("tcp", socksURL.Host, auth, proxy.Direct)
				if err != nil {
					return
				} else {
					transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						return dialer.Dial(network, addr)
					}
				}
			}
		} else {
			_proxy, err := url.Parse(proxyURL)
			if err != nil {
				return
			} else {
				transport.Proxy = http.ProxyURL(_proxy)
			}
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return
}
