package exec_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The in-process engine hands the stress harness the streams of a session, and the calls that manage sessions without one. What
// these tests hold it to is that it is the engine's own: an attach that the engine makes, a stream whose last frame is the one the
// served Open would send, frames that take effect only while their stream is the session's, and the served calls with their typed
// refusals. The behaviour of the ring, the lease and the tombstone is the engine's, and is proved where it lives; here it is the
// hook that is shown to carry it.

// hsRig is one in-process engine over a function a test steps, on a clock the test moves.
type hsRig struct {
	t      testing.TB
	conns  *exectestkit.Connections
	eng    exechook.Engine
	clock  *fakeClock
	grants chan int
	wrote  chan struct{}
}

// hsStepper is a function that writes the pattern in the pieces a test grants it, one grant at a time, and says when each piece is
// in the ring: a session whose output the test decides to the byte, and whose function the test never has to wait for blindly.
func hsStepper(grants <-chan int, wrote chan<- struct{}) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		var offset uint64
		for {
			select {
			case n := <-grants:
				if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
					return exec.Exit{}, err
				}
				offset += uint64(n)
				wrote <- struct{}{}
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}
}

// newHSRig builds the engine, which is closed when the test ends. cfg's clock is the rig's.
func newHSRig(t testing.TB, cfg exechook.EngineConfig, mods ...func(*exec.TargetInfo)) *hsRig {
	t.Helper()
	r := &hsRig{t: t, clock: newFakeClock(), grants: make(chan int), wrote: make(chan struct{}, 16)}
	r.conns = exectestkit.NewConnectionsT(t, "fake")
	cfg.Connections, cfg.Now = r.conns, r.clock.Now
	eng, err := exechook.NewEngine([]exec.Target{engTarget(hsStepper(r.grants, r.wrote), mods...)}, cfg)
	if err != nil {
		t.Fatalf("build the engine: %v", err)
	}
	r.eng = eng
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), engineWait)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			t.Errorf("the engine did not close: %v", err)
		}
	})
	return r
}

func (r *hsRig) request() context.Context { return r.conns.Context(context.Background()) }

// open starts a session with a terminal.
func (r *hsRig) open(id string) exechook.Command {
	r.t.Helper()
	start := &execv2.Start{Target: r.conns.WireTarget(r.t, "thing", "thing-1"), Tty: true}
	cmd, err := r.eng.Open(r.request(), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
	if err != nil {
		r.t.Fatalf("open %q: %v", id, err)
	}
	return cmd
}

// attachRequest is the first frame of an attach.
func attachRequest(id string, from uint64) *execv2.OpenRequest {
	return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: from}}}
}

func (r *hsRig) attach(id string, from uint64) exechook.Stream {
	r.t.Helper()
	s, err := r.eng.Attach(r.request(), attachRequest(id, from))
	if err != nil {
		r.t.Fatalf("attach to %q from %d: %v", id, from, err)
	}
	return s
}

// grant lets the function write n more bytes of the pattern and waits until they are in the ring. A session that ended meanwhile is
// a failure at once: its function returned and takes no grant.
func (r *hsRig) grant(cmd exechook.Command, n int) {
	r.t.Helper()
	select {
	case r.grants <- n:
	case <-cmd.Done():
		r.t.Fatalf("session %q ended before its function took a grant of %d bytes", cmd.ID(), n)
	case <-time.After(engineWait):
		r.t.Fatalf("the function took no grant of %d bytes within %v", n, engineWait)
	}
	select {
	case <-r.wrote:
	case <-time.After(engineWait):
		r.t.Fatalf("the function did not write %d bytes within %v", n, engineWait)
	}
}

// take reads n bytes from a stream, which have to be the pattern from offset with nothing lost.
func hsTake(t testing.TB, s exechook.Stream, offset uint64, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	for got := 0; got < n; {
		buf := make([]byte, n-got)
		chunk, err := s.Read(ctx, buf)
		if err != nil {
			t.Fatalf("waiting for %d more bytes at offset %d: %v", n-got, offset+uint64(got), err)
		}
		if len(chunk.Dropped) != 0 {
			t.Fatalf("a range was lost: %v", chunk.Dropped)
		}
		if chunk.Offset != offset+uint64(got) || !bytes.Equal(buf[:chunk.N], proofChunk(chunk.Offset, chunk.N)) {
			t.Fatalf("a chunk of %d bytes at offset %d, want the pattern from %d", chunk.N, chunk.Offset, offset+uint64(got))
		}
		got += chunk.N
	}
}

// hsLeft reads a stream that is no longer the session's, and holds its answer to what a stream that left is given. The context of the
// read has ended: a stream that left answers whatever its context says, and one that did not has nothing to give and says so at once.
func hsLeft(t testing.TB, s exechook.Stream, name string) exechook.Final {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 2 {
		if chunk, err := s.Read(ctx, make([]byte, 64)); !errors.Is(err, exechook.ErrStreamLeft) || chunk.N != 0 || len(chunk.Dropped) != 0 {
			t.Fatalf("%s: a read gave %d bytes, %d ranges and %v, want exechook.ErrStreamLeft and nothing else, every time", name, chunk.N, len(chunk.Dropped), err)
		}
	}
	return s.Final()
}

// hsRefusal returns the class and reason of a refusal that has to be the exec lane's.
func hsRefusal(t testing.TB, err error) (exec.ErrorClass, exec.ErrorReason) {
	t.Helper()
	var refusal *exec.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("the refusal is %T (%v), want an *exec.Error", err, err)
	}
	return refusal.Class, refusal.Reason
}

// hsStream is the stream that started a session, which every session has.
func hsStream(t testing.TB, cmd exechook.Command) exechook.Stream {
	t.Helper()
	s := cmd.Stream()
	if s == nil {
		t.Fatalf("session %q has no stream", cmd.ID())
	}
	return s
}

// An attach is the engine's: the replay it opens is exactly what the ring holds from the offset, a byte asked for is never
// replayed twice and none is lost between the replay and the live stream that begins where it ends, and the lease a Detach gives is
// what the host asked for, as far as the target's maximum.
func TestAnAttachThroughTheHookReplaysWhatTheRingHoldsFromTheOffsetAndThenGoesLive(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{})
	cmd := rig.open("s1")
	first := hsStream(t, cmd)
	if got := first.Replay(); got != (exechook.Replay{}) {
		t.Errorf("the stream that started the session opens with the replay %+v, want an empty one at offset 0", got)
	}
	rig.grant(cmd, 100)
	hsTake(t, first, 0, 40)

	// The host leaves for a lease of its own choosing: a day is above the target's 30 minutes, and is clamped to it.
	asked := time.Now()
	before := rig.clock.Now()
	expires, ok := first.Detach(24 * time.Hour)
	if !ok {
		t.Fatalf("Detach was refused for a session that lives")
	}
	if want := before.Add(30 * time.Minute); !expires.Equal(want) {
		t.Errorf("the lease ends at %v, want the clock plus the target's maximum, %v (asked at %v)", expires, want, asked)
	}
	final := hsLeft(t, first, "the stream that detached")
	if !final.Detached || final.Reason != execv2.DetachReason_DETACH_REASON_REQUESTED || !final.Lease.Equal(expires) {
		t.Errorf("the last frame of the stream that detached is %+v, want Detached for REQUESTED with the lease %v", final, expires)
	}

	rig.grant(cmd, 60) // written while no stream was attached: the ring holds it
	second := rig.attach("s1", 40)
	if got, want := second.Replay(), (exechook.Replay{Start: 40, End: 160, Bytes: 120}); got != want {
		t.Fatalf("the replay is %+v, want %+v: the ring holds [0, 160) and the host has 40 bytes", got, want)
	}
	hsTake(t, second, 40, 120) // the replay
	rig.grant(cmd, 25)
	hsTake(t, second, 160, 25) // and the live stream, from where the replay ended
	if opened := second.Opened(); opened.GetFacts().GetSessionId() != "s1" || opened.GetReplay().GetBytes() != 120 {
		t.Errorf("the Opened of the attach is %v, want the facts of session s1 and the replay of 120 bytes", opened)
	}
}

// Another stream's attach takes the session over: the older stream is told Detached for SUPERSEDED and never anything else, and
// whatever it sends afterwards takes no effect, a Close and a Leave included: the session goes on, attached to the new stream.
func TestASupersededStreamIsToldSoAndNothingItSendsAfterwardsTakesEffect(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{})
	cmd := rig.open("s2")
	older := hsStream(t, cmd)
	rig.grant(cmd, 10)
	hsTake(t, older, 0, 10)

	newer := rig.attach("s2", 10)
	if got := newer.Replay(); got != (exechook.Replay{Start: 10, End: 10}) {
		t.Errorf("the replay is %+v, want an empty one at offset 10", got)
	}
	final := hsLeft(t, older, "the superseded stream")
	if !final.Detached || final.Reason != execv2.DetachReason_DETACH_REASON_SUPERSEDED || !final.Lease.IsZero() {
		t.Errorf("the last frame of the superseded stream is %+v, want Detached for SUPERSEDED with no lease: the session is attached", final)
	}

	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	if n, err := older.Write(ctx, []byte("x")); n != 0 || !errors.Is(err, exechook.ErrStreamLeft) {
		t.Errorf("an input frame of the superseded stream took %d bytes and returned %v, want 0 and ErrStreamLeft", n, err)
	}
	if _, err := older.Resize(1, 80, 24); !errors.Is(err, exechook.ErrStreamLeft) {
		t.Errorf("a resize of the superseded stream returned %v, want ErrStreamLeft", err)
	}
	if _, err := older.Signal(execv2.Signal_SIGNAL_INT); !errors.Is(err, exechook.ErrStreamLeft) {
		t.Errorf("a signal of the superseded stream returned %v, want ErrStreamLeft", err)
	}
	if err := older.CloseInput(); !errors.Is(err, exechook.ErrStreamLeft) {
		t.Errorf("close_input of the superseded stream returned %v, want ErrStreamLeft", err)
	}
	if _, ok := older.Detach(0); ok {
		t.Errorf("a Detach of the superseded stream was applied: it is not the session's")
	}
	older.Close(true) // would end the session, if it were applied
	older.Release()   // would release the session's stream, if it were applied
	older.Leave()     // would leave it, if it were applied

	// The session lives, and is still attached to the stream that took it: its function writes on, and the stream reads it.
	rig.grant(cmd, 5)
	hsTake(t, newer, 10, 5)

	newer.Close(false)
	if res := awaitDone(t, cmd); res.Cause != "requested" {
		t.Errorf("the session ended with the cause %q, want \"requested\": the Close of the stream that is the session's", res.Cause)
	}
	ended := make([]byte, 8)
	if _, err := newer.Read(ctx, ended); !errors.Is(err, io.EOF) {
		t.Errorf("a read of the stream the session ended on returned %v, want io.EOF", err)
	}
	if got := newer.Final(); got.Detached {
		t.Errorf("the last frame of the stream the session ended on is %+v, want Ended", got)
	}
	if got := older.Final(); !got.Detached || got.Reason != execv2.DetachReason_DETACH_REASON_SUPERSEDED {
		t.Errorf("the last frame of the superseded stream changed to %+v when the session ended: nothing follows it", got)
	}
}

// A stream that is over without having left is an implicit detach: the session lives for its own lease, and the stream is told
// Detached with no reason. A session that ends while no stream is attached tells no stream Ended, and its end is the tombstone's.
func TestAStreamThatIsReleasedLeavesTheSessionLivingForItsLeaseAndAnEndWithNoStreamTellsNone(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{})
	cmd := rig.open("s3")
	stream := hsStream(t, cmd)
	rig.grant(cmd, 20)
	hsTake(t, stream, 0, 20)
	before := rig.clock.Now()
	stream.Release()
	final := hsLeft(t, stream, "the released stream")
	if !final.Detached || final.Reason != execv2.DetachReason_DETACH_REASON_UNSPECIFIED || !final.Lease.Equal(before.Add(5*time.Minute)) {
		t.Errorf("the last frame of the released stream is %+v, want Detached with no reason and the target's own lease of 5 minutes", final)
	}
	select {
	case <-cmd.Done():
		t.Fatalf("the session ended when its stream left")
	default:
	}

	// It ends while nobody is attached: no stream is told Ended, and the tombstone says how it ended.
	ctx, cancel := context.WithTimeout(rig.request(), engineWait)
	defer cancel()
	if err := rig.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "s3"}); err != nil {
		t.Fatalf("EndSession of a session with no stream: %v", err)
	}
	if got := hsLeft(t, stream, "the released stream after the end"); got != final {
		t.Errorf("the last frame of the released stream changed from %+v to %+v when the session ended", final, got)
	}
	infos, err := rig.eng.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	var found *execv2.SessionInfo
	for _, info := range infos {
		if info.GetFacts().GetSessionId() == "s3" {
			found = info
		}
	}
	if found == nil || found.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST || found.GetEndedWith().GetLastOffset() != 20 {
		t.Errorf("the tombstone of the session is %v, want one that says it was closed by request after 20 bytes", found)
	}
}

// A stream whose host closed its side after the session ended is released, which is a no-op: it goes on reading the tail it was given, and is
// told Ended. A stream whose handler is over has left: what it held of the replay it was given is dropped with it, and nothing is read from it.
func TestAStreamReleasedAfterTheSessionEndedStillReadsItsTailAndOneThatLeftReadsNothing(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{})
	for _, tc := range []struct {
		name  string
		done  func(exechook.Stream)
		bytes int // what the stream still reads of the replay it was given
	}{
		{"s10", exechook.Stream.Release, 300},
		{"s11", exechook.Stream.Leave, 0},
	} {
		cmd := rig.open(tc.name)
		first := hsStream(t, cmd)
		rig.grant(cmd, 300)
		if _, ok := first.Detach(0); !ok {
			t.Fatalf("%s: Detach was refused", tc.name)
		}
		replayed := rig.attach(tc.name, 0) // the replay is a copy of the ring, not yet read
		cmd.Close(false)
		awaitDone(t, cmd)
		tc.done(replayed)
		hsTake(t, replayed, 0, tc.bytes)
		ctx, cancel := context.WithTimeout(context.Background(), engineWait)
		if chunk, err := replayed.Read(ctx, make([]byte, 16)); !errors.Is(err, io.EOF) || chunk.N != 0 {
			t.Errorf("%s: after its tail the stream read %d bytes and %v, want io.EOF", tc.name, chunk.N, err)
		}
		cancel()
		if final := replayed.Final(); final.Detached {
			t.Errorf("%s: the stream the session ended on has the last frame %+v, want Ended", tc.name, final)
		}
	}
}

// The calls that manage sessions are the served ones: their requests are admitted as the served ones are, and what they refuse
// is typed as the wire types it. A session with a stream is not ended by EndSession, one without is, and one that ended is not
// an error; its tombstone is listed and read.
func TestTheServedCallsThatManageSessionsAreTypedAndAnswerForTheTombstone(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{})
	cmd := rig.open("s4")
	stream := hsStream(t, cmd)
	rig.grant(cmd, 100)
	ctx, cancel := context.WithTimeout(rig.request(), engineWait)
	defer cancel()

	if class, _ := hsRefusal(t, rig.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "s4"})); class != exec.ErrorClassConflict {
		t.Errorf("EndSession of a session with a stream is refused as %s, want CONFLICT: the host closes it on its stream", class)
	}
	if _, ok := stream.Detach(0); !ok {
		t.Fatalf("Detach was refused")
	}
	if err := rig.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "s4", Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
		t.Fatalf("EndSession of a detached session: %v", err)
	}
	select {
	case <-cmd.Gone():
	default:
		t.Errorf("EndSession returned before the session was gone: its place is not free")
	}
	if err := rig.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "s4"}); err != nil {
		t.Errorf("EndSession of a session that ended: %v, want none", err)
	}
	if class, reason := hsRefusal(t, rig.eng.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "never"})); class != exec.ErrorClassNotFound || reason != exec.ErrorReasonSessionUnknown {
		t.Errorf("EndSession of an id that never was is refused as %s/%s, want NOT_FOUND/SESSION_UNKNOWN", class, reason)
	}
	if class, _ := hsRefusal(t, rig.eng.EndSession(ctx, &execv2.EndSessionRequest{})); class != exec.ErrorClassInvalidRequest {
		t.Errorf("EndSession with no id is refused as %s, want INVALID_REQUEST: the request is admitted as the served one is", class)
	}

	resp, err := rig.eng.ReadOutput(ctx, &execv2.ReadOutputRequest{SessionId: "s4", FromOffset: 30, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("ReadOutput of a tombstone: %v", err)
	}
	var data []byte
	for _, c := range resp.GetChunks() {
		if c.GetOffset() != 30+uint64(len(data)) {
			t.Fatalf("a chunk at offset %d, want %d: the chunks are contiguous from the offset asked for", c.GetOffset(), 30+len(data))
		}
		data = append(data, c.GetData()...)
	}
	if !resp.GetEnded() || resp.GetNextOffset() != 100 || !bytes.Equal(data, proofChunk(30, 70)) {
		t.Errorf("ReadOutput answered ended %v, next offset %d and %d bytes, want ended, 100 and the 70 bytes of the pattern from 30", resp.GetEnded(), resp.GetNextOffset(), len(data))
	}
	if class, _ := hsRefusal(t, errOf(rig.eng.ReadOutput(ctx, &execv2.ReadOutputRequest{SessionId: "s4", FromOffset: 101}))); class != exec.ErrorClassInvalidRequest {
		t.Errorf("ReadOutput beyond the end is refused as %s, want INVALID_REQUEST", class)
	}

	attach := func(id string, from uint64) error {
		_, err := rig.eng.Attach(rig.request(), attachRequest(id, from))
		return err
	}
	if class, reason := hsRefusal(t, attach("s4", 0)); class != exec.ErrorClassNotFound || reason != exec.ErrorReasonSessionUnknown {
		t.Errorf("an attach to a session that ended is refused as %s/%s, want NOT_FOUND/SESSION_UNKNOWN: its tombstone is not a session", class, reason)
	}
	if class, reason := hsRefusal(t, attach("never", 0)); class != exec.ErrorClassNotFound || reason != exec.ErrorReasonSessionUnknown {
		t.Errorf("an attach to an id that never was is refused as %s/%s, want NOT_FOUND/SESSION_UNKNOWN", class, reason)
	}
	live := rig.open("s5")
	rig.grant(live, 4)
	if class, _ := hsRefusal(t, attach("s5", 5)); class != exec.ErrorClassInvalidRequest {
		t.Errorf("an attach from an offset the session has not reached is refused as %s, want INVALID_REQUEST", class)
	}
	start := &execv2.OpenRequest{SessionId: "s6", Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{Target: rig.conns.WireTarget(t, "thing", "thing-1")}}}
	if _, err := rig.eng.Attach(rig.request(), start); err == nil {
		t.Errorf("Attach took a request that starts a session")
	}
	live.Close(true)
}

// errOf is the error of a call whose answer the test does not need.
func errOf[T any](_ T, err error) error { return err }

// The engine's clock is the one the test gives: a tombstone holds its id and its place in the list for exactly its time to live
// on that clock, and is gone at the instant it runs out, which the test moves by hand.
func TestTheClockTheTestGivesDrivesTheTombstonesAndTheirIds(t *testing.T) {
	rig := newHSRig(t, exechook.EngineConfig{TombstoneTTL: time.Minute})
	cmd := rig.open("s7")
	rig.grant(cmd, 3)
	cmd.Close(false)
	awaitDone(t, cmd)
	awaitChannel(t, cmd.Gone(), "the id of the session to be released")

	ctx, cancel := context.WithTimeout(rig.request(), engineWait)
	defer cancel()
	listed := func() bool {
		infos, err := rig.eng.ListSessions(ctx)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		for _, info := range infos {
			if info.GetFacts().GetSessionId() == "s7" {
				return true
			}
		}
		return false
	}
	reopen := func() error {
		start := &execv2.Start{Target: rig.conns.WireTarget(t, "thing", "thing-1"), Tty: true}
		again, err := rig.eng.Open(rig.request(), &execv2.OpenRequest{SessionId: "s7", Kind: &execv2.OpenRequest_Start{Start: start}})
		if err == nil {
			again.Close(true)
			awaitChannel(t, again.Gone(), "the session that was opened again to be gone")
		}
		return err
	}
	rig.clock.advance(time.Minute - time.Nanosecond)
	if !listed() {
		t.Errorf("the tombstone is not listed a nanosecond before its time to live runs out")
	}
	if class, reason := hsRefusal(t, reopen()); class != exec.ErrorClassConflict || reason != exec.ErrorReasonSessionExists {
		t.Errorf("an Open of the id a nanosecond before its tombstone is gone is refused as %s/%s, want CONFLICT/SESSION_EXISTS", class, reason)
	}
	rig.clock.advance(time.Nanosecond)
	if listed() {
		t.Errorf("the tombstone is still listed when its time to live has run out")
	}
	if err := reopen(); err != nil {
		t.Errorf("an Open of the id once its tombstone is gone: %v, want it admitted", err)
	}
}

// A signal is the function's to read, in the order it was sent, and only a declared one is delivered: the session's consumer and a
// stream that is the session's send it alike, and a command that ended takes none.
func TestASignalReachesTheFunctionInOrderAndOnlyADeclaredOneIsTaken(t *testing.T) {
	conns := exectestkit.NewConnectionsT(t, "fake")
	got := make(chan exec.Signal, 8)
	target := engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		for {
			select {
			case sig, ok := <-p.Signals:
				if !ok {
					return exec.ExitCode(0), nil
				}
				got <- sig
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	})
	eng, err := exechook.NewEngine([]exec.Target{target}, exechook.EngineConfig{Connections: conns})
	if err != nil {
		t.Fatalf("build the engine: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	start := &execv2.Start{Target: conns.WireTarget(t, "thing", "thing-1"), Tty: true}
	cmd, err := eng.Open(conns.Context(context.Background()), &execv2.OpenRequest{SessionId: "s8", Kind: &execv2.OpenRequest_Start{Start: start}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if d := cmd.Signal(execv2.Signal_SIGNAL_INT); d != exechook.SignalQueued {
		t.Fatalf("a declared signal was %v, want queued", d)
	}
	if d, err := hsStream(t, cmd).Signal(execv2.Signal_SIGNAL_TERM); err != nil || d != exechook.SignalQueued {
		t.Fatalf("a declared signal of the session's stream was %v (%v), want queued", d, err)
	}
	for _, want := range []exec.Signal{exec.SignalInt, exec.SignalTerm} {
		select {
		case sig := <-got:
			if sig != want {
				t.Errorf("the function read the signal %v, want %v: they are read in the order they were sent", sig, want)
			}
		case <-time.After(engineWait):
			t.Fatalf("the function read no signal within %v", engineWait)
		}
	}
	if d := cmd.Signal(execv2.Signal_SIGNAL_HUP); d != exechook.SignalUndeclared {
		t.Errorf("a signal the session does not declare was %v, want undeclared", d)
	}
	cmd.Close(true)
	awaitDone(t, cmd)
	if d := cmd.Signal(execv2.Signal_SIGNAL_INT); d != exechook.SignalClosed {
		t.Errorf("a signal to a command that ended was %v, want closed", d)
	}
}
