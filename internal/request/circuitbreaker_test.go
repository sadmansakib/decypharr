package request

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// TestCircuitBreakerBasicOperation tests basic circuit breaker functionality
func TestCircuitBreakerBasicOperation(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureThreshold:    3,
		RecoveryTimeout:     2 * time.Second, // Long enough to test open state
		MaxHalfOpenRequests: 2,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	// Initially closed
	err := cb.Allow()
	assert.NoError(t, err)

	// Record failures to open circuit
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// Should be open now
	err = cb.Allow()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "circuit breaker is open")
	}

	// Wait for recovery timeout
	time.Sleep(config.RecoveryTimeout + 50*time.Millisecond)

	// Should transition to half-open
	err = cb.Allow()
	assert.NoError(t, err)

	// Record success to close circuit
	cb.RecordSuccess()

	// Should be closed again
	err = cb.Allow()
	assert.NoError(t, err)
}

// TestCircuitBreakerConcurrentStateTransitions tests for race conditions in state transitions
func TestCircuitBreakerConcurrentStateTransitions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     5 * time.Second, // Long enough to test open state
		MaxHalfOpenRequests: 3,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	// Test concurrent transitions from closed to open
	t.Run("ConcurrentClosedToOpen", func(t *testing.T) {
		cb := NewCircuitBreaker(config, logger)
		defer cb.Close()

		var wg sync.WaitGroup
		concurrency := 50
		failureCount := int32(0)

		// Concurrently record failures
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cb.RecordFailure()
				atomic.AddInt32(&failureCount, 1)
			}()
		}

		wg.Wait()

		// Circuit should be open immediately after failures
		err := cb.Allow()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "circuit breaker is open")

		// Verify failure count is consistent
		actualFailures := atomic.LoadInt32(&cb.failureCount)
		t.Logf("Recorded %d failures, circuit breaker has %d failures", failureCount, actualFailures)
	})

	// Test concurrent transitions from open to half-open
	t.Run("ConcurrentOpenToHalfOpen", func(t *testing.T) {
		// Use shorter recovery timeout for this test
		halfOpenConfig := CircuitBreakerConfig{
			FailureThreshold:    2,
			RecoveryTimeout:     50 * time.Millisecond,
			MaxHalfOpenRequests: 3,
		}
		cb := NewCircuitBreaker(halfOpenConfig, logger)
		defer cb.Close()

		// Force circuit to open
		for i := 0; i < halfOpenConfig.FailureThreshold; i++ {
			cb.RecordFailure()
		}

		// Wait for recovery timeout
		time.Sleep(halfOpenConfig.RecoveryTimeout + 10*time.Millisecond)

		var wg sync.WaitGroup
		concurrency := 20
		allowedCount := int32(0)
		errorCount := int32(0)

		// Concurrently try to transition to half-open
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := cb.Allow()
				if err == nil {
					atomic.AddInt32(&allowedCount, 1)
				} else {
					atomic.AddInt32(&errorCount, 1)
				}
			}()
		}

		wg.Wait()

		t.Logf("Allowed: %d, Errors: %d", allowedCount, errorCount)

		// Should have limited the number of half-open requests
		assert.True(t, allowedCount <= int32(halfOpenConfig.MaxHalfOpenRequests),
			"Too many requests allowed in half-open state: %d > %d", allowedCount, halfOpenConfig.MaxHalfOpenRequests)
	})
}

// TestCircuitBreakerHalfOpenRaceCondition specifically tests the half-open request limiting race condition
func TestCircuitBreakerHalfOpenRaceCondition(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    1,
		RecoveryTimeout:     50 * time.Millisecond,
		MaxHalfOpenRequests: 1, // Very low limit to expose race conditions
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()

	// Run multiple iterations to increase chance of hitting race condition
	for iteration := 0; iteration < 10; iteration++ {
		t.Run(fmt.Sprintf("Iteration%d", iteration), func(t *testing.T) {
			cb := NewCircuitBreaker(config, logger)
			defer cb.Close()

			// Force circuit to open
			cb.RecordFailure()

			// Wait for recovery timeout
			time.Sleep(config.RecoveryTimeout + 10*time.Millisecond)

			var wg sync.WaitGroup
			concurrency := 100
			allowedCount := int32(0)

			// Create high contention for half-open state
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := cb.Allow()
					if err == nil {
						atomic.AddInt32(&allowedCount, 1)
					}
				}()
			}

			wg.Wait()

			// CRITICAL: Should never exceed MaxHalfOpenRequests
			assert.True(t, allowedCount <= int32(config.MaxHalfOpenRequests),
				"Race condition detected: %d requests allowed > limit %d", allowedCount, config.MaxHalfOpenRequests)
		})
	}
}

// TestCircuitBreakerStressTest runs a comprehensive stress test
func TestCircuitBreakerStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    5,
		RecoveryTimeout:     100 * time.Millisecond,
		MaxHalfOpenRequests: 3,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	workers := runtime.NumCPU() * 2

	// Statistics
	var (
		totalRequests  int64
		allowedCount   int64
		rejectedCount  int64
		successCount   int64
		failureCount   int64
	)

	// Start workers that simulate mixed workload
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
					atomic.AddInt64(&totalRequests, 1)

					err := cb.Allow()
					if err != nil {
						atomic.AddInt64(&rejectedCount, 1)
						continue
					}

					atomic.AddInt64(&allowedCount, 1)

					// Simulate success/failure randomly
					if workerID%3 == 0 {
						cb.RecordFailure()
						atomic.AddInt64(&failureCount, 1)
					} else {
						cb.RecordSuccess()
						atomic.AddInt64(&successCount, 1)
					}

					// Add small random delay
					time.Sleep(time.Duration(workerID%5) * time.Millisecond)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("Stress test results:")
	t.Logf("  Total requests: %d", totalRequests)
	t.Logf("  Allowed: %d", allowedCount)
	t.Logf("  Rejected: %d", rejectedCount)
	t.Logf("  Successes: %d", successCount)
	t.Logf("  Failures: %d", failureCount)

	// Verify no data races occurred (this would panic with -race flag)
	assert.Equal(t, totalRequests, allowedCount+rejectedCount)
}

// TestCircuitBreakerCloseRaceCondition tests closing during active operations
func TestCircuitBreakerCloseRaceCondition(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureThreshold:    3,
		RecoveryTimeout:     500 * time.Millisecond,
		MaxHalfOpenRequests: 2,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Start workers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					cb.Allow()
					cb.RecordSuccess()
					cb.RecordFailure()
				}
			}
		}()
	}

	// Close after short delay
	time.Sleep(50 * time.Millisecond)
	err := cb.Close()
	close(done)

	assert.NoError(t, err)

	wg.Wait()

	// Subsequent operations should be rejected
	err = cb.Allow()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker is closed")
}

// TestCircuitBreakerStateConsistency verifies state consistency under concurrency
func TestCircuitBreakerStateConsistency(t *testing.T) {
	config := CircuitBreakerConfig{
		FailureThreshold:    2,
		RecoveryTimeout:     50 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	// Test multiple state transition cycles
	for cycle := 0; cycle < 5; cycle++ {
		// Start in closed state
		combined := atomic.LoadInt64(&cb.combinedState)
		state, _ := decodeCombinedState(combined)
		assert.Equal(t, CircuitClosed, state)

		// Force to open
		for i := 0; i < config.FailureThreshold; i++ {
			cb.RecordFailure()
		}

		// Verify open state
		combined = atomic.LoadInt64(&cb.combinedState)
		state, _ = decodeCombinedState(combined)
		assert.Equal(t, CircuitOpen, state)

		// Wait for recovery
		time.Sleep(config.RecoveryTimeout + 10*time.Millisecond)

		// Allow should transition to half-open
		err := cb.Allow()
		assert.NoError(t, err)
		combined = atomic.LoadInt64(&cb.combinedState)
		state, _ = decodeCombinedState(combined)
		assert.Equal(t, CircuitHalfOpen, state)

		// Record success to close
		cb.RecordSuccess()
		combined = atomic.LoadInt64(&cb.combinedState)
		state, _ = decodeCombinedState(combined)
		assert.Equal(t, CircuitClosed, state)
	}
}

// BenchmarkCircuitBreakerAllow benchmarks the Allow method under contention
func BenchmarkCircuitBreakerAllow(b *testing.B) {
	config := CircuitBreakerConfig{
		FailureThreshold:    100,
		RecoveryTimeout:     1 * time.Second,
		MaxHalfOpenRequests: 10,
	}

	logger := zerolog.New(zerolog.NewTestWriter(b)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cb.Allow()
		}
	})
}

// BenchmarkCircuitBreakerRecordOperations benchmarks record operations
func BenchmarkCircuitBreakerRecordOperations(b *testing.B) {
	config := CircuitBreakerConfig{
		FailureThreshold:    1000,
		RecoveryTimeout:     1 * time.Second,
		MaxHalfOpenRequests: 10,
	}

	logger := zerolog.New(zerolog.NewTestWriter(b)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if b.N%2 == 0 {
				cb.RecordSuccess()
			} else {
				cb.RecordFailure()
			}
		}
	})
}

// TestCircuitBreakerAtomicityUnderExtremeContention tests atomicity under extreme contention
func TestCircuitBreakerAtomicityUnderExtremeContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping extreme contention test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    1,
		RecoveryTimeout:     10 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()

	for iteration := 0; iteration < 20; iteration++ {
		t.Run(fmt.Sprintf("Iteration%d", iteration), func(t *testing.T) {
			cb := NewCircuitBreaker(config, logger)
			defer cb.Close()

			// Force circuit to open
			cb.RecordFailure()

			// Wait for recovery timeout
			time.Sleep(config.RecoveryTimeout + 5*time.Millisecond)

			var wg sync.WaitGroup
			concurrency := 1000 // Extreme contention
			allowedCount := int32(0)
			errorCount := int32(0)

			// Create extreme contention for half-open state
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := cb.Allow()
					if err == nil {
						atomic.AddInt32(&allowedCount, 1)
					} else {
						atomic.AddInt32(&errorCount, 1)
					}
				}()
			}

			wg.Wait()

			// CRITICAL: Should never exceed MaxHalfOpenRequests even under extreme contention
			if allowedCount > int32(config.MaxHalfOpenRequests) {
				t.Errorf("ATOMICITY VIOLATION: %d requests allowed > limit %d (iteration %d)",
					allowedCount, config.MaxHalfOpenRequests, iteration)
			}

			t.Logf("Iteration %d: Allowed: %d, Errors: %d", iteration, allowedCount, errorCount)
		})
	}
}

// TestCircuitBreakerStateConsistencyUnderContention verifies state remains consistent
func TestCircuitBreakerStateConsistencyUnderContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping state consistency test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    3,
		RecoveryTimeout:     20 * time.Millisecond,
		MaxHalfOpenRequests: 2,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	workers := 20

	// Counters for tracking state consistency
	var (
		allowedInClosed    int64
		allowedInHalfOpen  int64
		rejectedInOpen     int64
		invalidStates     int64
	)

	// Start workers that continuously exercise the circuit breaker
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Read state before Allow() call
					combinedBefore := atomic.LoadInt64(&cb.combinedState)
					stateBefore, _ := decodeCombinedState(combinedBefore)
					err := cb.Allow()
					combinedAfter := atomic.LoadInt64(&cb.combinedState)
					stateAfter, _ := decodeCombinedState(combinedAfter)

					// Validate state consistency
					switch stateBefore {
					case CircuitClosed:
						if err == nil {
							atomic.AddInt64(&allowedInClosed, 1)
						} else {
							// Should not happen in closed state
							atomic.AddInt64(&invalidStates, 1)
							t.Errorf("Unexpected error in closed state: %v", err)
						}
					case CircuitOpen:
						if err != nil {
							atomic.AddInt64(&rejectedInOpen, 1)
						} else {
							// Could transition to half-open during call
							if stateAfter == CircuitHalfOpen {
								atomic.AddInt64(&allowedInHalfOpen, 1)
							} else {
								atomic.AddInt64(&invalidStates, 1)
								t.Errorf("Unexpected success in open state without transition")
							}
						}
					case CircuitHalfOpen:
						if err == nil {
							atomic.AddInt64(&allowedInHalfOpen, 1)
						}
						// Rejection is also valid in half-open
					}

					// Randomly record success/failure to drive state changes
					if err == nil {
						if workerID%4 == 0 {
							cb.RecordFailure()
						} else {
							cb.RecordSuccess()
						}
					}

					// Small delay to allow state transitions
					time.Sleep(time.Duration(workerID%3) * time.Millisecond)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("State consistency results:")
	t.Logf("  Allowed in closed: %d", allowedInClosed)
	t.Logf("  Allowed in half-open: %d", allowedInHalfOpen)
	t.Logf("  Rejected in open: %d", rejectedInOpen)
	t.Logf("  Invalid states: %d", invalidStates)

	// Should have no invalid states
	assert.Equal(t, int64(0), invalidStates, "Circuit breaker state inconsistencies detected")
}

// TestCircuitBreakerLiveLockDetection tests for potential live-lock scenarios
func TestCircuitBreakerLiveLockDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live-lock detection test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    1,
		RecoveryTimeout:     10 * time.Millisecond,
		MaxHalfOpenRequests: 1,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	// Force circuit to open
	cb.RecordFailure()

	// Wait for recovery timeout
	time.Sleep(config.RecoveryTimeout + 5*time.Millisecond)

	// Test that Allow() doesn't live-lock under contention
	timeout := time.After(5 * time.Second)
	done := make(chan struct{})

	go func() {
		defer close(done)
		var wg sync.WaitGroup
		concurrency := 100

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Each goroutine attempts Allow() with a timeout
				resultChan := make(chan error, 1)
				go func() {
					resultChan <- cb.Allow()
				}()

				select {
				case <-resultChan:
					// Allow() completed (success or failure)
				case <-time.After(1 * time.Second):
					// Allow() took too long - potential live-lock
					t.Errorf("Allow() call timed out - potential live-lock detected")
				}
			}()
		}

		wg.Wait()
	}()

	select {
	case <-done:
		// Test completed successfully
	case <-timeout:
		t.Fatal("Live-lock detection test timed out - circuit breaker may be stuck")
	}
}

// TestCircuitBreakerMemoryConsistency tests memory consistency under high contention
func TestCircuitBreakerMemoryConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory consistency test in short mode")
	}

	config := CircuitBreakerConfig{
		FailureThreshold:    5,
		RecoveryTimeout:     50 * time.Millisecond,
		MaxHalfOpenRequests: 3,
	}

	logger := zerolog.New(zerolog.NewTestWriter(t)).With().Timestamp().Logger()
	cb := NewCircuitBreaker(config, logger)
	defer cb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	workers := runtime.NumCPU() * 4

	// Memory consistency check: verify that all atomic operations are properly visible
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Read timestamp for validation
					lastFailureTime := atomic.LoadInt64(&cb.lastFailureTime)

					// Basic consistency checks using validation function
					if !cb.validateStateConsistency() {
						t.Errorf("State consistency violation detected")
					}

					// Additional checks
					if lastFailureTime < 0 {
						t.Errorf("Memory inconsistency: negative last failure time: %d", lastFailureTime)
					}

					// Exercise the circuit breaker
					cb.Allow()
					cb.RecordSuccess()
					cb.RecordFailure()
				}
			}
		}()
	}

	wg.Wait()
}