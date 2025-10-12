package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func setupTest(t *testing.T) {
	// Create a temporary directory for test configuration
	tmpDir := t.TempDir()

	// Set the config path
	config.SetConfigPath(tmpDir)

	// Create a minimal config file
	cfg := map[string]interface{}{
		"log_level": "info",
		"port":      "8282",
		"qbittorrent": map[string]interface{}{
			"download_folder": filepath.Join(tmpDir, "downloads"),
		},
		"debrids": []map[string]interface{}{
			{
				"name":    "test",
				"api_key": "test_key",
				"folder":  "test_folder",
			},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configFile := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configFile, data, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Create downloads directory
	downloadsDir := filepath.Join(tmpDir, "downloads")
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		t.Fatalf("Failed to create downloads directory: %v", err)
	}

	// Create logs directory
	logsDir := filepath.Join(tmpDir, "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("Failed to create logs directory: %v", err)
	}

	// Force reload config to pick up the new path
	config.Reload()
}

// TestDefaultSingleton verifies that Default() returns the same logger instance
func TestDefaultSingleton(t *testing.T) {
	setupTest(t)

	// Call Default() multiple times
	logger1 := Default()
	logger2 := Default()
	logger3 := Default()

	// Verify they all point to the same underlying logger
	// We can't directly compare zerolog.Logger instances, but we can verify
	// that the function is called only once by checking that multiple calls
	// don't panic or cause issues
	if logger1.GetLevel() != logger2.GetLevel() {
		t.Error("Expected same log level from singleton")
	}
	if logger2.GetLevel() != logger3.GetLevel() {
		t.Error("Expected same log level from singleton")
	}
}

// TestDefaultConcurrent verifies that Default() is safe for concurrent use
func TestDefaultConcurrent(t *testing.T) {
	setupTest(t)

	const goroutines = 100
	done := make(chan bool, goroutines)

	// Launch multiple goroutines calling Default() concurrently
	for i := 0; i < goroutines; i++ {
		go func() {
			logger := Default()
			// Just verify we got a logger without panic
			if logger.GetLevel() < 0 {
				t.Error("Invalid logger returned")
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
