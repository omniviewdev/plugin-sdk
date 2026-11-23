package exectest

import (
	"testing"
	"time"
)

// A short run: the real engine under the stress, every invariant at rest.
func TestStressTheEngineLeavesTheEndStateItPromises(t *testing.T) {
	Stress(t, StressConfig{Duration: time.Second})
}
