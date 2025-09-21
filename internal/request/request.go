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

// CircuitBreakerConfig defines configuration for circuit breaker
type CircuitBreakerConfig struct {
	FailureThreshold    int           // Number of consecutive 429s to open circuit
	RecoveryTimeout     time.Duration // Time before attempting half-open
	MaxHalfOpenRequests int           // Max requests allowed in half-open state
}

// CircuitBreaker manages the circuit breaker state for rate limiting
type CircuitBreaker struct {
	config           CircuitBreakerConfig
	state            int32 // CircuitBreakerState (atomic)
	failureCount     int32 // Consecutive failures (atomic)
	halfOpenRequests int32 // Requests in half-open state (atomic)
	lastFailureTime  int64 // Unix timestamp of last failure (atomic)
	logger           zerolog.Logger
	mu               sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig, logger zerolog.Logger) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  int32(CircuitClosed),
		logger: logger,
	}
}

// Allow checks if a request should be allowed through the circuit breaker
func (cb *CircuitBreaker) Allow() error {
	state := CircuitBreakerState(atomic.LoadInt32(&cb.state))

	switch state {
	case CircuitClosed:
		return nil
	case CircuitOpen:
		// Check if recovery timeout has passed
		lastFailure := time.Unix(atomic.LoadInt64(&cb.lastFailureTime), 0)
		if time.Since(lastFailure) >= cb.config.RecoveryTimeout {
			// Transition to half-open
			if atomic.CompareAndSwapInt32(&cb.state, int32(CircuitOpen), int32(CircuitHalfOpen)) {
				atomic.StoreInt32(&cb.halfOpenRequests, 0)
				cb.logger.Info().Msg("Circuit breaker transitioning to half-open state")
			}
			return nil
		}
		return fmt.Errorf("circuit breaker is open - rate limit protection active")
	case CircuitHalfOpen:
		// Allow limited requests in half-open state
		if atomic.LoadInt32(&cb.halfOpenRequests) < int32(cb.config.MaxHalfOpenRequests) {
			atomic.AddInt32(&cb.halfOpenRequests, 1)
			return nil
		}
		return fmt.Errorf("circuit breaker half-open request limit exceeded")
	default:
		return nil
	}
}

// RecordSuccess records a successful response
func (cb *CircuitBreaker) RecordSuccess() {
	state := CircuitBreakerState(atomic.LoadInt32(&cb.state))

	switch state {
	case CircuitHalfOpen:
		// Reset to closed state after successful request in half-open
		if atomic.CompareAndSwapInt32(&cb.state, int32(CircuitHalfOpen), int32(CircuitClosed)) {
			atomic.StoreInt32(&cb.failureCount, 0)
			cb.logger.Info().Msg("Circuit breaker closed - service recovered")
		}
	case CircuitClosed:
		// Reset failure count on success
		atomic.StoreInt32(&cb.failureCount, 0)
	}
}

// RecordFailure records a 429 response failure
func (cb *CircuitBreaker) RecordFailure() {
	atomic.StoreInt64(&cb.lastFailureTime, time.Now().Unix())
	failures := atomic.AddInt32(&cb.failureCount, 1)

	state := CircuitBreakerState(atomic.LoadInt32(&cb.state))

	switch state {
	case CircuitClosed:
		if failures >= int32(cb.config.FailureThreshold) {
			if atomic.CompareAndSwapInt32(&cb.state, int32(CircuitClosed), int32(CircuitOpen)) {
				cb.logger.Warn().Int32("failures", failures).Msg("Circuit breaker opened due to rate limit violations")
			}
		}
	case CircuitHalfOpen:
		// Immediately return to open state on failure during half-open
		if atomic.CompareAndSwapInt32(&cb.state, int32(CircuitHalfOpen), int32(CircuitOpen)) {
			cb.logger.Warn().Msg("Circuit breaker reopened - service still rate limiting")
		}
	}
}

type ClientOption func(*Client)

// EndpointRateLimiter represents a rate limiter for a specific endpoint pattern
type EndpointRateLimiter struct {
	Pattern    string              // URL pattern to match (supports simple string matching or regex)
	Limiters   []ratelimit.Limiter // Multiple rate limiters (e.g., hourly + per-minute)
	IsRegex    bool                // Whether pattern is a regex
	CompiledRx *regexp.Regexp      // Compiled regex if IsRegex is true
}

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
	logger               zerolog.Logger
	proxy                string
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
		c.retryableStatus = make(map[int]struct{}) // reset the map
		for _, code := range statusCodes {
			c.retryableStatus[code] = struct{}{}
		}
	}
}

func WithProxy(proxyURL string) ClientOption {
	return func(c *Client) {
		c.proxy = proxyURL
	}
}

// doRequest performs a single HTTP request with rate limiting and circuit breaker
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

	return c.client.Do(req)
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
		if _, ok := c.retryableStatus[resp.StatusCode]; !ok || attempt == c.maxRetries {
			return resp, nil
		}

		// Close the response body before retrying
		resp.Body.Close()

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

func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GET request: %w", err)
	}

	return c.Do(req)
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
		if client.proxy != "" {
			if strings.HasPrefix(client.proxy, "socks5://") {
				// Handle SOCKS5 proxy
				socksURL, err := url.Parse(client.proxy)
				if err != nil {
					client.logger.Error().Msgf("Failed to parse SOCKS5 proxy URL: %v", err)
				} else {
					auth := &proxy.Auth{}
					if socksURL.User != nil {
						auth.User = socksURL.User.Username()
						password, _ := socksURL.User.Password()
						auth.Password = password
					}

					dialer, err := proxy.SOCKS5("tcp", socksURL.Host, auth, proxy.Direct)
					if err != nil {
						client.logger.Error().Msgf("Failed to create SOCKS5 dialer: %v", err)
					} else {
						transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
							return dialer.Dial(network, addr)
						}
					}
				}
			} else {
				proxyURL, err := url.Parse(client.proxy)
				if err != nil {
					client.logger.Error().Msgf("Failed to parse proxy URL: %v", err)
				} else {
					transport.Proxy = http.ProxyURL(proxyURL)
				}
			}
		} else {
			transport.Proxy = http.ProxyFromEnvironment
		}

		// Set the transport to the client
		client.client.Transport = transport
	}

	return client
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
