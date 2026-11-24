package exec_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// An Open that is an attach takes over a session that exists: the reply describes exactly the bytes that follow, the live stream
// continues where they end, and the older stream is told it was superseded. The tests run the stack a host and a plugin use.

// gatedRig serves one target whose function writes the pattern in the pieces the test grants it: a session that the test
// paces, whose output is a function of its offset.
type gatedRig struct {
	*sessionRig
	grants  chan int
	written atomic.Uint64
}

func newGatedRig(t testing.TB, cfg exechook.EngineConfig, mods ...func(*exec.TargetInfo)) *gatedRig {
	t.Helper()
	g := &gatedRig{grants: make(chan int, 1024)}
	g.sessionRig = newSessionRig(t, cfg, engTarget(gatedPattern(g.grants, &g.written), mods...))
	return g
}

// grant lets the function write n more bytes, and waits until it has.
func (g *gatedRig) grant(t testing.TB, n int) {
	t.Helper()
	want := g.written.Load() + uint64(n)
	g.grants <- n
	eventually(t, "the function to write what it was granted", func() bool { return g.written.Load() >= want })
}

// sum is how many bytes of output the frames carry.
func sum(frames []*execv2.ServerFrame) (n int) {
	for _, f := range frames {
		n += len(f.GetOutput().GetData())
	}
	return n
}

func TestAnAttachToASessionThatDoesNotExistIsNotFoundTypedAndNothingRan(t *testing.T) {
	var calls atomic.Int32
	rig := newWireRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	_, err := rig.attach(context.Background(), "nothing", 0)
	if !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
		t.Fatalf("an attach to an id nothing holds: %v, want NOT_FOUND / SESSION_UNKNOWN", err)
	}
	frame, rawErr := rig.rawOpen(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: &execv2.OpenRequest{
		SessionId: "nothing", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}},
	}}})
	if frame != nil || rawErr == nil {
		t.Fatalf("an attach that was refused sent %v and %v: a refusal is a status and no frame", frame, rawErr)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the function ran %d times for an attach that was refused", n)
	}
}

func TestAnAttachToARunIsNotFoundBecauseARunIsNotASession(t *testing.T) {
	started := exectest.NewGate()
	rig := newWireRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		started.Open()
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rig.client.Run(rig.request(ctx), &execv2.RunRequest{
			RequestId: "run-1", Target: rig.conns.WireTarget(t, "thing", "thing-1"), Argv: []string{"x"}, TimeoutMs: 60_000,
		})
	}()
	awaitChannel(t, started.Opened(), "the run to start")
	if _, err := rig.attach(context.Background(), "run-1", 0); !isRefusalOf(err, exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
		t.Fatalf("an attach to the id of a live run: %v, want NOT_FOUND / SESSION_UNKNOWN", err)
	}
	cancel()
	<-done
}

func TestAnAttachFromBeyondTheOutputIsRefusedInvalidAndTheSessionIsUntouched(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	conn := rig.mustOpen("s1", withoutTTY)
	rig.grant(t, 100)
	readPatternBytes(t, conn, 0, 100)
	if _, err := rig.attach(context.Background(), "s1", 101); !isRefusalOf(err, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified) {
		t.Fatalf("an attach from 101 to a session whose output ends at 100: %v, want INVALID_REQUEST", err)
	}
	// The refusal changed nothing: the stream is still the session's, and an attach at the very end is a replay of nothing.
	rig.grant(t, 50)
	readPatternBytes(t, conn, 100, 50)
	second := rig.mustAttach("s1", 150)
	if r := second.Opened().GetReplay(); r.GetStart() != 150 || r.GetEnd() != 150 || r.GetBytes() != 0 {
		t.Fatalf("an attach at the end of the output: replay %v, want start = end = 150 and no bytes", r)
	}
}

func TestAnAttachReplaysExactlyWhatOpenedDescribesAndTheLiveStreamContinuesWhereItEnds(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	first := rig.mustOpen("s1", withoutTTY)
	rig.grant(t, 300_000)
	have := uint64(sum(readPatternBytes(t, first, 0, 100_000)))

	second := rig.mustAttach("s1", have)
	r := second.Opened().GetReplay()
	if r.GetStart() != have || r.GetEnd() != 300_000 || r.GetBytes() != 300_000-have {
		t.Fatalf("replay %v, want start %d (what the host asked for), end 300000 (the output now) and %d bytes", r, have, 300_000-have)
	}
	if facts := second.Opened().GetFacts(); facts.GetSessionId() != "s1" || facts.GetCapabilities() == nil {
		t.Fatalf("the attach's Opened carries no session facts: %v", facts)
	}
	// The replay is exactly the bytes [start, end), as Output frames, and the live stream goes on at the end with no byte lost.
	frames := readPatternBytes(t, second, r.GetStart(), int(r.GetBytes()))
	if got := uint64(sum(frames)); got != r.GetBytes() {
		t.Fatalf("the replay carried %d bytes, Opened said %d", got, r.GetBytes())
	}
	rig.grant(t, 50_000)
	readPatternBytes(t, second, 300_000, 50_000)

	// The older stream was told that it was superseded, as its last frame, and never that the session ended.
	last := endsWith(t, drainToEnd(t, first))
	if d := last.GetDetached(); d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_SUPERSEDED || d.GetLeaseExpiresUnixMs() != 0 {
		t.Fatalf("the older stream's last frame is %v, want Detached{SUPERSEDED} with no lease", last)
	}
	// The session was not ended by it: it ends once, on the stream that is attached, when its host closes it.
	sendClose(t, second, execv2.CloseMode_CLOSE_MODE_GRACEFUL)
	frames = drainToEnd(t, second)
	if ended := endsWith(t, frames).GetEnded(); ended == nil || ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
		t.Fatalf("the session ended with %v, want Closed by REQUEST on the stream that closed it", lastFrame(t, frames))
	}
}

func TestAnAttachFromBeforeTheOldestByteTheRingHoldsStartsAtItsFrontAndSaysSo(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	first := rig.mustOpen("s1", withoutTTY, func(s *execv2.Start) { s.ReplayBytes = 1024 }) // 4 chunks of 256 bytes
	sendDetach(t, first, 0)
	endsWith(t, drainToEnd(t, first))
	rig.grant(t, 10_240) // nobody reads: the session is detached, and the ring keeps the newest whole chunks

	second := rig.mustAttach("s1", 0)
	r := second.Opened().GetReplay()
	if r.GetStart() != 9216 || r.GetEnd() != 10_240 || r.GetBytes() != 1024 {
		t.Fatalf("replay %v, want [9216, 10240) of 1024 bytes: the front of the ring, which is after the offset the host asked for", r)
	}
	readPatternBytes(t, second, r.GetStart(), int(r.GetBytes()))
}

func TestAnAttachFromInsideTheRingResumesExactlyThereAndNotAtAChunkBoundary(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	first := rig.mustOpen("s1", withoutTTY)
	rig.grant(t, 200_000)
	readPatternBytes(t, first, 0, 100)
	second := rig.mustAttach("s1", 70_001) // inside the second 64 KiB chunk
	r := second.Opened().GetReplay()
	if r.GetStart() != 70_001 || r.GetEnd() != 200_000 || r.GetBytes() != 200_000-70_001 {
		t.Fatalf("replay %v, want [70001, 200000): the offset the host has, not the chunk boundary before it", r)
	}
	readPatternBytes(t, second, 70_001, int(r.GetBytes()))
}

func TestAnAttachToASessionThatHasWrittenNothingHasAnEmptyReplay(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	rig.mustOpen("s1", withoutTTY)
	second := rig.mustAttach("s1", 0)
	if r := second.Opened().GetReplay(); r == nil || r.GetStart() != 0 || r.GetEnd() != 0 || r.GetBytes() != 0 {
		t.Fatalf("replay %v of a session that wrote nothing, want start = end = bytes = 0", r)
	}
}

// A stream that is no longer the session's does not drive it: its input is not the host's, and a Close it sent before it learned it
// was superseded does not destroy the session its successor holds.
func TestAFrameOfASupersededStreamIsNotAppliedToTheSession(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	first := rig.mustOpen("s1", withoutTTY)
	second, err := rig.attach(context.Background(), "s1", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	endsWith(t, drainToEnd(t, first)) // Detached{SUPERSEDED}: the stream is over
	_ = first.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_KILL}}})
	_ = first.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte("stale")}})
	sendInput(t, second, "fresh")
	frames := readUntilOutput(t, second, "fresh")
	if frames == "" {
		t.Fatal("the new stream got no echo")
	}
	if got := frames; got != "fresh" {
		t.Fatalf("the new stream's echo is %q, want %q: the old stream's input is not the host's", got, "fresh")
	}
}

// readUntilOutput reads the output of a stream until it holds at least len(want) bytes, and returns what it read.
func readUntilOutput(t testing.TB, conn exec.Conn, want string) string {
	t.Helper()
	var got []byte
	for len(got) < len(want) {
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("waiting for %q: read %q and then %v", want, got, err)
		}
		if ended := frame.GetEnded(); ended != nil {
			t.Fatalf("the session ended while waiting for %q: %v", want, ended.GetEnd())
		}
		got = append(got, frame.GetOutput().GetData()...)
	}
	return string(got)
}

// What an attach says of the session is the session's own: the program it was asked to run, and everything the function reported
// when it started (the resolved program, the pid, the terminal, the TERM, the transport and the capabilities it narrowed); never
// the target's default argv or the capabilities its kind declared.
func TestAnAttachCarriesTheSessionsOwnLatestFactsAndNeverTheTargetsDefaults(t *testing.T) {
	reported := exectest.NewGate()
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		narrowed := p.Capabilities
		narrowed.Resize = false
		p.Started(exec.SessionFacts{
			PID: 4242, Program: exec.Program{Argv: []string{"/usr/bin/top", "-b"}, Name: "top", Path: "/usr/bin/top"},
			TTY: new(true), Size: exec.Size{Cols: 100, Rows: 30}, Term: "xterm-256color", Transport: "test-transport",
			Capabilities: &narrowed,
		})
		reported.Open()
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, func(i *exec.TargetInfo) { i.Capabilities.Resize = true; i.DefaultArgv = []string{"/bin/sh"} }))
	first := rig.mustOpen("s1", func(s *execv2.Start) { s.Argv = []string{"/usr/bin/top", "-b"}; s.Tty = true })
	awaitChannel(t, reported.Opened(), "the function to report")
	if !opened(first).GetFacts().GetCapabilities().GetResize() {
		t.Fatal("the first Opened already carries the narrowed capabilities: it is made before the function runs")
	}

	second := rig.mustAttach("s1", 0)
	facts := second.Opened().GetFacts()
	if facts.GetSessionId() != "s1" || facts.GetPid() != 4242 || facts.GetTerm() != "xterm-256color" || facts.GetTransport() != "test-transport" || !facts.GetTty() {
		t.Errorf("the attach's facts %v lack what the function reported at Started", facts)
	}
	if p := facts.GetProgram(); p.GetName() != "top" || p.GetPath() != "/usr/bin/top" || len(p.GetArgv()) != 2 || p.GetArgv()[0] != "/usr/bin/top" {
		t.Errorf("the attach says the session runs %v, want the program it runs (top -b), never the target's default (/bin/sh)", p)
	}
	if facts.GetCapabilities().GetResize() {
		t.Error("the attach's capabilities still say Resize, which the session narrowed at Started: an attach carries the latest effective set")
	}
	if facts.GetSize().GetCols() != 100 || facts.GetSize().GetRows() != 30 || facts.GetTarget().GetProvider() != "fake" || facts.GetLimits() == nil {
		t.Errorf("the attach's facts %v lack the size, the target or the limits", facts)
	}
}

// opened is the Opened of a conn.
func opened(c exec.Conn) *execv2.Opened { return c.Opened() }

// A session that has not started when an attach takes it over is sent its Started on the stream that is attached when it is made, and
// the stream that was superseded is not: a session has at most one.
func TestAStartedThatComesAfterAnAttachGoesToTheStreamAttachedThenAndToNoOther(t *testing.T) {
	gate := exectest.NewGate()
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		<-gate.Opened()
		p.Started(exec.SessionFacts{PID: 77})
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	first := rig.mustOpen("s1", withoutTTY)
	second := rig.mustAttach("s1", 0)
	if second.Opened().GetFacts().GetPid() != 0 {
		t.Fatal("the attach before the function started carries a pid")
	}
	gate.Open()
	started := readFramesUntil(t, second, "the Started", func(f *execv2.ServerFrame) bool { return f.GetStarted() != nil })
	if got := started[len(started)-1].GetStarted().GetFacts().GetPid(); got != 77 {
		t.Fatalf("the Started carries pid %d, want 77", got)
	}
	// The stream that was superseded gets Detached and no Started.
	for _, f := range drainToEnd(t, first) {
		if f.GetStarted() != nil {
			t.Fatal("the superseded stream was sent the Started that the new stream was sent: a session has at most one")
		}
	}
}

// A replay is a copy of the bytes it describes, taken at the attach: what the ring does while the host takes it cannot take a byte
// out of it. A lossy target whose author goes on writing after the attach overwrites the ring many times over before the host reads
// a frame; the replay is still exactly [start, end), and the loss is reported where it is: in the live stream that follows.
func TestAReplayIsExactEvenWhenALossyRingOverflowsBeforeTheHostReadsIt(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallLossy })
	first := rig.mustOpen("s1", withoutTTY)
	sendDetach(t, first, 0)
	endsWith(t, drainToEnd(t, first))
	rig.grant(t, 600_000) // the ring (1 MiB) holds all of it

	second := rig.mustAttach("s1", 0)
	r := second.Opened().GetReplay()
	if r.GetStart() != 0 || r.GetEnd() != 600_000 {
		t.Fatalf("replay %v, want [0, 600000)", r)
	}
	rig.grant(t, 5<<20) // the host has read nothing: the ring is overwritten five times over

	tr := newTranscript(t)
	tr.attached(second.Opened(), 0)
	for tr.next < r.GetEnd() {
		frame, err := recvFrame(t, second)
		if err != nil {
			t.Fatalf("reading the replay: %v", err)
		}
		if frame.GetDropped() != nil {
			t.Fatalf("a Dropped frame at %d inside the replay [0, %d): a replay that Opened described is exact", tr.next, r.GetEnd())
		}
		tr.take(frame)
	}
	if tr.next != r.GetEnd() || tr.read != r.GetEnd() {
		t.Fatalf("the replay delivered %d bytes ending at %d, want exactly %d", tr.read, tr.next, r.GetEnd())
	}
	// The live stream goes on where the replay ends, and what the ring overwrote meanwhile is reported, to the byte.
	total := uint64(600_000 + 5<<20)
	for tr.have < total {
		frame, err := recvFrame(t, second)
		if err != nil {
			t.Fatalf("reading the live stream at %d of %d: %v", tr.have, total, err)
		}
		tr.take(frame)
	}
	tr.check()
	if tr.read+tr.lost != total || tr.lost == 0 {
		t.Fatalf("read %d and lost %d bytes of %d, want every byte delivered or reported lost, and some lost", tr.read, tr.lost, total)
	}
}

// start_size is the size the bytes at the start of a replay were produced for, and size the size now: the ring remembers every size
// the function took, with the offset it took it at.
func TestTheReplayDescribesTheSizeTheBytesAtItsStartWereProducedForAndTheSizeNow(t *testing.T) {
	grants := make(chan int, 16)
	sizesTaken := make(chan exec.Size, 256)
	var written atomic.Uint64
	rig := newWireRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		go func() {
			for s := range p.Resize {
				sizesTaken <- s
			}
		}()
		var offset uint64
		for {
			select {
			case n := <-grants:
				if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
					return exec.Exit{}, err
				}
				offset += uint64(n)
				written.Store(offset)
			case <-ctx.Done():
				return exec.Exit{}, ctx.Err()
			}
		}
	}))
	conn := rig.mustOpen("s1", withSize(100, 30))
	grant := func(n int) {
		t.Helper()
		want := written.Load() + uint64(n)
		grants <- n
		eventually(t, "the function to write", func() bool { return written.Load() >= want })
	}
	resize := func(seq, cols, rows uint32) {
		t.Helper()
		if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Resize{Resize: &execv2.Resize{Seq: seq, Size: &execv2.Size{Cols: cols, Rows: rows}}}}); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the session's size to be the one sent", func() bool {
			cols2, rows2, ok := exec.PluginCurrentSize(rig.server.Plugin, "s1")
			return ok && uint32(cols2) == cols && uint32(rows2) == rows
		})
	}
	grant(1000) // [0, 1000) at the size the session was opened with
	resize(1, 120, 40)
	grant(1000) // [1000, 2000)
	resize(2, 90, 20)
	grant(1000)                             // [2000, 3000)
	for seq := uint32(3); seq < 60; seq++ { // a storm: the latest wins, and the last is the one in force
		resize(seq, 200+seq, 50)
	}
	grant(1000) // [3000, 4000)

	for _, tc := range []struct {
		from       uint64
		start, now exec.Size
	}{
		{500, exec.Size{Cols: 100, Rows: 30}, exec.Size{Cols: 259, Rows: 50}},
		{1000, exec.Size{Cols: 120, Rows: 40}, exec.Size{Cols: 259, Rows: 50}},
		{1999, exec.Size{Cols: 120, Rows: 40}, exec.Size{Cols: 259, Rows: 50}},
		{2000, exec.Size{Cols: 90, Rows: 20}, exec.Size{Cols: 259, Rows: 50}},
		{3500, exec.Size{Cols: 259, Rows: 50}, exec.Size{Cols: 259, Rows: 50}},
	} {
		r := rig.mustAttach("s1", tc.from).Opened().GetReplay()
		if r.GetStart() != tc.from || r.GetStartSize().GetCols() != uint32(tc.start.Cols) || r.GetStartSize().GetRows() != uint32(tc.start.Rows) ||
			r.GetSize().GetCols() != uint32(tc.now.Cols) || r.GetSize().GetRows() != uint32(tc.now.Rows) {
			t.Errorf("an attach from %d: replay %v, want start_size %v and size %v", tc.from, r, tc.start, tc.now)
		}
	}
}

// A session without a terminal has no size to describe: both are absent, which is unknown and not zero.
func TestTheReplayOfASessionWithoutATerminalHasNoSize(t *testing.T) {
	rig := newWireRig(t, engTarget(echoFunction))
	rig.mustOpen("s1", withoutTTY)
	r := rig.mustAttach("s1", 0).Opened().GetReplay()
	if r.GetStartSize() != nil || r.GetSize() != nil || r.GetStartModes() != nil || r.GetModes() != nil {
		t.Fatalf("replay %v of a session without a terminal: its sizes and modes are absent (not known)", r)
	}
}
