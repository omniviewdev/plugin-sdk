//go:build linux || darwin

package exec_test

import "os"

// proofOpenFDs is how many file descriptors this process holds now, and whether the platform can say. Reading the directory takes
// one of its own, which every reading counts alike.
func proofOpenFDs() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}
