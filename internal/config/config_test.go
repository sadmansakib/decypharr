package config

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		name        string
		rateLimit   string
		expected    float64
		expectError bool
	}{
		{
			name:      "empty string",
			rateLimit: "",
			expected:  0,
		},
		{
			name:      "5 per second",
			rateLimit: "5/second",
			expected:  5.0,
		},
		{
			name:      "10 per minute",
			rateLimit: "10/minute",
			expected:  10.0 / 60.0,
		},
		{
			name:      "60 per hour",
			rateLimit: "60/hour",
			expected:  60.0 / 3600.0,
		},
		{
			name:      "case insensitive",
			rateLimit: "5/SECOND",
			expected:  5.0,
		},
		{
			name:      "with spaces",
			rateLimit: " 5 / second ",
			expected:  5.0,
		},
		{
			name:        "invalid format",
			rateLimit:   "invalid",
			expectError: true,
		},
		{
			name:        "invalid number",
			rateLimit:   "abc/second",
			expectError: true,
		},
		{
			name:        "invalid unit",
			rateLimit:   "5/invalid",
			expectError: true,
		},
		{
			name:      "decimal number",
			rateLimit: "2.5/second",
			expected:  2.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseRateLimit(tt.rateLimit)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != tt.expected {
				t.Errorf("expected %.6f, got %.6f", tt.expected, result)
			}
		})
	}
}

func TestValidateTorboxRateLimits(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	tests := []struct {
		name             string
		debrid           Debrid
		expectedDebrid   Debrid
		expectLogs       []string
		expectCorrection bool
	}{
		{
			name: "valid rates within limits",
			debrid: Debrid{
				Name:              "torbox",
				RateLimit:         "3/second",
				RepairRateLimit:   "2/second",
				DownloadRateLimit: "1/second",
			},
			expectedDebrid: Debrid{
				Name:              "torbox",
				RateLimit:         "3/second",
				RepairRateLimit:   "2/second",
				DownloadRateLimit: "1/second",
			},
			expectLogs:       []string{},
			expectCorrection: false,
		},
		{
			name: "rate limit exceeds maximum - corrected",
			debrid: Debrid{
				Name:      "torbox",
				RateLimit: "10/second",
			},
			expectedDebrid: Debrid{
				Name:      "torbox",
				RateLimit: "4/second", // Corrected to safe default
			},
			expectLogs:       []string{"Automatically corrected to '4/second'"},
			expectCorrection: true,
		},
		{
			name: "repair rate limit at limit - no correction",
			debrid: Debrid{
				Name:            "torbox",
				RepairRateLimit: "300/minute", // 5/second exactly at limit, 300/minute = 5/sec
			},
			expectedDebrid: Debrid{
				Name:            "torbox",
				RepairRateLimit: "300/minute", // Should remain unchanged
			},
			expectLogs:       []string{},
			expectCorrection: false,
		},
		{
			name: "download rate limit exceeds maximum - corrected",
			debrid: Debrid{
				Name:              "torbox",
				DownloadRateLimit: "400/minute", // > 5/second
			},
			expectedDebrid: Debrid{
				Name:              "torbox",
				DownloadRateLimit: "4/second", // Corrected to safe default
			},
			expectLogs:       []string{"Automatically corrected to '4/second'"},
			expectCorrection: true,
		},
		{
			name: "invalid rate format - corrected",
			debrid: Debrid{
				Name:      "torbox",
				RateLimit: "invalid-format",
			},
			expectedDebrid: Debrid{
				Name:      "torbox",
				RateLimit: "4/second", // Corrected to safe default
			},
			expectLogs:       []string{"Using safe default '4/second'"},
			expectCorrection: true,
		},
		{
			name: "multiple violations - all corrected",
			debrid: Debrid{
				Name:              "torbox",
				RateLimit:         "10/second",
				RepairRateLimit:   "8/second",
				DownloadRateLimit: "12/second",
			},
			expectedDebrid: Debrid{
				Name:              "torbox",
				RateLimit:         "4/second", // All corrected to safe defaults
				RepairRateLimit:   "4/second",
				DownloadRateLimit: "4/second",
			},
			expectLogs: []string{
				"Automatically corrected to '4/second'",
				"Automatically corrected to '4/second'",
				"Automatically corrected to '4/second'",
			},
			expectCorrection: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()

			result := validateTorboxConfiguration(tt.debrid)

			output := buf.String()

			// Check that the returned debrid configuration matches expected
			if result.Name != tt.expectedDebrid.Name {
				t.Errorf("expected Name '%s', got '%s'", tt.expectedDebrid.Name, result.Name)
			}
			if result.RateLimit != tt.expectedDebrid.RateLimit {
				t.Errorf("expected RateLimit '%s', got '%s'", tt.expectedDebrid.RateLimit, result.RateLimit)
			}
			if result.RepairRateLimit != tt.expectedDebrid.RepairRateLimit {
				t.Errorf("expected RepairRateLimit '%s', got '%s'", tt.expectedDebrid.RepairRateLimit, result.RepairRateLimit)
			}
			if result.DownloadRateLimit != tt.expectedDebrid.DownloadRateLimit {
				t.Errorf("expected DownloadRateLimit '%s', got '%s'", tt.expectedDebrid.DownloadRateLimit, result.DownloadRateLimit)
			}

			// Check that expected log messages are present
			for _, logMsg := range tt.expectLogs {
				if !strings.Contains(output, logMsg) {
					t.Errorf("expected log containing '%s' but not found in output: %s", logMsg, output)
				}
			}

			// If no logs expected and no correction expected, check that there are no correction messages
			if !tt.expectCorrection && (strings.Contains(output, "Automatically corrected") || strings.Contains(output, "Using safe default")) {
				t.Errorf("unexpected correction message in output: %s", output)
			}
		})
	}
}

func TestValidateConfigTorboxIntegration(t *testing.T) {
	// Capture log output
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	config := &Config{
		Debrids: []Debrid{
			{
				Name:      "torbox",
				APIKey:    "test-key",
				Folder:    "/downloads",
				RateLimit: "10/second", // Should be corrected
			},
			{
				Name:      "realdebrid",
				APIKey:    "test-key",
				Folder:    "/downloads",
				RateLimit: "10/second", // Should NOT be corrected (not Torbox)
			},
			{
				Name:              "TORBOX", // Case insensitive test
				APIKey:            "test-key2",
				Folder:            "/downloads2",
				DownloadRateLimit: "8/second", // Should be corrected
			},
		},
		QBitTorrent: QBitTorrent{
			DownloadFolder: "/tmp", // Use /tmp which should exist
		},
	}

	buf.Reset()
	err := ValidateConfig(config)

	if err != nil {
		t.Errorf("ValidateConfig returned error: %v", err)
	}

	output := buf.String()

	// Check that Torbox rate limits were corrected
	if config.Debrids[0].RateLimit != "4/second" {
		t.Errorf("expected first torbox RateLimit to be corrected to '4/second', got '%s'", config.Debrids[0].RateLimit)
	}
	if config.Debrids[2].DownloadRateLimit != "4/second" {
		t.Errorf("expected second torbox DownloadRateLimit to be corrected to '4/second', got '%s'", config.Debrids[2].DownloadRateLimit)
	}

	// Real-Debrid should remain unchanged
	if config.Debrids[1].RateLimit != "10/second" {
		t.Errorf("expected realdebrid RateLimit to remain unchanged at '10/second', got '%s'", config.Debrids[1].RateLimit)
	}

	// Should have correction messages for both Torbox instances
	correctionCount := strings.Count(output, "Automatically corrected")
	if correctionCount < 2 {
		t.Errorf("expected at least 2 correction messages but got %d in output: %s", correctionCount, output)
	}

	// Should contain references to both torbox instances
	if !strings.Contains(output, "[Torbox:torbox]") {
		t.Errorf("expected log for 'torbox' instance but not found in output: %s", output)
	}
	if !strings.Contains(output, "[Torbox:TORBOX]") {
		t.Errorf("expected log for 'TORBOX' instance but not found in output: %s", output)
	}

	// Should NOT contain logs for realdebrid
	if strings.Contains(output, "realdebrid") {
		t.Errorf("unexpected log for realdebrid in output: %s", output)
	}
}
