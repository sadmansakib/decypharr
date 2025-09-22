package store

import (
	"context"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/logger"
)

func TestNewStore(t *testing.T) {
	config := StoreConfig{
		TorrentsFile:       ":memory:",
		Logger:             logger.New("test"),
		RefreshInterval:    30 * time.Second,
		SkipPreCache:       true,
		MaxDownloads:       5,
		RemoveStalledAfter: 0,
	}

	store, err := NewStore(config)
	if err != nil {
		t.Fatalf("NewStore() failed: %v", err)
	}

	if store == nil {
		t.Fatal("NewStore() returned nil")
	}

	// Test that basic functionality is available
	if store.Torrents() == nil {
		t.Error("Torrents() returned nil")
	}

	if store.Scheduler() == nil {
		t.Error("Scheduler() returned nil")
	}

	// Test configuration getters
	if store.GetRefreshInterval() != config.RefreshInterval {
		t.Errorf("GetRefreshInterval() = %v, want %v", store.GetRefreshInterval(), config.RefreshInterval)
	}

	if store.GetSkipPreCache() != config.SkipPreCache {
		t.Errorf("GetSkipPreCache() = %v, want %v", store.GetSkipPreCache(), config.SkipPreCache)
	}

	if store.GetMaxDownloads() != config.MaxDownloads {
		t.Errorf("GetMaxDownloads() = %v, want %v", store.GetMaxDownloads(), config.MaxDownloads)
	}

	// Cleanup
	store.Reset()
}

func TestCreateStoreForTesting(t *testing.T) {
	store, err := CreateStoreForTesting()
	if err != nil {
		t.Fatalf("CreateStoreForTesting() failed: %v", err)
	}

	if store == nil {
		t.Fatal("CreateStoreForTesting() returned nil")
	}

	// Test that all services are available
	if store.Arr() == nil {
		t.Error("Arr() returned nil")
	}

	if store.Debrid() == nil {
		t.Error("Debrid() returned nil")
	}

	if store.Repair() == nil {
		t.Error("Repair() returned nil")
	}

	if store.Torrents() == nil {
		t.Error("Torrents() returned nil")
	}

	// Test worker startup (should not panic)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// This should not block or panic
	store.StartWorkers(ctx)

	// Cleanup
	store.Reset()
}

func TestStoreReset(t *testing.T) {
	store, err := CreateStoreForTesting()
	if err != nil {
		t.Fatalf("CreateStoreForTesting() failed: %v", err)
	}

	// Reset should not panic
	store.Reset()

	// After reset, services should still be accessible but may be in reset state
	if store.Torrents() == nil {
		t.Error("Torrents() returned nil after reset")
	}
}

func TestDependencyInjection(t *testing.T) {
	// Test that we can create a store and inject dependencies
	config := StoreConfig{
		TorrentsFile:       ":memory:",
		Logger:             logger.New("test"),
		RefreshInterval:    30 * time.Second,
		SkipPreCache:       true,
		MaxDownloads:       5,
		RemoveStalledAfter: 0,
	}

	store, err := NewStore(config)
	if err != nil {
		t.Fatalf("NewStore() failed: %v", err)
	}

	// Before injection, these should be nil
	if store.Arr() != nil {
		t.Error("Arr() should be nil before injection")
	}

	if store.Debrid() != nil {
		t.Error("Debrid() should be nil before injection")
	}

	if store.Repair() != nil {
		t.Error("Repair() should be nil before injection")
	}

	if store.RcloneManager() != nil {
		t.Error("RcloneManager() should be nil before injection")
	}

	// Test injection (using nil values is OK for this test)
	store.InjectServices(nil, nil, nil, nil)

	// After injection with nil, they should still be nil
	if store.Arr() != nil {
		t.Error("Arr() should still be nil after injecting nil")
	}

	// Cleanup
	store.Reset()
}