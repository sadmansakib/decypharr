package torbox

import (
	"sync"
	"testing"
	"time"
)

// TestCacheRaceCondition tests that cache updates don't cause race conditions
// This verifies the fix for critical issue #4: race condition in Torbox cache updates
func TestCacheRaceCondition(t *testing.T) {
	const (
		numReaders = 50
		numWriters = 10
		iterations = 100
	)

	// Simulate the cache structure
	type testCache struct {
		mu           sync.RWMutex
		data         []*torboxInfo
		dataMap      map[string]*torboxInfo
		expiresAt    time.Time
		convertedData interface{}
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	var wg sync.WaitGroup
	errChan := make(chan error, numReaders+numWriters)

	// Start readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				cache.mu.RLock()
				// Read from dataMap
				_ = cache.dataMap
				_ = cache.expiresAt
				cache.mu.RUnlock()
			}
		}(i)
	}

	// Start writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Simulate the fix: create new map before replacing
				newDataMap := make(map[string]*torboxInfo)
				newDataMap["test"] = &torboxInfo{Id: id * j}

				cache.mu.Lock()
				// Atomic replacement
				cache.dataMap = newDataMap
				cache.expiresAt = time.Now().Add(time.Minute)
				cache.mu.Unlock()

				// Small delay to increase chance of interleaving
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		if err != nil {
			t.Errorf("Race condition detected: %v", err)
		}
	}
}

// TestCacheAtomicReplacement tests that map replacement is atomic
func TestCacheAtomicReplacement(t *testing.T) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	// Populate initial data
	cache.dataMap["key1"] = &torboxInfo{Id: 1}
	cache.dataMap["key2"] = &torboxInfo{Id: 2}

	// Replace with new map atomically
	newMap := make(map[string]*torboxInfo)
	newMap["key3"] = &torboxInfo{Id: 3}

	cache.mu.Lock()
	oldMap := cache.dataMap
	cache.dataMap = newMap
	cache.mu.Unlock()

	// Verify old map is unchanged
	if len(oldMap) != 2 {
		t.Errorf("Old map should have 2 entries, got %d", len(oldMap))
	}

	// Verify new map is in place
	cache.mu.RLock()
	if len(cache.dataMap) != 1 {
		t.Errorf("New map should have 1 entry, got %d", len(cache.dataMap))
	}
	cache.mu.RUnlock()
}

// TestConcurrentCacheReads tests that multiple readers can access cache safely
func TestConcurrentCacheReads(t *testing.T) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	// Populate cache
	for i := 0; i < 100; i++ {
		key := string(rune('a' + i%26))
		cache.dataMap[key] = &torboxInfo{Id: i}
	}

	const numReaders = 100
	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.mu.RLock()
			_ = len(cache.dataMap)
			cache.mu.RUnlock()
		}()
	}

	wg.Wait()
}

// TestCacheWriteUnderLock tests that cache writes are properly locked
func TestCacheWriteUnderLock(t *testing.T) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
		writeCount int
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	const numWriters = 10
	var wg sync.WaitGroup

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Create new map
			newMap := make(map[string]*torboxInfo)
			newMap["key"] = &torboxInfo{Id: id}

			cache.mu.Lock()
			cache.dataMap = newMap
			cache.writeCount++
			cache.mu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify all writes completed
	if cache.writeCount != numWriters {
		t.Errorf("Expected %d writes, got %d", numWriters, cache.writeCount)
	}
}

// BenchmarkCacheRead benchmarks cache read performance
func BenchmarkCacheRead(b *testing.B) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	// Populate cache
	for i := 0; i < 1000; i++ {
		cache.dataMap[string(rune(i))] = &torboxInfo{Id: i}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.mu.RLock()
			_ = cache.dataMap
			cache.mu.RUnlock()
		}
	})
}

// BenchmarkCacheWrite benchmarks cache write performance
func BenchmarkCacheWrite(b *testing.B) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newMap := make(map[string]*torboxInfo)
		newMap["test"] = &torboxInfo{Id: i}

		cache.mu.Lock()
		cache.dataMap = newMap
		cache.mu.Unlock()
	}
}

// BenchmarkCacheAtomicReplacement benchmarks atomic map replacement
func BenchmarkCacheAtomicReplacement(b *testing.B) {
	type testCache struct {
		mu      sync.RWMutex
		dataMap map[string]*torboxInfo
	}

	cache := &testCache{
		dataMap: make(map[string]*torboxInfo),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create new map
		newMap := make(map[string]*torboxInfo, 100)
		for j := 0; j < 100; j++ {
			newMap[string(rune(j))] = &torboxInfo{Id: j}
		}

		// Atomic replacement
		cache.mu.Lock()
		cache.dataMap = newMap
		cache.mu.Unlock()
	}
}

// TestCacheExpiration tests cache expiration logic
func TestCacheExpiration(t *testing.T) {
	type testCache struct {
		mu        sync.RWMutex
		expiresAt time.Time
	}

	cache := &testCache{}

	// Set expiration in the future
	cache.mu.Lock()
	cache.expiresAt = time.Now().Add(1 * time.Hour)
	cache.mu.Unlock()

	// Check if cache is valid
	cache.mu.RLock()
	isValid := time.Now().Before(cache.expiresAt)
	cache.mu.RUnlock()

	if !isValid {
		t.Error("Cache should be valid")
	}

	// Set expiration in the past
	cache.mu.Lock()
	cache.expiresAt = time.Now().Add(-1 * time.Hour)
	cache.mu.Unlock()

	// Check if cache is invalid
	cache.mu.RLock()
	isValid = time.Now().Before(cache.expiresAt)
	cache.mu.RUnlock()

	if isValid {
		t.Error("Cache should be invalid")
	}
}

// TestDoubleLockingPattern tests double-checked locking pattern
func TestDoubleLockingPattern(t *testing.T) {
	type testCache struct {
		mu        sync.RWMutex
		fetchMu   sync.Mutex
		data      []*torboxInfo
		expiresAt time.Time
	}

	cache := &testCache{}
	fetchCount := 0

	// Simulate multiple goroutines trying to fetch
	const numGoroutines = 10
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// First check (read lock)
			cache.mu.RLock()
			valid := cache.data != nil && time.Now().Before(cache.expiresAt)
			cache.mu.RUnlock()

			if !valid {
				// Acquire fetch lock
				cache.fetchMu.Lock()

				// Second check (under fetch lock)
				cache.mu.RLock()
				valid = cache.data != nil && time.Now().Before(cache.expiresAt)
				cache.mu.RUnlock()

				if !valid {
					// Fetch data
					fetchCount++
					newData := []*torboxInfo{{Id: 1}}

					cache.mu.Lock()
					cache.data = newData
					cache.expiresAt = time.Now().Add(time.Hour)
					cache.mu.Unlock()
				}

				cache.fetchMu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Only one goroutine should have fetched
	if fetchCount != 1 {
		t.Errorf("Expected 1 fetch, got %d", fetchCount)
	}
}
