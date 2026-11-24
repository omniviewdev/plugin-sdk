package exec_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The ledger's host keeps the record of what it did and of what its streams told it, in the order it happened. A stream that ends when the
// host did not end it can be explained by nothing else: the attach that it was the answer to, the Detach that was never sent, the cancel
// that it should have outlived. Every last frame of every stream is put down to something the host did before the frame came
// (ledgerHost.attribution), and a failure prints the end of the record, so that what went wrong is read from the failure and not guessed
// from a rerun.

// ledgerEvents is the record. It is written by the host's goroutine and by the goroutines that read its streams.
type ledgerEvents struct {
	mu       sync.Mutex
	began    time.Time
	lines    []string
	attaches []*ledgerAttach
}

func newLedgerEvents() *ledgerEvents { return &ledgerEvents{began: time.Now()} }

// ledgerAttach is one attach that the host made: the open of the session (kind "start"), an attach that it holds ("real", and
// "concurrent" for the two that it makes at once), or one that it gives up on at once ("cancelled").
type ledgerAttach struct {
	n    int
	kind string
	from uint64
	// issued and returned are the numbers of the events at which the host made the call and at which the call came back; returned is zero
	// while it has not. err is what it came back with.
	issued, returned int
	err              error
}

// add records an event and returns its number.
func (e *ledgerEvents) add(format string, args ...any) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.addLocked(format, args...)
}

func (e *ledgerEvents) addLocked(format string, args ...any) int {
	n := len(e.lines) + 1
	e.lines = append(e.lines, fmt.Sprintf("%6d %10.3fms  %s", n, float64(time.Since(e.began).Microseconds())/1000, fmt.Sprintf(format, args...)))
	return n
}

// issue records that the host is making an attach.
func (e *ledgerEvents) issue(kind string, from uint64) *ledgerAttach {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := &ledgerAttach{n: len(e.attaches) + 1, kind: kind, from: from}
	a.issued = e.addLocked("attach #%d (%s) from offset %d: made", a.n, kind, from)
	e.attaches = append(e.attaches, a)
	return a
}

// answer records how the call of an attach came back.
func (e *ledgerEvents) answer(a *ledgerAttach, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a.err = err
	if err != nil {
		a.returned = e.addLocked("attach #%d (%s): the call came back with %v", a.n, a.kind, err)
		return
	}
	a.returned = e.addLocked("attach #%d (%s): the call came back with its Opened", a.n, a.kind)
}

// supersessor returns an attach of the host that can have registered after the one that made a stream, and so can have taken the session
// from it, before the frame that said so came (at the event before): one that was made before that frame, and was not over before the
// stream's own attach was made (it came back after, or has not come back). An attach whose call came back before the stream's was made is
// not one: it registered before the stream did, or never, because the engine registers a stream only while its stream has not ended and the
// host's cancel reaches the plugin before the stream that the host makes next. It returns nil when there is none.
func (e *ledgerEvents) supersessor(of *ledgerAttach, before int) *ledgerAttach {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.attaches {
		if a != of && a.issued < before && (a.returned == 0 || a.returned > of.issued) {
			return a
		}
	}
	return nil
}

// describe says an attach and how it stands, for a message.
func (e *ledgerEvents) describe(a *ledgerAttach) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := "has not come back"
	switch {
	case a.err != nil:
		state = fmt.Sprintf("came back with %v at event %d", a.err, a.returned)
	case a.returned != 0:
		state = fmt.Sprintf("came back with its Opened at event %d", a.returned)
	}
	return fmt.Sprintf("attach #%d (%s), made at event %d, %s", a.n, a.kind, a.issued, state)
}

// tail returns the last n events.
func (e *ledgerEvents) tail(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.lines) <= n {
		return append([]string(nil), e.lines...)
	}
	return append([]string(nil), e.lines[len(e.lines)-n:]...)
}

// ledgerLast says the last frame of a stream, for the record and for a message.
func ledgerLast(f *execv2.ServerFrame) string {
	if d := f.GetDetached(); d != nil {
		return fmt.Sprintf("Detached{%s, lease %d}", strings.TrimPrefix(d.GetReason().String(), "DETACH_REASON_"), d.GetLeaseExpiresUnixMs())
	}
	return frameKind(f)
}

// The clock of the ledger's engine stands still. The lease that a Detach is given is read from it, and the test holds it to the lease that
// was asked for, to the millisecond: the machine's own clock steps (a virtual machine that is told the time, a runner that is corrected), and a
// bound that is measured against it is a bound that fails on a runner that is not slow. The leases are timers that nobody runs out: a lease
// of the ledger's moves is a minute or more, and the session of a host that is away is not to end because the runner paused.
var ledgerEpoch = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

func ledgerNow() time.Time { return ledgerEpoch }

func ledgerLeaseTimer(time.Duration) (<-chan time.Time, func()) {
	return make(chan time.Time), func() {}
}

// ledgerLateKey is the metadata that marks an attach that the host means to be late.
const ledgerLateKey = "x-ledger-late"

// ledgerLate makes attaches late, on purpose. The handler of an attach that the host cancels and the handler of the attach that the host
// makes next run on goroutines of their own, and on a runner with few processors the first was late enough to register after the second and
// take the session from it. A test that waits for that to happen by chance fails once in a hundred runs and proves nothing on the others:
// most cancelled attaches are over before their handler has read the open frame. So the host makes some of its attaches late on purpose
// (ledgerHost.lateAttach): it marks the attach, waits until the plugin's seam (exec.SetPluginBeforeAttach) holds its handler with the open
// frame read and nothing registered, cancels the stream, makes the attach that it holds, and lets the handler go when that has been
// answered. The order that the runner reached by chance is then the order of every one of them, and the engine is held to it.
type ledgerLate struct {
	mu sync.Mutex
	// arrived is closed when the handler of the attach with the tag is in the seam, and released when the host lets it go.
	arrived, released map[string]chan struct{}
}

func newLedgerLate() *ledgerLate {
	return &ledgerLate{arrived: map[string]chan struct{}{}, released: map[string]chan struct{}{}}
}

// chans returns the two channels of the attach with the tag, which are made when either side asks for them first.
func (l *ledgerLate) chans(tag string) (arrived, released chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.arrived[tag] == nil {
		l.arrived[tag], l.released[tag] = make(chan struct{}), make(chan struct{})
	}
	return l.arrived[tag], l.released[tag]
}

// closeOnce closes a channel that may have been closed.
func (l *ledgerLate) closeOnce(c chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-c:
	default:
		close(c)
	}
}

// hold is the plugin's seam: the handler of a marked attach says that it is here and waits until the host lets it go. It never waits for
// the attach's own context, which has ended by then: that is what the handler is held for. The bound only ends a hang.
func (l *ledgerLate) hold(ctx context.Context, _ string, _ uint64) {
	md, _ := metadata.FromIncomingContext(ctx)
	tags := md.Get(ledgerLateKey)
	if len(tags) == 0 {
		return
	}
	arrived, released := l.chans(tags[0])
	l.closeOnce(arrived)
	select {
	case <-released:
	case <-time.After(engineWait):
	}
}

// let lets the handler of the attach with the tag go on. A handler that has not reached the seam yet passes it.
func (l *ledgerLate) let(tag string) {
	_, released := l.chans(tag)
	l.closeOnce(released)
}

// attachCall is the host's attach: an Open whose first frame attaches to the session from an offset. A tag marks it for the plugin's seam, which
// holds the handler of a marked attach (ledgerLate).
func (h *ledgerHost) attachCall(ctx context.Context, asked uint64, lateTag string) (exec.Conn, error) {
	call := h.rig.request(ctx)
	if lateTag != "" {
		call = metadata.AppendToOutgoingContext(call, ledgerLateKey, lateTag)
	}
	return h.rig.client.Open(call, &execv2.OpenRequest{SessionId: h.id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: asked}}})
}

// letLate lets go the handlers of the attaches that the host made late, once the attach that the host made after them has been answered.
func (h *ledgerHost) letLate() {
	for _, tag := range h.pendingLate {
		h.late.let(tag)
	}
	h.pendingLate = h.pendingLate[:0]
}
