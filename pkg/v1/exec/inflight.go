package exec

import (
	"context"
	"sync"
)

// inflight counts the calls of the server that hold a command, from before they start it until they have delivered its end and
// returned: the handler of an Open, which sends Ended, and the calls that run a command and answer with its result. The engine's
// own bookkeeping ends earlier: a command is removed once its end is emitted, and the stream or the caller learns of the end after,
// from the call's own goroutine. A plugin that stops tells each host the end of each command, so it must not let the transport
// that carries the ends go before the calls that send them have returned, and Plugin.Shutdown waits for them, after the engine.
//
// The zero value is ready to use. It is not a sync.WaitGroup, which must not be added to while a wait may be returning: a host
// starts a command while the plugin is stopping, which is exactly that.
type inflight struct {
	mu   sync.Mutex
	n    int           // the calls that began and have not ended
	idle chan struct{} // made by a wait that has to block, closed and dropped when the last call ends
}

// begin counts one call and returns the function that ends it, which the call makes once, when it has returned what it holds.
// A call begins before it starts its command, so that no command is admitted that a wait does not wait for.
func (f *inflight) begin() (end func()) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return f.end
}

func (f *inflight) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n--
	if f.n < 0 {
		panic("exec: an in-flight call ended twice")
	}
	if f.n == 0 && f.idle != nil {
		close(f.idle)
		f.idle = nil
	}
}

// wait returns once no call is in flight, or with ctx's error when ctx ends first; at the moment both hold, nil. A call that
// begins while it waits is waited for too.
func (f *inflight) wait(ctx context.Context) error {
	for {
		f.mu.Lock()
		if f.n == 0 {
			f.mu.Unlock()
			return nil
		}
		if f.idle == nil {
			f.idle = make(chan struct{})
		}
		idle := f.idle
		f.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			f.mu.Lock()
			none := f.n == 0
			f.mu.Unlock()
			if none {
				return nil
			}
			return ctx.Err()
		}
	}
}
