package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/internal/producers"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The stress of the lane over the wire, with an end-state invariant (plan section 4.8): eight workers open sessions of eight kinds of
// command through a real client and server, send input, resize, close gracefully or kill, cancel their context, walk away, and read
// to the end, at random, for lifecycletest.DefaultDuration(). At rest every stream that was opened has ended with exactly one Ended and
// then io.EOF, or with the host's own cancel, and nothing followed Ended; every byte of every session was read or reported dropped;
// the id of a session whose Ended arrived was free at once; a refused open ran nothing; the plugin holds no session (each kind's
// limit is whole again, each id is free); Shutdown returns and refuses what comes after; and no goroutine is left.

const (
	stressWorkers = 8
	stressFlood   = 1 << 20 // more than the 256 KiB of the output queue and the windows of the transport, so that a host that does not read holds the author
	stressLossy   = 2 << 20
	stressOwnIDs  = 16 // ids a worker uses for itself; the sessions of one id follow each other, and the free id of an ended one is checked
	stressShared  = 8  // ids every worker uses, which race
)

// stressKind is a kind of command of the stress.
type stressKind struct {
	name     string
	weight   int
	limit    int // Session.MaxSessions of the kind
	stall    exec.StallBehavior
	ring     int  // Session.ReplayBytes, for a lossy kind
	selfEnds bool // the function returns by itself (when the host reads what it writes)
	// natural holds the end of a session of the kind that nobody asked to end; nil for a kind that only ends when it is asked.
	natural func(*execv2.End) bool
	wrote   int64 // bytes the function writes when it runs to its return, or -1
	fn      func(*stressWorld) exec.ExecFunc
}

// stressWorld is what the kinds share: the gate of the functions that ignore their context, and a count of the calls of each
// kind, which has to equal the opens that were accepted.
type stressWorld struct {
	gate  *exectest.Gate
	calls map[string]*atomic.Int64
	rig   *proofRig
}

func exitedZero(end *execv2.End) bool {
	return end.GetExited() != nil && end.GetExited().GetExitCode() == 0
}

func failedWith(class execv2.ErrorClass, reason execv2.ErrorReason) func(*execv2.End) bool {
	return func(end *execv2.End) bool {
		return end.GetFailed().GetClass() == class && end.GetFailed().GetReason() == reason
	}
}

func stressFlooder(total uint64) func(*stressWorld) exec.ExecFunc {
	return func(*stressWorld) exec.ExecFunc {
		return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			for offset := uint64(0); offset < total; offset += floodChunk {
				if err := ctx.Err(); err != nil {
					return exec.Exit{}, err
				}
				if _, err := p.Stdout.Write(proofChunk(offset, floodChunk)); err != nil {
					return exec.Exit{}, err
				}
			}
			return exec.ExitCode(0), nil
		}
	}
}

var stressKinds = []*stressKind{
	{name: "echo", weight: 16, limit: 12, fn: func(*stressWorld) exec.ExecFunc { return proofEchoFunction }, wrote: -1},
	{name: "flood", weight: 5, limit: 12, selfEnds: true, natural: exitedZero, wrote: stressFlood, fn: stressFlooder(stressFlood)},
	{
		name: "lossy", weight: 5, limit: 12, stall: exec.StallLossy, ring: 16 << 10, selfEnds: true, natural: exitedZero, wrote: stressLossy,
		fn: stressFlooder(stressLossy),
	},
	{name: "exit", weight: 10, limit: 12, selfEnds: true, natural: exitedZero, wrote: 0, fn: func(*stressWorld) exec.ExecFunc {
		return func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }
	}},
	{
		name: "fail", weight: 8, limit: 12, selfEnds: true, wrote: 0,
		natural: failedWith(execv2.ErrorClass_ERROR_CLASS_NOT_FOUND, execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE),
		fn: func(*stressWorld) exec.ExecFunc {
			return func(context.Context, *exec.Proc) (exec.Exit, error) {
				return exec.Exit{}, &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable}
			}
		},
	},
	{
		name: "panic", weight: 5, limit: 12, selfEnds: true, wrote: 24,
		natural: failedWith(execv2.ErrorClass_ERROR_CLASS_INTERNAL, execv2.ErrorReason_ERROR_REASON_UNSPECIFIED),
		fn: func(*stressWorld) exec.ExecFunc {
			return func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
				_, _ = p.Stdout.Write(proofChunk(0, 24))
				panic("a function that panics")
			}
		},
	},
	{name: "block", weight: 12, limit: 12, wrote: 0, fn: func(*stressWorld) exec.ExecFunc { return blockUntilCancelled }},
	{name: "stuck", weight: 3, limit: 4, wrote: 0, fn: func(w *stressWorld) exec.ExecFunc {
		return func(context.Context, *exec.Proc) (exec.Exit, error) { <-w.gate.Opened(); return exec.ExitCode(0), nil }
	}},
}

// targets builds the target of each kind. A session whose label "hold" is set blocks until it is ended, whatever its kind: the
// probe of the end-state fills a kind's limit with them.
func (w *stressWorld) targets() []exec.Target {
	var targets []exec.Target
	for _, k := range stressKinds {
		w.calls[k.name] = new(atomic.Int64)
		fn, calls := k.fn(w), w.calls[k.name]
		targets = append(targets, proofTarget(k.name, func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			calls.Add(1)
			if p.Request.Labels["hold"] != "" {
				<-ctx.Done()
				return exec.Exit{}, ctx.Err()
			}
			return fn(ctx, p)
		}, func(i *exec.TargetInfo) {
			i.Capabilities.Stall = k.stall
			if k.stall == 0 {
				i.Capabilities.Stall = exec.StallPropagates
			}
			i.Session = exec.DefaultSessionLimits()
			i.Session.MaxSessions = k.limit
			if k.ring > 0 {
				i.Session.ReplayBytes = k.ring
			}
		}))
	}
	return targets
}

// stressWorker is one host goroutine.
type stressWorker struct {
	world    *stressWorld
	rng      *rand.Rand
	n        int
	known    map[string]bool // own ids whose last session ended with an Ended: free by contract
	tally    map[string]int
	opened   map[string]int
	problems []string
}

func newStressWorker(world *stressWorld, n int, seed uint64) *stressWorker {
	return &stressWorker{
		world: world, rng: rand.New(rand.NewPCG(seed, uint64(n))), n: n,
		known: map[string]bool{}, tally: map[string]int{}, opened: map[string]int{},
	}
}

func (s *stressWorker) problem(format string, args ...any) {
	if len(s.problems) < 20 {
		s.problems = append(s.problems, fmt.Sprintf(format, args...))
	}
}

func (s *stressWorker) pick() *stressKind {
	total := 0
	for _, k := range stressKinds {
		total += k.weight
	}
	r := s.rng.IntN(total)
	for _, k := range stressKinds {
		if r -= k.weight; r < 0 {
			return k
		}
	}
	return stressKinds[0]
}

// endKey names the arm of an end, for the tally.
func endKey(end *execv2.End) string {
	switch kind := end.GetKind().(type) {
	case *execv2.End_Exited:
		return fmt.Sprintf("exited/%d", kind.Exited.GetExitCode())
	case *execv2.End_Signaled:
		return "signaled"
	case *execv2.End_Closed:
		return "closed/" + strings.TrimPrefix(kind.Closed.GetBy().String(), "CLOSED_BY_")
	case *execv2.End_Failed:
		return "failed/" + strings.TrimPrefix(kind.Failed.GetClass().String(), "ERROR_CLASS_")
	case *execv2.End_Lost:
		return "lost"
	}
	return "unknown"
}

// stressSeq numbers the ids of the sessions the workers open: the id of a session that ended is held by its tombstone, so an id is
// used once (the ids that the workers share are the ones that are refused when they are live or held).
var stressSeq atomic.Int64

// iterate is one session: open, act, end, read, and check. It never blocks for longer than engineWait.
func (s *stressWorker) iterate() {
	kind := s.pick()
	bad := s.rng.IntN(60) == 0
	own := s.rng.IntN(4) != 0
	var id string
	switch {
	case bad:
		id = "bad id\n"
	case own:
		id = fmt.Sprintf("w%d-%d", s.n, stressSeq.Add(1))
	default:
		id = fmt.Sprintf("shared-%d", s.rng.IntN(stressShared))
	}
	tty := s.rng.IntN(2) == 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mods := []func(*execv2.Start){}
	if tty {
		mods = append(mods, proofTTY)
		if s.rng.IntN(2) == 0 {
			mods = append(mods, func(st *execv2.Start) { st.Size = &execv2.Size{Cols: 100, Rows: 30} })
		}
	}
	conn, err := s.world.rig.openKind(ctx, kind.name, id, mods...)
	if err != nil {
		s.refused(err, kind, id, own && !bad, bad)
		return
	}
	s.opened[kind.name]++
	defer func() { // a host that leaves detaches and its session lives for its lease: end what is left of this one
		cancel()
		s.world.reap(id)
	}()
	if got := conn.Opened().GetFacts().GetSessionId(); got != id {
		s.problem("%s: Opened names the session %q", id, got)
	}

	audit := &proofAudit{tty: tty, verify: true, lossy: kind.stall == exec.StallLossy}
	var leaving atomic.Bool
	var result chan error
	startReader := func() {
		result = make(chan error, 1)
		go func() { result <- proofDrain(conn, audit, leaving.Load) }()
	}
	mode := s.rng.IntN(10) // 0-5 read from the start, 6-7 read after the acts, 8-9 never read
	if mode <= 5 {
		startReader()
	}
	var sent uint64
	for range s.rng.IntN(4) {
		if tty && s.rng.IntN(3) == 0 {
			size := &execv2.Size{Cols: uint32(s.rng.IntN(299) + 1), Rows: uint32(s.rng.IntN(99) + 1)} // a malformed size ends the stream
			_ = conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Resize{Resize: &execv2.Resize{Seq: uint32(sent), Size: size}}})
			continue
		}
		n := []int{1, 7, 100, 4096, 30000}[s.rng.IntN(5)]
		if conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: proofChunk(sent, n)}}) == nil {
			sent += uint64(n)
		}
	}
	if mode >= 6 && mode <= 7 {
		startReader()
	}

	closeSent := false
	switch end := s.rng.IntN(10); {
	case kind.selfEnds && mode != 8 && mode != 9 && end < 6:
		// the command ends by itself, as the reader reads it
	case end < 4 && !(kind.name == "stuck" && result != nil):
		closeSent = true
		_ = conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_GRACEFUL}}})
	case end < 7:
		closeSent = true
		_ = conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_KILL}}})
	default:
		leaving.Store(true)
		cancel()
	}
	if kind.selfEnds && !closeSent && !leaving.Load() && result == nil {
		leaving.Store(true) // a command that waits for its reader, and a host that never reads: the host walks away
		cancel()
	}

	var streamErr error
	if result != nil {
		select {
		case streamErr = <-result:
		case <-time.After(engineWait):
			s.problem("%s (%s): the stream did not end within %v", id, kind.name, engineWait)
			leaving.Store(true)
			cancel()
			select {
			case streamErr = <-result:
			case <-time.After(10 * time.Second):
				s.problem("%s (%s): the stream did not end after its host cancelled", id, kind.name)
				return
			}
		}
		s.check(kind, id, audit, streamErr, closeSent, leaving.Load(), sent)
	}
	if own && !bad {
		s.known[id] = result != nil && audit.ended != nil && errors.Is(streamErr, io.EOF)
	}
	if own && !bad && s.known[id] {
		// When Ended is delivered the session's place is free and its id is held by its tombstone: a start of the same id right
		// after is refused as SESSION_EXISTS (or for a full kind), and never admitted.
		again, err := s.world.rig.openKind(context.Background(), kind.name, id, proofLabels("hold", "1"))
		switch {
		case err == nil:
			s.opened[kind.name]++
			_ = again.Close()
			s.problem("%s (%s): the id was admitted after its Ended was read, while its tombstone holds it", id, kind.name)
		case proofRefusal(err) == "CONFLICT/SESSION_EXISTS" || proofRefusal(err) == "LIMIT/TOO_MANY_SESSIONS" || proofRefusal(err) == "UNAVAILABLE/TARGET_WEDGED" && kind.name == "stuck":
		default:
			s.problem("%s (%s): the id was refused with %s right after its Ended was read, want SESSION_EXISTS", id, kind.name, proofRefusal(err))
		}
	}
	if result == nil {
		s.tally[kind.name+" walked away"]++
	}
}

// refused counts the refusal of an open and holds it to what may be refused: an id that is live, a kind that is full and an id
// that is not an id, and never the id of a session whose Ended this worker read.
func (s *stressWorker) refused(err error, kind *stressKind, id string, own, bad bool) {
	name := proofRefusal(err)
	s.tally["refused "+name]++
	switch {
	case bad && strings.HasPrefix(name, "INVALID_REQUEST"):
	case name == "LIMIT/TOO_MANY_SESSIONS":
	case name == "UNAVAILABLE/TARGET_WEDGED" && kind.name == "stuck":
		// the places of the kind are held by functions that were abandoned, until the gate lets them return
	case name == "CONFLICT/SESSION_EXISTS":
		if own && s.known[id] {
			s.problem("%s: refused CONFLICT/SESSION_EXISTS, but the session of that id had ended with an Ended this worker read: its id is free when Ended is delivered", id)
		}
	default:
		s.problem("%s: an open was refused with %s", id, name)
	}
}

// check holds the end of one session that was read to hold to the contract and to what its kind does.
func (s *stressWorker) check(kind *stressKind, id string, audit *proofAudit, streamErr error, closeSent, leaving bool, sent uint64) {
	for _, problem := range audit.problems {
		s.problem("%s (%s): %s", id, kind.name, problem)
	}
	ended := audit.ended
	switch {
	case errors.Is(streamErr, io.EOF):
	case leaving:
		s.tally[kind.name+" stream error after the host left"]++
	default:
		s.problem("%s (%s): the stream ended with %v and the host had not left", id, kind.name, streamErr)
	}
	if ended == nil {
		return
	}
	end := ended.GetEnd()
	s.tally[kind.name+" "+endKey(end)]++
	closedBy := end.GetClosed().GetBy()
	allowed := kind.natural != nil && kind.natural(end) ||
		closeSent && closedBy == execv2.ClosedBy_CLOSED_BY_REQUEST ||
		leaving && closedBy == execv2.ClosedBy_CLOSED_BY_CANCELLED
	if !allowed {
		s.problem("%s (%s): ended %s (close sent: %v, host left: %v)", id, kind.name, endKey(end), closeSent, leaving)
	}
	if kind.wrote >= 0 && ended.GetBytesOut() > uint64(kind.wrote) {
		s.problem("%s (%s): %d bytes out, the function writes %d", id, kind.name, ended.GetBytesOut(), kind.wrote)
	}
	if kind.name == "echo" && ended.GetBytesOut() > sent {
		s.problem("%s (echo): %d bytes echoed of %d sent", id, ended.GetBytesOut(), sent)
	}
	if kind.natural != nil && kind.natural(end) {
		if kind.wrote >= 0 && ended.GetBytesOut() != uint64(kind.wrote) {
			s.problem("%s (%s): ended by itself with %d bytes out, the function writes %d", id, kind.name, ended.GetBytesOut(), kind.wrote)
		}
		if kind.stall != exec.StallLossy && exitedZero(end) && (audit.dropped != 0 || ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE) {
			s.problem("%s (%s): ended by itself with %d bytes dropped and output %v, want none dropped and COMPLETE", id, kind.name, audit.dropped, ended.GetOutput())
		}
	}
}

// stressRun is the world of one stress: a rig, its gate and its workers.
func newStressWorld(t *testing.T) (*stressWorld, lifecycletest.GoroutineBaseline) {
	t.Helper()
	world := &stressWorld{gate: exectest.NewGate(), calls: map[string]*atomic.Int64{}}
	world.rig = newProofRig(t, world.targets()...)
	t.Cleanup(world.gate.Open) // registered after the rig, so it runs before the rig waits for the functions that ignore their context
	// The connection and its goroutines exist before the baseline: one session, echoed, its host left, and the session ended.
	warm := world.rig.mustOpenKind("echo", "warm", proofTTY)
	if _, err := newProofEchoer(warm).measure(3); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_ = warm.Close()
	proofEndDetached(t, world.rig.client, "warm")
	return world, lifecycletest.TakeBaseline()
}

// reap ends the session of an iteration whose host has left, when it is still alive: a stream that is over is an implicit detach
// and a session lives for its lease, so a stress that ended with its hosts leaving would end with sessions running.
func (w *stressWorld) reap(id string) {
	var info *execv2.SessionInfo
	if !proofPoll(engineWait, func() bool {
		info = proofListed(w.rig.client, id)
		return info == nil || info.GetEndedWith() != nil || !info.GetAttached()
	}) || info == nil || info.GetEndedWith() != nil {
		return
	}
	_ = w.rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: id, Mode: execv2.CloseMode_CLOSE_MODE_KILL})
}

// endState is the invariant at rest, once the workers have stopped and every host has left.
func endState(t *testing.T, world *stressWorld, baseline lifecycletest.GoroutineBaseline, opened map[string]int) {
	t.Helper()
	rig := world.rig
	world.gate.Open() // the functions that ignored their context return
	opened["echo"]++  // the warm-up session, which newStressWorld made
	// Every session whose host left is detached and lives for its lease: end what is left, and none is live. Every id that was used
	// is held by its tombstone and is not reusable until it expires.
	if !proofPoll(engineWait, func() bool {
		list, err := rig.client.ListSessions(context.Background())
		if err != nil {
			return false
		}
		live := 0
		for _, info := range list.GetSessions() {
			if info.GetEndedWith() != nil {
				continue
			}
			live++
			if !info.GetAttached() {
				_ = rig.client.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: info.GetFacts().GetSessionId(), Mode: execv2.CloseMode_CLOSE_MODE_KILL})
			}
		}
		return live == 0
	}) {
		t.Errorf("sessions are still live at rest: a session was not ended")
	}
	// Every kind holds no session: its whole limit is free, and the next one over it is refused.
	for _, k := range stressKinds {
		var held []exec.Conn
		for i := range k.limit {
			id := fmt.Sprintf("probe-%s-%d", k.name, i)
			ok := proofPoll(engineWait, func() bool {
				conn, err := rig.openKind(context.Background(), k.name, id, proofLabels("hold", "1"))
				if err == nil {
					opened[k.name]++
					held = append(held, conn)
				}
				return err == nil
			})
			if !ok {
				t.Errorf("kind %q: %d of its %d places are free at rest: a session is still counted", k.name, i, k.limit)
				break
			}
		}
		if conn, err := rig.openKind(context.Background(), k.name, "probe-"+k.name+"-over", proofLabels("hold", "1")); err == nil {
			opened[k.name]++
			held = append(held, conn)
			t.Errorf("kind %q: %d sessions are held and one more was accepted: the limit is %d", k.name, len(held), k.limit)
		} else if name := proofRefusal(err); name != "LIMIT/TOO_MANY_SESSIONS" {
			t.Errorf("kind %q: the open over its limit is refused with %s, want LIMIT/TOO_MANY_SESSIONS", k.name, name)
		}
		for _, conn := range held {
			_ = conn.Close()
		}
	}
	// The plugin's engine stops, returns, and refuses what comes after.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rig.server.Plugin.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if _, err := rig.openKind(context.Background(), "echo", "after-shutdown"); proofRefusal(err) != "UNAVAILABLE" {
		t.Errorf("an open after Shutdown: %v, want UNAVAILABLE", err)
	}
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines at rest: %v", err)
	}
	// A refusal ran nothing: each function was called once for each open that was accepted.
	for _, k := range stressKinds {
		if got, want := world.calls[k.name].Load(), int64(opened[k.name]); got != want {
			t.Errorf("kind %q: its function was called %d times for %d accepted opens", k.name, got, want)
		}
	}
}

// report merges what the workers counted, logs it and fails on what they found.
func stressReport(t *testing.T, workers []*stressWorker, took time.Duration) map[string]int {
	t.Helper()
	tally, opened := map[string]int{}, map[string]int{}
	total := 0
	for _, w := range workers {
		for k, v := range w.tally {
			tally[k] += v
		}
		for k, v := range w.opened {
			opened[k] += v
			total += v
		}
		for _, problem := range w.problems {
			t.Errorf("worker %d: %s", w.n, problem)
		}
	}
	var lines []string
	for k, v := range tally {
		lines = append(lines, fmt.Sprintf("%s: %d", k, v))
	}
	sort.Strings(lines)
	t.Logf("%d sessions opened in %v by %d workers: %v; refusals and ends: %s", total, took.Round(time.Millisecond), len(workers), opened, strings.Join(lines, ", "))
	for _, k := range stressKinds {
		if opened[k.name] == 0 {
			t.Errorf("no session of kind %q was opened: the stress did not exercise it", k.name)
		}
	}
	return opened
}

func TestOpenWireStressEveryStreamEndsOnceAndTheLaneRestsEmpty(t *testing.T) {
	duration := lifecycletest.DefaultDuration()
	world, baseline := newStressWorld(t)
	workers := make([]*stressWorker, stressWorkers)
	for i := range workers {
		workers[i] = newStressWorker(world, i, 1)
	}
	var group producers.Group
	group.Start(len(workers))
	deadline := time.Now().Add(duration)
	start := time.Now()
	for _, w := range workers {
		go func() {
			defer group.Done()
			for time.Now().Before(deadline) {
				w.iterate()
			}
		}()
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), duration+2*engineWait)
	defer cancel()
	if err := group.WaitContext(waitCtx); err != nil {
		t.Fatalf("the workers did not stop: %v", err)
	}
	opened := stressReport(t, workers, time.Since(start))
	endState(t, world, baseline, opened)
}

// A thousand sessions, one after the other, over the wire, of every kind and every way to end, leave nothing: no session, no
// goroutine over the baseline taken before the first.
func TestOpenWireStressAThousandSessionsInSequenceLeaveNoGoroutine(t *testing.T) {
	sessions := proofScaled(1000, 300)
	world, baseline := newStressWorld(t)
	worker := newStressWorker(world, 0, 7)
	start := time.Now()
	for accepted := 0; accepted < sessions; {
		worker.iterate() // a refusal (a bad id, a kind that is full) is not a session
		accepted = 0
		for _, n := range worker.opened {
			accepted += n
		}
	}
	opened := stressReport(t, []*stressWorker{worker}, time.Since(start))
	endState(t, world, baseline, opened)
}
