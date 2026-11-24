package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/lifecycletest"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// 64 sessions on one connection (plan, X3c): every session is its own stream with its own cursor into its own ring, and the plan
// asks that this is MEASURED, with the numbers of one session (open_flood_test.go, open_latency_test.go, open_isolation_test.go):
//
//   - 64 sessions flood at once: the floods run at the same time (the last session to deliver its first frame does so before the first to
//     end has ended, which sessions served one after the other do not), the connection delivers a tenth of what one session's flood
//     delivered through it in the same run (just before the 64 and just after them) or more (no bar is a number of MB/s: it is a ratio
//     against those floods), the frames stay large (an average of 16 KiB or more, so that the host's event count stays low), each
//     session's bytes are the pattern and every one arrives, the process allocates no more for a byte than it does for one session and
//     costs no more than a small multiple of its CPU for a byte, and no session is starved (the slowest finishes within a small
//     multiple of the median);
//   - 64 sessions are typed into at once: the echo of each stays under a floor that a shared runner cannot break (50 ms, five times the
//     floor of the isolation proofs);
//   - 32 sessions flood to hosts that spend 100us on a frame while 32 others are typed into: the echoes of the quiet ones stay under it too;
//   - 32 sessions flood to hosts that read nothing while 32 others are typed into: the floods are held (an author whose host reads
//     nothing is stopped by the queue and the windows of its stream, as in open_isolation_test.go), the echoes of the quiet sessions
//     finish, their p95 is within a small multiple of the p95 of the same number of sessions typed into when nothing floods, and when
//     the hosts read again every byte of every flood arrives. This is the case that tells a lock that every stream's send shares from
//     streams that are independent: the lock is held across the send that waits for the first absent host, and no other send of the
//     connection goes out until that host reads. The slow hosts of the case before do not tell it: a send waits for one of them for the
//     microseconds it spends on a frame, so the p95 of the echoes beside them is 3 to 5 ms under such a lock and 1 to 3 ms without (the
//     floor is 50 ms), and no multiple of the p95 of the echo alone separates the two at every processor count (the echo alone takes
//     9us on one processor and 60us on eighteen, and the one beside the floods 1.2 to 2.8 ms). Nor does the shape of what they
//     measure: the median of those echoes is the lower under the lock, which serializes the floods that load the processors.
//
// The numbers are logged, and the PR reports them. After the run the plugin holds no session, no goroutine and no descriptor that
// it did not hold before.

const concurrentSessions = 64

// concurrentLimits lets one kind hold every session of the proof.
func concurrentLimits(i *exec.TargetInfo) {
	i.Session = exec.DefaultSessionLimits()
	i.Session.MaxSessions = 2 * concurrentSessions
}

// hostResult is what one host goroutine found of its session.
type hostResult struct {
	n     int
	audit proofAudit
	wall  time.Duration
	err   error
	first time.Time // when its first Output frame arrived
}

// floodHost reads one flooding session to its end on its own goroutine, each frame costing the host spin of CPU.
func floodHost(rig *proofRig, kind, id string, total uint64, spin time.Duration, started chan<- struct{}, hold <-chan struct{}, done chan<- *hostResult, n int) {
	res := &hostResult{n: n, audit: proofAudit{verify: true}}
	start := time.Now()
	conn, err := rig.openKind(context.Background(), kind, id, proofLabels("bytes", strconv.FormatUint(total, 10), "n", strconv.Itoa(n)))
	if err != nil {
		res.err = err
		done <- res
		return
	}
	defer func() { _ = conn.Close() }()
	signalled := false
	for {
		frame, err := conn.Recv()
		if err != nil {
			res.err = err
			break
		}
		if res.first.IsZero() && frame.GetOutput() != nil {
			res.first = time.Now()
		}
		if !signalled && res.audit.frames >= 3 && started != nil {
			signalled = true
			started <- struct{}{}
			if hold != nil {
				<-hold // an absent host: it has read what it has and reads nothing more until it is let go
			}
		}
		if frame.GetOutput() != nil && spin > 0 {
			proofSpin(spin)
		}
		if res.audit.take(frame) {
			if _, err := conn.Recv(); errors.Is(err, io.EOF) {
				res.err = nil
			} else {
				res.err = fmt.Errorf("after Ended: %v", err)
			}
			break
		}
	}
	res.wall = time.Since(start)
	if !signalled && started != nil {
		started <- struct{}{}
	}
	done <- res
}

// checkFlood holds one flooding session to the contract and to what the proofs of one session demand of it.
func (r *hostResult) checkFlood(t *testing.T, total uint64) {
	t.Helper()
	if r.err != nil {
		t.Errorf("session %d: %v", r.n, r.err)
		return
	}
	for _, problem := range r.audit.problems {
		t.Errorf("session %d: the stream: %s", r.n, problem)
	}
	ended := r.audit.ended
	switch {
	case ended == nil:
		t.Errorf("session %d: no Ended", r.n)
	case r.audit.read != total || r.audit.dropped != 0 || ended.GetBytesOut() != total || ended.GetLastOffset() != total:
		t.Errorf("session %d: read %d and dropped %d of %d bytes (Ended: out %d, last offset %d): a target that propagates a stalled reader delivers all of it",
			r.n, r.audit.read, r.audit.dropped, total, ended.GetBytesOut(), ended.GetLastOffset())
	case ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE:
		t.Errorf("session %d: Ended.output is %v for a flood that lost nothing, want COMPLETE", r.n, ended.GetOutput())
	}
}

// waitHosts takes the results of n hosts, within the wait bound.
func waitHosts(t *testing.T, done <-chan *hostResult, n int, what string) []*hostResult {
	t.Helper()
	results := make([]*hostResult, 0, n)
	timeout := time.After(2 * engineWait)
	for len(results) < n {
		select {
		case r := <-done:
			results = append(results, r)
		case <-timeout:
			t.Fatalf("%d of %d %s finished within %v", len(results), n, what, 2*engineWait)
		}
	}
	slices.SortFunc(results, func(a, b *hostResult) int { return a.n - b.n })
	return results
}

// spread summarizes the per-session timings of a set of sessions.
func spread(results []*hostResult, pick func(*hostResult) time.Duration) proofLatency {
	samples := make([]time.Duration, 0, len(results))
	for _, r := range results {
		samples = append(samples, pick(r))
	}
	return proofSummarize(samples)
}

// echoHost types into one session, one byte at a time, and times every echo.
type echoResult struct {
	n       int
	samples []time.Duration
	err     error
}

func echoHost(conn exec.Conn, rounds, n int, gate <-chan struct{}, done chan<- *echoResult) {
	res := &echoResult{n: n}
	echo := newProofEchoer(conn)
	if _, err := echo.measure(20); err != nil { // warm-up of the session itself
		res.err = err
		done <- res
		return
	}
	<-gate
	res.samples, res.err = echo.measure(rounds)
	// The host is done: it ends its session and reads to its Ended.
	_ = conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Close{Close: &execv2.Close{Mode: execv2.CloseMode_CLOSE_MODE_GRACEFUL}}})
	for {
		frame, err := conn.Recv()
		if err != nil || frame.GetEnded() != nil {
			break
		}
	}
	_ = conn.Close()
	done <- res
}

func waitEchoes(t *testing.T, done <-chan *echoResult, n int, within time.Duration) []*echoResult {
	t.Helper()
	var out []*echoResult
	timeout := time.After(within)
	for len(out) < n {
		select {
		case r := <-done:
			if r.err != nil {
				t.Errorf("echo session %d: %v", r.n, r.err)
			}
			out = append(out, r)
		case <-timeout:
			t.Fatalf("%d of %d echo sessions finished within %v", len(out), n, within)
		}
	}
	return out
}

// worstEcho is the worst p95 among the sessions, and the distribution of every sample together.
func worstEcho(results []*echoResult) (worst proofLatency, all proofLatency) {
	var every []time.Duration
	for _, r := range results {
		if len(r.samples) == 0 {
			continue
		}
		s := proofSummarize(r.samples)
		if s.P95 > worst.P95 || worst.N == 0 {
			worst = s
		}
		every = append(every, r.samples...)
	}
	return worst, proofSummarize(every)
}

// echoFloor is what a p95 may not exceed: five times the floor of the isolation proofs on every machine, and no less than the clock can
// tell. The echoes of this proof are measured while 64 sessions are typed into at once, and beside 32 hosts that each spend 100us of CPU
// on a frame: they take turns with the transport and with each other for the processors, so what they measure is the scheduler's slice
// as much as the lane (1.2 to 2.4 ms on one to eighteen fast processors, which a runner of two to four slow ones that it shares
// multiplies). What a floor of this size tells is a lock or a consumer that the sessions share: the legacy lane's shared consumer put
// the echo at 431 to 469 ms.
func echoFloor() time.Duration { return proofBar(5 * isolationFloor) }

// typedInto types into n sessions of the echo target at once, rounds bytes into each, and returns what each found. Every session is
// closed by its host when it is done.
func typedInto(t *testing.T, rig *proofRig, prefix string, n, rounds int) []*echoResult {
	t.Helper()
	done := make(chan *echoResult, n)
	gate := make(chan struct{})
	for i := range n {
		go echoHost(rig.mustOpenKind("echo", prefix+strconv.Itoa(i), proofTTY), rounds, i, gate, done)
	}
	close(gate)
	return waitEchoes(t, done, n, 2*engineWait)
}

// heldAuthor is the flood of floodAuthor with a count of what each of its sessions has written (the label n names the session), so
// that a test can tell that every author is held by a host that reads nothing.
type heldAuthor struct {
	written [concurrentSessions / 2]atomic.Uint64
}

func (a *heldAuthor) run(_ context.Context, p *exec.Proc) (exec.Exit, error) {
	total, err := strconv.ParseUint(p.Request.Labels["bytes"], 10, 64)
	if err != nil {
		return exec.Exit{}, err
	}
	n, err := strconv.Atoi(p.Request.Labels["n"])
	if err != nil {
		return exec.Exit{}, err
	}
	for offset := uint64(0); offset < total; {
		size := int(min(uint64(floodChunk), total-offset))
		if _, err := p.Stdout.Write(proofChunk(offset, size)); err != nil {
			return exec.Exit{}, err
		}
		offset += uint64(size)
		a.written[n].Store(offset)
	}
	return exec.ExitCode(0), nil
}

// total is what every session has written.
func (a *heldAuthor) total() uint64 {
	var sum uint64
	for i := range a.written {
		sum += a.written[i].Load()
	}
	return sum
}

const (
	// heldEchoRatio is how many times the p95 of 32 sessions typed into when nothing floods the p95 of the same number of sessions
	// may be beside 32 floods that their hosts hold: a held flood uses no processor and sends nothing, so the echo has no reason to
	// move at all (0.4 to 1.2 times, measured on one to eighteen processors).
	heldEchoRatio = 10
	// heldEchoQuiet is the least that the p95 of the echoes with nothing flooding is taken to be, which the ratio divides by: it is 60 to
	// 400us on a fast machine, and ten times that is a bar of a millisecond or less, which a runner that is slow for the second the held
	// case lasts breaks, whatever the floods do. The bar is ten times this or ten times the p95 of the quiet echoes, the larger, and no
	// less than the clock can tell.
	heldEchoQuiet = 500 * time.Microsecond
	// heldWait bounds the waits of the case that a lock which every stream's send shares turns into a hang: the first held flood
	// keeps the lock for as long as its host reads nothing, and nothing else is sent meanwhile. Without such a lock they take a few
	// hundred milliseconds, and this is how long the test waits to find out that they never will.
	heldWait = 20 * time.Second
)

const (
	// concurrentFlood is what each of the 64 sessions writes, and concurrentFloodReduced what it writes under the race detector or with
	// -short (proofScaled).
	concurrentFlood        = 8 << 20
	concurrentFloodReduced = 1 << 20

	// concurrentFloodShare is the share of one session's flood rate that 64 floods at once must deliver together, on the same
	// connection and in the same run: a ratio, because what a runner delivers is not a number that a test can know. One session's flood
	// already fills the connection, so 64 deliver about what one does: 0.42 to 1.27 times the floods of one session that were run just
	// before them and just after them, in 244 runs on one, two, three, four and eighteen processors, alone on the machine and with
	// five more copies of the test on it. (The floods before them alone gave as little as 0.11 on a machine whose load moved by seconds,
	// and the CI's macOS runner delivered 270 MB/s for the 64, its one flood being unknown: hence the floods on both sides.) A tenth is
	// under a quarter of the lowest ratio that was measured with them. What the bar tells is a
	// collapse of the connection under concurrency (a cost for each frame that grows with the number of sessions, a lock that they
	// queue behind and that has a slow path); it cannot tell sessions that are served one after the other, because one flood fills the
	// connection as well as 64 do. The overlap of the floods does.
	concurrentFloodShare = 0.1
	// concurrentCPUFactor is how many times the CPU for a MB of one session's flood the 64 floods together may cost: 0.9 to 1.9 times
	// measured, on every processor count and loaded or not, and the CPU time of a process does not move with the load of the machine.
	concurrentCPUFactor = 4
	// floodBaselines is how many floods of one session are run before the 64, and as many after them: the baseline is their bytes over
	// their wall times, which is what the connection delivers in the stretch of time that holds the 64.
	floodBaselines = 3
)

// floodBaseline is what floods of one session delivered through the connection of the rig, one after the other, in the conditions of the
// run: the rate of each, their bytes over their wall times, and what they cost in CPU.
type floodBaseline struct {
	rates     []float64     // MB/s of each flood
	delivered uint64        // bytes of all of them
	wall      time.Duration // their wall times together
	cpu       time.Duration // the CPU time of the process meanwhile
	cpuOK     bool          // the platform can say what it was
}

// mbps is the rate of all the floods together, in MB/s; zero when the clock read no time at all.
func (b floodBaseline) mbps() float64 {
	if b.wall <= 0 {
		return 0
	}
	return float64(b.delivered) / 1e6 / b.wall.Seconds()
}

// cpuPerMB is the ms of CPU for each MB; zero when the platform cannot say.
func (b floodBaseline) cpuPerMB() float64 {
	if !b.cpuOK || b.delivered == 0 {
		return 0
	}
	return float64(b.cpu.Microseconds()) / 1e3 / (float64(b.delivered) / 1e6)
}

// plus is the baseline of the floods of b and those of o.
func (b floodBaseline) plus(o floodBaseline) floodBaseline {
	return floodBaseline{rates: slices.Concat(b.rates, o.rates), delivered: b.delivered + o.delivered, wall: b.wall + o.wall, cpu: b.cpu + o.cpu, cpuOK: b.cpuOK && o.cpuOK}
}

// aloneFloods runs floods of one session through the connection of the rig, one after the other.
func aloneFloods(t *testing.T, rig *proofRig, total uint64, runs int, tag string) floodBaseline {
	t.Helper()
	var base floodBaseline
	cpuBefore, cpuOK := proofCPUTime()
	for i := range runs {
		done := make(chan *hostResult, 1)
		floodHost(rig, "flood", "alone-"+tag+strconv.Itoa(i), total, 0, nil, nil, done, 0)
		r := <-done
		r.checkFlood(t, total)
		base.delivered += r.audit.read
		base.wall += r.wall
		if r.wall > 0 {
			base.rates = append(base.rates, float64(r.audit.read)/1e6/r.wall.Seconds())
		}
	}
	if cpuAfter, ok := proofCPUTime(); ok && cpuOK {
		base.cpu, base.cpuOK = cpuAfter-cpuBefore, true
	}
	return base
}

func TestSixtyFourConcurrentSessionsOnOneConnectionKeepTheNumbersOfOne(t *testing.T) {
	var author floodAuthor
	var held heldAuthor
	rig := newProofRig(t,
		proofTarget("flood", author.run, concurrentLimits),
		proofTarget("held", held.run, concurrentLimits),
		proofTarget("echo", proofEchoFunction, concurrentLimits),
	)
	// The connection and its goroutines exist before the baseline: a session of each kind, ended.
	warm := rig.mustOpenKind("echo", "warm", proofTTY)
	if _, err := newProofEchoer(warm).measure(3); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	_ = warm.Close()
	proofEndDetached(t, rig.client, "warm")
	warmFlood := make(chan *hostResult, 1)
	floodHost(rig, "flood", "warm-flood", floodWarmup, 0, nil, nil, warmFlood, 0)
	r := <-warmFlood
	r.checkFlood(t, floodWarmup)
	if t.Failed() {
		t.FailNow()
	}
	baseline := lifecycletest.TakeBaseline()
	fds, fdsOK := proofOpenFDs()

	// ---- 64 sessions flood at once, on one connection.
	perSession := uint64(proofScaled(concurrentFlood, concurrentFloodReduced))
	t.Run("sixty-four floods at once", func(t *testing.T) {
		// The baseline: what one session's flood delivers through this connection, in the conditions of this run: floods of one session
		// just before the 64 and just after them, so that a runner whose load moves by the second has moved about as much for the one as
		// for the other. It is not stated as a number.
		baseSize := uint64(proofScaled(floodTotal, 8<<20))
		beforeBase := aloneFloods(t, rig, baseSize, floodBaselines, "before-")
		done := make(chan *hostResult, concurrentSessions)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		cpuBefore, cpuOK := proofCPUTime()
		start := time.Now()
		for i := range concurrentSessions {
			go floodHost(rig, "flood", "flood-"+strconv.Itoa(i), perSession, 0, nil, nil, done, i)
		}
		results := waitHosts(t, done, concurrentSessions, "flooding sessions")
		wall := time.Since(start)
		runtime.ReadMemStats(&after)
		cpuAfter, ok := proofCPUTime()
		afterBase := aloneFloods(t, rig, baseSize, floodBaselines, "after-")
		alone := beforeBase.plus(afterBase)
		var delivered uint64
		var frames int
		var lastFirst, firstEnd time.Time
		for _, r := range results {
			r.checkFlood(t, perSession)
			delivered += r.audit.read
			frames += r.audit.frames
			if limit := floodMaxFrames(perSession); r.audit.frames > limit {
				t.Errorf("session %d: %d Output frames for %d bytes, the bar of one flood is %d (an average of %d KiB)", r.n, r.audit.frames, perSession, limit, floodMinFrameBytes>>10)
			}
			if r.first.After(lastFirst) {
				lastFirst = r.first
			}
			if end := r.audit.endedAt; !end.IsZero() && (firstEnd.IsZero() || end.Before(firstEnd)) {
				firstEnd = end
			}
		}
		mbps := float64(delivered) / 1e6 / wall.Seconds()
		perByte := float64(after.TotalAlloc-before.TotalAlloc) / float64(delivered)
		walls := spread(results, func(r *hostResult) time.Duration { return r.wall })
		// Starvation: a session that waits for the others to finish before it moves. Every session starts together and writes the same
		// number of bytes, so the slowest finishes within a small multiple of the median.
		starved := walls.Max > 4*walls.P50
		cpuPerMB := 0.0
		if cpuOK && ok {
			cpuPerMB = float64((cpuAfter - cpuBefore).Microseconds()) / 1e3 / (float64(delivered) / 1e6)
		}
		t.Logf("64 floods of %d MiB on one connection: %.0f MB/s delivered (%d MiB in %v), %d Output frames (average %.1f KiB), %.2f B allocated per byte, %.2f ms CPU/MB; each session's wall time: %v",
			perSession>>20, mbps, delivered>>20, wall.Round(time.Millisecond), frames, float64(delivered)/float64(frames)/1024, perByte, cpuPerMB, walls)
		t.Logf("one session's flood through the same connection, just before and just after the 64: %.0f MB/s before, %.0f after, %.0f together (%.0f each), %.2f ms CPU/MB; the 64 together deliver %.2f times it (bar %.2f) at %.2f times its CPU/MB (bar %d); the last session's first frame came at %v and the first session to end ended at %v, of %v in all",
			beforeBase.mbps(), afterBase.mbps(), alone.mbps(), alone.rates, alone.cpuPerMB(), mbps/max(alone.mbps(), 1e-9), concurrentFloodShare, cpuPerMB/max(alone.cpuPerMB(), 1e-9), concurrentCPUFactor,
			lastFirst.Sub(start).Round(time.Microsecond), firstEnd.Sub(start).Round(time.Microsecond), wall.Round(time.Microsecond))
		if !proofRace && alone.mbps() > 0 && mbps < concurrentFloodShare*alone.mbps() {
			t.Errorf("%.0f MB/s delivered by 64 sessions together, %.2f times the %.0f MB/s that one session's flood delivered through the same connection just before and after, the bar is %.2f times it: the connection collapses under concurrent sessions",
				mbps, mbps/alone.mbps(), alone.mbps(), concurrentFloodShare)
		}
		if perByte > floodBytesPerByteBar() {
			t.Errorf("%.2f bytes allocated for each byte delivered, the bar is %.0f", perByte, floodBytesPerByteBar())
		}
		if !proofRace && alone.cpuPerMB() > 0 && cpuPerMB > concurrentCPUFactor*alone.cpuPerMB() {
			t.Errorf("%.2f ms of CPU for each MB with 64 sessions flooding, %.2f times the %.2f ms of one session's flood just before and after, the bar is %d times it",
				cpuPerMB, cpuPerMB/alone.cpuPerMB(), alone.cpuPerMB(), concurrentCPUFactor)
		}
		if starved {
			t.Errorf("the slowest session took %v and the median %v: a session was starved by the others", walls.Max, walls.P50)
		}
		// The floods ran at the same time: each starts at once and none waits for another to end, so by the time the first of them has
		// ended every one of them has delivered output. A connection that served them one after the other, or a few at a time, has the
		// last session deliver its first byte after the first has delivered all of its own. The reduced floods (under the race
		// detector, or with -short) are not asserted: one of them ends in the time that the 64 opens take, which is the skew of the
		// opens and not the connection.
		if perSession >= concurrentFlood && lastFirst.After(firstEnd) {
			t.Errorf("the last session delivered its first frame %v after the first session ended: the floods ran one after the other", lastFirst.Sub(firstEnd).Round(time.Microsecond))
		}
	})

	// ---- 64 sessions are typed into at once.
	rounds := proofScaled(1000, 150)
	t.Run("sixty-four echoes at once", func(t *testing.T) {
		alone := rig.mustOpenKind("echo", "alone", proofTTY)
		single := newProofEchoer(alone)
		if _, err := single.measure(200); err != nil {
			t.Fatalf("warm-up: %v", err)
		}
		baselineEcho, err := single.measure(rounds)
		if err != nil {
			t.Fatalf("the echo alone: %v", err)
		}
		_ = alone.Close()
		proofEndDetached(t, rig.client, "alone")

		results := typedInto(t, rig, "echo-", concurrentSessions, rounds)
		worst, all := worstEcho(results)
		floor := echoFloor()
		t.Logf("echo of one session alone: %v", proofSummarize(baselineEcho))
		t.Logf("echo while 64 sessions are typed into at once: every sample %v; the session with the worst p95: %v (the plan's bar: p95 <= %v for another session's flood; floor %v)", all, worst, isolationBar, floor)
		if worst.P95 > floor {
			t.Errorf("the p95 of the worst session's echo is %v with 64 sessions typed into at once, the floor is %v", worst.P95.Round(time.Microsecond), floor)
		}
	})

	// ---- 32 sessions flood to slow hosts while 32 others are typed into.
	t.Run("thirty-two slow floods and thirty-two echoes", func(t *testing.T) {
		half := concurrentSessions / 2
		floods := make(chan *hostResult, half)
		started := make(chan struct{}, half)
		perFlood := uint64(proofScaled(16<<20, 2<<20))
		for i := range half {
			go floodHost(rig, "flood", "slow-"+strconv.Itoa(i), perFlood, isolationConsumer, started, nil, floods, i)
		}
		for range half {
			select {
			case <-started:
			case <-time.After(engineWait):
				t.Fatalf("the floods did not start")
			}
		}
		done := make(chan *echoResult, half)
		gate := make(chan struct{})
		for i := range half {
			conn := rig.mustOpenKind("echo", "typed-"+strconv.Itoa(i), proofTTY)
			go echoHost(conn, rounds, i, gate, done)
		}
		close(gate)
		results := waitEchoes(t, done, half, 2*engineWait)
		worst, all := worstEcho(results)
		flooded := waitHosts(t, floods, half, "slow flooding sessions")
		var delivered uint64
		for _, r := range flooded {
			r.checkFlood(t, perFlood)
			delivered += r.audit.read
		}
		walls := spread(flooded, func(r *hostResult) time.Duration { return r.wall })
		floor := echoFloor()
		t.Logf("32 floods of %d MiB to hosts that spend %v a frame (%d MiB delivered; wall time of each: %v) while 32 sessions are typed into: every echo %v; the worst session's p95 %v (floor %v)",
			perFlood>>20, isolationConsumer, delivered>>20, walls, all, worst, floor)
		if worst.P95 > floor {
			t.Errorf("the p95 of the worst echo is %v while 32 other sessions flood to slow hosts, the floor is %v", worst.P95.Round(time.Microsecond), floor)
		}
		if walls.Max > 8*walls.P50 {
			t.Errorf("the slowest flood took %v and the median %v: a session was starved", walls.Max, walls.P50)
		}
	})

	// ---- 32 sessions flood to hosts that read nothing while 32 others are typed into.
	t.Run("thirty-two floods held by absent hosts and thirty-two echoes", func(t *testing.T) {
		half := concurrentSessions / 2
		perFlood := uint64(proofScaled(8<<20, 4<<20)) // several times what the queue and the windows of a stream hold (0.6 to 1.6 MiB measured)
		// What 32 sessions cost when they are typed into at once and nothing floods: what the same cost beside the held floods is held to.
		_, quiet := worstEcho(typedInto(t, rig, "quiet-", half, rounds))

		// The sessions that are typed into beside the floods are open before the floods start: what is measured is the echo, and not
		// an Open that has to wait behind them.
		done := make(chan *echoResult, half)
		gate := make(chan struct{})
		for i := range half {
			go echoHost(rig.mustOpenKind("echo", "beside-"+strconv.Itoa(i), proofTTY), rounds, i, gate, done)
		}
		floods := make(chan *hostResult, half)
		started := make(chan struct{}, half)
		release := make(chan struct{})
		t.Cleanup(func() { // a run that failed lets the hosts go, so that nothing waits for them
			for _, c := range []chan struct{}{release, gate} {
				select {
				case <-c:
				default:
					close(c)
				}
			}
		})
		for i := range half {
			go floodHost(rig, "held", "held-"+strconv.Itoa(i), perFlood, 0, started, release, floods, i)
		}
		for got := range half {
			select {
			case <-started:
			case <-time.After(heldWait):
				t.Fatalf("%d of %d floods delivered their first frames within %v while the hosts of the others read nothing: a send of one session waits behind the send of another", got, half, heldWait)
			}
		}
		// An author is held when it writes no more: its host reads nothing, so what the queue and the windows of its stream take is all
		// it gets out.
		stopped := settled(t, "the floods", held.total)
		for i := range half {
			if w := held.written[i].Load(); w == 0 || w >= perFlood {
				t.Errorf("flood %d wrote %d of its %d bytes while its host read nothing: an author is held once the queue and the windows are full", i, w, perFlood)
			}
		}
		close(gate)
		results := waitEchoes(t, done, half, heldWait)
		worst, all := worstEcho(results)
		if got := held.total(); got != stopped {
			t.Errorf("the floods wrote %d more bytes while the echoes were measured: a target that propagates a stalled host must be held", got-stopped)
		}
		quietP95 := proofBaseline(max(quiet.P95, heldEchoQuiet)) // never zero, and never a bar that a slow second of the runner breaks
		ratio := float64(all.P95) / float64(quietP95)
		floor := echoFloor()
		t.Logf("32 floods of %d MiB held by hosts that read nothing (%d KiB written by all of them, %d KiB by each) while 32 sessions are typed into: every echo %v; the worst session's p95 %v (floor %v); the same 32 echo with no flood: %v; the p95 is %.2f times %v (bar %d)",
			perFlood>>20, stopped>>10, stopped>>10/uint64(half), all, worst, floor, quiet, ratio, quietP95.Round(time.Microsecond), heldEchoRatio)
		if worst.P95 > floor {
			t.Errorf("the p95 of the worst echo is %v while 32 floods are held by hosts that read nothing, the floor is %v", worst.P95.Round(time.Microsecond), floor)
		}
		if ratio > heldEchoRatio {
			t.Errorf("the p95 of the echoes is %v beside 32 held floods, %.1f times the %v that the same echoes take with none (%v at least), the bar is %d: held floods use no processor, so an echo that waits for them waits behind a send of another session",
				all.P95.Round(time.Microsecond), ratio, quiet.P95.Round(time.Microsecond), quietP95.Round(time.Microsecond), heldEchoRatio)
		}
		// The hosts read again: what they were held for is not lost.
		close(release)
		flooded := waitHosts(t, floods, half, "held flooding sessions")
		for _, r := range flooded {
			r.checkFlood(t, perFlood)
		}
	})

	// ---- the plugin rests empty: every echo session was closed by its host, and every flood ended by itself.
	plugin := rig.server.Plugin
	if !proofPoll(engineWait, func() bool { return len(exec.PluginLive(plugin)) == 0 }) {
		t.Errorf("the plugin still holds sessions: %v", exec.PluginLive(plugin))
	}
	count, retained := exec.PluginTombstones(plugin)
	t.Logf("the plugin rests with %d tombstones that keep %d KiB of output", count, retained>>10)
	if count > 256 || retained > 32<<20 {
		t.Errorf("the tombstones are %d, keeping %d bytes: the bounds are 256 and 32 MiB", count, retained)
	}
	if err := baseline.Check(10 * time.Second); err != nil {
		t.Errorf("goroutines at rest: %v", err)
	}
	if fdsOK {
		if !proofPoll(10*time.Second, func() bool { now, _ := proofOpenFDs(); return now <= fds }) {
			now, _ := proofOpenFDs()
			t.Errorf("%d descriptors at rest, %d before the sessions", now, fds)
		}
	}
}
