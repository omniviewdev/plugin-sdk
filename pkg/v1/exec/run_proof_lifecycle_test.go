package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The lifecycle proofs of the bounded run (the plan's section X2b): the timeout is kept, 64 runs at once and 1,000 in a row leave nothing
// behind (no goroutine, no descriptor, no command counted), and a short stress that cancels, times out and refuses at every stage ends at
// rest. They run through the stack a host and a plugin really use, on a socket (a loopback socket on Windows), so that they hold on the
// three operating systems of the CI matrix: the descriptors are counted where the platform can say.

// mixKinds are the commands of the proofs, each a target of its own so that each has the limit of its own.
var mixKinds = []string{"exit", "flood", "fail", "panic", "lost", "stdin", "block", "stuck"}

// endsByItself says that a command of the kind returns without being asked: the others need a timeout.
func endsByItself(kind string) bool { return kind != "block" && kind != "stuck" }

// mixWorld is the plugin of the proofs: a target for each kind, the gate the stuck function waits for, and what its functions saw.
type mixWorld struct {
	gate  *exectest.Gate
	calls map[string]*atomic.Int64
	// ready holds the channel of a run that has written and waits, by the token its label "ready" names.
	ready sync.Map
	limit map[string]int
	rig   *proofRig
}

// fn is the function of a kind. A run whose label "hold" is set waits to be ended, whatever its kind: the end-state probes fill the
// places of a target with them.
func (w *mixWorld) fn(kind string) exec.ExecFunc {
	calls := w.calls[kind]
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		calls.Add(1)
		labels := p.Request.Labels
		if labels["hold"] != "" {
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}
		switch kind {
		case "exit":
			_, _ = p.Stdout.Write(proofChunk(0, 100))
			_, _ = p.Stderr.Write(proofChunk(1000, 50))
			return exec.ExitCode(0), nil
		case "flood":
			total, _ := strconv.ParseUint(labels["bytes"], 10, 64)
			for offset := uint64(0); offset < total; {
				if err := ctx.Err(); err != nil {
					return exec.Exit{}, err
				}
				n := int(min(uint64(floodChunk), total-offset))
				if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
					return exec.Exit{}, err
				}
				offset += uint64(n)
			}
			return exec.ExitCode(0), nil
		case "fail":
			_, _ = p.Stdout.Write(proofChunk(0, 10))
			return exec.Exit{}, &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonNoSuchExecutable}
		case "panic":
			_, _ = p.Stdout.Write(proofChunk(0, 5))
			panic("a function that panics")
		case "lost":
			return exec.ExitLost(exec.LostNoStatus), nil
		case "stdin":
			if p.Stdin != nil {
				_, _ = io.Copy(p.Stdout, p.Stdin)
			}
			return exec.ExitCode(0), nil
		}
		// block and stuck: write, say so, and wait. The first honors its context and the second waits for the gate.
		_, _ = p.Stdout.Write(proofChunk(0, 4))
		if token := labels["ready"]; token != "" {
			if ch, ok := w.ready.LoadAndDelete(token); ok {
				close(ch.(chan struct{}))
			}
		}
		if kind == "stuck" {
			<-w.gate.Opened()
			return exec.ExitCode(0), nil
		}
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

// mixRest is what the plugin had before the proof began, to compare with what it has at rest.
type mixRest struct {
	baseline lifecycletest.GoroutineBaseline
	fds      int
	fdsOK    bool
	gauge    float64
}

// newMixWorld serves the plugin and makes the first run, so that the connection and its goroutines exist before the baseline.
func newMixWorld(t *testing.T, limit, stuckLimit int) (*mixWorld, mixRest) {
	t.Helper()
	w := &mixWorld{gate: exectest.NewGate(), calls: map[string]*atomic.Int64{}, limit: map[string]int{}}
	var targets []exec.Target
	for _, kind := range mixKinds {
		w.calls[kind] = new(atomic.Int64)
		w.limit[kind] = limit
		if kind == "stuck" {
			w.limit[kind] = stuckLimit
		}
		max := w.limit[kind]
		targets = append(targets, proofTarget(kind, w.fn(kind), func(i *exec.TargetInfo) { i.Run.MaxConcurrent = max }))
	}
	w.rig = newProofRig(t, targets...)
	t.Cleanup(w.gate.Open) // registered after the rig, so it runs before the rig waits for the functions that ignore their context
	if _, err := w.rig.callRun(context.Background(), w.rig.runReq("exit", "")); err != nil {
		t.Fatalf("the first run: %v", err)
	}
	var rest mixRest
	rest.baseline = lifecycletest.TakeBaseline()
	rest.fds, rest.fdsOK = countDescriptors()
	rest.gauge = proofLiveGauge()
	return w, rest
}

// check holds the plugin at rest to what it had before: the goroutines are back, the descriptors are, and no command is counted.
func (r mixRest) check(t *testing.T, w *mixWorld) {
	t.Helper()
	w.gate.Open() // the functions that ignored their context return
	if err := r.baseline.Check(15 * time.Second); err != nil {
		t.Errorf("goroutines at rest: %v", err)
	}
	if r.fdsOK && !proofPoll(15*time.Second, func() bool { n, _ := countDescriptors(); return n <= r.fds }) {
		n, _ := countDescriptors()
		t.Errorf("%d descriptors at rest, %d before the first run", n, r.fds)
	}
	if !proofPoll(15*time.Second, func() bool { return proofLiveGauge() == r.gauge }) {
		t.Errorf("the gauge of live commands is %v at rest, %v before the first run", proofLiveGauge(), r.gauge)
	}
	n, _ := countDescriptors()
	t.Logf("at rest: goroutines back to the baseline, %d descriptors (%d before, counted: %v), the gauge of live commands %v (%v before)", n, r.fds, r.fdsOK, proofLiveGauge(), r.gauge)
}

// mixRequest is a request for a run of a kind: what its function needs (the bytes to write and the caps of a flood, the stdin of an
// echo, the timeout of a command that does not end by itself). i and rng make it vary.
func mixRequest(w *mixWorld, kind, id string, i int, rng *rand.Rand) *execv2.RunRequest {
	var mods []func(*execv2.RunRequest)
	switch kind {
	case "flood":
		capBytes := []uint32{1, 7, 64, 4096}[rng.IntN(4)]
		policy := execv2.TruncatePolicy(rng.IntN(4))
		mods = append(mods, runLabels("bytes", strconv.Itoa(40_000+rng.IntN(400_000))), runCaps(capBytes, capBytes, policy))
	case "stdin":
		stdin := proofBytes(uint64(i), 1+rng.IntN(2000))
		mods = append(mods, func(r *execv2.RunRequest) { r.Stdin = stdin })
	}
	if !endsByItself(kind) {
		timeout := uint32(10 + rng.IntN(40)) //nolint:gosec // small
		mods = append(mods, func(r *execv2.RunRequest) { r.TimeoutMs = timeout })
	}
	return w.rig.runReq(kind, id, mods...)
}

// mixCheck holds the result of a run of a kind, that nobody cancelled, to what the kind does.
func mixCheck(kind string, req *execv2.RunRequest, res *execv2.RunResult) []string {
	found := runInvariants(res)
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	outcome := res.GetOutcome()
	switch kind {
	case "exit":
		if outcome != execv2.Outcome_OUTCOME_EXITED || res.GetExitCode() != 0 || string(res.GetStdout()) != string(proofChunk(0, 100)) || string(res.GetStderr()) != string(proofChunk(1000, 50)) {
			add("exit: %s, %v, %d and %d bytes: not the exit 0 with 100 and 50 bytes that was written", outcome, res.ExitCode, len(res.GetStdout()), len(res.GetStderr()))
		}
	case "flood":
		total, _ := strconv.ParseUint(runLabel(req, "bytes"), 10, 64)
		limits := req.GetOutput()
		if outcome != execv2.Outcome_OUTCOME_EXITED || res.GetStdoutAccount().GetTotalBytes() != total ||
			string(res.GetStdout()) != string(proofKept(total, int(limits.GetMaxStdoutBytes()), limits.GetPolicy())) {
			add("flood: %s, %d of %d bytes counted, %d returned: not the exit 0 and the capture of what was written", outcome, res.GetStdoutAccount().GetTotalBytes(), total, len(res.GetStdout()))
		}
	case "fail":
		if f := res.GetFailed(); outcome != execv2.Outcome_OUTCOME_FAILED || f.GetClass() != execv2.ErrorClass_ERROR_CLASS_NOT_FOUND ||
			f.GetReason() != execv2.ErrorReason_ERROR_REASON_NO_SUCH_EXECUTABLE || string(res.GetStdout()) != string(proofChunk(0, 10)) {
			add("fail: %s %v with %d bytes: not the failure it returned after 10 bytes", outcome, res.GetFailed(), len(res.GetStdout()))
		}
	case "panic":
		if f := res.GetFailed(); outcome != execv2.Outcome_OUTCOME_FAILED || f.GetClass() != execv2.ErrorClass_ERROR_CLASS_INTERNAL || string(res.GetStdout()) != string(proofChunk(0, 5)) {
			add("panic: %s %v with %d bytes: not an INTERNAL failure after 5 bytes", outcome, res.GetFailed(), len(res.GetStdout()))
		}
	case "lost":
		if outcome != execv2.Outcome_OUTCOME_LOST || res.RemoteMaySurvive == nil || !*res.RemoteMaySurvive {
			add("lost: %s and survival %v: not a lost run that may still run", outcome, res.RemoteMaySurvive)
		}
	case "stdin":
		if outcome != execv2.Outcome_OUTCOME_EXITED || string(res.GetStdout()) != string(req.GetStdin()) {
			add("stdin: %s and %d bytes echoed of %d sent", outcome, len(res.GetStdout()), len(req.GetStdin()))
		}
	default:
		if outcome != execv2.Outcome_OUTCOME_TIMED_OUT || res.GetExitSource() != execv2.ExitSource_EXIT_SOURCE_HOST || string(res.GetStdout()) != string(proofChunk(0, 4)) {
			add("%s: %s from %s with %d bytes: not a timeout of the host after 4 bytes", kind, outcome, res.GetExitSource(), len(res.GetStdout()))
		}
	}
	return found
}

func runLabel(req *execv2.RunRequest, name string) string {
	for _, l := range req.GetLabels() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

// streamCheck reads a RunStream of a kind that ends by itself to its end and holds it to what the kind does: RunEnded is last and once,
// the bytes delivered are the first bytes of each stream up to its cap, and the accounts count every byte.
func streamCheck(ctx context.Context, w *mixWorld, kind string, req *execv2.RunRequest) []string {
	var found []string
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	conn, err := w.rig.client.RunStream(w.rig.request(ctx), req)
	if err != nil {
		return []string{"RunStream: " + err.Error()}
	}
	defer func() { _ = conn.Close() }()
	var stdout, stderr []byte
	var ended *execv2.RunEnded
	for {
		event, err := conn.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return append(found, "RunStream: "+err.Error())
		}
		if ended != nil {
			add("an event after RunEnded")
		}
		switch e := event.GetEvent().(type) {
		case *execv2.RunEvent_Output:
			if e.Output.GetStream() == execv2.Stream_STREAM_STDERR {
				stderr = append(stderr, e.Output.GetData()...)
			} else {
				stdout = append(stdout, e.Output.GetData()...)
			}
		case *execv2.RunEvent_Ended:
			ended = e.Ended
		}
	}
	if ended == nil {
		return append(found, "RunStream: the stream ended with no RunEnded")
	}
	res := ended.GetResult()
	for _, a := range []*execv2.OutputAccount{res.GetStdoutAccount(), res.GetStderrAccount()} {
		if a.GetKeptBytes()+a.GetDroppedBytes() != a.GetTotalBytes() {
			add("RunEnded: kept %d plus dropped %d is not the total %d", a.GetKeptBytes(), a.GetDroppedBytes(), a.GetTotalBytes())
		}
	}
	if len(res.GetStdout()) != 0 || len(res.GetStderr()) != 0 {
		add("RunEnded carries payloads")
	}
	switch kind {
	case "exit":
		if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || string(stdout) != string(proofChunk(0, 100)) || string(stderr) != string(proofChunk(1000, 50)) {
			add("RunStream of exit: %s, %d and %d bytes delivered", res.GetOutcome(), len(stdout), len(stderr))
		}
	case "flood":
		total, _ := strconv.ParseUint(runLabel(req, "bytes"), 10, 64)
		capBytes := min(total, uint64(req.GetOutput().GetMaxStdoutBytes()))
		if res.GetStdoutAccount().GetTotalBytes() != total || string(stdout) != string(proofBytes(0, int(capBytes))) {
			add("RunStream of flood: %d of %d bytes counted, %d delivered: want the first %d", res.GetStdoutAccount().GetTotalBytes(), total, len(stdout), capBytes)
		}
	}
	return found
}

// ---------------------------------------------------------------------------------------------------------------
// the timeout
// ---------------------------------------------------------------------------------------------------------------

// A run that outlasts its timeout returns within the timeout and 100 ms, whatever its function does: one that honors its cancelled
// context, one that writes without end, and one that ignores its context altogether (the call does not wait for it: it is given up on,
// and keeps its place until it returns). The bound is asserted on the worst of five runs, the best of three batches, because the
// machine is shared; under the race detector the slack is a second. The result is TIMED_OUT, from the host, with the output written.
func TestARunThatOutlastsItsTimeoutReturnsWithinTheTimeoutAndHundredMilliseconds(t *testing.T) {
	const timeout = 200 * time.Millisecond
	slack := 100 * time.Millisecond
	if proofRace {
		slack = time.Second
	}
	w, _ := newMixWorld(t, 32, 32)
	for _, kind := range []string{"block", "flood", "stuck"} {
		var worst time.Duration
		var ok bool
		attempts := 0
		for attempts < 3 && !ok {
			attempts++
			worst = 0
			for range 5 {
				mods := []func(*execv2.RunRequest){func(r *execv2.RunRequest) { r.TimeoutMs = uint32(timeout.Milliseconds()) }} //nolint:gosec // small
				if kind == "flood" {
					mods = append(mods, runLabels("bytes", strconv.Itoa(1<<40)))
				}
				began := time.Now()
				res, err := w.rig.callRun(context.Background(), w.rig.runReq(kind, "", mods...))
				took := time.Since(began)
				if err != nil {
					t.Fatalf("%s: the run was not answered with a result: %v", kind, err)
				}
				for _, problem := range runInvariants(res) {
					t.Errorf("%s: %s", kind, problem)
				}
				if res.GetOutcome() != execv2.Outcome_OUTCOME_TIMED_OUT || res.GetExitSource() != execv2.ExitSource_EXIT_SOURCE_HOST || len(res.GetStdout()) == 0 ||
					time.Duration(res.GetDurationMs())*time.Millisecond < timeout-50*time.Millisecond {
					t.Errorf("%s: the run ended %s from %s after %d ms with %d bytes, want a timeout of the host at %v with the output written", kind, res.GetOutcome(), res.GetExitSource(), res.GetDurationMs(), len(res.GetStdout()), timeout)
				}
				worst = max(worst, took-timeout)
			}
			ok = worst <= slack
		}
		t.Logf("%s: the worst of 5 calls returned %v after its timeout of %v (the bar is %v), batch %d of at most 3", kind, worst.Round(time.Millisecond), timeout, slack, attempts)
		if !ok {
			t.Errorf("%s: a call returned %v after its timeout of %v in each of %d batches, the bar is %v", kind, worst, timeout, attempts, slack)
		}
		if kind == "stuck" {
			if got, want := w.calls["stuck"].Load(), int64(5*attempts); got != want {
				t.Errorf("stuck: %d functions were called for %d runs", got, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 64 at once and 1,000 in a row
// ---------------------------------------------------------------------------------------------------------------

// Sixty-four runs at once, each of which the plugin holds until all of them are running, are all answered, each with its own exit and its
// own bytes, and leave nothing behind: no goroutine, no descriptor, no command counted.
func TestSixtyFourRunsAtOnceLeaveNoGoroutineAndNoDescriptor(t *testing.T) {
	const n = 64
	var running, peak atomic.Int64
	gate := exectest.NewGate()
	fn := func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Request.Labels["warm"] != "" {
			return exec.ExitCode(0), nil
		}
		idx, _ := strconv.Atoi(p.Request.Labels["n"])
		cur := running.Add(1)
		defer running.Add(-1)
		for old := peak.Load(); cur > old && !peak.CompareAndSwap(old, cur); old = peak.Load() {
		}
		select {
		case <-gate.Opened():
		case <-ctx.Done():
			return exec.Exit{}, ctx.Err()
		}
		_, _ = p.Stdout.Write(proofChunk(uint64(idx)*100, 100))
		return exec.ExitCode(idx % 256), nil
	}
	rig := newProofRig(t, proofTarget("many", fn, func(i *exec.TargetInfo) { i.Run.MaxConcurrent = n }))
	t.Cleanup(gate.Open)
	if _, err := rig.callRun(context.Background(), rig.runReq("many", "", runLabels("warm", "1"))); err != nil {
		t.Fatalf("the first run: %v", err)
	}
	rest := mixRest{baseline: lifecycletest.TakeBaseline(), gauge: proofLiveGauge()}
	rest.fds, rest.fdsOK = countDescriptors()

	type answer struct {
		res *execv2.RunResult
		err error
	}
	answers := make(chan answer, n)
	for i := range n {
		go func() {
			res, err := rig.callRun(context.Background(), rig.runReq("many", "", runLabels("n", strconv.Itoa(i))))
			answers <- answer{res, err}
		}()
	}
	if !proofPoll(engineWait, func() bool { return running.Load() == n }) {
		t.Fatalf("%d of %d functions are running: the target does not run %d runs at once", running.Load(), n, n)
	}
	gate.Open()
	seen := map[int]bool{}
	for range n {
		select {
		case a := <-answers:
			if a.err != nil {
				t.Errorf("a run was refused or broke: %v", a.err)
				continue
			}
			for _, problem := range runInvariants(a.res) {
				t.Errorf("a result: %s", problem)
			}
			// Its bytes are the pattern at its own offset, so the index of the run is read from them and checked against its exit.
			code := int(a.res.GetExitCode())
			idx := -1
			for i := code; i < n; i += 256 {
				if string(a.res.GetStdout()) == string(proofChunk(uint64(i)*100, 100)) {
					idx = i
				}
			}
			if a.res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || idx < 0 || seen[idx] {
				t.Errorf("a result of %s with exit %d and %d bytes is no run of the 64, or a run answered twice", a.res.GetOutcome(), code, len(a.res.GetStdout()))
			}
			seen[idx] = true
		case <-time.After(engineWait):
			t.Fatalf("%d of %d runs were answered within %v", len(seen), n, engineWait)
		}
	}
	if got := peak.Load(); got != n {
		t.Errorf("at most %d functions ran at once, want %d", got, n)
	}
	t.Logf("%d runs at once: %d answered, each with its own exit and bytes; %d functions ran at the same time", n, len(seen), peak.Load())
	rest.check(t, &mixWorld{gate: gate})
}

// A thousand runs in a row, of every kind of command and every way to end, over the wire, some of them as streams, leave nothing behind:
// no goroutine over the baseline taken after the first, no descriptor, no command counted. Each result is exact.
func TestAThousandRunsInARowLeaveNoGoroutineAndNoDescriptor(t *testing.T) {
	n := proofScaled(1000, 300)
	w, rest := newMixWorld(t, 12, 4)
	rng := rand.New(rand.NewPCG(7, 1))
	tally := map[string]int{}
	began := time.Now()
	for i := range n {
		kind := mixKinds[i%(len(mixKinds)-1)] // every kind but the stuck one
		req := mixRequest(w, kind, "", i, rng)
		res, err := w.rig.callRun(context.Background(), req)
		if err != nil {
			t.Fatalf("run %d (%s) was refused or broke: %v", i, kind, err)
		}
		for _, problem := range mixCheck(kind, req, res) {
			t.Errorf("run %d: %s", i, problem)
		}
		tally[kind+" "+res.GetOutcome().String()]++
		if i%10 == 0 && (kind == "exit" || kind == "flood") {
			for _, problem := range streamCheck(context.Background(), w, kind, req) {
				t.Errorf("run %d: %s", i, problem)
			}
			tally["streamed "+kind]++
		}
		if t.Failed() {
			break
		}
	}
	t.Logf("%d runs in %v: %v", n, time.Since(began).Round(time.Millisecond), tally)
	rest.check(t, w)
}

// ---------------------------------------------------------------------------------------------------------------
// the stress: cancel, timeout and refusal at every stage
// ---------------------------------------------------------------------------------------------------------------

// mixWorker is one host goroutine of the stress.
type mixWorker struct {
	w        *mixWorld
	rng      *rand.Rand
	n        int
	tally    map[string]int
	results  map[string]int // results received, by kind
	maybe    map[string]int // calls that ended with no result and whose function may have run, by kind
	ids      map[string]bool
	problems []string
}

func (m *mixWorker) problem(format string, args ...any) {
	if len(m.problems) < 20 {
		m.problems = append(m.problems, fmt.Sprintf(format, args...))
	}
}

// refusalOK says whether a refusal is one that a run may meet at any time: its target is full, its id is taken, or its places are held by
// functions that were given up on.
func refusalOK(name string) bool {
	return name == "LIMIT/TOO_MANY_RUNS" || name == "CONFLICT/SESSION_EXISTS" || strings.HasPrefix(name, "UNAVAILABLE")
}

// iterate is one run, at one stage: it picks a command, a way to be asked and a way to be ended, and holds what comes back to what that
// allows. It never waits for longer than engineWait.
func (m *mixWorker) iterate(i int) {
	kind := mixKinds[m.rng.IntN(len(mixKinds))]
	id := ""
	switch m.rng.IntN(4) {
	case 0:
		id = fmt.Sprintf("w%d-%d", m.n, m.rng.IntN(8))
	case 1:
		id = fmt.Sprintf("shared-%d", m.rng.IntN(4))
	}
	if id != "" {
		m.ids[id] = true
	}
	req := mixRequest(m.w, kind, id, i, m.rng)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mode := m.rng.IntN(12)
	switch {
	case mode == 4: // a request that breaks a bound: refused, and nothing runs
		bad := []func(*execv2.RunRequest){
			func(r *execv2.RunRequest) { r.Argv = nil },
			func(r *execv2.RunRequest) { r.TimeoutMs = 1 << 30 },
			func(r *execv2.RunRequest) { r.Output = &execv2.OutputLimits{MaxStdoutBytes: 1 << 30} },
			func(r *execv2.RunRequest) { r.Stdin = make([]byte, 1<<20+1) },
		}
		bad[m.rng.IntN(len(bad))](req)
		res, err := m.w.rig.callRun(ctx, req)
		if res != nil || err == nil || !strings.HasPrefix(proofRefusal(err), "INVALID_REQUEST") && proofRefusal(err) != "LIMIT/INPUT_TOO_LARGE" {
			m.problem("run %d: a request that breaks a bound was answered %v and %v", i, res.GetOutcome(), err)
		}
		m.tally["refused"]++
		return
	case mode == 5: // cancelled before the call: the call returns the cancel at once
		cancel()
		if _, err := m.w.rig.callRun(ctx, req); err == nil {
			m.problem("run %d: a call that was cancelled before it began returned a result", i)
		}
		m.maybe[kind]++
		m.tally["cancelled before"]++
		return
	case mode == 6 && !endsByItself(kind): // cancelled once its function has written and waits
		token := fmt.Sprintf("w%d-%d", m.n, i)
		ch := make(chan struct{})
		m.w.ready.Store(token, ch)
		runLabels("ready", token)(req)
		req.TimeoutMs = 0
		done := make(chan error, 1)
		go func() { _, err := m.w.rig.callRun(ctx, req); done <- err }()
		select {
		case <-ch:
		case err := <-done: // refused before it began, or answered
			m.w.ready.Delete(token)
			m.accept(kind, nil, err, i)
			return
		case <-time.After(engineWait):
			m.problem("run %d (%s): its function did not start within %v", i, kind, engineWait)
		}
		cancel()
		select {
		case err := <-done:
			m.cancelled(kind, err, i)
		case <-time.After(engineWait):
			m.problem("run %d (%s): cancelling its call did not end it within %v", i, kind, engineWait)
		}
		return
	case mode == 7: // cancelled at a random moment: before the function ran, while it runs, or after it ended
		delay := time.Duration(m.rng.IntN(3000)) * time.Microsecond
		timer := time.AfterFunc(delay, cancel)
		defer timer.Stop()
		if !endsByItself(kind) {
			req.TimeoutMs = 0
		}
		res, err := m.w.rig.callRun(ctx, req)
		if err == nil {
			// The run ended before the cancel landed: a result of the kind, or of a run that the cancel reached first.
			m.results[kind]++
			if endsByItself(kind) {
				m.checkResult(kind, req, res, i)
			}
			return
		}
		m.cancelled(kind, err, i)
		return
	case mode == 8, mode == 9, mode == 10: // a stream: read to its end, closed after its first event, cancelled after its first event
		if !endsByItself(kind) {
			m.tally["skipped stream"]++
			return
		}
		m.stream(ctx, cancel, kind, req, mode, i)
		return
	}
	// the run as asked: with its timeout when it does not end by itself
	res, err := m.w.rig.callRun(ctx, req)
	m.accept(kind, res, err, i)
	if err == nil && endsByItself(kind) {
		m.checkResult(kind, req, res, i)
	} else if err == nil {
		for _, problem := range mixCheck(kind, req, res) {
			m.problem("run %d: %s", i, problem)
		}
	}
}

// accept counts the answer of a call that nobody cancelled: a result, or a refusal that a run may meet.
func (m *mixWorker) accept(kind string, res *execv2.RunResult, err error, i int) {
	switch {
	case err == nil:
		m.results[kind]++
		m.tally[kind+" "+res.GetOutcome().String()]++
	case refusalOK(proofRefusal(err)):
		m.tally["refused "+proofRefusal(err)]++
	default:
		m.problem("run %d (%s): the call ended with %v: a run that nobody cancelled is a result or a refusal", i, kind, err)
	}
}

func (m *mixWorker) checkResult(kind string, req *execv2.RunRequest, res *execv2.RunResult, i int) {
	for _, problem := range mixCheck(kind, req, res) {
		m.problem("run %d: %s", i, problem)
	}
}

// cancelled holds the answer of a call that was cancelled: the cancel, typed, or a result when the run ended first.
func (m *mixWorker) cancelled(kind string, err error, i int) {
	var typed *exec.Error
	switch {
	case errors.As(err, &typed) && typed.Class == exec.ErrorClassCancelled && errors.Is(err, context.Canceled):
		m.maybe[kind]++
		m.tally["cancelled"]++
	case err != nil && refusalOK(proofRefusal(err)):
		m.tally["refused "+proofRefusal(err)]++
	default:
		m.problem("run %d (%s): a call that was cancelled ended with %v, want the cancel or a refusal", i, kind, err)
	}
}

// stream runs a RunStream of a kind that ends by itself, and ends it as the mode says.
func (m *mixWorker) stream(ctx context.Context, cancel context.CancelFunc, kind string, req *execv2.RunRequest, mode, i int) {
	if mode == 8 {
		problems := streamCheck(ctx, m.w, kind, req)
		if len(problems) == 1 && strings.Contains(problems[0], "RunStream: ") && refusalOK(strings.TrimPrefix(problems[0], "RunStream: ")) {
			m.tally["refused stream"]++
			return
		}
		for _, problem := range problems {
			if strings.HasPrefix(problem, "RunStream: exec: ") {
				m.tally["refused stream"]++
				return
			}
			m.problem("run %d: %s", i, problem)
		}
		m.results[kind]++
		m.tally["streamed "+kind]++
		return
	}
	conn, err := m.w.rig.client.RunStream(m.w.rig.request(ctx), req)
	if err != nil {
		m.accept(kind, nil, err, i)
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Recv(); err != nil && !errors.Is(err, io.EOF) {
		m.problem("run %d: the first event of a stream: %v", i, err)
	}
	m.maybe[kind]++
	if mode == 10 {
		cancel()
		m.tally["stream cancelled"]++
	} else {
		m.tally["stream closed"]++
	}
}

// probe fills the places of a kind with runs that wait, checks that the one over is refused, and ends them: the plugin holds no command,
// whatever the stress did.
func (w *mixWorld) probe(t *testing.T, kind string) {
	t.Helper()
	limit := w.limit[kind]
	before := w.calls[kind].Load()
	var cancels []context.CancelFunc
	done := make(chan error, limit)
	for i := range limit {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		go func() {
			_, err := w.rig.callRun(ctx, w.rig.runReq(kind, "", runLabels("hold", "1")))
			done <- err
		}()
		_ = i
	}
	if !proofPoll(engineWait, func() bool { return w.calls[kind].Load()-before == int64(limit) }) {
		t.Errorf("kind %q: %d of its %d places are free at rest: a command is still counted", kind, w.calls[kind].Load()-before, limit)
	} else if res, err := w.rig.callRun(context.Background(), w.rig.runReq(kind, "", runLabels("hold", "1"))); res != nil || proofRefusal(err) != "LIMIT/TOO_MANY_RUNS" {
		t.Errorf("kind %q: the run over its limit of %d was answered %v and %v, want LIMIT/TOO_MANY_RUNS", kind, limit, res.GetOutcome(), err)
	}
	for _, cancel := range cancels {
		cancel()
	}
	for range limit {
		select {
		case <-done:
		case <-time.After(engineWait):
			t.Errorf("kind %q: a run that was cancelled did not return within %v", kind, engineWait)
		}
	}
}

// A short stress with an end-state invariant: eight workers run commands of eight kinds through a real client and server, cancelling
// before the call, once the function waits and at a random moment, timing out, being refused for a bound, reading streams to their end,
// closing them and cancelling them, for lifecycletest.DefaultDuration(). Every call ends with a result of what its kind does, or with
// the cancel that was asked, or with a refusal that a run may meet; and at rest no function ran for a refused request, every id that was
// used is free, every kind has its whole limit, the plugin's shutdown returns and refuses what follows, no goroutine and no descriptor
// is left, and no command is counted.
func TestRunStressCancelTimeoutAndRefusalAtEveryStageEndsAtRest(t *testing.T) {
	duration := lifecycletest.DefaultDuration()
	w, rest := newMixWorld(t, 12, 4)
	const workers = 8
	ws := make([]*mixWorker, workers)
	var wg sync.WaitGroup
	deadline := time.Now().Add(duration)
	began := time.Now()
	for k := range ws {
		ws[k] = &mixWorker{w: w, rng: rand.New(rand.NewPCG(1, uint64(k))), n: k, tally: map[string]int{}, results: map[string]int{}, maybe: map[string]int{}, ids: map[string]bool{}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				ws[k].iterate(i)
			}
		}()
	}
	wg.Wait()

	tally, results, maybe, ids := map[string]int{}, map[string]int{}, map[string]int{}, map[string]bool{}
	for _, m := range ws {
		for _, p := range m.problems {
			t.Errorf("worker %d: %s", m.n, p)
		}
		for k, v := range m.tally {
			tally[k] += v
		}
		for k, v := range m.results {
			results[k] += v
		}
		for k, v := range m.maybe {
			maybe[k] += v
		}
		for id := range m.ids {
			ids[id] = true
		}
	}
	t.Logf("%d workers for %v: %v", workers, time.Since(began).Round(time.Millisecond), tally)
	if len(tally) < 8 {
		t.Errorf("the stress exercised %d kinds of outcome: %v", len(tally), tally)
	}
	w.gate.Open() // the stuck functions return
	// A refused request ran nothing: every function that ran belongs to a run that was answered or whose caller left.
	for _, kind := range mixKinds {
		if !proofPoll(engineWait, func() bool { return w.calls[kind].Load() >= int64(results[kind]) }) {
			t.Errorf("kind %q: %d functions ran for %d results", kind, w.calls[kind].Load(), results[kind])
		}
		if calls := w.calls[kind].Load(); calls > int64(results[kind]+maybe[kind]) {
			t.Errorf("kind %q: %d functions ran for %d results and %d calls that ended with no result: a refused request ran its function", kind, calls, results[kind], maybe[kind])
		}
	}
	// Every id that was used is free.
	for id := range ids {
		if !proofPoll(engineWait, func() bool {
			res, err := w.rig.callRun(context.Background(), w.rig.runReq("exit", id))
			return err == nil && res.GetOutcome() == execv2.Outcome_OUTCOME_EXITED
		}) {
			t.Errorf("the id %q is still held at rest", id)
		}
	}
	// Every kind has its whole limit.
	for _, kind := range mixKinds {
		w.probe(t, kind)
	}
	// The plugin stops, returns and refuses what comes after.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := w.rig.server.Plugin.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if res, err := w.rig.callRun(context.Background(), w.rig.runReq("exit", "after-shutdown")); res != nil || proofRefusal(err) != "UNAVAILABLE" {
		t.Errorf("a run after Shutdown: %v and %v, want UNAVAILABLE", res.GetOutcome(), err)
	}
	rest.check(t, w)
}
