package exec_test

// Helpers of the tests of the repairs to the legacy lane that the tests with and without a real terminal share.

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	logging "github.com/omniviewdev/plugin-sdk/log"
)

// anyGoroutine reports whether a goroutine is alive whose stack contains every one of parts.
func anyGoroutine(parts ...string) bool {
	var dump string
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			dump = string(buf[:n])
			break
		}
	}
	for _, g := range strings.Split(dump, "\n\n") {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(g, part)
		}
		if all {
			return true
		}
	}
	return false
}

// hookedLogger is a logger that discards what it is given and runs hook (when there is one) on every debug
// message: the seam the tests use to see where the manager is.
func hookedLogger(hook func(msg string)) logging.Logger {
	var seam atomic.Pointer[func(string)]
	if hook != nil {
		seam.Store(&hook)
	}
	return hookLogger{Logger: logging.NewNop(), hook: &seam}
}

// streamOpened returns a hook that notes when the manager has opened a stream, and what is closed by then. A
// client that has opened a stream over gRPC has one at the server only when the manager says so: output of a
// session that comes before is delivered to nobody.
func streamOpened() (hook func(msg string), opened <-chan struct{}) {
	signal := make(chan struct{})
	var once sync.Once
	return func(msg string) {
		if msg == "stream opened" {
			once.Do(func() { close(signal) })
		}
	}, signal
}
