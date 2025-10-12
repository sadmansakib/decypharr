package decypharr

import (
	"context"
	"testing"
	"time"
)

// TestStartContextCancellation verifies that the main loop respects context cancellation
// and exits promptly without starting unnecessary work.
func TestStartContextCancellation(t *testing.T) {
	tests := []struct {
		name           string
		cancelDelay    time.Duration
		expectQuickExit bool
	}{
		{
			name:           "immediate cancellation",
			cancelDelay:    0,
			expectQuickExit: true,
		},
		{
			name:           "delayed cancellation",
			cancelDelay:    100 * time.Millisecond,
			expectQuickExit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())

			// Cancel after delay
			if tt.cancelDelay > 0 {
				time.AfterFunc(tt.cancelDelay, cancel)
			} else {
				cancel()
			}

			// Track start time
			start := time.Now()

			// Run Start in a goroutine since it blocks
			done := make(chan error, 1)
			go func() {
				done <- Start(ctx)
			}()

			// Wait for completion or timeout
			select {
			case err := <-done:
				elapsed := time.Since(start)

				if err != nil {
					t.Errorf("Start() returned unexpected error: %v", err)
				}

				// Should exit quickly (within 2 seconds) when context is cancelled
				if tt.expectQuickExit && elapsed > 2*time.Second {
					t.Errorf("Start() took too long to exit after context cancellation: %v", elapsed)
				}

				t.Logf("Start() exited in %v", elapsed)

			case <-time.After(5 * time.Second):
				t.Fatal("Start() did not exit within timeout after context cancellation")
			}
		})
	}
}

// TestStartLoopContextCheck verifies that the loop checks context at the start of each iteration
func TestStartLoopContextCheck(t *testing.T) {
	// This test verifies the fix for critical issue #1: context checking in loops
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- Start(ctx)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Start() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not respect context timeout")
	}
}

// TestStartMultipleCancellations tests that Start handles multiple rapid cancellations gracefully
func TestStartMultipleCancellations(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run("iteration", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // Cancel immediately

			err := Start(ctx)
			if err != nil {
				t.Errorf("Start() iteration %d returned error: %v", i, err)
			}
		})
	}
}

// BenchmarkStartContextCheck benchmarks the context check overhead in the main loop
func BenchmarkStartContextCheck(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_ = Start(ctx)
	}
}
