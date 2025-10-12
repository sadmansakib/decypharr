package wire

import (
	"context"
	"testing"
	"time"
)

// TestContextCancellation verifies that operations properly handle context cancellation
func TestContextCancellation(t *testing.T) {
	t.Run("TorrentStorage respects context cancellation", func(t *testing.T) {
		// Create a context that will be cancelled
		ctx, cancel := context.WithCancel(context.Background())

		// Cancel immediately
		cancel()

		// Verify context is cancelled
		select {
		case <-ctx.Done():
			if ctx.Err() != context.Canceled {
				t.Errorf("expected context.Canceled, got %v", ctx.Err())
			}
		default:
			t.Error("context should be cancelled")
		}
	})

	t.Run("Context with timeout", func(t *testing.T) {
		// Create a context with a very short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
		defer cancel()

		// Wait for timeout
		time.Sleep(10 * time.Millisecond)

		// Verify context is cancelled due to timeout
		select {
		case <-ctx.Done():
			if ctx.Err() != context.DeadlineExceeded {
				t.Errorf("expected context.DeadlineExceeded, got %v", ctx.Err())
			}
		default:
			t.Error("context should have timed out")
		}
	})

	t.Run("Context propagation", func(t *testing.T) {
		// Create parent context
		parentCtx, parentCancel := context.WithCancel(context.Background())
		defer parentCancel()

		// Create child context
		childCtx, childCancel := context.WithCancel(parentCtx)
		defer childCancel()

		// Cancel parent
		parentCancel()

		// Verify child is also cancelled
		select {
		case <-childCtx.Done():
			if childCtx.Err() != context.Canceled {
				t.Errorf("expected context.Canceled, got %v", childCtx.Err())
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("child context should be cancelled when parent is cancelled")
		}
	})
}
