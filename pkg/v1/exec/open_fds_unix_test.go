//go:build linux || darwin

package exec_test

import "syscall"

// proofOpenFDs is how many file descriptors this process holds now, and whether the platform can say. It asks the kernel about each
// descriptor number below the process's limit (at most proofFDScan of them) and counts those it knows. It does not list /dev/fd: a
// listing of that directory counts the descriptor that reading it takes, and on macOS two readings of it, a few seconds apart and with
// the same descriptors open, were seen to list 7 entries and 8, which a proof of the descriptors at rest took for a leak.
func proofOpenFDs() (int, bool) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0, false
	}
	open := 0
	for fd := range int(min(limit.Cur, proofFDScan)) {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil {
			open++
		}
	}
	return open, true
}

// proofFDScan is the most descriptor numbers that proofOpenFDs asks about: a process of the proofs holds a few dozen, in the lowest numbers.
const proofFDScan = 4096
