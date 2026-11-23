//go:build !linux && !darwin

package exec_test

import "time"

// proofCPUTime is the CPU time this process has used so far, and whether the platform can say: this one cannot, so a proof
// that bounds the CPU time per byte reports nothing here.
func proofCPUTime() (time.Duration, bool) { return 0, false }
