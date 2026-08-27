package validation

import "testing"

func TestParseHTTPSLOSamples(t *testing.T) {
	tests := []struct {
		name        string
		stdout      string
		wantTotal   int
		wantSuccess int
	}{
		{"all success", "200\n200\n200\n", 3, 3},
		{"all failure", "500\n500\n", 2, 0},
		{"connection failures report 000", "000\n000\n200\n", 3, 1},
		{"redirects count as success", "301\n302\n200\n", 3, 3},
		{"client errors do not count as success", "404\n403\n200\n", 3, 1},
		{"blank lines ignored", "200\n\n200\n\n", 2, 2},
		{"no trailing newline", "200\n404", 2, 1},
		{"empty output", "", 0, 0},
		{"unparseable line still counts toward total, not success", "200\nnot-a-code\n", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, success := parseHTTPSLOSamples(tt.stdout)
			if total != tt.wantTotal || success != tt.wantSuccess {
				t.Errorf("parseHTTPSLOSamples(%q) = (%d, %d), want (%d, %d)", tt.stdout, total, success, tt.wantTotal, tt.wantSuccess)
			}
		})
	}
}

func TestClampHTTPSLOWindow(t *testing.T) {
	tests := []struct {
		name             string
		requestedSeconds int
		ctxRemaining     int
		hasDeadline      bool
		want             int
	}{
		{"under cap, no deadline info, passes through unchanged", 20, 0, false, 20},
		{"doc's own window_s:180 example gets capped", 180, 0, false, httpSLOMaxWindowSeconds},
		{"exactly at the cap stays unchanged", httpSLOMaxWindowSeconds, 0, false, httpSLOMaxWindowSeconds},
		{"shrunk to fit a tight real deadline", 60, 10, true, 7}, // 10 - 3s teardown headroom
		{"deadline larger than request does not expand the window", 20, 100, true, 20},
		{"never goes below one sample interval even with almost no time left", 60, 1, true, httpSLOSampleIntervalSeconds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampHTTPSLOWindow(tt.requestedSeconds, tt.ctxRemaining, tt.hasDeadline)
			if got != tt.want {
				t.Errorf("clampHTTPSLOWindow(%d, %d, %v) = %d, want %d", tt.requestedSeconds, tt.ctxRemaining, tt.hasDeadline, got, tt.want)
			}
		})
	}
}
