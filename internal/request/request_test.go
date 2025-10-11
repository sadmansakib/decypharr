package request

import (
	"testing"

	"go.uber.org/ratelimit"
)

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantNil bool
		checkFn func(ratelimit.Limiter) bool
	}{
		{
			name:    "empty string",
			input:   "",
			wantNil: true,
		},
		{
			name:    "invalid format - no slash",
			input:   "100",
			wantNil: true,
		},
		{
			name:    "invalid format - non-numeric count",
			input:   "abc/sec",
			wantNil: true,
		},
		{
			name:    "invalid format - zero count",
			input:   "0/sec",
			wantNil: true,
		},
		{
			name:    "invalid format - negative count",
			input:   "-10/sec",
			wantNil: true,
		},
		{
			name:    "invalid unit",
			input:   "100/invalid",
			wantNil: true,
		},
		{
			name:    "valid - per second",
			input:   "100/sec",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per second (long form)",
			input:   "100/second",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per minute",
			input:   "60/min",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per minute (long form)",
			input:   "60/minute",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per hour",
			input:   "3600/hr",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per hour (long form)",
			input:   "3600/hour",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per day",
			input:   "86400/d",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - per day (long form)",
			input:   "86400/day",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - with whitespace",
			input:   "  100  /  sec  ",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
		{
			name:    "valid - plural units",
			input:   "100/seconds",
			wantNil: false,
			checkFn: func(l ratelimit.Limiter) bool { return l != nil },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRateLimit(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseRateLimit(%q) = %v, want nil", tt.input, got)
				}
			} else {
				if got == nil {
					t.Errorf("ParseRateLimit(%q) = nil, want non-nil", tt.input)
				} else if tt.checkFn != nil && !tt.checkFn(got) {
					t.Errorf("ParseRateLimit(%q) failed custom check", tt.input)
				}
			}
		})
	}
}

func TestParseRateLimitWithSlack_ZeroSlack(t *testing.T) {
	tests := []struct {
		name    string
		rateStr string
		slack   int
		wantNil bool
	}{
		{
			name:    "zero slack",
			rateStr: "120/hour",
			slack:   0,
			wantNil: false,
		},
		{
			name:    "default slack (-1)",
			rateStr: "120/hour",
			slack:   -1,
			wantNil: false,
		},
		{
			name:    "custom slack",
			rateStr: "120/hour",
			slack:   5,
			wantNil: false,
		},
		{
			name:    "P0 Fix: negative slack other than -1 should return nil",
			rateStr: "120/hour",
			slack:   -2,
			wantNil: true,
		},
		{
			name:    "P0 Fix: negative slack -10 should return nil",
			rateStr: "120/hour",
			slack:   -10,
			wantNil: true,
		},
		{
			name:    "empty rate string",
			rateStr: "",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "invalid rate string",
			rateStr: "invalid",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "zero count",
			rateStr: "0/sec",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "Edge case: very large count should return nil",
			rateStr: "99999999/sec",
			slack:   0,
			wantNil: true,
		},
		{
			name:    "Edge case: max safe count",
			rateStr: "10000000/sec",
			slack:   0,
			wantNil: false,
		},
		{
			name:    "Edge case: overflow attempt",
			rateStr: "999999999999/sec",
			slack:   0,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRateLimitWithSlack(tt.rateStr, tt.slack)
			if tt.wantNil {
				if got != nil {
					t.Errorf("ParseRateLimitWithSlack(%q, %d) = %v, want nil", tt.rateStr, tt.slack, got)
				}
			} else {
				if got == nil {
					t.Errorf("ParseRateLimitWithSlack(%q, %d) = nil, want non-nil", tt.rateStr, tt.slack)
				}
			}
		})
	}
}

func TestParseRateLimitWithSlack_AllUnits(t *testing.T) {
	units := []string{"sec", "second", "seconds", "min", "minute", "minutes", "hr", "hour", "hours", "d", "day", "days"}

	for _, unit := range units {
		t.Run("unit_"+unit, func(t *testing.T) {
			rateStr := "100/" + unit
			limiter := ParseRateLimitWithSlack(rateStr, 0)
			if limiter == nil {
				t.Errorf("ParseRateLimitWithSlack(%q, 0) = nil, want non-nil", rateStr)
			}
		})
	}
}

func BenchmarkParseRateLimit(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseRateLimit("100/sec")
	}
}

func BenchmarkParseRateLimitWithSlack(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ParseRateLimitWithSlack("100/sec", 0)
	}
}
