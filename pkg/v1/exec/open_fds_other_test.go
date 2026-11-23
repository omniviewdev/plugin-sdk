//go:build !linux && !darwin

package exec_test

// proofOpenFDs is how many file descriptors this process holds now, and whether the platform can say: this one cannot, so a proof that
// holds the descriptors to a baseline reports nothing here.
func proofOpenFDs() (int, bool) { return 0, false }
