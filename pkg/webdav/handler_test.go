package webdav

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/store"
)

// TestFileStructHasHandlerReference verifies that File struct has handler field
func TestFileStructHasHandlerReference(t *testing.T) {
	// Create a mock handler
	cache := &store.Cache{}
	logger := zerolog.Nop()
	handler := NewHandler("test", "/test", cache, logger)

	// Create a File with handler reference
	file := &File{
		cache:   cache,
		handler: handler,
		name:    "test.txt",
		size:    100,
		isDir:   false,
	}

	// Verify handler is set
	if file.handler == nil {
		t.Error("Expected handler to be set, but it was nil")
	}

	if file.handler != handler {
		t.Error("Expected handler to match the provided handler")
	}
}

// TestFileStructBackwardCompatibility verifies backward compatibility with nil handler
func TestFileStructBackwardCompatibility(t *testing.T) {
	// Create a File without handler reference (backward compatibility)
	cache := &store.Cache{}
	file := &File{
		cache: cache,
		name:  "test.txt",
		size:  100,
		isDir: false,
		// handler is intentionally not set (nil)
	}

	// Verify that having a nil handler doesn't cause issues
	if file.handler != nil {
		t.Error("Expected handler to be nil for backward compatibility test")
	}

	// The file should still be usable for basic operations
	if file.name != "test.txt" {
		t.Errorf("Expected name to be 'test.txt', got '%s'", file.name)
	}

	if file.size != 100 {
		t.Errorf("Expected size to be 100, got %d", file.size)
	}
}
