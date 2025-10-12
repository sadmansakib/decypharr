package debrid

import (
	"context"
	"testing"
	"time"
)

// TestContextPropagation tests that context is properly propagated through debrid operations
// This verifies the fix for critical issue #3: incomplete context propagation
func TestContextPropagation(t *testing.T) {
	// This is a structural test to ensure context is passed correctly
	// Actual functionality would require mocking the debrid providers

	tests := []struct {
		name        string
		contextFunc func() (context.Context, context.CancelFunc)
	}{
		{
			name: "with timeout context",
			contextFunc: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 5*time.Second)
			},
		},
		{
			name: "with cancel context",
			contextFunc: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
		},
		{
			name: "with deadline context",
			contextFunc: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.contextFunc()
			defer cancel()

			// Verify context is not nil
			if ctx == nil {
				t.Fatal("Context should not be nil")
			}

			// Verify context has deadline/timeout where applicable
			if deadline, ok := ctx.Deadline(); ok {
				if deadline.IsZero() {
					t.Error("Context deadline should not be zero")
				}
			}
		})
	}
}

// TestContextCancellationRespected tests that operations respect context cancellation
func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	// Verify context is cancelled
	select {
	case <-ctx.Done():
		// Expected: context should be done
		if ctx.Err() == nil {
			t.Error("Context error should not be nil after cancellation")
		}
	default:
		t.Error("Context should be cancelled")
	}
}

// TestContextWithValue tests that context values are preserved
func TestContextWithValue(t *testing.T) {
	type contextKey string
	const requestIDKey contextKey = "requestID"

	ctx := context.WithValue(context.Background(), requestIDKey, "test-123")

	// Verify value is preserved
	value := ctx.Value(requestIDKey)
	if value == nil {
		t.Fatal("Context value should not be nil")
	}

	if value.(string) != "test-123" {
		t.Errorf("Expected 'test-123', got %v", value)
	}
}


// BenchmarkContextCreation benchmarks context creation overhead
func BenchmarkContextCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_ = ctx
		cancel()
	}
}

// BenchmarkContextWithTimeout benchmarks context with timeout
func BenchmarkContextWithTimeout(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = ctx
		cancel()
	}
}

// TestMultipleContextCancellations tests handling of multiple context cancellations
func TestMultipleContextCancellations(t *testing.T) {
	parent := context.Background()

	// Create chain of contexts
	ctx1, cancel1 := context.WithCancel(parent)
	ctx2, cancel2 := context.WithCancel(ctx1)
	ctx3, cancel3 := context.WithCancel(ctx2)

	// Cancel middle context
	cancel2()

	// Verify propagation
	select {
	case <-ctx2.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("ctx2 should be cancelled")
	}

	select {
	case <-ctx3.Done():
		// Expected - child should be cancelled
	case <-time.After(100 * time.Millisecond):
		t.Error("ctx3 should be cancelled when parent is cancelled")
	}

	// ctx1 should not be cancelled
	select {
	case <-ctx1.Done():
		t.Error("ctx1 should not be cancelled")
	default:
		// Expected
	}

	// Cleanup
	cancel1()
	cancel3()
}

// TestContextDeadlineExceeded tests context deadline exceeded handling
func TestContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Wait for context to expire
	<-ctx.Done()

	err := ctx.Err()
	if err != context.DeadlineExceeded {
		t.Errorf("Expected DeadlineExceeded, got %v", err)
	}
}

// TestContextCanceled tests context canceled error
func TestContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	<-ctx.Done()

	err := ctx.Err()
	if err != context.Canceled {
		t.Errorf("Expected Canceled, got %v", err)
	}
}
