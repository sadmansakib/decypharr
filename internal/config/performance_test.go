package config

import (
	"fmt"
	"os"
	"testing"
)

func BenchmarkIsAllowedFile_Original(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:       tempDir,
		AllowedExt: []string{"mp4", "avi", "mkv", "mov", "wmv", "flv", "webm", "m4v"},
	}

	// Disable caching to test original implementation
	cfg.cacheValid = false

	testFiles := []string{
		"movie.mp4",
		"video.avi",
		"show.mkv",
		"clip.mov",
		"doc.txt",    // Not allowed
		"image.jpg",  // Not allowed
		"music.mp3",  // Not allowed
		"archive.zip", // Not allowed
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			filename := testFiles[i%len(testFiles)]
			cfg.IsAllowedFile(filename)
			i++
		}
	})
}

func BenchmarkIsAllowedFile_Optimized(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:       tempDir,
		AllowedExt: []string{"mp4", "avi", "mkv", "mov", "wmv", "flv", "webm", "m4v"},
	}

	// Enable caching to test optimized implementation
	cfg.precomputeValues()

	testFiles := []string{
		"movie.mp4",
		"video.avi",
		"show.mkv",
		"clip.mov",
		"doc.txt",    // Not allowed
		"image.jpg",  // Not allowed
		"music.mp3",  // Not allowed
		"archive.zip", // Not allowed
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			filename := testFiles[i%len(testFiles)]
			cfg.IsAllowedFile(filename)
			i++
		}
	})
}

func BenchmarkGetMinFileSize_Original(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:        tempDir,
		MinFileSize: "100MB",
	}

	// Disable caching to test original behavior
	cfg.cacheValid = false

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cfg.GetMinFileSize()
		}
	})
}

func BenchmarkGetMinFileSize_Optimized(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:        tempDir,
		MinFileSize: "100MB",
	}

	// Enable caching to test optimized behavior
	cfg.precomputeValues()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cfg.GetMinFileSize()
		}
	})
}

func BenchmarkConfigAccess_Individual(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:        tempDir,
		BindAddress: "0.0.0.0",
		Port:        "8282",
		URLBase:     "/",
		LogLevel:    "info",
		MinFileSize: "100MB",
		MaxFileSize: "10GB",
		AllowedExt:  []string{"mp4", "avi", "mkv"},
	}
	cfg.precomputeValues()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Simulate accessing multiple config values individually
			_ = cfg.GetServerConfig()
			_ = cfg.GetMinFileSize()
			_ = cfg.GetMaxFileSize()
			_ = cfg.GetQBitTorrentConfig()
			_ = cfg.GetRepairConfig()
		}
	})
}

func BenchmarkConfigAccess_Batch(b *testing.B) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		b.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:        tempDir,
		BindAddress: "0.0.0.0",
		Port:        "8282",
		URLBase:     "/",
		LogLevel:    "info",
		MinFileSize: "100MB",
		MaxFileSize: "10GB",
		AllowedExt:  []string{"mp4", "avi", "mkv"},
	}
	cfg.precomputeValues()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Access all values in a single call
			_ = cfg.GetBatchConfig()
		}
	})
}

// TestPerformanceImprovements demonstrates the performance gains
func TestPerformanceImprovements(t *testing.T) {
	// Create a temporary config directory
	tempDir, err := os.MkdirTemp("", "config_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	SetConfigPath(tempDir)
	cfg := &Config{
		Path:       tempDir,
		AllowedExt: getDefaultExtensions(), // Use realistic extension list
	}

	// Test file extension checking improvement
	t.Run("FileExtensionCheck", func(t *testing.T) {
		// Test without cache (original behavior)
		cfg.cacheValid = false
		start := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				cfg.IsAllowedFile("test.mp4")
			}
		})

		// Test with cache (optimized behavior)
		cfg.precomputeValues()
		optimized := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				cfg.IsAllowedFile("test.mp4")
			}
		})

		speedup := float64(start.NsPerOp()) / float64(optimized.NsPerOp())
		t.Logf("File extension check speedup: %.2fx", speedup)

		if speedup < 1.5 {
			t.Logf("WARNING: Expected at least 1.5x speedup, got %.2fx", speedup)
		}
	})

	// Test size calculation caching
	t.Run("SizeCalculation", func(t *testing.T) {
		cfg.MinFileSize = "100MB"
		cfg.MaxFileSize = "10GB"

		// Test without cache
		cfg.cacheValid = false
		start := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				cfg.GetMinFileSize()
				cfg.GetMaxFileSize()
			}
		})

		// Test with cache
		cfg.precomputeValues()
		optimized := testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				cfg.GetMinFileSize()
				cfg.GetMaxFileSize()
			}
		})

		speedup := float64(start.NsPerOp()) / float64(optimized.NsPerOp())
		t.Logf("Size calculation speedup: %.2fx", speedup)

		if speedup < 2.0 {
			t.Logf("WARNING: Expected at least 2x speedup, got %.2fx", speedup)
		}
	})
}

// ExampleConfig shows how to use the optimized config patterns
func ExampleConfig() {
	// Set up config path
	SetConfigPath("/tmp/test_config")
	defer os.RemoveAll("/tmp/test_config")

	// Get config provider (interface-based access)
	provider := GetProvider()

	// Use batch access for multiple values
	batch := provider.(*Config).GetBatchConfig()
	fmt.Printf("Server running on %s:%s\n", batch.ServerConfig.BindAddress, batch.ServerConfig.Port)
	fmt.Printf("Min file size: %d bytes\n", batch.MinFileSize)
	fmt.Printf("Allowed extensions: %d types\n", len(batch.Extensions))

	// Use optimized file checking
	if provider.IsAllowedFile("movie.mp4") {
		fmt.Println("MP4 files are allowed")
	}

	// Use optimized size checking
	if provider.IsSizeAllowed(1024 * 1024 * 50) { // 50MB
		fmt.Println("50MB file is within limits")
	}

	// Output:
	// Config file not found, creating a new one at /tmp/test_config/config.json
	// Server running on :8282
	// Min file size: 0 bytes
	// Allowed extensions: 66 types
	// MP4 files are allowed
	// 50MB file is within limits
}