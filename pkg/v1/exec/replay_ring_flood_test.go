package exec_test

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What a flood does to a host that cannot keep up is decided by the host, and not by the pace of anything. A host that cannot keep up is
// a host that is held: it reads nothing while the author writes a number of bytes that the test chooses, and the test knows how many the
// author has written because the author reports it (the grants of gatedPattern, and the engine's own count of its ring). What is lost
// is then a function of the size of the ring, of its chunk and of that number, on a machine of any speed and with timers of any
// granularity: no author or reader here is paced by a sleep, a timer or a spin.

const (
	// transitWindow is the flow-control window of the host's connection. A window that is not below the default turns off gRPC's dynamic
	// window, so what a host that does not read can leave in transit is this window and no more, whatever the speed of the machine.
	transitWindow = 65535
	// transitBound is the most that a stream can take ahead of a host that reads nothing: the window of the host's side, what the plugin's
	// transport queues behind it (its write quota) and the frame the stream is sending, each at most 64 KiB, with ample margin. It is a
	// bound of the transport's buffers, which no clock changes.
	transitBound = 1 << 20
)

// chunks is how many chunks the ring of the size holds.
func (s ringSizeCase) chunks() uint64 { return s.size / s.chunk }

// front is the offset of the oldest byte that the ring of the size holds once its author has written n bytes: the ring starts a chunk
// when the first byte of it arrives and drops its oldest when it needs one more than it holds, so it holds the newest chunks() chunks
// that were started. It is the arithmetic of the contract, written from the table of sizes and never read from the engine.
func (s ringSizeCase) front(n uint64) uint64 {
	started := (n + s.chunk - 1) / s.chunk
	return s.chunk * (max(started, s.chunks()) - s.chunks())
}

// lap is the most that the author can write, from any offset at which the host has read everything, without the host losing a byte
// whatever it does meanwhile: a ring less one chunk, because such a burst cannot reach a chunk that holds a byte the host has not read.
func (s ringSizeCase) lap() uint64 { return s.size - s.chunk }

// unreadLimit is how many bytes the host of a target that stalls may leave unread before the author is held: 256 KiB, and for a ring too
// small to hold that, a ring less one chunk.
func (s ringSizeCase) unreadLimit() uint64 { return min(256<<10, s.lap()) }

// ringRig serves one author, which writes the pattern in the bursts its test grants it (gatedPattern), as a target that stalls the way the
// test says, to a host whose connection has a fixed window.
type ringRig struct {
	*wireRig
	grants  chan int
	written atomic.Uint64
}

func newRingRig(t testing.TB, stall exec.StallBehavior) *ringRig {
	t.Helper()
	r := &ringRig{grants: make(chan int, 1024)}
	conns := exectestkit.NewConnectionsT(t, "fake")
	server := exectestkit.Serve(t, exectestkit.ServeConfig{
		Targets:     []exec.Target{engTarget(gatedPattern(r.grants, &r.written), func(i *exec.TargetInfo) { i.Capabilities.Stall = stall })},
		Connections: conns,
		DialOptions: []grpc.DialOption{grpc.WithInitialWindowSize(transitWindow), grpc.WithInitialConnWindowSize(transitWindow)},
	})
	r.wireRig = &wireRig{t: t, conns: conns, server: server, client: server.V2(t)}
	return r
}

// ringRun is the one session of a ringRig, and the host's side of it: the audit of every frame it has read, and where the author is.
type ringRun struct {
	t     *testing.T
	rig   *ringRig
	size  ringSizeCase
	id    string
	conn  exec.Conn
	audit proofAudit

	written uint64 // what the author has written: the offset the output has reached
	dropped uint64 // what the engine said it had dropped when the author last stopped

	// What the host read of the burst in hand: how many ranges it was told of, and where the last ends.
	drops   int
	dropEnd uint64
}

// openRun opens the session, without a terminal and with the ring of the size.
func (r *ringRig) openRun(t *testing.T, size ringSizeCase, lossy bool) *ringRun {
	t.Helper()
	run := &ringRun{t: t, rig: r, size: size, id: "s1", audit: proofAudit{verify: true, lossy: lossy}}
	run.conn = r.mustOpen(run.id, withoutTTY, func(s *execv2.Start) { s.ReplayBytes = size.ask })
	return run
}

// account is the engine's own count of the ring of the session.
func (r *ringRun) account() exec.RingAccount {
	r.t.Helper()
	acc, ok := exec.PluginRingAccount(r.rig.server.Plugin, r.id)
	if !ok {
		r.t.Fatalf("%s: the engine holds no live session of this id", r.id)
	}
	return acc
}

// write has the author write n more bytes and waits until it has. A lossy target never holds its author, so the author finishes whatever
// the host reads: an author that the engine holds fails the test at once, and does not wait for a bound to run out.
func (r *ringRun) write(n uint64) {
	r.t.Helper()
	want := r.written + n
	r.rig.grants <- int(n)
	eventually(r.t, fmt.Sprintf("the author to write the %d bytes it was granted, though the host reads nothing", n), func() bool {
		if r.rig.written.Load() >= want {
			return true
		}
		if acc := r.account(); acc.Stalls > 0 {
			r.t.Fatalf("%s: the author was held in a Write after %d of the %d bytes it was granted, by a host that reads nothing: a target that stalls lossily never holds its author",
				r.id, acc.Written-r.written, n)
		}
		return false
	})
	r.written = want
}

// holdBlocked has the author of a target that stalls the way a blocking target does write total bytes while the host reads nothing, and
// waits until the engine holds it. The author cannot finish: the host has left the unread limit unread, however far it has still to go, and
// the engine holds it there, having dropped nothing. The bytes the ring has taken are what the host had left unread and what its stream
// took in transit, so they are at least the limit and at most the limit and transitBound more, and the host has not read a byte.
func (r *ringRun) holdBlocked(total uint64) {
	t := r.t
	t.Helper()
	r.rig.grants <- int(total)
	r.written = total
	var acc exec.RingAccount
	eventually(t, "the engine to hold the author, or the author to finish", func() bool {
		acc = r.account()
		return acc.Stalls > 0 || r.rig.written.Load() >= total
	})
	limit := r.size.unreadLimit()
	switch {
	case acc.Stalls == 0:
		t.Fatalf("%s: the author wrote all %d bytes while the host read nothing and was never held: a target whose stalled reader slows the process holds its author at %d unread bytes", r.id, total, limit)
	case acc.Written < limit || acc.Written > limit+transitBound || acc.Queued > limit || acc.Dropped != 0 || acc.DroppedDetached != 0:
		t.Fatalf("%s: the author was held with %d bytes written, %d of them unread, %d dropped and %d dropped while detached: want at least %d written and at most %d, no more than %d unread, and nothing dropped",
			r.id, acc.Written, acc.Queued, acc.Dropped, acc.DroppedDetached, limit, limit+transitBound, limit)
	}
	t.Logf("the host read nothing and the engine held the author after %d of %d bytes: the unread limit is %d, and %d were unread", acc.Written, total, limit, acc.Queued)
}

// readTo reads the stream into the audit until the host has been told of every byte below offset, delivered or dropped, and no further.
func (r *ringRun) readTo(offset uint64) {
	r.t.Helper()
	r.drops, r.dropEnd = 0, 0
	for r.audit.next < offset {
		frame, err := recvFrame(r.t, r.conn)
		if err != nil {
			r.t.Fatalf("%s: reading up to the offset %d, the stream at %d: %v", r.id, offset, r.audit.next, err)
		}
		if d := frame.GetDropped(); d != nil {
			r.drops++
			r.dropEnd = d.GetFromOffset() + d.GetBytes()
		}
		if r.audit.take(frame) {
			r.t.Fatalf("%s: the session ended at the offset %d, before the host was told of the byte %d", r.id, r.audit.next, offset)
		}
	}
	r.failOnProblems()
	if r.audit.next != offset {
		r.t.Fatalf("%s: the host was told of the bytes up to %d, the author wrote %d", r.id, r.audit.next, offset)
	}
}

func (r *ringRun) failOnProblems() {
	r.t.Helper()
	for _, problem := range r.audit.problems {
		r.t.Errorf("%s: the stream: %s", r.id, problem)
	}
	if len(r.audit.problems) > 0 {
		r.t.FailNow()
	}
}

// burst has the author write n bytes while the host reads nothing, holds the engine to what the size of the ring decides at that moment,
// and then has the host read until it has been told of every byte the author wrote, and holds the stream to the same account.
//
// The most the host can lose of the burst is what the ring no longer holds of it and the host has not read: from the offset the host is
// at up to the front of the ring. The host can lose less only by the bytes its stream took meanwhile, which are in transit, bounded by
// transitBound. A burst that cannot reach a byte the host has not read loses exactly nothing.
func (r *ringRun) burst(n uint64) {
	t := r.t
	t.Helper()
	from, to := r.written, r.written+n
	if r.audit.next != from {
		t.Fatalf("%s: a burst starts where the host has read everything: the host is at %d, the author at %d", r.id, r.audit.next, from)
	}
	front := r.size.front(to)
	lostMost := max(front, from) - from
	lostLeast := uint64(0)
	if lostMost > transitBound {
		lostLeast = lostMost - transitBound
	}

	r.write(n)

	// The author is parked and the host has read nothing: the ring is what the burst made it, and the engine's account is exact.
	acc := r.account()
	if acc.Written != to || acc.Stalls != 0 || acc.DroppedDetached != 0 {
		t.Fatalf("%s: after %d bytes the engine says written %d, stalls %d, dropped while detached %d: want %d written and the other two none", r.id, to, acc.Written, acc.Stalls, acc.DroppedDetached, to)
	}
	lost := acc.Dropped - r.dropped
	if acc.Dropped < r.dropped || lost < lostLeast || lost > lostMost {
		t.Fatalf("%s: a burst of %d bytes from %d into a ring of %s lost %d bytes (the engine's count went from %d to %d), want between %d and %d: the ring holds [%d, %d) and the host had read up to %d",
			r.id, n, from, r.size.name, lost, r.dropped, acc.Dropped, lostLeast, lostMost, front, to, from)
	}
	info := proofListed(r.rig.client, r.id)
	if info == nil {
		t.Fatalf("%s: ListSessions does not list the session", r.id)
	}
	if !info.GetAttached() || info.GetNextOffset() != to || info.GetReplayStart() != front {
		t.Fatalf("%s: ListSessions says the ring of a session that is attached (%v) is [%d, %d) after %d bytes, want [%d, %d): the newest %d chunks of %d bytes",
			r.id, info.GetAttached(), info.GetReplayStart(), info.GetNextOffset(), to, front, to, r.size.chunks(), r.size.chunk)
	}

	r.readTo(to)
	// What the host was told is what the engine counted.
	if r.audit.dropped != acc.Dropped {
		t.Fatalf("%s: the Dropped frames of the stream say %d bytes were lost in all, the engine counted %d", r.id, r.audit.dropped, acc.Dropped)
	}
	r.dropped = acc.Dropped
	// The ring never reports as lost a byte that it still holds: every range the host was told of lies below the front of the ring, so the host
	// read every byte of what the ring held when the author stopped. The last range ends at the front or before it: before it when the stream,
	// overtaken late in the burst, took the bytes between that range and the front before the author wrote on.
	if r.drops > 0 && r.dropEnd > front {
		t.Fatalf("%s: the last range the host was told of ends at %d, beyond the front of the ring, %d: the ring holds bytes that range says it lost", r.id, r.dropEnd, front)
	}
	if lostMost > 0 {
		t.Logf("a burst of %d bytes from %d, the host reading nothing: the ring holds [%d, %d), %d bytes lost of at most %d (%.2f%% of the burst), %d Dropped frames",
			n, from, front, to, lost, lostMost, 100*float64(lost)/float64(n), r.drops)
	}
}

// readToEnd reads the stream until its Ended and checks that nothing follows it. It is how the host reads what is left once the author is done.
func (r *ringRun) readToEnd() *execv2.Ended {
	r.t.Helper()
	for {
		frame, err := recvFrame(r.t, r.conn)
		if err != nil {
			r.t.Fatalf("%s: the stream at %d ended with %v before its Ended", r.id, r.audit.next, err)
		}
		if !r.audit.take(frame) {
			continue
		}
		if next, err := recvFrame(r.t, r.conn); !errors.Is(err, io.EOF) {
			r.t.Fatalf("%s: after Ended: a %s frame and %v, want io.EOF: nothing follows Ended", r.id, frameKind(next), err)
		}
		r.failOnProblems()
		return r.audit.ended
	}
}

// finish ends the session, as its author does by returning, and holds its Ended to what the host was told: every byte the author wrote was
// read or dropped, Ended says the same, the output is TRUNCATED when a byte was dropped and COMPLETE when none was, and the exit is the
// author's.
func (r *ringRun) finish() {
	r.t.Helper()
	close(r.rig.grants)
	r.checkEnded(r.readToEnd())
	a := &r.audit
	r.t.Logf("%d bytes written: %d read in %d Output frames, %d dropped (%.4f%%) in %d Dropped frames",
		r.written, a.read, a.frames, a.dropped, 100*float64(a.dropped)/float64(max(r.written, 1)), a.droppedFrames)
}

func (r *ringRun) checkEnded(ended *execv2.Ended) {
	t := r.t
	t.Helper()
	a := &r.audit
	if a.read+a.dropped != r.written || ended.GetBytesOut() != r.written || ended.GetLastOffset() != r.written {
		t.Errorf("%s: read %d + dropped %d = %d, Ended.bytes_out %d, Ended.last_offset %d, the author wrote %d",
			r.id, a.read, a.dropped, a.read+a.dropped, ended.GetBytesOut(), ended.GetLastOffset(), r.written)
	}
	if ended.GetDroppedBytes() != a.dropped {
		t.Errorf("%s: Ended.dropped_bytes is %d, the Dropped frames said %d", r.id, ended.GetDroppedBytes(), a.dropped)
	}
	want := execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	if a.dropped > 0 {
		want = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	}
	if ended.GetOutput() != want {
		t.Errorf("%s: Ended.output is %v after %d bytes were dropped, want %v", r.id, ended.GetOutput(), a.dropped, want)
	}
	if exited := ended.GetEnd().GetExited(); exited == nil || exited.GetExitCode() != 0 {
		t.Errorf("%s: the author returned exit 0 and the session ended %v", r.id, ended.GetEnd())
	}
	if a.dropped > 0 && a.droppedFrames == 0 {
		t.Errorf("%s: %d bytes were dropped and no Dropped frame said so", r.id, a.dropped)
	}
}
