package exec_test

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The proofs of step X2b (the plan's section X2b) run the stack a host and a plugin really use: a real gRPC server over a socket behind
// the SDK's middleware, a real client, and the author's function. This file holds what they share (a request, an audit of a result, the
// descriptors and the gauge of the process) and the proofs of the capture: a run of 64 MiB with a cap of 256 KiB keeps the heap flat and
// is read at the rate of the transport.

// runReq is a RunRequest for a kind of the rig: the thing, an argv that names no real program, and the mods.
func (r *proofRig) runReq(kind, id string, mods ...func(*execv2.RunRequest)) *execv2.RunRequest {
	req := &execv2.RunRequest{RequestId: id, Target: proto.Clone(r.refs[kind]).(*execv2.TargetRef), Argv: []string{"proof", kind}}
	for _, mod := range mods {
		mod(req)
	}
	return req
}

// callRun runs a request over the host's connection.
func (r *proofRig) callRun(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
	return r.client.Run(r.request(ctx), req)
}

// runLabels gives a run labels, which its function reads as Proc.Request.Labels. The arguments are name, value pairs.
func runLabels(pairs ...string) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) {
		for i := 0; i+1 < len(pairs); i += 2 {
			r.Labels = append(r.Labels, &execv2.NameValue{Name: pairs[i], Value: pairs[i+1]})
		}
	}
}

// runCaps sets the output caps of a run, and its policy.
func runCaps(stdout, stderr uint32, policy execv2.TruncatePolicy) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) {
		r.Output = &execv2.OutputLimits{MaxStdoutBytes: stdout, MaxStderrBytes: stderr, Policy: policy}
	}
}

// proofBytes returns the n bytes of the pattern that start at offset.
func proofBytes(offset uint64, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		out = append(out, proofChunk(offset+uint64(len(out)), min(n-len(out), proofMaxFrame))...)
	}
	return out
}

// proofKept is what a capture of the cap and the policy keeps of the first total bytes of the pattern: the head and the tail of it
// under HEAD_TAIL (the default), the beginning under HEAD and the end under TAIL.
func proofKept(total uint64, capBytes int, policy execv2.TruncatePolicy) []byte {
	if total <= uint64(capBytes) {
		return proofBytes(0, int(total))
	}
	head, tail := (capBytes+1)/2, capBytes/2
	switch policy {
	case execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD:
		head, tail = capBytes, 0
	case execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL:
		head, tail = 0, capBytes
	case execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL:
	}
	return append(proofBytes(0, head), proofBytes(total-uint64(tail), tail)...)
}

// runInvariants are the rules that hold for the result of every run: the account of each stream is exact (kept is what was
// returned, kept and dropped are the total, truncated says dropped, head and tail are the kept, valid_utf8 is the bytes as returned,
// and the completeness claims no more than the end allows), the failure is present exactly for a failed run and the exit code exactly
// for one that exited.
func runInvariants(res *execv2.RunResult) []string {
	var found []string
	add := func(format string, args ...any) { found = append(found, fmt.Sprintf(format, args...)) }
	outcome := res.GetOutcome()
	for _, s := range []struct {
		name string
		a    *execv2.OutputAccount
		data []byte
	}{{"stdout", res.GetStdoutAccount(), res.GetStdout()}, {"stderr", res.GetStderrAccount(), res.GetStderr()}} {
		a := s.a
		switch {
		case a == nil:
			add("%s: no account", s.name)
			continue
		case a.GetKeptBytes() != uint64(len(s.data)):
			add("%s: kept_bytes is %d, %d bytes were returned", s.name, a.GetKeptBytes(), len(s.data))
		case a.GetKeptBytes()+a.GetDroppedBytes() != a.GetTotalBytes():
			add("%s: kept %d plus dropped %d is not the total %d", s.name, a.GetKeptBytes(), a.GetDroppedBytes(), a.GetTotalBytes())
		case a.GetTruncated() != (a.GetDroppedBytes() > 0):
			add("%s: truncated is %v with %d bytes dropped", s.name, a.GetTruncated(), a.GetDroppedBytes())
		case uint64(a.GetHeadBytes())+uint64(a.GetTailBytes()) != a.GetKeptBytes():
			add("%s: head %d plus tail %d is not the kept %d", s.name, a.GetHeadBytes(), a.GetTailBytes(), a.GetKeptBytes())
		case a.GetValidUtf8() != utf8.Valid(s.data):
			add("%s: valid_utf8 is %v for the bytes returned", s.name, a.GetValidUtf8())
		case a.GetDroppedBytes() > 0 && a.GetCompleteness() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED:
			add("%s: %d bytes dropped and completeness %s", s.name, a.GetDroppedBytes(), a.GetCompleteness())
		case a.GetCompleteness() == execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE && outcome != execv2.Outcome_OUTCOME_EXITED && outcome != execv2.Outcome_OUTCOME_SIGNALED:
			add("%s: COMPLETE for a run that ended %s", s.name, outcome)
		}
	}
	if outcome == execv2.Outcome_OUTCOME_UNSPECIFIED {
		add("the outcome is not said")
	}
	if (res.Failed != nil) != (outcome == execv2.Outcome_OUTCOME_FAILED) {
		add("failed is present: %v for the outcome %s", res.Failed != nil, outcome)
	}
	if (res.ExitCode != nil) != (outcome == execv2.Outcome_OUTCOME_EXITED) {
		add("exit_code is present: %v for the outcome %s", res.ExitCode != nil, outcome)
	}
	return found
}

// countDescriptors counts the descriptors this process holds, and whether the platform can say (it cannot on Windows).
func countDescriptors() (int, bool) {
	if runtime.GOOS == "windows" {
		return 0, false
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

// proofLiveGauge is the gauge of the commands that the process's engines hold, summed over its points.
func proofLiveGauge() float64 {
	metrics, _ := sdkdiag.Default().Snapshot()
	var sum float64
	for _, m := range metrics {
		if m.GetName() == "omniview.plugin.self.sdk.exec.sessions" {
			for _, p := range m.GetPoints() {
				sum += p.GetValue()
			}
		}
	}
	return sum
}

// ---------------------------------------------------------------------------------------------------------------
// the capture: 64 MiB through a cap of 256 KiB
// ---------------------------------------------------------------------------------------------------------------

const (
	// capTotal is the output of the run of the plan; capBytes its cap, which is also the target's default.
	capTotal = 64 << 20
	capBytes = 256 << 10
	// capSampleEvery is how many writes the author makes between two readings of the heap: 64 writes of 32 KiB are 2 MiB.
	capSampleEvery = 64
	// capPlanMBps is the rate of the plan, which a machine that nothing else uses delivers: logged, with whether the run met it, and not
	// asserted (see capMinMBps).
	capPlanMBps = 300
	// capMinMBps is what is asserted, at the best of three and not under the race detector: a third of the plan's rate, and three times
	// what the legacy lane moved at best (35 MB/s). The connection of the macOS runner that CI uses carried 270 MB/s for 64 sessions
	// together (open_concurrent_sessions_test.go), so 300 for one run is a number that the load of the runner decides; a capture that
	// copied or locked for each byte would fall under this one on any runner.
	capMinMBps = 100
)

// capAuthor writes the bytes its label "bytes" names, in writes of floodChunk bytes of the pattern, to standard output. When its label
// "sample" is set it reads the heap itself, between writes (a garbage collection and then runtime.ReadMemStats, so that what it reads is
// what is live, and no timer is involved), and keeps the first and the highest reading.
type capAuthor struct {
	baseline, peak atomic.Uint64
	samples        atomic.Int64
}

func (a *capAuthor) run(_ context.Context, p *exec.Proc) (exec.Exit, error) {
	total, err := strconv.ParseUint(p.Request.Labels["bytes"], 10, 64)
	if err != nil {
		return exec.Exit{}, err
	}
	sample := p.Request.Labels["sample"] == "1"
	var ms runtime.MemStats
	heap := func() uint64 {
		runtime.GC()
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}
	if sample {
		h := heap()
		a.baseline.Store(h)
		a.peak.Store(h)
	}
	for offset, writes := uint64(0), 0; offset < total; writes++ {
		n := int(min(uint64(floodChunk), total-offset))
		if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
			return exec.Exit{}, err
		}
		offset += uint64(n)
		if sample && (writes+1)%capSampleEvery == 0 {
			if h := heap(); h > a.peak.Load() {
				a.peak.Store(h)
			}
			a.samples.Add(1)
		}
	}
	return exec.ExitCode(0), nil
}

// capRun runs the author for total bytes under the cap of the plan.
func capRun(t testing.TB, rig *proofRig, total uint64, sample bool) (*execv2.RunResult, time.Duration) {
	t.Helper()
	labels := []string{"bytes", strconv.FormatUint(total, 10)}
	if sample {
		labels = append(labels, "sample", "1")
	}
	began := time.Now()
	res, err := rig.callRun(context.Background(), rig.runReq("cap", "", runLabels(labels...), runCaps(capBytes, capBytes, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL)))
	took := time.Since(began)
	if err != nil {
		t.Fatalf("the run of %d bytes: %v", total, err)
	}
	return res, took
}

// checkCapRun holds the result of a run of total bytes to the cap: exited with 0, the head and the tail of the output exactly, and an
// account that counts every byte.
func checkCapRun(t testing.TB, res *execv2.RunResult, total uint64) {
	t.Helper()
	for _, problem := range runInvariants(res) {
		t.Errorf("the result: %s", problem)
	}
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || res.GetExitCode() != 0 {
		t.Errorf("the run ended %s with exit %v, want exit 0", res.GetOutcome(), res.ExitCode)
	}
	a := res.GetStdoutAccount()
	if a.GetTotalBytes() != total || a.GetKeptBytes() != capBytes || a.GetDroppedBytes() != total-capBytes || a.GetHeadBytes() != capBytes/2 || a.GetTailBytes() != capBytes/2 ||
		a.GetCompleteness() != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED {
		t.Errorf("the account is %v for %d bytes under a cap of %d", a, total, capBytes)
	}
	if want := proofKept(total, capBytes, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL); string(res.GetStdout()) != string(want) {
		t.Errorf("the %d bytes returned are not the head and the tail of the pattern", len(res.GetStdout()))
	}
}

func newCapRig(t testing.TB, author *capAuthor) *proofRig {
	t.Helper()
	rig := newProofRig(t, proofTarget("cap", author.run, func(i *exec.TargetInfo) { i.Run.MaxConcurrent = 4 }))
	// The connection, its buffers and the pools exist before anything is measured.
	checkCapRun(t, mustCapRun(t, rig, 4<<20), 4<<20)
	return rig
}

func mustCapRun(t testing.TB, rig *proofRig, total uint64) *execv2.RunResult {
	res, _ := capRun(t, rig, total, false)
	return res
}

// A run of 64 MiB with a cap of 256 KiB keeps the heap flat: what the process holds live grows by no more than the cap and 1 MiB, however
// much the command writes, and the result is exact. The author reads the heap itself, between its writes. A capture that kept what it
// read would grow by 64 MiB.
func TestARunOf64MiBWithACapOf256KiBKeepsTheHeapFlat(t *testing.T) {
	author := &capAuthor{}
	rig := newCapRig(t, author)
	res, took := capRun(t, rig, capTotal, true)
	checkCapRun(t, res, capTotal)
	baseline, peak := int64(author.baseline.Load()), int64(author.peak.Load()) //nolint:gosec // a heap size
	growth := peak - baseline
	t.Logf("%d MiB through a cap of %d KiB in %v: the heap read %d times by the author, %d KiB live at the start, %d KiB at the highest, growth %d KiB (the bar is %d KiB)",
		capTotal>>20, capBytes>>10, took.Round(time.Millisecond), author.samples.Load(), baseline>>10, peak>>10, growth>>10, (capBytes+1<<20)>>10)
	if got, want := author.samples.Load(), int64(capTotal/floodChunk/capSampleEvery); got < want {
		t.Errorf("the author read the heap %d times, want %d: the run did not go through the author's loop", got, want)
	}
	if growth > capBytes+1<<20 {
		t.Errorf("the live heap grew by %d KiB during a run capped at %d KiB, the bar is the cap and 1 MiB", growth>>10, capBytes>>10)
	}
}

// A run of 64 MiB with a cap of 256 KiB is read at the rate of the transport: the plan's rate is 300 MB/s, which is logged with whether
// the run met it, and what is asserted is capMinMBps, the best of three because the machine is shared, and not under the race
// detector, which slows everything by a factor. The rate is the bytes the command wrote over the time of the call, which is the
// engine's queue, the reader and the capture, and a result of 256 KiB.
func TestARunOf64MiBWithACapOf256KiBIsReadAtTheRateOfTheTransport(t *testing.T) {
	rig := newCapRig(t, &capAuthor{})
	var best float64
	for attempt := 1; attempt <= 3; attempt++ {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		cpuBefore, cpuOK := proofCPUTime()
		res, took := capRun(t, rig, capTotal, false)
		cpuAfter, ok := proofCPUTime()
		runtime.ReadMemStats(&after)
		checkCapRun(t, res, capTotal)
		mbps := float64(capTotal) / 1e6 / took.Seconds()
		cpu := "CPU not measured here"
		if cpuOK && ok {
			cpu = strconv.FormatFloat(float64((cpuAfter-cpuBefore).Microseconds())/1e3/(float64(capTotal)/1e6), 'f', 2, 64) + " ms CPU/MB"
		}
		t.Logf("attempt %d: %.0f MB/s: %d MiB in %v, %.2f B allocated per byte, %s", attempt, mbps, capTotal>>20, took.Round(time.Millisecond),
			float64(after.TotalAlloc-before.TotalAlloc)/float64(capTotal), cpu)
		best = max(best, mbps)
		if proofRace || mbps >= capPlanMBps {
			break
		}
	}
	t.Logf("best: %.0f MB/s (the plan's rate of %d MB/s: %s; the floor is %d MB/s, not asserted under the race detector)", best, capPlanMBps, planBarVerdict(best >= capPlanMBps), capMinMBps)
	if !proofRace && best < capMinMBps {
		t.Errorf("%.0f MB/s at best of three, the floor is %d (the plan's rate is %d)", best, capMinMBps, capPlanMBps)
	}
}
