package consoleshared

import (
	"testing"
	"time"
)

func TestProgressTimingUsesCompletedThisRun(t *testing.T) {
	// 2 of the 10 completed jobs belong to this run; the other 8 were already
	// done when the phase started. The average must be 5s, not 1s or 10s.
	eta, speed, avg := progressTiming(12, 20, 10, 10*time.Second)
	if avg != "5s" {
		t.Fatalf("avg = %q, want 5s", avg)
	}
	if speed != "12.0" {
		t.Fatalf("speed = %q, want 12.0", speed)
	}
	if eta != "40s" {
		t.Fatalf("eta = %q, want 40s", eta)
	}
}

func TestProgressTimingCountsSkippedJobs(t *testing.T) {
	// done-startDone includes skipped jobs; the denominator is not ran.
	if _, _, avg := progressTiming(4, 4, 2, 6*time.Second); avg != "3s" {
		t.Fatalf("avg = %q, want 3s", avg)
	}
}

func TestProgressTimingNoCompletedJobs(t *testing.T) {
	eta, speed, avg := progressTiming(10, 20, 10, 5*time.Second)
	if eta != "—" || speed != "—" || avg != "—" {
		t.Fatalf("got eta=%q speed=%q avg=%q, want dashes", eta, speed, avg)
	}
}

func TestProgressTimingFinished(t *testing.T) {
	eta, _, _ := progressTiming(5, 5, 0, 5*time.Second)
	if eta != "0s" {
		t.Fatalf("eta = %q, want 0s", eta)
	}
}
