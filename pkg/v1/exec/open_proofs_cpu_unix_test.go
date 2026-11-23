//go:build linux || darwin

package exec_test

import (
	"syscall"
	"time"
)

// proofCPUTime is the CPU time this process has used so far, user and system, and whether the platform can say.
func proofCPUTime() (time.Duration, bool) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, false
	}
	return proofTimeval(usage.Utime) + proofTimeval(usage.Stime), true
}

func proofTimeval(t syscall.Timeval) time.Duration {
	return time.Duration(int64(t.Sec))*time.Second + time.Duration(int64(t.Usec))*time.Microsecond
}
