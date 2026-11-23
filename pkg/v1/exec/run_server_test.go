package exec_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The bounded run on the wire: a plugin served over a real transport (a unix socket behind the SDK's middleware, with a fake
// connection registry), called with the generated client and nothing of the SDK's own. What the contract promises a host of
// Run is checked here byte by byte: the output and its account, the end, the bounds, the refusals, and what is left behind.

// runSrvRig is a plugin that serves targets, and the raw client of its exec service.
type runSrvRig struct {
	t      testing.TB
	conns  *exectestkit.Connections
	server *exectestkit.Server
	raw    execv2.ExecClient
}

// newRunSrvRig serves targets. When the test ends the plugin's engine is shut down and must hold nothing.
func newRunSrvRig(t testing.TB, targets ...exec.Target) *runSrvRig {
	t.Helper()
	conns := exectestkit.NewConnectionsT(t, "fake")
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: targets, Connections: conns})
	rig := &runSrvRig{t: t, conns: conns, server: server, raw: server.Raw()}
	t.Cleanup(func() { // registered after the server, so that it runs before the server stops
		ctx, cancel := context.WithTimeout(context.Background(), engineWait)
		defer cancel()
		if err := server.Plugin.Shutdown(ctx); err != nil {
			t.Errorf("the plugin did not shut down within %v: %v; it holds %v", engineWait, err, exec.RunEngineLive(server.Plugin))
			return
		}
		if live := exec.RunEngineLive(server.Plugin); len(live) != 0 {
			t.Errorf("a plugin that shut down holds %v", live)
		}
	})
	return rig
}

// ctx is the context of a host's call: the connection and the catalog pin, in the metadata the middleware reads. The wait
// is a bound that turns a hang into a failure; it decides no outcome.
func (r *runSrvRig) ctx() context.Context {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	r.t.Cleanup(cancel)
	ctx, err := resourceplugin.OutgoingSessionContext(r.conns.Context(ctx))
	if err != nil {
		r.t.Fatalf("the context of the call: %v", err)
	}
	return ctx
}

// request is a run of the fake target's thing, with the argv ["prog", "a b"], and what the mods change.
func (r *runSrvRig) request(mods ...func(*execv2.RunRequest)) *execv2.RunRequest {
	r.t.Helper()
	req := &execv2.RunRequest{Target: r.conns.WireTarget(r.t, "thing", "thing-1"), Argv: []string{"prog", "a b"}}
	for _, mod := range mods {
		mod(req)
	}
	return req
}

// run calls Run.
func (r *runSrvRig) run(req *execv2.RunRequest) (*execv2.RunResult, error) {
	r.t.Helper()
	return r.raw.Run(r.ctx(), req)
}

// mustRun calls Run and requires a result.
func (r *runSrvRig) mustRun(req *execv2.RunRequest) *execv2.RunResult {
	r.t.Helper()
	res, err := r.run(req)
	if err != nil {
		r.t.Fatalf("Run: %v", err)
	}
	return res
}

func runSrvID(id string) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) { r.RequestId = id }
}

func runSrvArgv(argv ...string) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) { r.Argv = argv }
}

func runSrvTimeout(ms uint32) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) { r.TimeoutMs = ms }
}

func runSrvStdin(data string) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) { r.Stdin = []byte(data) }
}

func runSrvLabel(name, value string) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) { r.Labels = append(r.Labels, &execv2.NameValue{Name: name, Value: value}) }
}

func runSrvCaps(stdout, stderr uint32, policy execv2.TruncatePolicy) func(*execv2.RunRequest) {
	return func(r *execv2.RunRequest) {
		r.Output = &execv2.OutputLimits{MaxStdoutBytes: stdout, MaxStderrBytes: stderr, Policy: policy}
	}
}

// runSrvLimits sets the run limits of the target; a field left zero takes the SDK's default.
func runSrvLimits(limits exec.RunLimits) func(*exec.TargetInfo) {
	return func(info *exec.TargetInfo) { info.Run = limits }
}

// runSrvRefusal is a status as a host reads it: the gRPC code and what the ErrorInfo of the exec domain says. The class is its
// Reason, the reason of the refusal its metadata "reason".
type runSrvRefusal struct {
	code          codes.Code
	domain        string
	class, reason string
}

func decodeRunSrvRefusal(t testing.TB, err error) runSrvRefusal {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || err == nil {
		t.Fatalf("err = %v, want a status", err)
	}
	out := runSrvRefusal{code: st.Code()}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			out.domain, out.class, out.reason = info.GetDomain(), info.GetReason(), info.GetMetadata()["reason"]
		}
	}
	return out
}

// requireRunSrvRefusal requires err to be the refusal of the exec lane with the class and the reason (the empty reason is none).
func requireRunSrvRefusal(t testing.TB, err error, code codes.Code, class, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want the refusal %s/%s", class, reason)
	}
	got := decodeRunSrvRefusal(t, err)
	want := runSrvRefusal{code: code, domain: "omniview.sdk.exec.v2", class: class, reason: reason}
	if got != want {
		t.Fatalf("refused with %+v, want %+v (%v)", got, want, err)
	}
}

// runSrvText is n bytes of printable ASCII in which no two neighbours at a distance that a cap or a chunk would have are alike.
func runSrvText(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(33 + (i*31+i/7)%94)
	}
	return out
}

// runSrvWindow is what a cap of limit bytes keeps of data under a policy, and how the kept bytes split: the oracle of the
// tests below, written out here as the contract states it.
func runSrvWindow(data []byte, limit int, policy execv2.TruncatePolicy) (kept []byte, head, tail int) {
	headCap, tailCap := (limit+1)/2, limit/2
	switch policy {
	case execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD:
		headCap, tailCap = limit, 0
	case execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL:
		headCap, tailCap = 0, limit
	case execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL:
	}
	head = min(headCap, len(data))
	tail = min(tailCap, len(data)-head)
	kept = append(append([]byte{}, data[:head]...), data[len(data)-tail:]...)
	return kept, head, tail
}

// noDuration clears the one field of a result that no test can predict.
func noDuration(res *execv2.RunResult) *execv2.RunResult {
	res = proto.Clone(res).(*execv2.RunResult)
	res.DurationMs = 0
	return res
}

// requireRunSrvResult compares a result with the one the contract says, apart from its duration.
func requireRunSrvResult(t testing.TB, got, want *execv2.RunResult) {
	t.Helper()
	if !proto.Equal(noDuration(got), noDuration(want)) {
		t.Fatalf("result:\n%s\nwant:\n%s", prototext.Format(noDuration(got)), prototext.Format(noDuration(want)))
	}
}

// goodAccount is the account of a stream that was written in full, read to its end and was valid text.
func goodAccount(n int) *execv2.OutputAccount {
	return &execv2.OutputAccount{
		TotalBytes: uint64(n), KeptBytes: uint64(n), HeadBytes: uint32(n), ValidUtf8: true,
		Completeness: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE,
	}
}

var fakeTargetFacts = &execv2.TargetFacts{Provider: "fake", Type: "thing", Id: "thing-1", DisplayName: "Fake"}

const (
	completeAll       = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	completeTruncated = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	completeUnknown   = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
)

// ---------------------------------------------------------------------------------------------------------------
// the answer of a run that ran
// ---------------------------------------------------------------------------------------------------------------

func TestServedRunAnswersWithTheOutputOfEachStreamTheExitAndTheFactsOfTheStart(t *testing.T) {
	var seen atomic.Pointer[exec.Request]
	var stdinWasNil atomic.Bool
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		request := p.Request
		seen.Store(&request)
		stdinWasNil.Store(p.Stdin == nil)
		_, _ = fmt.Fprintf(p.Stdout, "out:%s", strings.Join(p.Request.Argv, "|"))
		_, _ = io.WriteString(p.Stderr, "err")
		return exec.ExitCode(3), nil
	}))
	res := rig.mustRun(rig.request(runSrvID("audit-1"), runSrvArgv("prog", "a b", "-x")))

	const out = "out:prog|a b|-x"
	requireRunSrvResult(t, res, &execv2.RunResult{
		RequestId:        "audit-1",
		Outcome:          execv2.Outcome_OUTCOME_EXITED,
		ExitCode:         new(int64(3)),
		ExitSource:       execv2.ExitSource_EXIT_SOURCE_PROCESS,
		Stdout:           []byte(out),
		Stderr:           []byte("err"),
		StdoutAccount:    goodAccount(len(out)),
		StderrAccount:    goodAccount(3),
		StartObserved:    true,
		RemoteMaySurvive: new(false), // the target said it terminates what it ends, and relayed the process's own status
		Target:           fakeTargetFacts,
		Program:          &execv2.Program{Argv: []string{"prog", "a b", "-x"}, Name: "prog"},
	})
	got := seen.Load()
	if got == nil || strings.Join(got.Argv, "|") != "prog|a b|-x" || got.TTY || got.Ref.Address.ID != "thing-1" {
		t.Errorf("the function saw %+v, want the argv as sent, no terminal and the reference of the request", got)
	}
	if !stdinWasNil.Load() {
		t.Error("a run with no stdin was given a Stdin: it is nil, as Proc says")
	}
}

// A run's id is the request's, and when the host gave none the SDK makes one for itself and echoes none: the host's own
// correlation is what comes back.
func TestServedRunEchoesTheRequestIDAndMakesOneOfItsOwnWhenThereIsNone(t *testing.T) {
	entered := make(chan struct{}, 8)
	gate := exectest.NewGate()
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		entered <- struct{}{}
		select {
		case <-gate.Opened():
		case <-ctx.Done():
			return exec.Exit{}, ctx.Err()
		}
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{MaxConcurrent: 8})))
	t.Cleanup(gate.Open)

	type answer struct {
		res *execv2.RunResult
		err error
	}
	answers := make(chan answer, 3)
	for _, id := range []string{"", "", "mine"} {
		go func() {
			res, err := rig.run(rig.request(runSrvID(id)))
			answers <- answer{res, err}
		}()
	}
	for range 3 {
		awaitChannel(t, entered, "a run to start")
	}
	ids := exec.RunEngineLive(rig.server.Plugin)
	var generated int
	for _, id := range ids {
		switch {
		case id == "mine":
		case strings.HasPrefix(id, "run-") && len(id) > len("run-"):
			generated++
		default:
			t.Errorf("the engine holds a run under %q: want the request's id, or %q and an identifier", id, "run-")
		}
	}
	if len(ids) != 3 || generated != 2 {
		t.Errorf("the engine holds %v: want the request's id and two made ones", ids)
	}
	gate.Open()
	echoed := map[string]int{}
	for range 3 {
		a := <-answers
		if a.err != nil {
			t.Fatalf("Run: %v", a.err)
		}
		echoed[a.res.GetRequestId()]++
	}
	if echoed[""] != 2 || echoed["mine"] != 1 {
		t.Errorf("request ids echoed: %v, want the host's own and none for the two that gave none", echoed)
	}
}

func TestServedRunReportsTheProgramThatReallyRanWhenTheFunctionSaidSo(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{Program: exec.Program{Name: "custom", Path: "/usr/bin/custom"}})
		return exec.ExitCode(0), nil
	}))
	res := rig.mustRun(rig.request(runSrvArgv("custom", "--flag")))
	if !res.GetStartObserved() || res.GetTarget().GetId() != "thing-1" {
		t.Fatalf("result %v: want an observed start in thing-1", res)
	}
	want := &execv2.Program{Argv: []string{"custom", "--flag"}, Name: "custom", Path: "/usr/bin/custom"}
	if !proto.Equal(res.GetProgram(), want) {
		t.Errorf("program %v, want %v: the facts the function reported refine what was asked", res.GetProgram(), want)
	}
}

// A command that exits with a status and never wrote a byte or reported a start still has a start that was observed: the
// status says so.
func TestServedRunWithNoOutputAndNoReportIsStartedByItsStatus(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil }))
	res := rig.mustRun(rig.request())
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || !res.GetStartObserved() || len(res.GetStdout()) != 0 || len(res.GetStderr()) != 0 {
		t.Fatalf("result %v: want an exit with an observed start and no output", res)
	}
	for name, account := range map[string]*execv2.OutputAccount{"stdout": res.GetStdoutAccount(), "stderr": res.GetStderrAccount()} {
		if !proto.Equal(account, &execv2.OutputAccount{ValidUtf8: true, Completeness: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE}) {
			t.Errorf("%s account %v: an empty stream read to its end is complete, valid text and all zero", name, account)
		}
	}
}

func TestServedRunCarriesEveryKindOfExitExactly(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		switch p.Request.Labels["exit"] {
		case "zero":
			return exec.ExitCode(0), nil
		case "dword":
			return exec.ExitCode(uint32(0xC0000005)), nil
		case "negative":
			return exec.ExitCode(int32(-1073741819)), nil
		case "folded":
			return exec.ExitFolded(137), nil
		case "transport":
			return exec.ExitTransport(1), nil
		case "signal":
			return exec.ExitSignal("SIGKILL", 9), nil
		}
		return exec.Exit{}, fmt.Errorf("unknown exit %q", p.Request.Labels["exit"])
	}))
	const (
		process = execv2.ExitSource_EXIT_SOURCE_PROCESS
		folded  = execv2.ExitSource_EXIT_SOURCE_SIGNAL_FOLDED
		wire    = execv2.ExitSource_EXIT_SOURCE_TRANSPORT
	)
	for _, tc := range []struct {
		exit     string
		outcome  execv2.Outcome
		code     *int64
		signal   string
		number   int32
		source   execv2.ExitSource
		survive  *bool
		complete execv2.OutputCompleteness
	}{
		{"zero", execv2.Outcome_OUTCOME_EXITED, new(int64(0)), "", 0, process, new(false), completeAll},
		{"dword", execv2.Outcome_OUTCOME_EXITED, new(int64(3221225477)), "", 0, process, new(false), completeAll},
		{"negative", execv2.Outcome_OUTCOME_EXITED, new(int64(-1073741819)), "", 0, process, new(false), completeAll},
		{"folded", execv2.Outcome_OUTCOME_EXITED, new(int64(137)), "", 0, folded, new(false), completeAll},
		{"transport", execv2.Outcome_OUTCOME_EXITED, new(int64(1)), "", 0, wire, nil, completeUnknown},
		{"signal", execv2.Outcome_OUTCOME_SIGNALED, nil, "SIGKILL", 9, process, new(false), completeAll},
	} {
		t.Run(tc.exit, func(t *testing.T) {
			res := rig.mustRun(rig.request(runSrvLabel("exit", tc.exit)))
			if res.GetOutcome() != tc.outcome || !proto.Equal(&execv2.RunResult{ExitCode: res.ExitCode}, &execv2.RunResult{ExitCode: tc.code}) ||
				res.GetSignal() != tc.signal || res.GetSignalNumber() != tc.number || res.GetExitSource() != tc.source {
				t.Errorf("result:\n%s\nwant outcome %v code %v signal %q %d source %v", prototext.Format(res), tc.outcome, tc.code, tc.signal, tc.number, tc.source)
			}
			if !proto.Equal(&execv2.RunResult{RemoteMaySurvive: res.RemoteMaySurvive}, &execv2.RunResult{RemoteMaySurvive: tc.survive}) {
				t.Errorf("remote_may_survive = %v, want %v", res.RemoteMaySurvive, tc.survive)
			}
			if res.GetStdoutAccount().GetCompleteness() != tc.complete || res.GetStderrAccount().GetCompleteness() != tc.complete {
				t.Errorf("completeness %v and %v, want %v", res.GetStdoutAccount().GetCompleteness(), res.GetStderrAccount().GetCompleteness(), tc.complete)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the output: caps, policies and the account
// ---------------------------------------------------------------------------------------------------------------

// runSrvWriter writes the text of the label "n" bytes to both streams, a few bytes at a time and alternating, so that the
// queue holds many runs of each and the reader gets them in pieces.
func runSrvWriter(_ context.Context, p *exec.Proc) (exec.Exit, error) {
	n, err := strconv.Atoi(p.Request.Labels["n"])
	if err != nil {
		return exec.Exit{}, err
	}
	data := runSrvText(n)
	for rest := data; len(rest) > 0; {
		k := min(len(rest), 37)
		if _, err := p.Stdout.Write(rest[:k]); err != nil {
			return exec.Exit{}, err
		}
		if _, err := p.Stderr.Write(rest[:k]); err != nil {
			return exec.Exit{}, err
		}
		rest = rest[k:]
	}
	return exec.ExitCode(0), nil
}

func TestServedRunKeepsTheWindowThePolicyNamesAndAccountsForTheRest(t *testing.T) {
	const (
		headTail = execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL
		head     = execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD
		tail     = execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL
		unset    = execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED
	)
	rig := newRunSrvRig(t, engTarget(runSrvWriter, runSrvLimits(exec.RunLimits{DefaultOutputBytes: 64, MaxOutputBytes: 1 << 20})))
	for _, tc := range []struct {
		name                 string
		n                    int
		stdout, stderr       uint32
		policy               execv2.TruncatePolicy
		stdoutCap, stderrCap int
	}{
		{"head and tail by default", 1000, 100, 100, unset, 100, 100},
		{"head and tail asked for", 1000, 100, 100, headTail, 100, 100},
		{"the head", 1000, 100, 100, head, 100, 100},
		{"the tail", 1000, 100, 100, tail, 100, 100},
		{"an odd cap: the head is the larger half", 1000, 101, 101, unset, 101, 101},
		{"a cap of one", 1000, 1, 1, unset, 1, 1},
		{"each stream has its own cap", 1000, 10, 333, unset, 10, 333},
		{"the target's default cap when none is asked", 1000, 0, 0, unset, 64, 64},
		{"output that fits is kept whole", 90, 100, 100, unset, 100, 100},
		{"output of exactly the cap is kept whole", 100, 100, 100, unset, 100, 100},
		{"output one byte over the cap loses one byte", 101, 100, 100, unset, 100, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := rig.mustRun(rig.request(runSrvLabel("n", strconv.Itoa(tc.n)), runSrvCaps(tc.stdout, tc.stderr, tc.policy)))
			data := runSrvText(tc.n)
			for _, s := range []struct {
				name     string
				got      []byte
				account  *execv2.OutputAccount
				capacity int
			}{
				{"stdout", res.GetStdout(), res.GetStdoutAccount(), tc.stdoutCap},
				{"stderr", res.GetStderr(), res.GetStderrAccount(), tc.stderrCap},
			} {
				kept, wantHead, wantTail := runSrvWindow(data, s.capacity, tc.policy)
				if string(s.got) != string(kept) {
					t.Errorf("%s kept %q, want %q", s.name, s.got, kept)
				}
				want := &execv2.OutputAccount{
					TotalBytes: uint64(tc.n), KeptBytes: uint64(len(kept)), DroppedBytes: uint64(tc.n - len(kept)), Truncated: tc.n > len(kept),
					HeadBytes: uint32(wantHead), TailBytes: uint32(wantTail), ValidUtf8: true,
					Completeness: completeAll,
				}
				if want.Truncated {
					want.Completeness = completeTruncated
				}
				if !proto.Equal(s.account, want) {
					t.Errorf("%s account %v, want %v", s.name, s.account, want)
				}
			}
			if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
				t.Errorf("outcome %v: a command whose output was cut ended as it ended", res.GetOutcome())
			}
		})
	}
}

// The text is raw: valid_utf8 is about the bytes returned, so a cap that cuts a sequence says so and the bytes are not repaired.
func TestServedRunReportsWhetherTheBytesItReturnsAreValidText(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		// The bytes arrive as hex: a label is a string of the request, which is valid text, and the output is not to be.
		out, _ := hex.DecodeString(p.Request.Labels["out"])
		errOut, _ := hex.DecodeString(p.Request.Labels["err"])
		_, _ = p.Stdout.Write(out)
		_, _ = p.Stderr.Write(errOut)
		return exec.ExitCode(0), nil
	}))
	for _, tc := range []struct {
		name         string
		out, errText string
		capacity     uint32
		policy       execv2.TruncatePolicy
		wantOut      string
		validOut     bool
		validErr     bool
	}{
		{"valid text kept whole", "héllo €", "plain", 64, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD, "héllo €", true, true},
		{"a sequence cut at the cap is not repaired", "abc€", "ok", 4, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD, "abc\xE2", false, true},
		{"a tail that begins inside a sequence", "€abc", "ok", 4, execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL, "\xACabc", false, true},
		{"bytes that are not text", "a\xFFb", "\x80", 64, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD, "a\xFFb", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := rig.mustRun(rig.request(runSrvLabel("out", hex.EncodeToString([]byte(tc.out))), runSrvLabel("err", hex.EncodeToString([]byte(tc.errText))), runSrvCaps(tc.capacity, 0, tc.policy)))
			if string(res.GetStdout()) != tc.wantOut {
				t.Errorf("stdout %q, want %q: the bytes are returned as they are", res.GetStdout(), tc.wantOut)
			}
			if res.GetStdoutAccount().GetValidUtf8() != tc.validOut || res.GetStderrAccount().GetValidUtf8() != tc.validErr {
				t.Errorf("valid_utf8 stdout %v stderr %v, want %v and %v", res.GetStdoutAccount().GetValidUtf8(), res.GetStderrAccount().GetValidUtf8(), tc.validOut, tc.validErr)
			}
		})
	}
}

// What a target that stalls lossily lost in the engine's queue before the SDK could read it is accounted for as dropped output,
// so that the host is told the output is not whole.
func TestServedRunCountsWhatTheEnginesQueueLostAsDropped(t *testing.T) {
	const written = 1 << 20 // four times the queue of a run: one write loses the part of it that the queue cannot hold
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, err := p.Stdout.Write(runSrvText(written))
		if err != nil {
			return exec.Exit{}, err
		}
		return exec.ExitCode(0), nil
	}, func(info *exec.TargetInfo) { info.Capabilities.Stall = exec.StallLossy }, runSrvLimits(exec.RunLimits{MaxOutputBytes: 1 << 20})))
	res := rig.mustRun(rig.request(runSrvCaps(1<<20, 0, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL)))
	account := res.GetStdoutAccount()
	if account.GetTotalBytes() != written {
		t.Errorf("total_bytes = %d, want %d: every byte the command wrote is the SDK's to account for, the ones that were lost included", account.GetTotalBytes(), written)
	}
	if account.GetDroppedBytes() == 0 || !account.GetTruncated() || account.GetCompleteness() != completeTruncated {
		t.Errorf("account %v: a queue that overwrote output makes the output truncated", account)
	}
	if account.GetKeptBytes()+account.GetDroppedBytes() != account.GetTotalBytes() || int(account.GetKeptBytes()) != len(res.GetStdout()) {
		t.Errorf("account %v with %d bytes returned: kept + dropped must be total, and kept the bytes returned", account, len(res.GetStdout()))
	}
	if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Errorf("outcome %v", res.GetOutcome())
	}
}

// ---------------------------------------------------------------------------------------------------------------
// stdin
// ---------------------------------------------------------------------------------------------------------------

func TestServedRunGivesTheCommandItsStdinAndThenEndOfInput(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Stdin == nil {
			return exec.Exit{}, fmt.Errorf("a run that carries stdin has none")
		}
		n, err := io.Copy(p.Stdout, p.Stdin) // returns only at the end of input
		if err != nil {
			return exec.Exit{}, err
		}
		_, _ = fmt.Fprintf(p.Stderr, "%d", n)
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{MaxStdinBytes: 512 << 10, MaxOutputBytes: 1 << 20})))

	// Each run is given two seconds, not the 20 the others of this file get: the command echoes its stdin and returns once the input
	// has ended, which takes milliseconds, so a timeout here is a plugin that never ended the input, and it should fail the test at once
	// (a run that is held to its default timeout holds the test for as long).
	const stdinBound = 2_000
	t.Run("a small stdin is echoed and the input ends", func(t *testing.T) {
		res := rig.mustRun(rig.request(runSrvStdin("hello\nworld\n"), runSrvTimeout(stdinBound)))
		if res.GetOutcome() == execv2.Outcome_OUTCOME_TIMED_OUT {
			t.Fatalf("the run ended at its timeout of %d ms with %d bytes echoed: the command was sent its stdin and was never told that the input had ended", stdinBound, len(res.GetStdout()))
		}
		if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || string(res.GetStdout()) != "hello\nworld\n" || string(res.GetStderr()) != "12" {
			t.Errorf("result %v: want the stdin echoed and its end seen (12 bytes)", res)
		}
	})
	t.Run("a stdin larger than the input queue arrives whole", func(t *testing.T) {
		stdin := string(runSrvText(300 << 10))
		res := rig.mustRun(rig.request(runSrvStdin(stdin), runSrvTimeout(stdinBound), runSrvCaps(400<<10, 0, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD)))
		if res.GetOutcome() == execv2.Outcome_OUTCOME_TIMED_OUT {
			t.Fatalf("the run ended at its timeout of %d ms with %d of %d bytes echoed: the command was sent its stdin and was never told that the input had ended", stdinBound, len(res.GetStdout()), len(stdin))
		}
		if res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED || string(res.GetStdout()) != stdin || string(res.GetStderr()) != strconv.Itoa(len(stdin)) {
			t.Errorf("outcome %v, %d bytes echoed, stderr %q: want all %d bytes and then the end of input", res.GetOutcome(), len(res.GetStdout()), res.GetStderr(), len(stdin))
		}
	})
	t.Run("an empty stdin is no stdin", func(t *testing.T) {
		res := rig.mustRun(rig.request(runSrvTimeout(stdinBound)))
		if res.GetOutcome() != execv2.Outcome_OUTCOME_FAILED || res.GetFailed().GetClass() != execv2.ErrorClass_ERROR_CLASS_INTERNAL {
			t.Errorf("result %v: the function reports an error for a run with no stdin, which is a failure of the target", res)
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// the end: timeout, cancel, failure, loss
// ---------------------------------------------------------------------------------------------------------------

func TestServedRunEndsAtItsTimeoutWithTheOutputSoFar(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "partial")
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	res := rig.mustRun(rig.request(runSrvTimeout(50)))
	requireRunSrvResult(t, res, &execv2.RunResult{
		Outcome:          execv2.Outcome_OUTCOME_TIMED_OUT,
		ExitSource:       execv2.ExitSource_EXIT_SOURCE_HOST,
		Stdout:           []byte("partial"),
		StdoutAccount:    &execv2.OutputAccount{TotalBytes: 7, KeptBytes: 7, HeadBytes: 7, ValidUtf8: true},
		StderrAccount:    &execv2.OutputAccount{ValidUtf8: true},
		StartObserved:    true,
		RemoteMaySurvive: new(false), // the target declared that ending a run terminates the process
		Target:           fakeTargetFacts,
		Program:          &execv2.Program{Argv: []string{"prog", "a b"}, Name: "prog"},
	})
	if live := exec.RunEngineLive(rig.server.Plugin); len(live) != 0 {
		t.Errorf("the engine holds %v after the result: the place is free when the run has ended", live)
	}
}

// The timeout of a request that gives none is the target's default.
func TestServedRunWithNoTimeoutUsesTheTargetsDefault(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, runSrvLimits(exec.RunLimits{DefaultTimeout: 50_000_000, MaxTimeout: 60_000_000_000})))
	res := rig.mustRun(rig.request())
	if res.GetOutcome() != execv2.Outcome_OUTCOME_TIMED_OUT {
		t.Errorf("outcome %v, want TIMED_OUT at the target's default timeout of 50 ms", res.GetOutcome())
	}
}

// The call returns at its timeout whether or not the function does: a function that ignores its context is given up on, and it
// holds its place until it returns.
func TestServedRunReturnsAtItsTimeoutEvenIfTheFunctionNeverReturns(t *testing.T) {
	gate := exectest.NewGate()
	var runs atomic.Int32
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		runs.Add(1)
		_, _ = io.WriteString(p.Stdout, "stuck")
		<-gate.Opened() // looks at nothing else
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{MaxConcurrent: 1})))
	t.Cleanup(gate.Open) // registered after the rig: it runs before the rig waits for the functions that ignore their context

	started := time.Now()
	res := rig.mustRun(rig.request(runSrvTimeout(30)))
	// An upper bound, never a measure: a timeout is a kill, which does not wait out the grace (5 s) that a function that honors its
	// context is given, so the call returns long before it. A bound well under the grace tells the two apart.
	if took := time.Since(started); took > 3*time.Second {
		t.Errorf("the call took %v for a timeout of 30 ms: a timeout kills, and does not wait for a function that ignores its context", took)
	}
	if res.GetOutcome() != execv2.Outcome_OUTCOME_TIMED_OUT || string(res.GetStdout()) != "stuck" || res.GetExitSource() != execv2.ExitSource_EXIT_SOURCE_HOST {
		t.Fatalf("result %v, want TIMED_OUT from the host with the output so far", res)
	}
	if got := exec.RunEngineAbandoned(rig.server.Plugin); got != 1 {
		t.Errorf("%d functions abandoned, want the one that never returned", got)
	}
	if res.GetStdoutAccount().GetCompleteness() == completeAll {
		t.Error("the output of a function that was given up on is never complete")
	}

	// The function still holds the place of the run: the target is unavailable, not full, and nothing new ran.
	_, err := rig.run(rig.request(runSrvTimeout(30)))
	if err == nil {
		t.Fatal("a run was admitted on a target whose only place is held by a function that never returned")
	}
	if got := decodeRunSrvRefusal(t, err); got.class != "UNAVAILABLE" || got.code != codes.Unavailable {
		t.Errorf("refused with %+v, want UNAVAILABLE: the places are held, which is not a limit of concurrent runs", got)
	}
	if runs.Load() != 1 {
		t.Errorf("the function ran %d times, want once", runs.Load())
	}

	// It returns, and the place is free.
	gate.Open()
	eventually(t, "the abandoned function to return and its place to be free", func() bool {
		res, err := rig.run(rig.request(runSrvTimeout(20_000)))
		return err == nil && res.GetOutcome() == execv2.Outcome_OUTCOME_EXITED
	})
}

func TestServedRunThatTheCallerCancelsFreesItsPlace(t *testing.T) {
	entered := make(chan struct{})
	observed := make(chan struct{})
	var once atomic.Bool
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		if once.CompareAndSwap(false, true) {
			close(entered)
			<-ctx.Done()
			close(observed)
			return exec.Exit{}, ctx.Err()
		}
		return exec.ExitCode(0), nil
	}, runSrvLimits(exec.RunLimits{MaxConcurrent: 1})))

	ctx, cancel := context.WithCancel(rig.ctx())
	returned := make(chan error, 1)
	go func() {
		_, err := rig.raw.Run(ctx, rig.request())
		returned <- err
	}()
	awaitChannel(t, entered, "the run to start")
	cancel()
	if err := <-returned; status.Code(err) != codes.Canceled {
		t.Errorf("the cancelled call ended with %v, want the status Canceled", err)
	}
	awaitChannel(t, observed, "the function to see its context end")
	eventually(t, "the cancelled run to free its place", func() bool {
		res, err := rig.run(rig.request())
		return err == nil && res.GetOutcome() == execv2.Outcome_OUTCOME_EXITED
	})
}

func TestServedRunThatFailsAfterItsOutputIsAResultWithTheOutputSoFar(t *testing.T) {
	const canary = "CANARY-RUN-FAILURE-7c1e"
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "partial output")
		_, _ = io.WriteString(p.Stderr, "and a warning")
		return exec.Exit{}, &exec.Error{Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonTargetDenied, Err: fmt.Errorf("%s: raw cause", canary)}
	}))
	res := rig.mustRun(rig.request())
	requireRunSrvResult(t, res, &execv2.RunResult{
		Outcome: execv2.Outcome_OUTCOME_FAILED,
		Failed: &execv2.Failed{
			Class: execv2.ErrorClass_ERROR_CLASS_FORBIDDEN, Reason: execv2.ErrorReason_ERROR_REASON_TARGET_DENIED,
			Stage: execv2.FailureStage_FAILURE_STAGE_RUN,
		},
		Stdout:        []byte("partial output"),
		Stderr:        []byte("and a warning"),
		StdoutAccount: &execv2.OutputAccount{TotalBytes: 14, KeptBytes: 14, HeadBytes: 14, ValidUtf8: true},
		StderrAccount: &execv2.OutputAccount{TotalBytes: 13, KeptBytes: 13, HeadBytes: 13, ValidUtf8: true},
		StartObserved: true,
		Target:        fakeTargetFacts,
		Program:       &execv2.Program{Argv: []string{"prog", "a b"}, Name: "prog"},
	})
	if strings.Contains(prototext.Format(res), canary) {
		t.Error("the local cause of a failure reached the result")
	}
}

func TestServedRunThatFailsBeforeItsStartWasObservedIsAFailureAtTheStart(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		return exec.Exit{}, fmt.Errorf("could not reach the target")
	}))
	res := rig.mustRun(rig.request())
	requireRunSrvResult(t, res, &execv2.RunResult{
		Outcome:       execv2.Outcome_OUTCOME_FAILED,
		Failed:        &execv2.Failed{Class: execv2.ErrorClass_ERROR_CLASS_INTERNAL, Stage: execv2.FailureStage_FAILURE_STAGE_START},
		StdoutAccount: &execv2.OutputAccount{ValidUtf8: true},
		StderrAccount: &execv2.OutputAccount{ValidUtf8: true},
	})
}

func TestServedRunWhoseFunctionPanicsIsAFailureAndRevealsNothing(t *testing.T) {
	const canary = "CANARY-RUN-PANIC-93ab"
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "before")
		panic(canary)
	}))
	res := rig.mustRun(rig.request())
	if res.GetOutcome() != execv2.Outcome_OUTCOME_FAILED || res.GetFailed().GetClass() != execv2.ErrorClass_ERROR_CLASS_INTERNAL || string(res.GetStdout()) != "before" {
		t.Fatalf("result %v: want a failure of the target with the output so far", res)
	}
	if strings.Contains(prototext.Format(res), canary) {
		t.Error("the panic value reached the result")
	}
}

func TestServedRunWhoseStreamEndedWithNoStatusIsLostAndKeepsItsOutput(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Request.Labels["output"] == "yes" {
			_, _ = io.WriteString(p.Stdout, "some output")
		}
		return exec.ExitLost(exec.LostNoStatus), nil
	}))
	res := rig.mustRun(rig.request(runSrvLabel("output", "yes")))
	requireRunSrvResult(t, res, &execv2.RunResult{
		Outcome:          execv2.Outcome_OUTCOME_LOST,
		Stdout:           []byte("some output"),
		StdoutAccount:    &execv2.OutputAccount{TotalBytes: 11, KeptBytes: 11, HeadBytes: 11, ValidUtf8: true},
		StderrAccount:    &execv2.OutputAccount{ValidUtf8: true},
		StartObserved:    true,
		RemoteMaySurvive: new(true),
		Target:           fakeTargetFacts,
		Program:          &execv2.Program{Argv: []string{"prog", "a b"}, Name: "prog"},
	})
	silent := rig.mustRun(rig.request())
	if silent.GetOutcome() != execv2.Outcome_OUTCOME_LOST || silent.GetStartObserved() || silent.GetTarget() != nil || silent.GetProgram() != nil ||
		silent.GetRemoteMaySurvive() != true || silent.ExitCode != nil {
		t.Errorf("a stream lost before anything was seen: %v, want LOST with no observed start, no facts, no code and a process that may run", silent)
	}
}

// A plugin that shuts down while a run is under way answers the run with a failure of the plugin, not with a status: the
// command was handed to its function. Later runs are refused.
func TestServedRunThatTheShutdownOfThePluginEndsIsAFailureAndLaterRunsAreRefused(t *testing.T) {
	entered := make(chan struct{})
	var once atomic.Bool
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	answers := make(chan *execv2.RunResult, 1)
	go func() {
		res, err := rig.run(rig.request())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		answers <- res
	}()
	awaitChannel(t, entered, "the run to start")
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	if err := rig.server.Plugin.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	res := <-answers
	if res.GetOutcome() != execv2.Outcome_OUTCOME_FAILED || res.GetFailed().GetClass() != execv2.ErrorClass_ERROR_CLASS_UNAVAILABLE ||
		res.GetFailed().GetStage() != execv2.FailureStage_FAILURE_STAGE_RUN || string(res.GetStdout()) != "x" {
		t.Errorf("result %v: want FAILED, UNAVAILABLE at stage RUN, with the output so far", res)
	}
	_, err := rig.run(rig.request())
	requireRunSrvRefusal(t, err, codes.Unavailable, "UNAVAILABLE", "")
}

// A retired connection ends the runs that use it, with a failure that says so.
func TestServedRunThatTheRetirementOfItsConnectionEndsIsAFailureOfTheConnection(t *testing.T) {
	entered := make(chan struct{})
	var once atomic.Bool
	rig := newRunSrvRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = io.WriteString(p.Stdout, "x")
		if once.CompareAndSwap(false, true) {
			close(entered)
		}
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	answers := make(chan *execv2.RunResult, 1)
	go func() {
		res, err := rig.run(rig.request())
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		answers <- res
	}()
	awaitChannel(t, entered, "the run to start")
	rig.conns.Retire()
	res := <-answers
	failed := res.GetFailed()
	if res.GetOutcome() != execv2.Outcome_OUTCOME_FAILED || failed.GetClass() != execv2.ErrorClass_ERROR_CLASS_CONNECTION ||
		failed.GetReason() != execv2.ErrorReason_ERROR_REASON_CONNECTION_RETIRED || failed.GetStage() != execv2.FailureStage_FAILURE_STAGE_RUN {
		t.Errorf("result %v: want FAILED, CONNECTION / CONNECTION_RETIRED at stage RUN", res)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// refusals: a status, nothing ran, and the id space and the limits of runs
// ---------------------------------------------------------------------------------------------------------------

func TestServedRunRefusesWhatItCannotAcceptWithATypedStatusAndRunsNothing(t *testing.T) {
	var ran atomic.Int32
	rig := newRunSrvRig(t,
		engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			ran.Add(1)
			return exec.ExitCode(0), nil
		}, func(info *exec.TargetInfo) { info.Capabilities.Env = false }),
		exectest.NewFakeTarget(exectest.FakeRouting("fake", "sessions-only"), exectest.FakeInfo(exec.TargetInfo{
			DisplayName: "Sessions only", Capabilities: exec.Capabilities{Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates},
			Session: exec.DefaultSessionLimits(),
		})),
	)
	max := exec.DefaultRunLimits()
	unknownField := func(r *execv2.RunRequest) {
		r.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
	}
	for _, tc := range []struct {
		name          string
		mod           func(*execv2.RunRequest)
		code          codes.Code
		class, reason string
	}{
		{"no argv", runSrvArgv(), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"an empty program", runSrvArgv("", "x"), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a NUL in an argument", runSrvArgv("prog", "a\x00b"), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a request id that is not printable ASCII", runSrvID("caf\u00e9"), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a request id of 129 bytes", runSrvID(strings.Repeat("a", 129)), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a timeout above the target's maximum", runSrvTimeout(uint32(max.MaxTimeout.Milliseconds()) + 1), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a stdout cap above the target's maximum", runSrvCaps(uint32(max.MaxOutputBytes)+1, 0, 0), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"a stderr cap above the target's maximum", runSrvCaps(0, uint32(max.MaxOutputBytes)+1, 0), codes.InvalidArgument, "INVALID_REQUEST", ""},
		{"an output policy this build does not know", runSrvCaps(0, 0, execv2.TruncatePolicy(99)), codes.Unimplemented, "UNSUPPORTED", ""},
		{"a stdin above the target's maximum", func(r *execv2.RunRequest) { r.Stdin = make([]byte, max.MaxStdinBytes+1) }, codes.ResourceExhausted, "LIMIT", "INPUT_TOO_LARGE"},
		{"an env for a target that does not honor one", func(r *execv2.RunRequest) { r.Env = []*execv2.NameValue{{Name: "A", Value: "b"}} }, codes.Unimplemented, "UNSUPPORTED", ""},
		{"a field this build does not know", unknownField, codes.Unimplemented, "UNSUPPORTED", ""},
		{"a target this plugin does not have", func(r *execv2.RunRequest) {
			r.Target = rig.conns.WireTarget(t, "nothing", "x-1")
		}, codes.Unimplemented, "UNSUPPORTED", ""},
		{"a target that serves no runs", func(r *execv2.RunRequest) {
			r.Target = rig.conns.WireTarget(t, "sessions-only", "thing-1")
		}, codes.Unimplemented, "UNSUPPORTED", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := ran.Load()
			req := rig.request(tc.mod)
			_, err := rig.run(req)
			requireRunSrvRefusal(t, err, tc.code, tc.class, tc.reason)
			if ran.Load() != before {
				t.Error("the author's function ran for a request that was refused")
			}
			if live := exec.RunEngineLive(rig.server.Plugin); len(live) != 0 {
				t.Errorf("a refused request left %v behind", live)
			}
		})
	}
}
