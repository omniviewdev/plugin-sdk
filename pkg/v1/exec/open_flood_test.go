package exec_test

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The flood of the plan (section 3.6): an author writes 64 MiB into the session as fast as it can, and the host reads every
// frame, through the stack a host and a plugin really use (a gRPC server over a unix socket behind the SDK's middleware, a real
// client). The legacy lane moved 10 to 35 MB/s on macOS, in 65,500 frames, and allocated about 1,000 bytes for each byte it
// moved; the proofs hold this lane to the numbers below, and the tests log what they measured, which the PR reports.
const (
	// floodChunk is what the author writes at a time: the size of a copy loop's buffer.
	floodChunk = 32 << 10
	// floodTotal is the flood of the plan.
	floodTotal = 64 << 20
	// floodWarmup is the flood that goes through the connection first, so that its set-up, its buffers and the pools are not
	// part of what is measured.
	floodWarmup = 4 << 20

	// The bars of section 3.6.
	//
	// floodPlanMBps is the plan's bar for the delivered MB/s, SDK and gRPC, unix socket: what a machine that nothing else uses delivers
	// (700 to 1,300 MB/s on the development machine). It is logged, with whether the run met it, and it is the number that the attempts
	// of the proof go on until they have met; it is not asserted, because a shared runner is not that machine: the connection of the
	// macOS runner that CI uses carried 270 MB/s for 64 sessions together (open_concurrent_sessions_test.go), so 300 for one session is
	// a number that its load decides.
	floodPlanMBps = 300
	// floodMinMBps is what is asserted, at the best of three, and not under the race detector: a third of the plan's bar, and three
	// times what the legacy lane moved at best (35 MB/s). A lane that fell back to the legacy lane's way of moving bytes (a frame for
	// each write, a copy for each hop) would fall under it on any runner, and a runner would have to be slower than the CI's by a
	// factor to bring this lane under it.
	floodMinMBps       = 100
	floodMinFrameBytes = 16 << 10
	floodMaxBytesPerB  = 4.0  // bytes allocated for each byte moved
	floodMaxCPUMsPerMB = 15.0 // CPU milliseconds for each MB moved; not asserted where the platform cannot say
)

// floodMaxFrames is the most Output frames the bar allows for a flood of total bytes: an average frame of at least 16 KiB,
// which for 64 MiB is 4,100 with the slack of four frames.
func floodMaxFrames(total uint64) int { return int(total/floodMinFrameBytes) + 4 }

// floodAuthor is the author's function of the flood, and what it saw of its own run.
type floodAuthor struct {
	from, to atomic.Int64 // when the last run started and returned, in Unix nanoseconds
	longest  atomic.Int64 // the longest single Write of the last run, in nanoseconds
}

// run writes the number of bytes its label "bytes" names, in writes of floodChunk bytes of the pattern, as fast as the session
// takes them, and ends with exit 0. A write that fails because the session ended ends the run.
func (f *floodAuthor) run(_ context.Context, p *exec.Proc) (exec.Exit, error) {
	total, err := strconv.ParseUint(p.Request.Labels["bytes"], 10, 64)
	if err != nil {
		return exec.Exit{}, err
	}
	f.from.Store(time.Now().UnixNano())
	defer func() { f.to.Store(time.Now().UnixNano()) }()
	var longest time.Duration
	for offset := uint64(0); offset < total; {
		n := int(min(uint64(floodChunk), total-offset))
		start := time.Now()
		if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
			return exec.Exit{}, err
		}
		longest = max(longest, time.Since(start))
		offset += uint64(n)
	}
	f.longest.Store(int64(longest))
	return exec.ExitCode(0), nil
}

// floodRig serves a target of the flood, with a flood already through its connection.
type floodRig struct {
	*proofRig
	author *floodAuthor
	stall  exec.StallBehavior
	runs   int
}

func newFloodRig(tb testing.TB, stall exec.StallBehavior) *floodRig {
	tb.Helper()
	author := &floodAuthor{}
	rig := newProofRig(tb, proofTarget("flood", author.run, func(i *exec.TargetInfo) { i.Capabilities.Stall = stall }))
	f := &floodRig{proofRig: rig, author: author, stall: stall}
	if res := f.flood(tb, floodWarmup, true, false); len(res.audit.problems) > 0 {
		tb.Fatalf("the warm-up flood broke the contract of the stream: %v", res.audit.problems)
	}
	return f
}

// floodRun is one flood and what it cost.
type floodRun struct {
	total        uint64
	audit        proofAudit
	wall         time.Duration // from the open to the Ended
	alloc        uint64        // bytes the whole process allocated meanwhile: the client and the server are one process
	cpu          time.Duration // CPU time of the whole process meanwhile
	cpuOK        bool
	writerWall   time.Duration // how long the author took to write it all
	longestWrite time.Duration
	streamErr    error
}

// flood runs one flood of total bytes and reads every frame of it. verify compares the bytes with the pattern; measure takes a
// garbage collection first so that the allocations and the CPU time are the flood's own.
func (f *floodRig) flood(tb testing.TB, total uint64, verify, measure bool) *floodRun {
	tb.Helper()
	f.runs++
	run := &floodRun{total: total, audit: proofAudit{verify: verify, lossy: f.stall == exec.StallLossy}}
	var before, after runtime.MemStats
	var cpuBefore time.Duration
	if measure {
		runtime.GC()
		runtime.ReadMemStats(&before)
		cpuBefore, run.cpuOK = proofCPUTime()
	}
	start := time.Now()
	conn, err := f.openKind(context.Background(), "flood", "flood-"+strconv.Itoa(f.runs), proofLabels("bytes", strconv.FormatUint(total, 10)))
	if err != nil {
		tb.Fatalf("open the flood: %v", err)
	}
	defer func() { _ = conn.Close() }()
	run.streamErr = proofDrain(conn, &run.audit, nil)
	run.wall = run.audit.endedAt.Sub(start)
	if measure {
		runtime.ReadMemStats(&after)
		cpuAfter, ok := proofCPUTime()
		run.cpu, run.cpuOK = cpuAfter-cpuBefore, run.cpuOK && ok
		run.alloc = after.TotalAlloc - before.TotalAlloc
	}
	run.writerWall = time.Duration(f.author.to.Load() - f.author.from.Load())
	run.longestWrite = time.Duration(f.author.longest.Load())
	return run
}

// delivered is what the host read.
func (r *floodRun) delivered() uint64 { return r.audit.read }

func (r *floodRun) megabytes() float64 { return float64(r.delivered()) / 1e6 }

// mbps is the delivered throughput, in MB/s of 10^6 bytes as go test counts them.
func (r *floodRun) mbps() float64 { return r.megabytes() / r.wall.Seconds() }

// bytesPerByte is how many bytes the process allocated for each byte it delivered.
func (r *floodRun) bytesPerByte() float64 { return float64(r.alloc) / float64(r.delivered()) }

// floodBytesPerByteBar is the bar for the bytes allocated for each byte moved: the plan's 4. The race detector makes sync.Pool
// drop a quarter of what is put back, so gRPC's buffer pool (a 1 MiB buffer for each miss) misses often and the same flood
// allocates 10 to 20 bytes for each byte: under it only a floor that still catches an allocation for each write holds.
func floodBytesPerByteBar() float64 {
	if proofRace {
		return 40
	}
	return floodMaxBytesPerB
}

// cpuMsPerMBPrecise keeps the microseconds a short flood costs.
func (r *floodRun) cpuMsPerMBPrecise() float64 {
	return float64(r.cpu.Microseconds()) / 1e3 / r.megabytes()
}

func (r *floodRun) avgFrame() float64 {
	if r.audit.frames == 0 {
		return 0
	}
	return float64(r.delivered()) / float64(r.audit.frames)
}

func (r *floodRun) dropRatio() float64 { return float64(r.audit.dropped) / float64(r.total) }

// checkContract holds one flood to what must be true of any: the stream ended as the contract says, every byte the author wrote
// was read or reported dropped, the bytes are the pattern in order, and the accounts of Ended say the same.
func (r *floodRun) checkContract(t testing.TB) {
	t.Helper()
	if !errors.Is(r.streamErr, io.EOF) {
		t.Errorf("the stream ended with %v, want Ended and then io.EOF", r.streamErr)
	}
	for _, problem := range r.audit.problems {
		t.Errorf("the stream: %s", problem)
	}
	ended := r.audit.ended
	if ended == nil {
		t.Fatalf("the stream ended without an Ended")
	}
	if got := r.audit.read + r.audit.dropped; got != r.total {
		t.Errorf("read %d and dropped %d bytes: %d, the author wrote %d", r.audit.read, r.audit.dropped, got, r.total)
	}
	if ended.GetBytesOut() != r.total {
		t.Errorf("Ended.bytes_out is %d, the author wrote %d", ended.GetBytesOut(), r.total)
	}
	if exited := ended.GetEnd().GetExited(); exited == nil || exited.GetExitCode() != 0 {
		t.Errorf("the flood ended %v, want exit 0", ended.GetEnd())
	}
}

func (r *floodRun) String() string {
	cpu := "CPU not measured here"
	if r.cpuOK {
		cpu = strconv.FormatFloat(r.cpuMsPerMBPrecise(), 'f', 2, 64) + " ms CPU/MB"
	}
	return strconv.FormatFloat(r.mbps(), 'f', 0, 64) + " MB/s delivered of " + strconv.FormatFloat(float64(r.total)/1e6, 'f', 1, 64) + " MB in " +
		r.wall.Round(time.Millisecond).String() + ", " + strconv.Itoa(r.audit.frames) + " Output frames (average " +
		strconv.FormatFloat(r.avgFrame()/1024, 'f', 1, 64) + " KiB), " + strconv.FormatFloat(r.bytesPerByte(), 'f', 2, 64) +
		" B allocated per byte, " + cpu + ", " + strconv.Itoa(r.audit.droppedFrames) + " Dropped frames for " +
		strconv.FormatUint(r.audit.dropped, 10) + " bytes (" + strconv.FormatFloat(100*r.dropRatio(), 'f', 1, 64) + "%)"
}

// A flood that a stalled reader may slow (the target propagates) is delivered in full, in order, in few large frames, for
// little CPU and little allocation. The plan's bars are: at least 300 MB/s (logged, with whether the run met it, and not asserted:
// floodMinMBps is, which is a third of it, and neither is asserted under the race detector, which slows everything by a factor), at
// most 4,100 frames for 64 MiB, at most 4 bytes allocated for each byte and 15 ms of CPU for each MB. Frames and allocation are
// asserted on every run; throughput and CPU are asserted on the best of three, because the machine is shared.
func TestOpenFloodBlockingDeliversEveryByteWithinTheBars(t *testing.T) {
	rig := newFloodRig(t, exec.StallPropagates)
	var best *floodRun
	for attempt := 1; attempt <= 3; attempt++ {
		run := rig.flood(t, floodTotal, true, true)
		t.Logf("blocking flood, attempt %d: %v", attempt, run)
		run.checkContract(t)
		if run.audit.dropped != 0 {
			t.Errorf("a target that propagates a stalled reader lost %d bytes in %d Dropped frames while its reader read", run.audit.dropped, run.audit.droppedFrames)
		}
		if got := run.audit.ended.GetOutput(); got != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE {
			t.Errorf("Ended.output is %v for a flood that lost nothing, want COMPLETE", got)
		}
		if limit := floodMaxFrames(floodTotal); run.audit.frames > limit {
			t.Errorf("%d Output frames for %d bytes, the bar is %d (an average frame of %d KiB)", run.audit.frames, uint64(floodTotal), limit, floodMinFrameBytes>>10)
		}
		if run.bytesPerByte() > floodBytesPerByteBar() {
			t.Errorf("%.2f bytes allocated for each byte delivered, the bar is %.0f", run.bytesPerByte(), floodBytesPerByteBar())
		}
		if best == nil || run.mbps() > best.mbps() {
			best = run
		}
		if proofRace || (run.mbps() >= floodPlanMBps && (!run.cpuOK || run.cpuMsPerMBPrecise() <= floodMaxCPUMsPerMB)) {
			break
		}
	}
	if !proofRace && best.mbps() < floodMinMBps {
		t.Errorf("%.0f MB/s delivered at best of three, the floor is %d (the plan's bar is %d)", best.mbps(), floodMinMBps, floodPlanMBps)
	}
	if !proofRace && best.cpuOK && best.cpuMsPerMBPrecise() > floodMaxCPUMsPerMB {
		t.Errorf("%.2f ms of CPU for each MB, the bar is %.0f", best.cpuMsPerMBPrecise(), floodMaxCPUMsPerMB)
	}
	t.Logf("blocking flood, best: %v (the plan's bar of %d MB/s: %s; the engine into a bare gRPC stream measured about 1,020 MB/s in the review of X2a; the legacy lane 10 to 35)",
		best, floodPlanMBps, planBarVerdict(best.mbps() >= floodPlanMBps))
}

// A target that a stalled reader would harm (lossy) never holds its author: what the reader cannot take is overwritten, oldest
// first, and reported. The proof is the accounting, which has to be exact: every byte the author wrote was read or lies in a
// Dropped range, the ranges and the frames tile the output with nothing missing and nothing twice, and Ended says the same.
// The throughput that counts is what is delivered, with the share that was dropped; none is asserted.
func TestOpenFloodLossyReportsWhatItDeliversAndAccountsForTheRest(t *testing.T) {
	rig := newFloodRig(t, exec.StallLossy)
	run := rig.flood(t, floodTotal, true, true)
	t.Logf("lossy flood: %v; the author wrote it in %v (%.0f MB/s) and its longest Write took %v",
		run, run.writerWall.Round(time.Millisecond), float64(run.total)/1e6/run.writerWall.Seconds(), run.longestWrite.Round(time.Microsecond))
	run.checkContract(t)
	if run.audit.dropped == 0 {
		t.Errorf("a lossy flood of %d bytes into a queue of 1 MiB lost nothing: the author was held until the host had read it, and a lossy target is never held", run.total)
	}
	if ended := run.audit.ended; ended != nil && ended.GetOutput() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED {
		t.Errorf("Ended.output is %v after %d bytes were dropped, want TRUNCATED", ended.GetOutput(), run.audit.dropped)
	}
	if run.audit.droppedFrames == 0 && run.audit.dropped > 0 {
		t.Errorf("%d bytes were dropped and no Dropped frame said so", run.audit.dropped)
	}
	if limit := floodMaxFrames(run.delivered()); run.audit.frames > limit {
		t.Errorf("%d Output frames for the %d bytes delivered, the bar is an average of %d KiB: at most %d", run.audit.frames, run.delivered(), floodMinFrameBytes>>10, limit)
	}
	// A lossy flood delivers little, so what it allocates is not comparable with what it delivered: the fixed cost of a session
	// (its queue grows to 1 MiB, gRPC's buffers are 1 MiB each when its pool is empty) is spread over a few MB. What the lane
	// allocates for each byte the author WROTE is bounded instead.
	if per := float64(run.alloc) / float64(run.total); per > floodBytesPerByteBar() {
		t.Errorf("%.2f bytes allocated for each byte the author wrote, the bar is %.0f", per, floodBytesPerByteBar())
	}
}

// BenchmarkOpenFlood is the flood of the plan through a real client and server, for the numbers the PR reports: delivered
// MB/s, bytes allocated per byte delivered, and the Output frames. Each op is one write of the author, 32 KiB, so ns/op is the
// cost of moving 32 KiB through the lane. For the lossy target the MB/s column counts the bytes DELIVERED, which is what a wire
// carries (ns/op is the author's pace, which a lossy target never slows), and dropped-% says how much of the flood the reader
// could not take.
func BenchmarkOpenFlood(b *testing.B) {
	for _, variant := range []struct {
		name  string
		stall exec.StallBehavior
	}{{"blocking", exec.StallPropagates}, {"lossy", exec.StallLossy}} {
		b.Run(variant.name, func(b *testing.B) {
			rig := newFloodRig(b, variant.stall)
			total := uint64(b.N) * floodChunk
			b.SetBytes(floodChunk)
			b.ReportAllocs()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			b.ResetTimer()
			run := rig.flood(b, total, false, false)
			b.StopTimer()
			runtime.ReadMemStats(&after)
			run.alloc = after.TotalAlloc - before.TotalAlloc
			run.checkContract(b)
			b.ReportMetric(run.bytesPerByte(), "B/B")
			b.ReportMetric(run.avgFrame()/1024, "KiB/frame")
			if variant.stall == exec.StallLossy {
				b.SetBytes(int64(run.delivered() / uint64(b.N)))
				b.ReportMetric(100*run.dropRatio(), "dropped-%")
			}
		})
	}
}
