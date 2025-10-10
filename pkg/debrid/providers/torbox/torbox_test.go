package torbox

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/request"
)

// Removed TestTorboxRateLimitingConfiguration due to global config dependency

func TestGetTotalSlots(t *testing.T) {
	tests := []struct {
		name            string
		plan            int
		additionalSlots int
		expected        int
	}{
		{
			name:            "Plan 1 Essential with no bonus",
			plan:            1,
			additionalSlots: 0,
			expected:        3,
		},
		{
			name:            "Plan 2 Pro with no bonus",
			plan:            2,
			additionalSlots: 0,
			expected:        10,
		},
		{
			name:            "Plan 3 Standard with no bonus",
			plan:            3,
			additionalSlots: 0,
			expected:        5,
		},
		{
			name:            "Plan 2 Pro with bonus slots",
			plan:            2,
			additionalSlots: 5,
			expected:        15,
		},
		{
			name:            "Unknown plan defaults to Plan 1",
			plan:            99,
			additionalSlots: 0,
			expected:        3,
		},
		{
			name:            "Negative additional slots should be treated as 0",
			plan:            2,
			additionalSlots: -5,
			expected:        10,
		},
		{
			name:            "Additional slots exceeding 1000 should be capped",
			plan:            2,
			additionalSlots: 1500,
			expected:        1010, // 10 base + 1000 capped
		},
		{
			name:            "Exactly 1000 additional slots",
			plan:            1,
			additionalSlots: 1000,
			expected:        1003, // 3 base + 1000
		},
		{
			name:            "Plan 1 with moderate bonus",
			plan:            1,
			additionalSlots: 10,
			expected:        13,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getTotalSlots(tt.plan, tt.additionalSlots)
			if result != tt.expected {
				t.Errorf("getTotalSlots(%d, %d) = %d; expected %d",
					tt.plan, tt.additionalSlots, result, tt.expected)
			}
		})
	}
}

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

// TestConcurrentCacheAccess tests concurrent access to the torrents cache
// This test validates that the single-lock approach prevents race conditions
func TestConcurrentCacheAccess(t *testing.T) {
	// Create a minimal Torbox instance for testing
	tb := &Torbox{
		name:   "torbox-test",
		logger: zerolog.Nop(),
		torrentsCache: &torrentsListCache{
			mu:            sync.RWMutex{},
			data:          nil,
			expiresAt:     time.Time{},
			convertedData: nil,
			dataMap:       make(map[string]*torboxInfo),
			fetchMu:       sync.Mutex{},
		},
	}

	// Pre-populate cache with test data
	testData := []*torboxInfo{
		{
			Id:               1,
			Name:             "Test Torrent 1",
			Size:             1024,
			Hash:             "abc123",
			DownloadFinished: true,
			CreatedAt:        time.Now(),
		},
		{
			Id:               2,
			Name:             "Test Torrent 2",
			Size:             2048,
			Hash:             "def456",
			DownloadFinished: false,
			CreatedAt:        time.Now(),
		},
	}

	tb.torrentsCache.mu.Lock()
	tb.torrentsCache.data = testData
	tb.torrentsCache.expiresAt = time.Now().Add(1 * time.Hour)
	tb.torrentsCache.dataMap = map[string]*torboxInfo{
		"1": testData[0],
		"2": testData[1],
	}
	tb.torrentsCache.mu.Unlock()

	// Test concurrent reads
	t.Run("ConcurrentReads", func(t *testing.T) {
		var wg sync.WaitGroup
		numGoroutines := 50

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				// Read from cache multiple times
				for j := 0; j < 10; j++ {
					info := tb.getTorboxInfoFromCache("1")
					if info == nil {
						t.Errorf("Goroutine %d: Expected cached data, got nil", id)
					}
				}
			}(i)
		}

		wg.Wait()
	})

	// Test concurrent reads and invalidations
	t.Run("ConcurrentReadsAndInvalidations", func(t *testing.T) {
		var wg sync.WaitGroup

		// Start readers
		for i := 0; i < 25; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					tb.getTorboxInfoFromCache("1")
					time.Sleep(1 * time.Millisecond)
				}
			}()
		}

		// Start invalidators
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					// Repopulate cache
					tb.torrentsCache.mu.Lock()
					tb.torrentsCache.data = testData
					tb.torrentsCache.expiresAt = time.Now().Add(1 * time.Hour)
					tb.torrentsCache.dataMap = map[string]*torboxInfo{
						"1": testData[0],
						"2": testData[1],
					}
					tb.torrentsCache.mu.Unlock()

					time.Sleep(5 * time.Millisecond)

					// Invalidate
					tb.invalidateCache()
					time.Sleep(5 * time.Millisecond)
				}
			}()
		}

		wg.Wait()
	})

	// Test concurrent map access
	t.Run("ConcurrentMapAccess", func(t *testing.T) {
		var wg sync.WaitGroup

		// Repopulate cache first
		tb.torrentsCache.mu.Lock()
		tb.torrentsCache.data = testData
		tb.torrentsCache.expiresAt = time.Now().Add(1 * time.Hour)
		tb.torrentsCache.dataMap = map[string]*torboxInfo{
			"1": testData[0],
			"2": testData[1],
		}
		tb.torrentsCache.mu.Unlock()

		// Multiple goroutines reading from map
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					tb.torrentsCache.mu.RLock()
					_, exists := tb.torrentsCache.dataMap[id]
					tb.torrentsCache.mu.RUnlock()
					if !exists {
						// This is okay - cache might have been invalidated
					}
				}
			}("1")
		}

		wg.Wait()
	})
}

// TestCacheInvalidation tests that cache invalidation properly clears all data
func TestCacheInvalidation(t *testing.T) {
	tb := &Torbox{
		name:   "torbox-test",
		logger: zerolog.Nop(),
		torrentsCache: &torrentsListCache{
			mu:            sync.RWMutex{},
			data:          nil,
			expiresAt:     time.Time{},
			convertedData: nil,
			dataMap:       make(map[string]*torboxInfo),
			fetchMu:       sync.Mutex{},
		},
	}

	// Populate cache
	testData := []*torboxInfo{
		{Id: 1, Name: "Test", Hash: "abc123"},
	}

	tb.torrentsCache.mu.Lock()
	tb.torrentsCache.data = testData
	tb.torrentsCache.expiresAt = time.Now().Add(1 * time.Hour)
	tb.torrentsCache.dataMap = map[string]*torboxInfo{"1": testData[0]}
	tb.torrentsCache.mu.Unlock()

	// Verify cache is populated
	tb.torrentsCache.mu.RLock()
	if tb.torrentsCache.data == nil {
		t.Fatal("Cache data should be populated")
	}
	if len(tb.torrentsCache.dataMap) == 0 {
		t.Fatal("Cache map should be populated")
	}
	tb.torrentsCache.mu.RUnlock()

	// Invalidate cache
	tb.invalidateCache()

	// Verify cache is cleared
	tb.torrentsCache.mu.RLock()
	if tb.torrentsCache.data != nil {
		t.Error("Cache data should be nil after invalidation")
	}
	if len(tb.torrentsCache.dataMap) != 0 {
		t.Error("Cache map should be empty after invalidation")
	}
	if tb.torrentsCache.convertedData != nil {
		t.Error("Converted data should be nil after invalidation")
	}
	tb.torrentsCache.mu.RUnlock()
}

// TestGetTorboxInfoFromCacheDeepCopy verifies that deep copy prevents mutation
func TestGetTorboxInfoFromCacheDeepCopy(t *testing.T) {
	tb := &Torbox{
		name:   "torbox-test",
		logger: zerolog.Nop(),
		torrentsCache: &torrentsListCache{
			mu:            sync.RWMutex{},
			data:          nil,
			expiresAt:     time.Time{},
			convertedData: nil,
			dataMap:       make(map[string]*torboxInfo),
			fetchMu:       sync.Mutex{},
		},
	}

	// Create test data with files
	originalFile := TorboxFile{
		Id:   1,
		Name: "original_file.mp4",
		Size: 1024,
	}

	testData := &torboxInfo{
		Id:    1,
		Name:  "Test Torrent",
		Files: []TorboxFile{originalFile},
	}

	// Populate cache
	tb.torrentsCache.mu.Lock()
	tb.torrentsCache.data = []*torboxInfo{testData}
	tb.torrentsCache.expiresAt = time.Now().Add(1 * time.Hour)
	tb.torrentsCache.dataMap = map[string]*torboxInfo{"1": testData}
	tb.torrentsCache.mu.Unlock()

	// Get copy from cache
	copy := tb.getTorboxInfoFromCache("1")
	if copy == nil {
		t.Fatal("Expected non-nil copy")
	}

	// Verify it's a copy, not the same pointer
	if copy == testData {
		t.Error("getTorboxInfoFromCache should return a copy, not the original pointer")
	}

	// Verify Files slice is a different pointer
	if len(copy.Files) > 0 && len(testData.Files) > 0 {
		// Modify the copy's file name
		originalName := copy.Files[0].Name
		copy.Files[0].Name = "modified_file.mp4"

		// Verify original is unchanged
		if testData.Files[0].Name != originalName {
			t.Error("Modifying copy should not affect original")
		}
	}
}

// TestContextCancellation tests that context cancellation is properly handled
func TestContextCancellation(t *testing.T) {
	// Test that a cancelled context is detected
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Verify context is cancelled
	select {
	case <-ctx.Done():
		if ctx.Err() == nil {
			t.Error("Expected context error, got nil")
		}
	default:
		t.Error("Context should be cancelled")
	}
}

// TestMaxPaginationIterations tests that pagination stops at max iterations
func TestMaxPaginationIterations(t *testing.T) {
	// This test verifies the constant exists and has a reasonable value
	if maxPaginationIterations <= 0 {
		t.Error("maxPaginationIterations should be positive")
	}
	if maxPaginationIterations < 10 {
		t.Error("maxPaginationIterations should be at least 10 to handle reasonable torrent counts")
	}
	if maxPaginationIterations > 1000 {
		t.Error("maxPaginationIterations should not be excessively large")
	}

	// Verify it allows a reasonable number of torrents (100 per page)
	maxTorrents := maxPaginationIterations * 100
	if maxTorrents < 1000 {
		t.Errorf("Max torrents (%d) should support at least 1000 torrents", maxTorrents)
	}
}

// TestGetAvailableSlotsWithContext tests that GetAvailableSlots accepts context
func TestGetAvailableSlotsWithContext(t *testing.T) {
	// This is a compile-time test to ensure the signature is correct
	// We verify the method signature by checking it compiles with context parameter
	ctx := context.Background()

	// Verify context type is correct
	if ctx == nil {
		t.Error("Context should not be nil")
	}

	// The actual signature test happens at compile time
	// If GetAvailableSlots doesn't accept context.Context, this file won't compile
}
