package exec

import (
	"testing"
	"time"
)

// The bounds and the timings below are written in docs/exec.md and in the doc comments of the constants. A change of one is a
// change of what every author and every host is told, so each is pinned here as a number and the change is made in all
// the places together.

func TestTheQueueBoundsAre256KiBAnd4096RunsAnd64Drops(t *testing.T) {
	if stdioQueueBytes != 256<<10 {
		t.Errorf("stdioQueueBytes = %d, the contract says 262144 (256 KiB)", stdioQueueBytes)
	}
	if maxOutputSegments != 4096 {
		t.Errorf("maxOutputSegments = %d, the contract says 4096", maxOutputSegments)
	}
	if maxPendingDrops != 64 {
		t.Errorf("maxPendingDrops = %d, the contract says 64", maxPendingDrops)
	}
}

func TestTheDefaultGraceIsFiveSeconds(t *testing.T) {
	if defaultCloseGrace != 5*time.Second {
		t.Errorf("defaultCloseGrace = %v, the contract says 5s", defaultCloseGrace)
	}
	if defaultResizeUnreadAfter != 5*time.Second {
		t.Errorf("defaultResizeUnreadAfter = %v, the contract says 5s", defaultResizeUnreadAfter)
	}
	if defaultMetricsEvery != 10*time.Second {
		t.Errorf("defaultMetricsEvery = %v, the contract says 10s", defaultMetricsEvery)
	}
}

// The bounds that docs/exec.md prints for the start of a session and the end of a command: how long a stream may take to send its
// open frame, how long an Options callback may take before its context ends, and how long a Classify function may take (the review
// of #90 found the first and the last unpinned: a change of either was seen by no test).
func TestTheOpenFirstFrameOptionsAndClassifyBoundsAreTheOnesTheDocsPrint(t *testing.T) {
	if defaultOpenFirstFrameTimeout != 10*time.Second {
		t.Errorf("defaultOpenFirstFrameTimeout = %v, the docs say 10s", defaultOpenFirstFrameTimeout)
	}
	if defaultOptionsDeadline != 30*time.Second {
		t.Errorf("defaultOptionsDeadline = %v, the docs say 30s", defaultOptionsDeadline)
	}
	if defaultClassifyDeadline != 5*time.Second {
		t.Errorf("defaultClassifyDeadline = %v, the docs say 5s", defaultClassifyDeadline)
	}
}

// An engine built with no timings uses the defaults: a zero is "the default", never "no wait".
func TestAnEngineWithNoTimingsUsesTheDefaultGraceAndTheOtherDefaults(t *testing.T) {
	cfg := newEngine(engineConfig{}).cfg
	if cfg.closeGrace != defaultCloseGrace {
		t.Errorf("closeGrace = %v, want the default %v", cfg.closeGrace, defaultCloseGrace)
	}
	if cfg.resizeUnreadAfter != defaultResizeUnreadAfter {
		t.Errorf("resizeUnreadAfter = %v, want the default %v", cfg.resizeUnreadAfter, defaultResizeUnreadAfter)
	}
	if cfg.metricsEvery != defaultMetricsEvery {
		t.Errorf("metricsEvery = %v, want the default %v", cfg.metricsEvery, defaultMetricsEvery)
	}
	if cfg.optionsDeadline != defaultOptionsDeadline || cfg.classifyDeadline != defaultClassifyDeadline {
		t.Errorf("optionsDeadline = %v and classifyDeadline = %v, want the defaults %v and %v", cfg.optionsDeadline, cfg.classifyDeadline, defaultOptionsDeadline, defaultClassifyDeadline)
	}
}
