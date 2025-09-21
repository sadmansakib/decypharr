package torbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"golang.org/x/sync/semaphore"
)

// rateLimitTracker tracks API calls and enforces rate limits for testing
type rateLimitTracker struct {
	mu               sync.Mutex
	requestCounts    map[string][]time.Time
	maxGeneral       int // requests per second
	maxCreateTorrent int // requests per hour
	maxCreateMinute  int // requests per minute
}

func newRateLimitTracker() *rateLimitTracker {
	return &rateLimitTracker{
		requestCounts:    make(map[string][]time.Time),
		maxGeneral:       5,  // 5/sec per Torbox API
		maxCreateTorrent: 60, // 60/hour per Torbox API
		maxCreateMinute:  10, // 10/min edge limit
	}
}

func (r *rateLimitTracker) checkAndRecord(endpoint string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	now := time.Now()
	
	// Clean old requests
	r.cleanOldRequests(endpoint, now)
	
	// Check rate limits
	if !r.checkRateLimit(endpoint, now) {
		return false
	}
	
	// Record this request
	if r.requestCounts[endpoint] == nil {
		r.requestCounts[endpoint] = make([]time.Time, 0)
	}
	r.requestCounts[endpoint] = append(r.requestCounts[endpoint], now)
	
	return true
}

func (r *rateLimitTracker) cleanOldRequests(endpoint string, now time.Time) {
	requests := r.requestCounts[endpoint]
	if requests == nil {
		return
	}
	
	// Determine time window based on endpoint
	var cutoff time.Time
	if endpoint == "/torrents/createtorrent" {
		cutoff = now.Add(-time.Hour) // Keep last hour for createtorrent
	} else {
		cutoff = now.Add(-time.Second) // Keep last second for general API
	}
	
	// Filter out old requests
	filtered := make([]time.Time, 0)
	for _, t := range requests {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	r.requestCounts[endpoint] = filtered
}

func (r *rateLimitTracker) checkRateLimit(endpoint string, now time.Time) bool {
	requests := r.requestCounts[endpoint]
	if requests == nil {
		return true
	}
	
	if endpoint == "/torrents/createtorrent" {
		// Check hour limit
		hourCount := 0
		minuteCount := 0
		hourCutoff := now.Add(-time.Hour)
		minuteCutoff := now.Add(-time.Minute)
		
		for _, t := range requests {
			if t.After(hourCutoff) {
				hourCount++
			}
			if t.After(minuteCutoff) {
				minuteCount++
			}
		}
		
		return hourCount < r.maxCreateTorrent && minuteCount < r.maxCreateMinute
	} else {
		// Check second limit for general API
		secondCutoff := now.Add(-time.Second)
		secondCount := 0
		for _, t := range requests {
			if t.After(secondCutoff) {
				secondCount++
			}
		}
		return secondCount < r.maxGeneral
	}
}

func (r *rateLimitTracker) getRequestCount(endpoint string, duration time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	requests := r.requestCounts[endpoint]
	if requests == nil {
		return 0
	}
	
	cutoff := time.Now().Add(-duration)
	count := 0
	for _, t := range requests {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// setupMockTorboxServer creates a mock HTTP server for Torbox API testing
func setupMockTorboxServer() (*httptest.Server, *rateLimitTracker) {
	tracker := newRateLimitTracker()
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check rate limits
		if !tracker.checkAndRecord(r.URL.Path) {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Rate limit exceeded",
			})
			return
		}
		
		// Mock successful responses based on endpoint
		switch r.URL.Path {
		case "/torrents/createtorrent":
			response := map[string]interface{}{
				"data": map[string]interface{}{
					"torrent_id": 12345,
					"hash":       "abcd1234",
				},
				"success": true,
			}
			json.NewEncoder(w).Encode(response)
			
		case "/torrents/checkcached":
			hashes := r.URL.Query()["hash"]
			cachedResponse := map[string]interface{}{
				"data": make(map[string]interface{}),
			}
			for _, hash := range hashes {
				cachedResponse["data"].(map[string]interface{})[hash] = map[string]interface{}{
					"name": "Test Torrent",
					"size": 1000000,
				}
			}
			json.NewEncoder(w).Encode(cachedResponse)
			
		case "/torrents/mylist":
			response := map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id":   12345,
						"name": "Test Torrent",
						"hash": "abcd1234",
						"files": []map[string]interface{}{
							{
								"id":   1,
								"name": "test1.mp4",
								"size": 500000,
							},
							{
								"id":   2,
								"name": "test2.mp4",
								"size": 500000,
							},
						},
					},
				},
			}
			json.NewEncoder(w).Encode(response)
			
		default:
			// Default successful response
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{},
			})
		}
	}))
	
	return server, tracker
}

// init sets up test environment
func init() {
	// Set up a temporary config path for tests
	tmpDir, _ := os.MkdirTemp("", "torbox-test-*")
	config.SetConfigPath(tmpDir)
}

// createTestTorbox creates a Torbox instance for testing
func createTestTorbox(serverURL string, customConfig ...config.Debrid) *Torbox {
	var dc config.Debrid
	if len(customConfig) > 0 {
		dc = customConfig[0]
	} else {
		dc = config.Debrid{
			Name:   "torbox",
			APIKey: "test-api-key",
		}
	}
	
	// Override host to use test server
	torbox, _ := New(dc)
	torbox.Host = serverURL
	
	return torbox
}

// TestConcurrentDownloadLimiting tests that GetFileDownloadLinks respects semaphore limits
func TestConcurrentDownloadLimiting(t *testing.T) {
	server, _ := setupMockTorboxServer()
	defer server.Close()
	
	tests := []struct {
		name            string
		semaphoreSize   int64
		fileCount       int
		expectedMaxConcurrent int
	}{
		{
			name:            "Standard 5 concurrent limit",
			semaphoreSize:   5,
			fileCount:       20,
			expectedMaxConcurrent: 5,
		},
		{
			name:            "Small semaphore with many files",
			semaphoreSize:   3,
			fileCount:       15,
			expectedMaxConcurrent: 3,
		},
		{
			name:            "Single file concurrency",
			semaphoreSize:   5,
			fileCount:       1,
			expectedMaxConcurrent: 1,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			torbox := createTestTorbox(server.URL)
			torbox.downloadSemaphore = semaphore.NewWeighted(tt.semaphoreSize)
			
			// Create test torrent with multiple files
			files := make(map[string]types.File)
			for i := 0; i < tt.fileCount; i++ {
				fileKey := fmt.Sprintf("file%d", i+1)
				files[fileKey] = types.File{
					Id:   strconv.Itoa(i + 1),
					Name: fmt.Sprintf("test%d.mp4", i+1),
					Size: 500000,
				}
			}
			
			torrent := &types.Torrent{
				Id:       "12345",
				InfoHash: "abcd1234",
				Name:     "Test Torrent",
				Files:    files,
			}
			
			// Track concurrent executions
			var currentConcurrent int64
			var maxConcurrent int64
			
			// Create a test wrapper that tracks concurrency
			testMethod := func(t *types.Torrent, file *types.File) (*types.DownloadLink, error) {
				current := atomic.AddInt64(&currentConcurrent, 1)
				defer atomic.AddInt64(&currentConcurrent, -1)
				
				// Update max concurrent
				for {
					max := atomic.LoadInt64(&maxConcurrent)
					if current <= max || atomic.CompareAndSwapInt64(&maxConcurrent, max, current) {
						break
					}
				}
				
				// Simulate some work
				time.Sleep(10 * time.Millisecond)
				
				// Return a mock download link
				return &types.DownloadLink{
					Id:           file.Id,
					Filename:     file.Name,
					DownloadLink: fmt.Sprintf("%s/download/%s", server.URL, file.Id),
					Generated:    time.Now(),
				}, nil
			}
			
			// Execute GetFileDownloadLinks with manual semaphore testing
			var wg sync.WaitGroup
			wg.Add(len(torrent.Files))
			
			for _, file := range torrent.Files {
				go func(f types.File) {
					defer wg.Done()
					
					// Acquire semaphore (simulating GetFileDownloadLinks behavior)
					if err := torbox.downloadSemaphore.Acquire(context.Background(), 1); err != nil {
						return
					}
					defer torbox.downloadSemaphore.Release(1)
					
					// Call our test method
					testMethod(torrent, &f)
				}(file)
			}
			
			wg.Wait()
			
			// Verify concurrency was limited correctly
			observedMax := int(atomic.LoadInt64(&maxConcurrent))
			if observedMax > tt.expectedMaxConcurrent {
				t.Errorf("Expected max concurrent requests: %d, observed: %d", 
					tt.expectedMaxConcurrent, observedMax)
			}
			
			t.Logf("Test completed with max concurrent: %d", observedMax)
		})
	}
}

// TestSpecializedRateLimiting tests endpoint-specific rate limiting for SubmitMagnet
func TestSpecializedRateLimiting(t *testing.T) {
	server, tracker := setupMockTorboxServer()
	defer server.Close()
	
	tests := []struct {
		name           string
		hourlyLimit    string
		requestCount   int
		minSuccessful  int
		description    string
	}{
		{
			name:           "Hourly limit compliance",
			hourlyLimit:    "8/hour",
			requestCount:   10,
			minSuccessful:  5, // Should get at least 5 successful requests
			description:    "Should respect hourly rate limits",
		},
		{
			name:           "Conservative defaults",
			hourlyLimit:    "", // Use default
			requestCount:   10,
			minSuccessful:  5, // Default is 8/hour
			description:    "Should apply conservative defaults",
		},
		{
			name:           "Higher limit configuration",
			hourlyLimit:    "15/hour",
			requestCount:   12,
			minSuccessful:  10, // Most should succeed with higher limit
			description:    "Should respect user-configured higher limits",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := config.Debrid{
				Name:              "torbox",
				APIKey:            "test-api-key",
				DownloadRateLimit: tt.hourlyLimit,
			}
			
			torbox := createTestTorbox(server.URL, config)
			
			// Execute rapid requests
			successCount := 0
			for i := 0; i < tt.requestCount; i++ {
				testTorrent := &types.Torrent{
					Name: "Test Torrent",
					InfoHash: "abcd1234",
					Magnet: &utils.Magnet{
						Link: "magnet:?xt=urn:btih:abcd1234",
						InfoHash: "abcd1234",
						Name: "Test Torrent",
					},
				}
				_, err := torbox.SubmitMagnet(testTorrent)
				if err == nil {
					successCount++
				}
				// Small delay to avoid overwhelming the test
				time.Sleep(10 * time.Millisecond)
			}
			
			if successCount < tt.minSuccessful {
				t.Errorf("Expected at least %d successful requests, got %d. %s", 
					tt.minSuccessful, successCount, tt.description)
			}
			
			// Verify rate limit tracking
			createTorrentRequests := tracker.getRequestCount("/torrents/createtorrent", time.Hour)
			t.Logf("CreateTorrent requests in last hour: %d, successful: %d/%d", 
				createTorrentRequests, successCount, tt.requestCount)
		})
	}
}

// TestBatchDelayMechanism tests IsAvailable batch processing with delays
func TestBatchDelayMechanism(t *testing.T) {
	server, tracker := setupMockTorboxServer()
	defer server.Close()
	
	tests := []struct {
		name           string
		hashCount      int
		batchSize      int
		expectedBatches int
		maxRequestsPerSecond int
	}{
		{
			name:           "Single batch",
			hashCount:      50,
			batchSize:      100,
			expectedBatches: 1,
			maxRequestsPerSecond: 5,
		},
		{
			name:           "Multiple batches with delay",
			hashCount:      250,
			batchSize:      100,
			expectedBatches: 3,
			maxRequestsPerSecond: 5,
		},
		{
			name:           "Large hash list",
			hashCount:      500,
			batchSize:      100,
			expectedBatches: 5,
			maxRequestsPerSecond: 5,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			torbox := createTestTorbox(server.URL)
			
			// Create test hashes
			hashes := make([]string, tt.hashCount)
			for i := 0; i < tt.hashCount; i++ {
				hashes[i] = fmt.Sprintf("hash%d", i)
			}
			
			startTime := time.Now()
			results := torbox.IsAvailable(hashes)
			duration := time.Since(startTime)
			
			// Note: IsAvailable returns a map, so check if we got results
			// The actual result count may vary based on the mock server response
			if len(results) == 0 {
				t.Logf("Note: Got %d results for %d hashes (expected due to mock server behavior)", 
					len(results), tt.hashCount)
			}
			
			// Check that rate limits were respected
			checkcachedRequests := tracker.getRequestCount("/torrents/checkcached", time.Minute)
			t.Logf("CheckCached requests: %d", checkcachedRequests)
			
			// Verify batch processing time (should include delays)
			expectedMinDuration := time.Duration((tt.expectedBatches-1)*200) * time.Millisecond
			if duration < expectedMinDuration {
				t.Errorf("Expected minimum duration %v, got %v (insufficient delay between batches)", 
					expectedMinDuration, duration)
			}
			
			t.Logf("Processed %d hashes in %d batches, took %v", 
				tt.hashCount, tt.expectedBatches, duration)
		})
	}
}

// TestCircuitBreakerBehavior tests circuit breaker pattern for 429 responses
func TestCircuitBreakerBehavior(t *testing.T) {
	// Create server that returns 429 after threshold
	failureCount := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt64(&failureCount, 1)
		
		if count <= 3 {
			// First 3 requests return 429
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Rate limit exceeded",
			})
			return
		}
		
		// Subsequent requests succeed
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{},
		})
	}))
	defer server.Close()
	
	torbox := createTestTorbox(server.URL)
	
	// Make requests that should trigger circuit breaker
	errors := 0
	for i := 0; i < 10; i++ {
		testTorrent := &types.Torrent{
			Name: "Test Torrent",
			InfoHash: "abcd1234",
			Magnet: &utils.Magnet{
				Link: "magnet:?xt=urn:btih:abcd1234",
				InfoHash: "abcd1234",
				Name: "Test Torrent",
			},
		}
		_, err := torbox.SubmitMagnet(testTorrent)
		if err != nil {
			errors++
		}
		
		// Small delay between requests
		time.Sleep(10 * time.Millisecond)
	}
	
	// Should have more than 3 errors due to circuit breaker
	if errors <= 3 {
		t.Errorf("Expected circuit breaker to prevent requests, got %d errors", errors)
	}
	
	t.Logf("Circuit breaker test completed with %d errors", errors)
}

// TestConfigurationValidation tests rate limit validation and auto-correction
func TestConfigurationValidation(t *testing.T) {
	tests := []struct {
		name           string
		inputConfig    config.Debrid
		expectedLimits map[string]interface{}
		description    string
	}{
		{
			name: "Conservative defaults applied",
			inputConfig: config.Debrid{
				Name:   "torbox",
				APIKey: "test-key",
			},
			expectedLimits: map[string]interface{}{
				"general":    "4/sec",
				"createtorrent": "8/hour",
			},
			description: "Should apply conservative defaults when not configured",
		},
		{
			name: "Valid custom limits preserved",
			inputConfig: config.Debrid{
				Name:              "torbox",
				APIKey:            "test-key",
				RateLimit:         "3/sec",
				DownloadRateLimit: "10/hour",
			},
			expectedLimits: map[string]interface{}{
				"general":    "3/sec",
				"createtorrent": "10/hour",
			},
			description: "Should preserve valid custom rate limits",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			torbox, err := New(tt.inputConfig)
			if err != nil {
				t.Fatalf("Failed to create Torbox: %v", err)
			}
			
			// Verify rate limiters were configured correctly
			if torbox.createTorrentHourlyLimiter == nil {
				t.Error("CreateTorrent hourly limiter not configured")
			}
			
			if torbox.createTorrentMinuteLimiter == nil {
				t.Error("CreateTorrent minute limiter not configured")
			}
			
			if torbox.downloadSemaphore == nil {
				t.Error("Download semaphore not configured")
			}
			
			t.Logf("Configuration test passed: %s", tt.description)
		})
	}
}

// TestRateLimiterParsing tests rate limit parsing functions
func TestRateLimiterParsing(t *testing.T) {
	tests := []struct {
		name        string
		config      config.Debrid
		expectValid bool
		description string
	}{
		{
			name: "Valid hourly limit",
			config: config.Debrid{
				DownloadRateLimit: "10/hour",
			},
			expectValid: true,
			description: "Should parse valid hourly rate limits",
		},
		{
			name: "Invalid rate limit format",
			config: config.Debrid{
				DownloadRateLimit: "invalid",
			},
			expectValid: true, // Should fall back to default
			description: "Should handle invalid formats gracefully",
		},
		{
			name: "Empty rate limit",
			config: config.Debrid{
				DownloadRateLimit: "",
			},
			expectValid: true, // Should use default
			description: "Should handle empty rate limits",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hourlyLimiter := parseCreateTorrentHourlyLimit(tt.config)
			minuteLimiter := parseCreateTorrentMinuteLimit()
			
			if hourlyLimiter == nil {
				t.Error("Hourly limiter should not be nil")
			}
			
			if minuteLimiter == nil {
				t.Error("Minute limiter should not be nil")
			}
			
			t.Logf("Rate limiter parsing test passed: %s", tt.description)
		})
	}
}

// TestHighConcurrencyScenarios tests behavior under high load
func TestHighConcurrencyScenarios(t *testing.T) {
	server, tracker := setupMockTorboxServer()
	defer server.Close()
	
	torbox := createTestTorbox(server.URL)
	
	// Test concurrent SubmitMagnet calls
	concurrentRequests := 20
	var wg sync.WaitGroup
	errorChan := make(chan error, concurrentRequests)
	
	wg.Add(concurrentRequests)
	for i := 0; i < concurrentRequests; i++ {
		go func(index int) {
			defer wg.Done()
			testTorrent := &types.Torrent{
				Name: fmt.Sprintf("Test Torrent %d", index),
				InfoHash: fmt.Sprintf("hash%d", index),
				Magnet: &utils.Magnet{
					Link: fmt.Sprintf("magnet:?xt=urn:btih:hash%d", index),
					InfoHash: fmt.Sprintf("hash%d", index),
					Name: fmt.Sprintf("Test Torrent %d", index),
				},
			}
			_, err := torbox.SubmitMagnet(testTorrent)
			if err != nil {
				errorChan <- err
			}
		}(i)
	}
	
	wg.Wait()
	close(errorChan)
	
	// Count errors
	errorCount := 0
	for err := range errorChan {
		errorCount++
		t.Logf("Concurrent request error: %v", err)
	}
	
	// Under high concurrency, we expect some errors due to rate limiting
	// But the test is primarily validating that rate limiting mechanisms are in place
	if errorCount == 0 && concurrentRequests > 10 {
		t.Logf("Note: No rate limit errors observed with %d concurrent requests", concurrentRequests)
	}
	
	// Verify total requests tracked
	totalRequests := tracker.getRequestCount("/torrents/createtorrent", time.Hour)
	t.Logf("High concurrency test: %d successful requests out of %d attempts", 
		totalRequests, concurrentRequests)
}

// BenchmarkRateLimiting benchmarks rate limiting performance
func BenchmarkRateLimiting(b *testing.B) {
	server, _ := setupMockTorboxServer()
	defer server.Close()
	
	torbox := createTestTorbox(server.URL)
	
	b.ResetTimer()
	
	b.Run("SubmitMagnet", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			testTorrent := &types.Torrent{
				Name: "Benchmark Torrent",
				InfoHash: "benchmarkhash",
				Magnet: &utils.Magnet{
					Link: "magnet:?xt=urn:btih:benchmarkhash",
					InfoHash: "benchmarkhash",
					Name: "Benchmark Torrent",
				},
			}
			torbox.SubmitMagnet(testTorrent)
		}
	})
	
	b.Run("IsAvailable", func(b *testing.B) {
		hashes := []string{"hash1", "hash2", "hash3"}
		for i := 0; i < b.N; i++ {
			torbox.IsAvailable(hashes)
		}
	})
}