package exectest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ConformanceOption configures Conformance.
type ConformanceOption func(*conformanceConfig)

type conformanceConfig struct {
	id string
}

// ConformanceResource names the thing the Options callback is asked about (default "conformance-target"). A target
// whose callback looks the thing up in a test double gives the id that double knows.
func ConformanceResource(id string) ConformanceOption {
	return func(c *conformanceConfig) { c.id = id }
}

// Conformance runs the conformance suite against an author's Target, through the real SDK: the target is served as
// a plugin over a real transport (a unix socket) behind the SDK's own middleware, a fake connection registry stands
// in for the resource provider, and the cases call it as the host does. An author runs it in their own go test:
//
//	func TestConformance(t *testing.T) { exectest.Conformance(t, myTarget()) }
//
// The cases that need a session or a run (the numbered cases 1 to 28, the pair tests and the stress harness) are listed
// under "engine", and each runs, or skips with the step that fills it.
//
// Over the wire (a subtest "over the wire", run when the build lists FEATURE_SESSION): case 1 (Opened is first and its facts
// agree with Describe), case 2 (input and resize frames reach the author in order), case 3 (output offsets are contiguous and
// the bytes untouched), case 4 (a slow consumer bounds memory and a blocking target drops nothing; skipped for a target that
// declares STALL_LOSSY), case 5 (one session's stall does not move another's echo) and case 18 (completeness: a lossy target
// counts what it lost, a blocking one ends COMPLETE). They open sessions with Client.Open against a fake target that carries
// the Info you declared and a script of its own.
//
// The bounded run over the wire (the same subtest, run when the build lists FEATURE_RUN, and what RunStream adds when it lists
// FEATURE_RUN_STREAM): case 15 (Run: an empty argv and every bound refused and never clamped, the exit as the target reported it,
// a timeout and a cancel that end the run, the capture of a head and a tail with an exact account per stream, valid_utf8 of the
// bytes as returned, the limit on runs in flight and the id of a run), the Run part of case 13 (class, reason and facts round-trip,
// and a canary in the local cause of an error or in a panic appears nowhere), the Run part of case 16 (a stream that ends with no
// status ends a run OUTCOME_LOST, never exit 0), case 27 (a failure after the command was handed to the target is a result with
// OUTCOME_FAILED, the partial output and its completeness, while a refusal before it is a status), the pair test P1 (Run equals
// Open without a terminal collected to the end) and P3 (the engine over gRPC equals the engine in process). They skip for a target
// that declares no runs.
//
// In this process (a subtest "in process"): case 9 (every kind of end is typed, a command has one, nothing follows it), case
// 14 (a retired connection ends its commands), case 28 (remote_may_survive per kind of end), the pair test P5 (ending twice,
// after the exit and during the start give the same end) and the stress harness (Stress), against the real engine.
//
// Your Exec is never called by any of them, so what you declared decides what they expect: a signal is expected only when
// ExitReport is CODE_AND_SIGNAL, and a Closed end says the process is gone only when Kill is TERMINATES. The stress harness
// runs the SDK's own scripted targets: it checks the end state of the engine, not your declaration, for 5 seconds (1 under
// -short, or as long as OMNIVIEW_LIFECYCLE_HAMMER says). A target that declares neither sessions nor runs has nothing to run,
// so they skip.
//
// Skipped with their step: the wire part of cases 9, 12, 14, 17, 23, 25, 26, 28 and of P5, the Open part of cases 13 and 16 (the
// in-process parts of 9, 14, 28 and P5 and the Run parts of 13 and 16 run), every case of the steps that are not served yet (6 to
// 8, 10, 11, 19 to 22, 24 and the other pair tests). A build that lists a feature in Describe whose cases are not written fails
// instead of skipping, so a feature cannot be served without being checked.
func Conformance(t *testing.T, target exec.Target, opts ...ConformanceOption) {
	t.Helper()
	cfg := conformanceConfig{id: "conformance-target"}
	for _, opt := range opts {
		opt(&cfg)
	}
	connections := exectestkit.NewConnectionsT(t, target.Info.Provider)
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}, Connections: connections})
	newClient := func(t testing.TB) exec.Client { return server.V2(t) }

	t.Run("registration", func(t *testing.T) { caseRegistration(t, newClient(t), target, connections) })
	exectestkit.RunClientSuite(t, newClient, exectestkit.ClientSuiteOptions{
		Context: connections.Context,
		Ref: func(info *execv2.TargetInfo) (exec.TargetRef, bool) {
			if info.GetProvider() != target.Info.Provider || info.GetType() != target.Info.Type || target.Options == nil {
				return exec.TargetRef{}, false
			}
			return connections.TargetRef(target.Info.Type, cfg.id), true
		},
	})
	env := &inProcessEnv{resource: cfg.id}
	wire := &wireEnv{resource: cfg.id}
	t.Run("engine", func(t *testing.T) {
		listEngineCases(t, target, env, wire, func(t testing.TB) *execv2.DescribeResponse {
			t.Helper()
			description, err := newClient(t).Describe(connections.Context(context.Background()))
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			return description
		})
	})
	if target.Options != nil {
		t.Run("options wiring", func(t *testing.T) { caseOptionsWiring(t, newClient(t), server, target, connections, cfg.id) })
	}
}

// caseRegistration: Describe reports the target as declared, and claims no capability that was not declared.
func caseRegistration(t *testing.T, client exec.Client, target exec.Target, connections *exectestkit.Connections) {
	t.Helper()
	description, err := client.Describe(connections.Context(context.Background()))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if description.GetContractVersion() != exec.ContractVersion {
		t.Errorf("contract version %d, the SDK implements %d", description.GetContractVersion(), exec.ContractVersion)
	}
	declared := target.Info
	var info *execv2.TargetInfo
	for _, candidate := range description.GetTargets() {
		if candidate.GetProvider() == declared.Provider && candidate.GetType() == declared.Type {
			info = candidate
		}
	}
	if info == nil || len(description.GetTargets()) != 1 {
		t.Fatalf("Describe lists %d targets and not exactly %s/%s", len(description.GetTargets()), declared.Provider, declared.Type)
	}
	if info.GetDisplayName() != declared.DisplayName || !slices.Equal(info.GetDefaultArgv(), declared.DefaultArgv) {
		t.Errorf("Describe changed the display name or the default argv: %v, declared %+v", info, declared)
	}
	if diffs := capabilityDiffs(info.GetCapabilities(), declared.Capabilities); len(diffs) > 0 {
		t.Errorf("Describe does not report the capabilities as declared: %s", strings.Join(diffs, "; "))
	}
	if declared.Session.IsZero() != (info.GetSession() == nil) {
		t.Errorf("sessions are declared (%v) and described (%v) differently", !declared.Session.IsZero(), info.GetSession() != nil)
	}
	if declared.Run.IsZero() != (info.GetRun() == nil) {
		t.Errorf("runs are declared (%v) and described (%v) differently", !declared.Run.IsZero(), info.GetRun() != nil)
	}
	if session := info.GetSession(); session != nil {
		if zero := zeroFields(session); len(zero) > 0 {
			t.Errorf("session limits are not effective, Describe left %v at zero: %v", zero, session)
		}
		for name, pair := range map[string][2]int64{
			"MaxSessions":    {int64(declared.Session.MaxSessions), int64(session.GetMaxSessions())},
			"MaxInputFrame":  {int64(declared.Session.MaxInputFrame), int64(session.GetMaxInputFrame())},
			"ReplayBytes":    {int64(declared.Session.ReplayBytes), int64(session.GetReplayBytes())},
			"MaxReplayBytes": {int64(declared.Session.MaxReplayBytes), int64(session.GetMaxReplayBytes())},
			"MaxDimension":   {int64(declared.Session.MaxDimension), int64(session.GetMaxDimension())},
		} {
			if pair[0] != 0 && pair[0] != pair[1] {
				t.Errorf("session limit %s was declared %d and is described %d", name, pair[0], pair[1])
			}
		}
	}
	if run := info.GetRun(); run != nil {
		if zero := zeroFields(run); len(zero) > 0 {
			t.Errorf("run limits are not effective, Describe left %v at zero: %v", zero, run)
		}
	}
	for _, feature := range description.GetFeatures() {
		if _, named := execv2.Feature_name[int32(feature)]; !named || feature == execv2.Feature_FEATURE_UNSPECIFIED {
			t.Errorf("Describe lists feature %s, which this SDK does not know", feature)
		}
	}
}

// zeroFields lists the fields of a wire message that are not set. In a message of proto3 scalars a field that is
// not set is a field that is zero.
func zeroFields(m proto.Message) []string {
	reflected := m.ProtoReflect()
	var zero []string
	fields := reflected.Descriptor().Fields()
	for i := range fields.Len() {
		if !reflected.Has(fields.Get(i)) {
			zero = append(zero, string(fields.Get(i).Name()))
		}
	}
	return zero
}

// capabilityDiffs compares the capabilities a plugin described with the ones its author declared, field by field, and
// lists the differences. The SDK writes every field, so a field that is absent is a difference; the Go enums name
// their values as the wire does, so an enum is compared by name.
func capabilityDiffs(got *execv2.Capabilities, want exec.Capabilities) []string {
	if got == nil {
		return []string{"no capabilities are described"}
	}
	var diffs []string
	flag := func(name string, described *bool, declared bool) {
		switch {
		case described == nil:
			diffs = append(diffs, fmt.Sprintf("%s is absent, declared %t", name, declared))
		case *described != declared:
			diffs = append(diffs, fmt.Sprintf("%s is %t, declared %t", name, *described, declared))
		}
	}
	enum := func(name string, described fmt.Stringer, prefix string, declared fmt.Stringer) {
		if strings.TrimPrefix(described.String(), prefix) != declared.String() {
			diffs = append(diffs, fmt.Sprintf("%s is %s, declared %s", name, described, declared))
		}
	}
	flag("tty", got.Tty, want.TTY)
	flag("separate_stderr", got.SeparateStderr, want.SeparateStderr)
	flag("resize", got.Resize, want.Resize)
	flag("initial_size", got.InitialSize, want.InitialSize)
	flag("stdin_eof", got.StdinEof, want.StdinEOF)
	flag("env", got.Env, want.Env)
	flag("cwd", got.Cwd, want.Cwd)
	flag("link_health", got.LinkHealth, want.LinkHealth)
	enum("kill", got.GetKill(), "KILL_BEHAVIOR_", want.Kill)
	enum("cwd_fact", got.GetCwdFact(), "FACT_MODE_", want.CwdFact)
	enum("foreground_fact", got.GetForegroundFact(), "FACT_MODE_", want.ForegroundFact)
	enum("host_restart", got.GetHostRestart(), "SURVIVAL_", want.HostRestart)
	enum("exit_report", got.GetExitReport(), "EXIT_REPORT_", want.ExitReport)
	enum("stall", got.GetStall(), "STALL_BEHAVIOR_", want.Stall)
	if got.GetSignals() == nil {
		diffs = append(diffs, "signals is absent")
	} else if names := signalNames(got.GetSignals().GetSignals()); !slices.Equal(names, signalDeclared(want.Signals)) {
		diffs = append(diffs, fmt.Sprintf("signals are %v, declared %v", names, signalDeclared(want.Signals)))
	}
	if got.GetIntegration() == nil {
		diffs = append(diffs, "integration is absent")
	} else {
		var described, declared []string
		for _, m := range got.GetIntegration().GetModes() {
			described = append(described, strings.TrimPrefix(m.String(), "INTEGRATION_MODE_"))
		}
		for _, m := range want.Integration {
			declared = append(declared, m.String())
		}
		if !slices.Equal(described, declared) {
			diffs = append(diffs, fmt.Sprintf("integration is %v, declared %v", described, declared))
		}
	}
	return diffs
}

func signalNames(signals []execv2.Signal) []string {
	var names []string
	for _, s := range signals {
		names = append(names, strings.TrimPrefix(s.String(), "SIGNAL_"))
	}
	return names
}

func signalDeclared(signals []exec.Signal) []string {
	var names []string
	for _, s := range signals {
		names = append(names, s.String())
	}
	return names
}

// caseOptionsWiring: Options is admitted like any resource utility call: the connection is the caller's, and the
// catalog pin the host sent is the current one.
func caseOptionsWiring(t *testing.T, client exec.Client, server *exectestkit.Server, target exec.Target, connections *exectestkit.Connections, id string) {
	t.Helper()
	ref := connections.TargetRef(target.Info.Type, id)

	t.Run("refused without the caller's connection", func(t *testing.T) {
		_, err := client.Options(context.Background(), ref)
		var resourceErr *resourcev2.ResourceOperationError
		if err == nil || !errors.As(err, &resourceErr) {
			t.Fatalf("Options without a connection: %v, want a resource-lane refusal", err)
		}
	})
	t.Run("refused with a stale catalog pin", func(t *testing.T) {
		pinned := connections.Context(context.Background())
		connections.SetRevision("catalog-conformance-2")
		t.Cleanup(func() { connections.SetRevision("catalog-1") })
		_, err := client.Options(pinned, ref)
		var resourceErr *resourcev2.ResourceOperationError
		if err == nil || !errors.As(err, &resourceErr) || resourceErr.Code != "RESET_REQUIRED" {
			t.Fatalf("Options with a stale pin: %v, want RESET_REQUIRED", err)
		}
	})
	t.Run("refuses a selection", func(t *testing.T) {
		// The SDK's client has no way to send a selection, so the request goes through the generated client.
		ctx, err := resourceplugin.OutgoingSessionContext(connections.Context(context.Background()))
		if err != nil {
			t.Fatalf("the call's context: %v", err)
		}
		wire := &execv2.OptionsRequest{Target: connections.WireTarget(t, target.Info.Type, id, &execv2.NameValue{Name: "container", Value: "app"})}
		_, err = server.Raw().Options(ctx, wire)
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Fatalf("Options with a selection: %v, want an invalid request", err)
		}
	})
}

// caseRow is one case of the suite that needs the engine: its number (0 for a pair test or the stress harness), what it checks,
// the step that writes what is unwritten of it, and the feature that step makes the build list.
type caseRow struct {
	number   int
	name     string
	step     string
	requires execv2.Feature
}

// engineCase is a row of the skeleton and the bodies that run now: the one that runs the engine in this process, and the one that
// drives a plugin over a transport as a host does. The row's step and feature gate what is left of the case; a step of "" says that
// nothing is left.
type engineCase struct {
	caseRow
	// inProcess runs the case against the real engine in this process, with the author's declared target. It is nil for a case
	// that has none.
	inProcess func(t checkT, target exec.Target, env *inProcessEnv)
	// wire runs the case against a plugin served over a transport, with a fake that carries the author's declared description. It
	// is nil for a case that has none.
	wire func(t checkT, target exec.Target, env *wireEnv)
	// wireNeeds are the features that the build must list for wire to run; none means FEATURE_SESSION, which serves Open.
	wireNeeds []execv2.Feature
}

// wireFeatures returns the features the build must list for the wire body of the case to run.
func (c engineCase) wireFeatures() []execv2.Feature {
	if len(c.wireNeeds) == 0 {
		return []execv2.Feature{execv2.Feature_FEATURE_SESSION}
	}
	return c.wireNeeds
}

// caseT is the part of testing.T that an engine case uses to say how it ended.
type caseT interface {
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
}

// conclude ends what is left of the case, as the build calls for: a case with no step has nothing left; a row that names no feature
// always skips with its step; a build that lists the feature its step makes it list has the case fail, because what is left of it is
// not written and a feature is not served until its conformance cases run; any other build skips it.
func (c engineCase) conclude(t caseT, served []execv2.Feature) {
	switch {
	case c.step == "":
		return
	case c.requires == execv2.Feature_FEATURE_UNSPECIFIED:
		t.Skipf("step %s: not written by this SDK build", c.step)
		return
	case slices.Contains(served, c.requires):
		t.Fatalf("the SDK build lists %s but this case (step %s) is not written: a feature is not served until its conformance cases run", c.requires, c.step)
		return
	}
	t.Skipf("step %s: the SDK build does not serve %s yet", c.step, c.requires)
}

// The names of the cases that carry no number, which the bodies are found by.
const (
	pairRunOpenName    = "pair P1: Run(argv) equals Open(argv, tty=false) collected to the end"
	pairEngineWireName = "pair P3: the engine over gRPC equals the engine in process"
	pairEndingName     = "pair P5: ending twice, after the exit and during the start give the same final Ended"
	stressName         = "stress: N workers loop open, input, resize, signal, detach, attach, end, run, cancel; the end-state invariant holds"
)

// engineCases is the skeleton of the suite: the plan's numbered cases 1 to 24 and the cases 25 to 28 that the review of
// the contract added, the pair tests P1 to P6 and the stress harness, each with the step that writes it and the feature
// that step makes the build list. Cases 9, 14, 28, P5 and the stress run in process now, and cases 1 to 5, 13 (the Run part), 15,
// 16 (the Run part), 18, 27 and the pairs P1 and P3 over the wire: the step of each case that has one names what is left of it.
func engineCases() []engineCase {
	management := execv2.Feature_FEATURE_SESSION_MANAGEMENT
	// A row whose step is "" is complete: its bodies are all of it. A row whose feature is FEATURE_UNSPECIFIED is one whose step
	// lists no feature of its own (a step of the host, or the wire body of a case that is not written yet): it always skips with
	// its step.
	none := execv2.Feature_FEATURE_UNSPECIFIED
	rows := []caseRow{
		{1, "Opened is first and its facts agree with Describe", "", none},
		{2, "input and resize frames reach the author in order", "", none},
		{3, "output offsets are contiguous and the bytes untouched", "", none},
		{4, "a slow consumer bounds memory; PROPAGATES blocks Write and drops nothing", "", none},
		{5, "a stuck, slow or flooding session does not move another session's echo", "", none},
		{6, "resize: a storm delivers at most two sizes, the last seq is acknowledged, the initial size is first", "X3b", execv2.Feature_FEATURE_STDIN_EOF},
		{7, "signals only as declared, Close graceful and kill, remote_may_survive agrees with the capability", "X3b", execv2.Feature_FEATURE_STDIN_EOF},
		{8, "stdin EOF", "X3b", execv2.Feature_FEATURE_STDIN_EOF},
		{9, "a session has exactly one end and at most one Ended (the frame, last on its stream, when a stream is attached at the end; the tombstone when none is), typed for every kind of end (exited, signaled, closed, failed, lost), counts exact", "X3a (the wire body) and X3c (the tombstone)", none},
		{10, "detach and reattach: replay plus live is one uninterrupted transcript; a tombstone is readable", "X3c", management},
		{11, "the lease ends a detached session at the fake-clock timeout", "X3c", management},
		{12, "limits: too many sessions, input too large, env or cwd unsupported are typed", "X3a (the wire body)", none},
		{13, "errors: class, reason and facts round-trip and a canary in Error.Err appears nowhere", "X3a (the Open wire body)", none},
		{14, "a retired connection ends its sessions", "X3a (the wire body)", none},
		{15, "Run: empty argv, bounds, timeout, cancel, capture accounting, valid_utf8, concurrency", "", none},
		{16, "lost: a stream that ends with no status ends Lost, never Exited 0, and a run ends OUTCOME_LOST", "X3a (the Open wire body)", none},
		{17, "exit source and report round-trip; a Signaled from a CODE target is a contract violation", "X3a (the wire body)", none},
		{18, "completeness: LOSSY drains into the ring and counts; PROPAGATES blocks and ends COMPLETE", "", none},
		{19, "marks: nonce trust, command ids, D without C, a foreign 133, and the nonce appears nowhere", "X4a", execv2.Feature_FEATURE_MARKS},
		{20, "integration: no wrapper without consent; the wrapper runs the requested shell", "X4b", execv2.Feature_FEATURE_INTEGRATION},
		{21, "attach is atomic: replay.bytes == end - start, replay plus live is the transcript", "X3c", management},
		{22, "detach: the stream ends and not the session, Detached is last, the lease is clamped", "X3c", management},
		{23, "link: sent on a change only; a stalled link that ends the session ends it Lost", "X3a (the wire body)", none},
		{24, "mode_reset: the exact bytes with FEATURE_MODES and the fixed full reset without it, present and empty for a session without a terminal; the step pins the fixed string in its own test", "X4a", execv2.Feature_FEATURE_MODES},
		{25, "every stream that can still receive frames ends with exactly one of Ended or Detached; a session has at most one Ended, none when it ends while no stream is attached (its end is the tombstone's), never to a detached or superseded stream (X3a: one stream, X3c: attach, supersede, detach)", "X3a (the one-stream wire body) and X3c", none},
		{26, "capabilities narrow per session: exec and attach on one Pod kind get different ExitReport and HostRestart in Opened (from Choice.capabilities), not in Started, which narrows only what is learned at run time; a widening is clamped, a choice's and a Started's alike", "X3a (the wire body)", none},
		{27, "a run failure after the command was handed to the target is a RunResult with OUTCOME_FAILED carrying partial output, completeness, start_observed and remote_may_survive; a refusal before the hand-off is a status; a host never retries a failed or lost end", "", none},
		{28, "remote_may_survive per end kind: Lost true, process exit and signal false, transport exit and failure absent, closed false only when the session's Kill is TERMINATES; absent is never read as no (X2a: the end mapping, X3a: sessions)", "X3a (the wire body)", none},
		{0, pairRunOpenName, "", none},
		{0, "pair P2: Open, detach, attach(from_offset) equals one uninterrupted Open", "X3c", management},
		{0, pairEngineWireName, "", none},
		{0, "pair P4: the host's local provider equals the engine with a pty-backed fake", "H1a-2", none},
		{0, pairEndingName, "X3a (the wire body)", none},
		{0, "pair P6: the legacy adapter equals the new lane on the same fake remote, for the bytes", "H1a-1", none},
		{0, stressName, "X3b and X3c", management},
	}
	cases := make([]engineCase, len(rows))
	for i, row := range rows {
		cases[i] = engineCase{caseRow: row, inProcess: inProcessBody(row), wire: wireBody(row), wireNeeds: wireFeatures(row)}
	}
	return cases
}

// inProcessBody is the body that the engine satisfies for a row, or nil for a row that is only listed.
func inProcessBody(row caseRow) func(t checkT, target exec.Target, env *inProcessEnv) {
	switch {
	case row.number == 9:
		return caseEveryEndIsTyped
	case row.number == 14:
		return caseRetiredConnection
	case row.number == 28:
		return caseSurvival
	case row.name == pairEndingName:
		return casePairEnding
	case row.name == stressName:
		return caseStressEntry
	}
	return nil
}

// wireBody is the body that drives a plugin over a transport for a row, or nil for a row that has none yet.
func wireBody(row caseRow) func(t checkT, target exec.Target, env *wireEnv) {
	switch {
	case row.number == 1:
		return caseWireOpened
	case row.number == 2:
		return caseWireInput
	case row.number == 3:
		return caseWireOutput
	case row.number == 4:
		return caseWireSlowConsumer
	case row.number == 5:
		return caseWireIsolation
	case row.number == 13:
		return caseWireErrorsRun
	case row.number == 15:
		return caseWireRun
	case row.number == 16:
		return caseWireLostRun
	case row.number == 18:
		return caseWireCompleteness
	case row.number == 27:
		return caseWireRunFailure
	case row.name == pairRunOpenName:
		return casePairRunOpen
	case row.name == pairEngineWireName:
		return casePairEngineWire
	}
	return nil
}

// wireFeatures is what the build must list for the wire body of a row to run: FEATURE_SESSION for the cases that open a session,
// FEATURE_RUN for the cases of the bounded run, and both for the pair that runs a command both ways.
func wireFeatures(row caseRow) []execv2.Feature {
	session, run := execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_RUN
	switch {
	case row.name == pairRunOpenName:
		return []execv2.Feature{session, run}
	case row.number == 13, row.number == 15, row.number == 16, row.number == 27, row.name == pairEngineWireName:
		return []execv2.Feature{run}
	}
	return []execv2.Feature{session}
}

// listEngineCases lists the engine cases as subtests. A case that has bodies runs them first: the one that runs the engine in this
// process in a subtest "in process", and the one that drives a plugin over a transport in a subtest "over the wire" (skipped when
// the build does not list the features that the case drives: FEATURE_SESSION for Open, FEATURE_RUN for Run), each passing or
// failing. What is left of the case is skipped while the build does not list the feature its step makes it list, and a build that
// lists the feature without that written fails the case (see conclude).
func listEngineCases(t *testing.T, target exec.Target, env *inProcessEnv, wire *wireEnv, describe func(t testing.TB) *execv2.DescribeResponse) {
	t.Helper()
	listCases(t, engineCases(), target, env, wire, describe)
}

// listCases is listEngineCases for the cases it is given.
func listCases(t *testing.T, cases []engineCase, target exec.Target, env *inProcessEnv, wire *wireEnv, describe func(t testing.TB) *execv2.DescribeResponse) {
	t.Helper()
	for _, c := range cases {
		name := c.name
		if c.number > 0 {
			name = fmt.Sprintf("case %02d: %s", c.number, c.name)
		}
		t.Run(name, func(t *testing.T) {
			if c.inProcess != nil {
				t.Run("in process", func(t *testing.T) { c.inProcess(t, target, env) })
			}
			features := describe(t).GetFeatures()
			if c.wire != nil {
				t.Run("over the wire", func(t *testing.T) {
					for _, need := range c.wireFeatures() {
						if !slices.Contains(features, need) {
							t.Skipf("the SDK build does not list %s: it does not serve what this case drives", need)
						}
					}
					c.wire(t, target, wire)
				})
			}
			c.conclude(t, features)
		})
	}
}
