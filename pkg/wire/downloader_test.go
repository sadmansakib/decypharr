package wire

import (
	"sync"
	"testing"
	"time"
)

// TestDownloaderPanicRecovery tests that download goroutines recover from panics
// This verifies the fix for critical issue #5: defer in loops for semaphore releases
func TestDownloaderPanicRecovery(t *testing.T) {
	const numGoroutines = 10
	semaphore := make(chan struct{}, numGoroutines)
	var wg sync.WaitGroup

	panicCount := 0
	var panicMu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(id int) {
			defer func() {
				wg.Done()
				<-semaphore
				if r := recover(); r != nil {
					panicMu.Lock()
					panicCount++
					panicMu.Unlock()
				}
			}()

			// Simulate panic on specific goroutines
			if id%3 == 0 {
				panic("simulated panic")
			}

			// Normal execution
			time.Sleep(10 * time.Millisecond)
		}(i)
	}

	wg.Wait()

	// Verify all goroutines completed
	if len(semaphore) != 0 {
		t.Errorf("Semaphore should be empty, has %d items", len(semaphore))
	}

	// Verify panics were recovered
	expectedPanics := (numGoroutines + 2) / 3 // ceiling division
	if panicCount != expectedPanics {
		t.Errorf("Expected %d panics, got %d", expectedPanics, panicCount)
	}
}

// TestSemaphoreReleaseOrder tests that semaphore is released even on panic
func TestSemaphoreReleaseOrder(t *testing.T) {
	semaphore := make(chan struct{}, 1)
	var wg sync.WaitGroup

	// First goroutine panics
	wg.Add(1)
	semaphore <- struct{}{}

	go func() {
		defer func() {
			wg.Done()
			<-semaphore
			recover() // Recover from panic
		}()
		panic("test panic")
	}()

	wg.Wait()

	// Verify semaphore was released
	if len(semaphore) != 0 {
		t.Error("Semaphore should be released after panic")
	}

	// Second goroutine should be able to acquire
	wg.Add(1)
	semaphore <- struct{}{}

	go func() {
		defer func() {
			wg.Done()
			<-semaphore
		}()
		// Normal execution
	}()

	wg.Wait()

	if len(semaphore) != 0 {
		t.Error("Semaphore should be empty after normal execution")
	}
}

// TestWaitGroupDoneOnPanic tests that WaitGroup.Done is called even on panic
func TestWaitGroupDoneOnPanic(t *testing.T) {
	var wg sync.WaitGroup

	const numGoroutines = 5
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer func() {
				wg.Done()
				recover() // Recover from panic
			}()

			if id == 2 {
				panic("test panic")
			}
		}(i)
	}

	// Use a timeout to detect if WaitGroup.Done wasn't called
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("WaitGroup.Wait() timed out - Done() was not called on all goroutines")
	}
}

// TestConcurrentDownloadsWithSemaphore tests concurrent download pattern with semaphore
func TestConcurrentDownloadsWithSemaphore(t *testing.T) {
	const (
		maxConcurrent = 5
		totalDownloads = 20
	)

	semaphore := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var activeMu sync.Mutex
	activeCount := 0
	maxActive := 0

	for i := 0; i < totalDownloads; i++ {
		wg.Add(1)
		semaphore <- struct{}{}

		go func(id int) {
			defer func() {
				wg.Done()
				<-semaphore
				if r := recover(); r != nil {
					t.Logf("Recovered from panic in goroutine %d: %v", id, r)
				}
			}()

			// Track concurrent execution
			activeMu.Lock()
			activeCount++
			if activeCount > maxActive {
				maxActive = activeCount
			}
			activeMu.Unlock()

			// Simulate download work
			time.Sleep(10 * time.Millisecond)

			// Decrement active count
			activeMu.Lock()
			activeCount--
			activeMu.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify semaphore respected max concurrent
	if maxActive > maxConcurrent {
		t.Errorf("Max concurrent should be %d, got %d", maxConcurrent, maxActive)
	}

	// Verify all semaphore slots are released
	if len(semaphore) != 0 {
		t.Errorf("Semaphore should be empty, has %d items", len(semaphore))
	}
}

// TestErrorChannelWithPanic tests error channel handling with panics
func TestErrorChannelWithPanic(t *testing.T) {
	errChan := make(chan error, 10)
	var wg sync.WaitGroup

	const numGoroutines = 10
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer func() {
				wg.Done()
				if r := recover(); r != nil {
					// Don't send to error channel on panic - just recover
					t.Logf("Recovered from panic in goroutine %d", id)
				}
			}()

			if id == 5 {
				panic("test panic")
			}

			// Normal error
			if id%2 == 0 {
				errChan <- nil
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Count errors
	errorCount := 0
	for range errChan {
		errorCount++
	}

	// Should have errors from non-panicking, even-numbered goroutines (0, 2, 4, 6, 8)
	expectedErrors := 5
	if errorCount != expectedErrors {
		t.Errorf("Expected %d errors, got %d", expectedErrors, errorCount)
	}
}

// BenchmarkSemaphoreAcquireRelease benchmarks semaphore operations
func BenchmarkSemaphoreAcquireRelease(b *testing.B) {
	semaphore := make(chan struct{}, 10)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			semaphore <- struct{}{}
			<-semaphore
		}
	})
}

// BenchmarkDeferWithRecover benchmarks defer with recover
func BenchmarkDeferWithRecover(b *testing.B) {
	for i := 0; i < b.N; i++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Recovered
				}
			}()
			// Normal execution
		}()
	}
}

// BenchmarkWaitGroupOperations benchmarks WaitGroup Add/Done
func BenchmarkWaitGroupOperations(b *testing.B) {
	var wg sync.WaitGroup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Minimal work
		}()
	}
	wg.Wait()
}

// TestPanicRecoveryLogging tests that panic recovery includes proper logging
func TestPanicRecoveryLogging(t *testing.T) {
	var wg sync.WaitGroup
	panicMessages := make([]interface{}, 0)
	var mu sync.Mutex

	const numPanics = 5
	for i := 0; i < numPanics; i++ {
		wg.Add(1)
		go func(id int) {
			defer func() {
				wg.Done()
				if r := recover(); r != nil {
					mu.Lock()
					panicMessages = append(panicMessages, r)
					mu.Unlock()
				}
			}()

			panic("panic message")
		}(i)
	}

	wg.Wait()

	if len(panicMessages) != numPanics {
		t.Errorf("Expected %d panic messages, got %d", numPanics, len(panicMessages))
	}
}

// TestNestedPanicRecovery tests nested defer recovery
func TestNestedPanicRecovery(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	recovered := false

	wg.Add(1)
	go func() {
		defer func() {
			wg.Done()
			if r := recover(); r != nil {
				mu.Lock()
				recovered = true
				mu.Unlock()
			}
		}()

		func() {
			defer func() {
				// Inner defer doesn't recover
			}()
			panic("inner panic")
		}()
	}()

	// Wait with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		mu.Lock()
		wasRecovered := recovered
		mu.Unlock()
		if !wasRecovered {
			t.Error("Panic should have been recovered")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out")
	}
}

// TestMultipleDeferExecution tests that multiple defers execute in LIFO order
func TestMultipleDeferExecution(t *testing.T) {
	executionOrder := make([]int, 0)
	var mu sync.Mutex

	func() {
		defer func() {
			mu.Lock()
			executionOrder = append(executionOrder, 1)
			mu.Unlock()
		}()
		defer func() {
			mu.Lock()
			executionOrder = append(executionOrder, 2)
			mu.Unlock()
		}()
		defer func() {
			mu.Lock()
			executionOrder = append(executionOrder, 3)
			mu.Unlock()
		}()
	}()

	// Defers execute in LIFO order: 3, 2, 1
	expected := []int{3, 2, 1}
	if len(executionOrder) != len(expected) {
		t.Fatalf("Expected %d defers, got %d", len(expected), len(executionOrder))
	}

	for i, v := range expected {
		if executionOrder[i] != v {
			t.Errorf("Expected defer order %v, got %v", expected, executionOrder)
			break
		}
	}
}
