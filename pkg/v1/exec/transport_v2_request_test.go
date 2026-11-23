package exec

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

// The tests of the bounds of a request that every target shares (this file, with the fixtures every request test
// uses) and of the strict admission (admission_request_v2_test.go). The numbers in the tables are the numbers of the
// contract (proto/exec/v2/exec.proto), written out on purpose and never read from the constants of limits.go: a test
// that shared the constant of the code under test could not see the constant drift.

// The limits of the base target of the request tests. They differ from the bounds of the contract and from the SDK's
// defaults, so that a row shows which of the two a check reads.
const (
	rqtMaxDimension     = 500
	rqtMaxDetachMillis  = 600_000 // ten minutes
	rqtMaxReplayBytes   = 2 << 20
	rqtRunArgvBytes     = 100_000
	rqtRunEnvBytes      = 100_000
	rqtRunStdinBytes    = 64 << 10
	rqtRunTimeoutMillis = 300_000 // five minutes
	rqtRunOutputBytes   = 512 << 10
)

// The features of the contract, under the names the tables use.
const (
	rqtFeatureMarks             = execv2.Feature_FEATURE_MARKS
	rqtFeatureSessionManagement = execv2.Feature_FEATURE_SESSION_MANAGEMENT
	rqtFeatureRunStream         = execv2.Feature_FEATURE_RUN_STREAM
	rqtFeatureEnv               = execv2.Feature_FEATURE_ENV
	rqtFeatureCwd               = execv2.Feature_FEATURE_CWD
	rqtFeatureStdinEOF          = execv2.Feature_FEATURE_STDIN_EOF
	rqtFeatureModes             = execv2.Feature_FEATURE_MODES
	rqtFeatureIntegration       = execv2.Feature_FEATURE_INTEGRATION
	rqtFeatureSession           = execv2.Feature_FEATURE_SESSION
	rqtFeatureRun               = execv2.Feature_FEATURE_RUN
)

// rqtAllFeatures lists every feature of the contract: what a build that serves the whole lane lists in Describe.
// TestEveryFeatureOfTheContractIsInTheTables keeps it complete.
var rqtAllFeatures = []execv2.Feature{
	rqtFeatureMarks, rqtFeatureSessionManagement, rqtFeatureRunStream, rqtFeatureEnv, rqtFeatureCwd,
	rqtFeatureStdinEOF, rqtFeatureModes, rqtFeatureIntegration, rqtFeatureSession, rqtFeatureRun,
}

// rqtServed returns the features of a build that serves the whole lane except the given ones.
func rqtServed(without ...execv2.Feature) featureSet {
	served := make([]execv2.Feature, 0, len(rqtAllFeatures))
	for _, f := range rqtAllFeatures {
		if !slices.Contains(without, f) {
			served = append(served, f)
		}
	}
	return newFeatureSet(served)
}

// rqtBaseTarget is a target that serves sessions and runs and can do everything a request may ask for.
func rqtBaseTarget() Target {
	return Target{
		Info: TargetInfo{
			Provider:    "k8s",
			Type:        "pods",
			DisplayName: "Pod",
			DefaultArgv: []string{"/bin/sh"},
			Capabilities: Capabilities{
				TTY:         true,
				Env:         true,
				Cwd:         true,
				Integration: []IntegrationMode{IntegrationArgvWrapper},
				Kill:        KillTerminates,
				ExitReport:  ExitReportCodeAndSignal,
				Stall:       StallPropagates,
			},
			Session: SessionLimits{
				MaxSessions:      8,
				MaxInputFrame:    64 << 10,
				ReplayBytes:      1 << 20,
				MaxReplayBytes:   rqtMaxReplayBytes,
				DetachTimeout:    5 * time.Minute,
				MaxDetachTimeout: rqtMaxDetachMillis * time.Millisecond,
				MaxDimension:     rqtMaxDimension,
			},
			Run: RunLimits{
				DefaultTimeout:     30 * time.Second,
				MaxTimeout:         rqtRunTimeoutMillis * time.Millisecond,
				DefaultOutputBytes: 256 << 10,
				MaxOutputBytes:     rqtRunOutputBytes,
				MaxStdinBytes:      rqtRunStdinBytes,
				MaxConcurrent:      8,
				MaxArgvBytes:       rqtRunArgvBytes,
				MaxEnvBytes:        rqtRunEnvBytes,
				PostExitDrain:      time.Second,
			},
		},
		Exec: func(context.Context, *Proc) (Exit, error) { return ExitCode(0), nil },
	}
}

// rqtTarget registers the base target, changed by mutate when it is not nil, in a real registry and returns both.
func rqtTarget(t testing.TB, mutate func(*Target)) (*targetRegistry, *registeredTarget) {
	t.Helper()
	target := rqtBaseTarget()
	if mutate != nil {
		mutate(&target)
	}
	reg, err := newTargetRegistry([]Target{target})
	require.NoError(t, err)
	registered, ok := reg.lookup("k8s", "pods")
	require.True(t, ok)
	return reg, registered
}

// rqtGoTarget is a canonical target reference to a Pod of the base target.
func rqtGoTarget() TargetRef {
	incarnation := "uid-1"
	return TargetRef{
		Reference: resourcev2.ResourceReference{
			Address: resourcev2.ResourceAddress{
				Provider:  "k8s",
				Authority: "auth",
				Type:      "pods",
				Scope:     []resourcev2.ScopeEntry{{Dimension: "namespace", Value: "default"}},
				ID:        "pod-1",
			},
			Incarnation: &incarnation,
		},
		Route: resourcev2.AccessRoute{Connection: "conn-1"},
	}
}

// rqtGoStart is a Start as admission returns it, with every field set, valid against the base target.
func rqtGoStart() startRequest {
	return startRequest{
		Target:        rqtGoTarget(),
		Selection:     map[string]string{"container": "app"},
		Argv:          []string{"/bin/bash", "-l"},
		TTY:           true,
		Size:          Size{Cols: 120, Rows: 40},
		Env:           map[string]string{"LANG": "C.UTF-8", "EDITOR_2": "vi"},
		Cwd:           "/work",
		Labels:        map[string]string{"purpose": "debug"},
		DetachTimeout: 90 * time.Second,
		ReplayBytes:   1 << 20,
		Features:      []execv2.Feature{rqtFeatureMarks, rqtFeatureModes},
		Integration:   &integrationRequest{Mode: IntegrationArgvWrapper, Consent: true},
	}
}

// rqtGoRun is a RunRequest as admission returns it, with every field set, valid against the base target.
func rqtGoRun() runRequest {
	return runRequest{
		RequestID: "req-1",
		Target:    rqtGoTarget(),
		Selection: map[string]string{"container": "app"},
		Argv:      []string{"/bin/echo", "hello"},
		Stdin:     []byte("input"),
		Env:       map[string]string{"LANG": "C.UTF-8"},
		Cwd:       "/work",
		Timeout:   2 * time.Minute,
		Output:    outputLimits{MaxStdoutBytes: 4096, MaxStderrBytes: 2048, Policy: execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL},
		Labels:    map[string]string{"purpose": "diagnose"},
	}
}

// rqtListOf returns the list of NameValue of m, ordered by name so that a message built twice is the same bytes and
// two messages built from the same map are equal. An empty map is no list.
func rqtListOf(m map[string]string) []*execv2.NameValue {
	if len(m) == 0 {
		return nil
	}
	out := make([]*execv2.NameValue, 0, len(m))
	for _, name := range slices.Sorted(maps.Keys(m)) {
		out = append(out, &execv2.NameValue{Name: name, Value: m[name]})
	}
	return out
}

// rqtWireOfTarget builds the wire form of a target reference and its selection field by field, without validating
// anything: it is the test's own encoder, so that a row can put a value on the wire that admission must refuse.
func rqtWireOfTarget(ref TargetRef, selection map[string]string) *execv2.TargetRef {
	address := &resourcepb.ResourceAddress{
		Provider:  ref.Reference.Address.Provider,
		Authority: ref.Reference.Address.Authority,
		Type:      ref.Reference.Address.Type,
		Id:        ref.Reference.Address.ID,
	}
	for _, s := range ref.Reference.Address.Scope {
		address.Scope = append(address.Scope, &resourcepb.ScopeEntry{Dimension: s.Dimension, Value: s.Value})
	}
	reference := &resourcepb.ResourceReference{Address: address}
	if ref.Reference.Incarnation != nil {
		incarnation := *ref.Reference.Incarnation
		reference.Incarnation = &incarnation
	}
	return &execv2.TargetRef{
		Reference: reference,
		Route:     &resourcepb.AccessRoute{Connection: ref.Route.Connection},
		Selection: rqtListOf(selection),
	}
}

// rqtWireOfStart is the test's own encoder of a Start: a field-by-field copy that validates nothing.
func rqtWireOfStart(s startRequest) *execv2.Start {
	wire := &execv2.Start{
		Target:          rqtWireOfTarget(s.Target, s.Selection),
		Argv:            slices.Clone(s.Argv),
		Tty:             s.TTY,
		Env:             rqtListOf(s.Env),
		Cwd:             s.Cwd,
		Labels:          rqtListOf(s.Labels),
		DetachTimeoutMs: uint32(s.DetachTimeout / time.Millisecond), //nolint:gosec // test data within the wire range
		ReplayBytes:     uint32(s.ReplayBytes),                      //nolint:gosec // test data within the wire range
		Features:        slices.Clone(s.Features),
	}
	if !s.Size.IsZero() {
		wire.Size = &execv2.Size{Cols: uint32(s.Size.Cols), Rows: uint32(s.Size.Rows)}
	}
	if s.Integration != nil {
		wire.Integration = &execv2.IntegrationRequest{Mode: s.Integration.Mode.toProto(), Consent: s.Integration.Consent}
	}
	return wire
}

// rqtWireOfRun is the test's own encoder of a RunRequest: a field-by-field copy that validates nothing.
func rqtWireOfRun(r runRequest) *execv2.RunRequest {
	return &execv2.RunRequest{
		RequestId: r.RequestID,
		Target:    rqtWireOfTarget(r.Target, r.Selection),
		Argv:      slices.Clone(r.Argv),
		Stdin:     slices.Clone(r.Stdin),
		Env:       rqtListOf(r.Env),
		Cwd:       r.Cwd,
		TimeoutMs: uint32(r.Timeout / time.Millisecond), //nolint:gosec // test data within the wire range
		Output: &execv2.OutputLimits{
			MaxStdoutBytes: uint32(r.Output.MaxStdoutBytes), //nolint:gosec // test data within the wire range
			MaxStderrBytes: uint32(r.Output.MaxStderrBytes), //nolint:gosec // test data within the wire range
			Policy:         r.Output.Policy,
		},
		Labels: rqtListOf(r.Labels),
	}
}

// rqtWireStart is a hand-built Start message, with every field set, valid against the base target. Unlike
// rqtWireOfStart it does not go through the Go types, so the admission tests have an input of their own. Its lists
// are ordered by name, as rqtListOf orders them.
func rqtWireStart() *execv2.Start {
	return &execv2.Start{
		Target: rqtHandWireTarget(),
		Argv:   []string{"/bin/bash", "-l"},
		Tty:    true,
		Size:   &execv2.Size{Cols: 120, Rows: 40},
		Env: []*execv2.NameValue{
			{Name: "EDITOR_2", Value: "vi"},
			{Name: "LANG", Value: "C.UTF-8"},
		},
		Cwd:             "/work",
		Labels:          []*execv2.NameValue{{Name: "purpose", Value: "debug"}},
		DetachTimeoutMs: 90_000,
		ReplayBytes:     1 << 20,
		Features:        []execv2.Feature{execv2.Feature_FEATURE_MARKS, execv2.Feature_FEATURE_MODES},
		Integration:     &execv2.IntegrationRequest{Mode: execv2.IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER, Consent: true},
	}
}

// rqtWireRun is a hand-built RunRequest message, with every field set, valid against the base target.
func rqtWireRun() *execv2.RunRequest {
	return &execv2.RunRequest{
		RequestId: "req-1",
		Target:    rqtHandWireTarget(),
		Argv:      []string{"/bin/echo", "hello"},
		Stdin:     []byte("input"),
		Env:       []*execv2.NameValue{{Name: "LANG", Value: "C.UTF-8"}},
		Cwd:       "/work",
		TimeoutMs: 120_000,
		Output: &execv2.OutputLimits{
			MaxStdoutBytes: 4096,
			MaxStderrBytes: 2048,
			Policy:         execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL,
		},
		Labels: []*execv2.NameValue{{Name: "purpose", Value: "diagnose"}},
	}
}

// rqtHandWireTarget is the hand-built wire form of rqtGoTarget with the selection of rqtGoStart.
func rqtHandWireTarget() *execv2.TargetRef {
	incarnation := "uid-1"
	return &execv2.TargetRef{
		Reference: &resourcepb.ResourceReference{
			Address: &resourcepb.ResourceAddress{
				Provider:  "k8s",
				Authority: "auth",
				Type:      "pods",
				Scope:     []*resourcepb.ScopeEntry{{Dimension: "namespace", Value: "default"}},
				Id:        "pod-1",
			},
			Incarnation: &incarnation,
		},
		Route:     &resourcepb.AccessRoute{Connection: "conn-1"},
		Selection: []*execv2.NameValue{{Name: "container", Value: "app"}},
	}
}

// rqtBytes returns n bytes of "a".
func rqtBytes(n int) string { return strings.Repeat("a", n) }

// rqtRepeat returns n copies of s.
func rqtRepeat(n int, s string) []string { return slices.Repeat([]string{s}, n) }

// rqtArgvOfSize returns an argv of exactly total bytes, in elements of at most 32 KiB.
func rqtArgvOfSize(total int) []string {
	var argv []string
	for total > 0 {
		n := min(total, 32<<10)
		argv = append(argv, rqtBytes(n))
		total -= n
	}
	return argv
}

// rqtEnvOfSize returns an environment of exactly total bytes, names plus values, with values of at most 32 KiB and
// at most 256 entries. Its entries are E0, E1, ... so that a total of at least 4 bytes is always reachable.
func rqtEnvOfSize(total int) map[string]string {
	env := map[string]string{}
	for i := 0; total > 0; i++ {
		name := "E" + strconv.Itoa(i)
		n := min(total-len(name), 32<<10)
		env[name] = rqtBytes(max(n, 0))
		total -= len(name) + max(n, 0)
	}
	return env
}

// rqtEnvOfCount returns n environment entries with valid names.
func rqtEnvOfCount(n int) map[string]string {
	env := make(map[string]string, n)
	for i := range n {
		env["E"+strconv.Itoa(i)] = "v"
	}
	return env
}

// rqtLabelsOfCount returns n labels.
func rqtLabelsOfCount(n int) map[string]string {
	labels := make(map[string]string, n)
	for i := range n {
		labels["k"+strconv.Itoa(i)] = "v"
	}
	return labels
}

// rqtSelectionOfCount returns n selections whose keys are closed codes.
func rqtSelectionOfCount(n int) map[string]string {
	selection := make(map[string]string, n)
	for i := range n {
		selection["opt"+strconv.Itoa(i)] = "v"
	}
	return selection
}

// rqtEnvSize and rqtArgvSize measure what a generator built, so that a bound's boundary row is exactly at the bound.
func rqtEnvSize(env map[string]string) int {
	total := 0
	for name, value := range env {
		total += len(name) + len(value)
	}
	return total
}

// rqtArgvSize is the total size of an argv in bytes.
func rqtArgvSize(argv []string) int {
	total := 0
	for _, a := range argv {
		total += len(a)
	}
	return total
}

// rqtRequireRefusal requires refusal to carry class and reason, a local cause and a pairing that may cross the wire.
func rqtRequireRefusal(t *testing.T, refusal *Error, class ErrorClass, reason ErrorReason) {
	t.Helper()
	require.NotNil(t, refusal, "want a %s refusal", class)
	require.Equal(t, class, refusal.Class, "class of %v", refusal.Err)
	require.Equal(t, reason, refusal.Reason, "reason of %v", refusal.Err)
	require.Error(t, refusal.Err, "a refusal keeps its local cause")
	require.True(t, validPair(refusal.Class, refusal.Reason), "a refusal must be a pairing that may cross the wire")
}

// rqtBound is one rule of the contract that holds for every target: a request that breaks it is refused with the
// class and reason of the row. A zero class means accepted.
type rqtBound[T any] struct {
	name   string
	change func(*T)
	class  ErrorClass
	reason ErrorReason
}

// rqtFields says where a request type keeps the fields that Start and RunRequest share.
type rqtFields[T any] struct {
	target    func(*T) *TargetRef
	selection func(*T) *map[string]string
	argv      func(*T) *[]string
	env       func(*T) *map[string]string
	cwd       func(*T) *string
	labels    func(*T) *map[string]string
}

// rqtStartFields locates the shared fields in a Start.
var rqtStartFields = rqtFields[startRequest]{
	target:    func(s *startRequest) *TargetRef { return &s.Target },
	selection: func(s *startRequest) *map[string]string { return &s.Selection },
	argv:      func(s *startRequest) *[]string { return &s.Argv },
	env:       func(s *startRequest) *map[string]string { return &s.Env },
	cwd:       func(s *startRequest) *string { return &s.Cwd },
	labels:    func(s *startRequest) *map[string]string { return &s.Labels },
}

// rqtRunFields locates the shared fields in a RunRequest.
var rqtRunFields = rqtFields[runRequest]{
	target:    func(r *runRequest) *TargetRef { return &r.Target },
	selection: func(r *runRequest) *map[string]string { return &r.Selection },
	argv:      func(r *runRequest) *[]string { return &r.Argv },
	env:       func(r *runRequest) *map[string]string { return &r.Env },
	cwd:       func(r *runRequest) *string { return &r.Cwd },
	labels:    func(r *runRequest) *map[string]string { return &r.Labels },
}

// rqtSharedBounds returns the rows of the bounds that a Start and a RunRequest share: the target, the argv elements,
// the environment, the working directory and the labels. The accepted row of each bound is its boundary value and
// the refused row is the first value beyond it.
func rqtSharedBounds[T any](f rqtFields[T]) []rqtBound[T] {
	invalid := ErrorClassInvalidRequest
	rows := []rqtBound[T]{
		{name: "valid"},

		// target
		{name: "target: route without a connection", change: func(r *T) { f.target(r).Route.Connection = "" }, class: invalid},
		{name: "target: reference without a provider", change: func(r *T) { f.target(r).Reference.Address.Provider = "" }, class: invalid},
		{name: "selection: no entry", change: func(r *T) { *f.selection(r) = nil }},
		{name: "selection: 8 entries", change: func(r *T) { *f.selection(r) = rqtSelectionOfCount(8) }},
		{name: "selection: 9 entries", change: func(r *T) { *f.selection(r) = rqtSelectionOfCount(9) }, class: invalid},
		{name: "selection: name that is not a closed code", change: func(r *T) { *f.selection(r) = map[string]string{"Container": "app"} }, class: invalid},
		{name: "selection: empty name", change: func(r *T) { *f.selection(r) = map[string]string{"": "app"} }, class: invalid},
		{name: "selection: name of 40 bytes", change: func(r *T) { *f.selection(r) = map[string]string{rqtBytes(40): "app"} }},
		{name: "selection: name of 41 bytes", change: func(r *T) { *f.selection(r) = map[string]string{rqtBytes(41): "app"} }, class: invalid},
		{name: "selection: name with every character of a closed code", change: func(r *T) { *f.selection(r) = map[string]string{"a-z_0.9": "app"} }},
		{name: "selection: name with a space", change: func(r *T) { *f.selection(r) = map[string]string{"my option": "app"} }, class: invalid},
		{name: "selection: value of 256 bytes", change: func(r *T) { *f.selection(r) = map[string]string{"container": rqtBytes(256)} }},
		{name: "selection: value of 257 bytes", change: func(r *T) { *f.selection(r) = map[string]string{"container": rqtBytes(257)} }, class: invalid},
		{name: "selection: empty value", change: func(r *T) { *f.selection(r) = map[string]string{"container": ""} }},
		{name: "selection: value that is not UTF-8", change: func(r *T) { *f.selection(r) = map[string]string{"container": "a\xff"} }, class: invalid},
		{name: "selection: value with a NUL", change: func(r *T) { *f.selection(r) = map[string]string{"container": "a\x00"} }, class: invalid},
		{name: "selection: value with a newline", change: func(r *T) { *f.selection(r) = map[string]string{"container": "a\nb"} }, class: invalid},
		{name: "selection: non-ASCII UTF-8 value", change: func(r *T) { *f.selection(r) = map[string]string{"container": "caf\u00e9 \u65e5\u672c\u8a9e"} }},

		// argv elements
		{name: "argv: 1024 elements", change: func(r *T) { *f.argv(r) = rqtRepeat(1024, "a") }},
		{name: "argv: 1025 elements", change: func(r *T) { *f.argv(r) = rqtRepeat(1025, "a") }, class: invalid},
		{name: "argv: element of 32 KiB", change: func(r *T) { *f.argv(r) = []string{rqtBytes(32 << 10)} }},
		{name: "argv: element of 32 KiB + 1", change: func(r *T) { *f.argv(r) = []string{rqtBytes(32<<10 + 1)} }, class: invalid},
		{name: "argv: element with a NUL", change: func(r *T) { *f.argv(r) = []string{"sh", "a\x00b"} }, class: invalid},
		{name: "argv: first element with a NUL", change: func(r *T) { *f.argv(r) = []string{"\x00"} }, class: invalid},
		{name: "argv: element that is not UTF-8", change: func(r *T) { *f.argv(r) = []string{"sh", "a\xff"} }, class: invalid},
		{name: "argv: first element that is not UTF-8", change: func(r *T) { *f.argv(r) = []string{"\xc3\x28"} }, class: invalid},
		{name: "argv: non-ASCII UTF-8 elements", change: func(r *T) { *f.argv(r) = []string{"sh", "caf\u00e9", "\u65e5\u672c\u8a9e", "\U0001f600"} }},
		{name: "argv: empty first element", change: func(r *T) { *f.argv(r) = []string{""} }, class: invalid},
		{name: "argv: empty first element before others", change: func(r *T) { *f.argv(r) = []string{"", "-c"} }, class: invalid},
		{name: "argv: a later empty element", change: func(r *T) { *f.argv(r) = []string{"sh", "", "-c"} }},

		// env: entries, names, values
		{name: "env: 256 entries", change: func(r *T) { *f.env(r) = rqtEnvOfCount(256) }},
		{name: "env: 257 entries", change: func(r *T) { *f.env(r) = rqtEnvOfCount(257) }, class: invalid},
		{name: "env: name of 256 bytes", change: func(r *T) { *f.env(r) = map[string]string{"A" + rqtBytes(255): "v"} }},
		{name: "env: name of 257 bytes", change: func(r *T) { *f.env(r) = map[string]string{"A" + rqtBytes(256): "v"} }, class: invalid},
		{name: "env: name of one underscore", change: func(r *T) { *f.env(r) = map[string]string{"_": "v"} }},
		{name: "env: name with letters, digits and underscores", change: func(r *T) { *f.env(r) = map[string]string{"a1_B2": "v"} }},
		{name: "env: empty name", change: func(r *T) { *f.env(r) = map[string]string{"": "v"} }, class: invalid},
		{name: "env: name starting with a digit", change: func(r *T) { *f.env(r) = map[string]string{"1A": "v"} }, class: invalid},
		{name: "env: name with a hyphen", change: func(r *T) { *f.env(r) = map[string]string{"A-B": "v"} }, class: invalid},
		{name: "env: name with a dot", change: func(r *T) { *f.env(r) = map[string]string{"A.B": "v"} }, class: invalid},
		{name: "env: name with a space", change: func(r *T) { *f.env(r) = map[string]string{"A B": "v"} }, class: invalid},
		{name: "env: name with an equals sign", change: func(r *T) { *f.env(r) = map[string]string{"A=B": "v"} }, class: invalid},
		{name: "env: name with a non-ASCII letter", change: func(r *T) { *f.env(r) = map[string]string{"\u00c4B": "v"} }, class: invalid},
		{name: "env: name with a NUL", change: func(r *T) { *f.env(r) = map[string]string{"A\x00": "v"} }, class: invalid},
		{name: "env: name with a newline", change: func(r *T) { *f.env(r) = map[string]string{"A\n": "v"} }, class: invalid},
		{name: "env: value of 32 KiB", change: func(r *T) { *f.env(r) = map[string]string{"A": rqtBytes(32 << 10)} }},
		{name: "env: value of 32 KiB + 1", change: func(r *T) { *f.env(r) = map[string]string{"A": rqtBytes(32<<10 + 1)} }, class: invalid},
		{name: "env: value with a NUL", change: func(r *T) { *f.env(r) = map[string]string{"A": "x\x00y"} }, class: invalid},
		{name: "env: value that is not UTF-8", change: func(r *T) { *f.env(r) = map[string]string{"A": "x\xffy"} }, class: invalid},
		{name: "env: non-ASCII UTF-8 value", change: func(r *T) { *f.env(r) = map[string]string{"A": "caf\u00e9 \u65e5\u672c\u8a9e"} }},
		{name: "env: empty value", change: func(r *T) { *f.env(r) = map[string]string{"A": ""} }},
		{name: "env: value with a newline", change: func(r *T) { *f.env(r) = map[string]string{"A": "line1\nline2"} }},

		// cwd
		{name: "cwd: 4096 bytes", change: func(r *T) { *f.cwd(r) = "/" + rqtBytes(4095) }},
		{name: "cwd: 4097 bytes", change: func(r *T) { *f.cwd(r) = "/" + rqtBytes(4096) }, class: invalid},
		{name: "cwd: with a NUL", change: func(r *T) { *f.cwd(r) = "/work\x00" }, class: invalid},
		{name: "cwd: that is not UTF-8", change: func(r *T) { *f.cwd(r) = "/work/\xff" }, class: invalid},
		{name: "cwd: non-ASCII UTF-8", change: func(r *T) { *f.cwd(r) = "/work/caf\u00e9/\u65e5\u672c\u8a9e" }},
		{name: "cwd: empty", change: func(r *T) { *f.cwd(r) = "" }},

		// labels
		{name: "labels: 16 entries", change: func(r *T) { *f.labels(r) = rqtLabelsOfCount(16) }},
		{name: "labels: 17 entries", change: func(r *T) { *f.labels(r) = rqtLabelsOfCount(17) }, class: invalid},
		{name: "labels: key of 64 bytes", change: func(r *T) { *f.labels(r) = map[string]string{rqtBytes(64): "v"} }},
		{name: "labels: key of 65 bytes", change: func(r *T) { *f.labels(r) = map[string]string{rqtBytes(65): "v"} }, class: invalid},
		{name: "labels: value of 256 bytes", change: func(r *T) { *f.labels(r) = map[string]string{"k": rqtBytes(256)} }},
		{name: "labels: value of 257 bytes", change: func(r *T) { *f.labels(r) = map[string]string{"k": rqtBytes(257)} }, class: invalid},
		{name: "labels: key of 32 two-byte letters", change: func(r *T) { *f.labels(r) = map[string]string{strings.Repeat("\u00fc", 32): "v"} }},
		{name: "labels: key of 33 two-byte letters", change: func(r *T) { *f.labels(r) = map[string]string{strings.Repeat("\u00fc", 33): "v"} }, class: invalid},
		{name: "labels: key with a space", change: func(r *T) { *f.labels(r) = map[string]string{"my label": "v"} }},
		{name: "labels: key with a control character", change: func(r *T) { *f.labels(r) = map[string]string{"k\x01": "v"} }, class: invalid},
		{name: "labels: value with a newline", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\nb"} }, class: invalid},
		{name: "labels: value with a tab", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\tb"} }, class: invalid},
		{name: "labels: value with DEL", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\x7fb"} }, class: invalid},
		{name: "labels: value with a NUL", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\x00b"} }, class: invalid},
		{name: "labels: value with the last C1 control character", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\u009fb"} }, class: invalid},
		{name: "labels: value with the first C1 control character", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\u0080b"} }, class: invalid},
		{name: "labels: key with a C1 control character", change: func(r *T) { *f.labels(r) = map[string]string{"k\u0085": "v"} }, class: invalid},
		{name: "labels: value with the character after the C1 controls", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a\u00a0b"} }},
		{name: "labels: value with the first printable character", change: func(r *T) { *f.labels(r) = map[string]string{"k": "a b"} }},
		{name: "labels: key that is not UTF-8", change: func(r *T) { *f.labels(r) = map[string]string{"k\xff": "v"} }, class: invalid},
		{name: "labels: value that is not UTF-8", change: func(r *T) { *f.labels(r) = map[string]string{"k": "\xc3\x28"} }, class: invalid},
	}
	return rows
}

// rqtStartBounds returns the rows of the target-independent bounds of a Start: the shared ones and the ones of the
// Start alone (its argv may be empty and has a total of 64 KiB, its env a total of 64 KiB, its size, its features and
// its integration).
func rqtStartBounds() []rqtBound[startRequest] {
	invalid, unsupported := ErrorClassInvalidRequest, ErrorClassUnsupported
	own := []rqtBound[startRequest]{
		{name: "argv: empty (the target's default)", change: func(s *startRequest) { s.Argv = nil }},
		{name: "argv: total of 64 KiB", change: func(s *startRequest) { s.Argv = rqtArgvOfSize(64 << 10) }},
		{name: "argv: total of 64 KiB + 1", change: func(s *startRequest) { s.Argv = rqtArgvOfSize(64<<10 + 1) }, class: invalid},
		{name: "env: total of 64 KiB", change: func(s *startRequest) { s.Env = rqtEnvOfSize(64 << 10) }},
		{name: "env: total of 64 KiB + 1", change: func(s *startRequest) { s.Env = rqtEnvOfSize(64<<10 + 1) }, class: invalid},

		{name: "size: absent", change: func(s *startRequest) { s.Size = Size{} }},
		{name: "size: 1 x 1", change: func(s *startRequest) { s.Size = Size{Cols: 1, Rows: 1} }},
		{name: "size: no columns", change: func(s *startRequest) { s.Size = Size{Rows: 24} }, class: invalid},
		{name: "size: no rows", change: func(s *startRequest) { s.Size = Size{Cols: 80} }, class: invalid},

		{name: "features: none", change: func(s *startRequest) { s.Features = nil }},
		{name: "features: marks", change: func(s *startRequest) { s.Features = []execv2.Feature{rqtFeatureMarks} }},
		{name: "features: modes", change: func(s *startRequest) { s.Features = []execv2.Feature{rqtFeatureModes} }},
		{name: "features: modes then marks", change: func(s *startRequest) { s.Features = []execv2.Feature{rqtFeatureModes, rqtFeatureMarks} }},
		{name: "features: marks twice", change: func(s *startRequest) { s.Features = []execv2.Feature{rqtFeatureMarks, rqtFeatureMarks} }, class: invalid},
		{name: "features: modes twice", change: func(s *startRequest) { s.Features = []execv2.Feature{rqtFeatureModes, rqtFeatureModes} }, class: invalid},
		{name: "features: marks, modes, marks", change: func(s *startRequest) {
			s.Features = []execv2.Feature{rqtFeatureMarks, rqtFeatureModes, rqtFeatureMarks}
		}, class: invalid},
		{name: "features: unspecified", change: func(s *startRequest) { s.Features = []execv2.Feature{execv2.Feature_FEATURE_UNSPECIFIED} }, class: unsupported},
		{name: "features: a value this build does not know", change: func(s *startRequest) { s.Features = []execv2.Feature{99} }, class: unsupported},
		{name: "features: a known feature after an unknown one", change: func(s *startRequest) {
			s.Features = []execv2.Feature{99, rqtFeatureSessionManagement}
		}, class: unsupported},
		{name: "features: an unknown feature after a known one", change: func(s *startRequest) {
			s.Features = []execv2.Feature{rqtFeatureSessionManagement, 99}
		}, class: invalid},

		{name: "integration: none", change: func(s *startRequest) { s.Integration = nil }},
		{name: "integration: off without consent", change: func(s *startRequest) { s.Integration = &integrationRequest{} }},
		{name: "integration: off with a consent flag", change: func(s *startRequest) { s.Integration = &integrationRequest{Consent: true} }},
		{name: "integration: argv wrapper with consent", change: func(s *startRequest) {
			s.Integration = &integrationRequest{Mode: IntegrationArgvWrapper, Consent: true}
		}},
		{name: "integration: argv wrapper without consent", change: func(s *startRequest) {
			s.Integration = &integrationRequest{Mode: IntegrationArgvWrapper}
		}, class: invalid, reason: ErrorReasonConsentRequired},
	}
	// Every feature that is not requestable is refused, whatever it is.
	for _, f := range rqtAllFeatures {
		if f == rqtFeatureMarks || f == rqtFeatureModes {
			continue
		}
		own = append(own, rqtBound[startRequest]{
			name:   "features: " + f.String() + " cannot be requested",
			change: func(s *startRequest) { s.Features = []execv2.Feature{f} },
			class:  invalid,
		})
	}
	return slices.Concat(rqtSharedBounds(rqtStartFields), own)
}

// rqtRunBounds returns the rows of the target-independent bounds of a RunRequest: the shared ones and the ones of the
// RunRequest alone (the ids, an argv that is required and has the target's total, an env with the target's total,
// the stdin and the policies).
func rqtRunBounds() []rqtBound[runRequest] {
	invalid := ErrorClassInvalidRequest
	own := []rqtBound[runRequest]{
		{name: "request id: empty", change: func(r *runRequest) { r.RequestID = "" }},
		{name: "request id: 128 bytes", change: func(r *runRequest) { r.RequestID = rqtBytes(128) }},
		{name: "request id: 129 bytes", change: func(r *runRequest) { r.RequestID = rqtBytes(129) }, class: invalid},
		{name: "request id: first printable byte", change: func(r *runRequest) { r.RequestID = "!" }},
		{name: "request id: last printable byte", change: func(r *runRequest) { r.RequestID = "~" }},
		{name: "request id: with a space", change: func(r *runRequest) { r.RequestID = "a b" }, class: invalid},
		{name: "request id: with DEL", change: func(r *runRequest) { r.RequestID = "a\x7f" }, class: invalid},
		{name: "request id: with a control character", change: func(r *runRequest) { r.RequestID = "a\n" }, class: invalid},
		{name: "request id: with a non-ASCII letter", change: func(r *runRequest) { r.RequestID = "caf\u00e9" }, class: invalid},

		{name: "argv: absent", change: func(r *runRequest) { r.Argv = nil }, class: invalid},
		{name: "argv: empty", change: func(r *runRequest) { r.Argv = []string{} }, class: invalid},
		{name: "argv: one element", change: func(r *runRequest) { r.Argv = []string{"true"} }},
		// A run has its own total: more than the 64 KiB of a Start is fine while the target and the contract allow it.
		{name: "argv: total of 64 KiB + 1", change: func(r *runRequest) { r.Argv = rqtArgvOfSize(64<<10 + 1) }},
		{name: "env: total of 64 KiB + 1", change: func(r *runRequest) { r.Env = rqtEnvOfSize(64<<10 + 1) }},

		{name: "stdin: absent", change: func(r *runRequest) { r.Stdin = nil }},
		{name: "stdin: 64 KiB", change: func(r *runRequest) { r.Stdin = make([]byte, rqtRunStdinBytes) }},

		{name: "output: zero limits", change: func(r *runRequest) { r.Output = outputLimits{} }},
		{name: "output: default policy", change: func(r *runRequest) { r.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED }},
		{name: "output: head and tail", change: func(r *runRequest) { r.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL }},
		{name: "output: head", change: func(r *runRequest) { r.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD }},
		{name: "output: tail", change: func(r *runRequest) { r.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL }},

		{name: "timeout: zero", change: func(r *runRequest) { r.Timeout = 0 }},
		{name: "timeout: the target's maximum", change: func(r *runRequest) { r.Timeout = rqtRunTimeoutMillis * time.Millisecond }},
	}
	return slices.Concat(rqtSharedBounds(rqtRunFields), own)
}

// TestRequestFixtureSizes checks the generators of the tables, so that a boundary row is exactly at its boundary.
func TestRequestFixtureSizes(t *testing.T) {
	for _, total := range []int{4, 100, 64 << 10, 64<<10 + 1, rqtRunEnvBytes, rqtRunEnvBytes + 1, 256 << 10, 256<<10 + 1} {
		require.Equal(t, total, rqtArgvSize(rqtArgvOfSize(total)), "argv of %d", total)
		env := rqtEnvOfSize(total)
		require.Equal(t, total, rqtEnvSize(env), "env of %d", total)
		require.LessOrEqual(t, len(env), 256)
		for name, value := range env {
			require.LessOrEqual(t, len(value), 32<<10, "value of %s", name)
		}
	}
	require.Len(t, rqtEnvOfCount(256), 256)
	require.Len(t, rqtLabelsOfCount(17), 17)
	require.Len(t, rqtSelectionOfCount(9), 9)
}

// TestRqtListOfIsOrderedAndComplete checks the encoder the tables use for the lists of a request: every pair, once,
// ordered by name, and no list for no pair.
func TestRqtListOfIsOrderedAndComplete(t *testing.T) {
	require.Nil(t, rqtListOf(nil))
	require.Nil(t, rqtListOf(map[string]string{}))
	got := rqtListOf(map[string]string{"b": "2", "a": "1", "c": ""})
	require.Len(t, got, 3)
	for i, want := range []*execv2.NameValue{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}, {Name: "c"}} {
		require.True(t, proto.Equal(want, got[i]), "entry %d is %v", i, got[i])
	}
}

// TestStartBounds is the table of the target-independent bounds of a Start: each row goes through the plugin's
// admission against a registered target, the boundary value is accepted and the first value beyond it is refused with
// the stated class and reason.
func TestStartBounds(t *testing.T) {
	_, target := rqtTarget(t, nil)
	served := rqtServed()
	for _, row := range rqtStartBounds() {
		t.Run(row.name, func(t *testing.T) {
			start := rqtGoStart()
			if row.change != nil {
				row.change(&start)
			}
			got, refusal := admitStart(rqtWireOfStart(start), target, served)
			if row.class == ErrorClassUnspecified {
				require.Nil(t, refusal)
				require.Empty(t, rqtStartViolation(got, target), "an admitted Start is within every bound")
				return
			}
			require.Zero(t, got, "a refused Start leaves nothing behind")
			rqtRequireRefusal(t, refusal, row.class, row.reason)
		})
	}
}

// TestRunRequestBounds is the table of the target-independent bounds of a RunRequest, run through the plugin's
// admission like the table of a Start.
func TestRunRequestBounds(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()
	for _, row := range rqtRunBounds() {
		t.Run(row.name, func(t *testing.T) {
			run := rqtGoRun()
			if row.change != nil {
				row.change(&run)
			}
			got, admitted, refusal := admitRunRequest(rqtWireOfRun(run), reg, served)
			if row.class == ErrorClassUnspecified {
				require.Nil(t, refusal)
				require.Same(t, target, admitted)
				require.Empty(t, rqtRunViolation(got, target), "an admitted RunRequest is within every bound")
				return
			}
			require.Zero(t, got, "a refused RunRequest leaves nothing behind")
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, row.class, row.reason)
		})
	}
}

// rqtMaxRun gives the target the run limits at the ceilings of the contract: the most any target may declare.
func rqtMaxRun(tg *Target) {
	tg.Info.Run = RunLimits{
		DefaultTimeout:     time.Second,
		MaxTimeout:         time.Hour,
		DefaultOutputBytes: 100,
		MaxOutputBytes:     1 << 20,
		MaxStdinBytes:      1 << 20,
		MaxConcurrent:      1,
		MaxArgvBytes:       256 << 10,
		MaxEnvBytes:        256 << 10,
		PostExitDrain:      time.Millisecond,
	}
}

// TestRunRequestCeilingsOfTheContract covers the bounds a RunRequest has at the ceilings of the contract, which a
// target may declare at most: the base target's limits sit below them and cannot show a ceiling that is too low.
// The first value beyond each ceiling is refused, never clamped.
func TestRunRequestCeilingsOfTheContract(t *testing.T) {
	invalid := ErrorClassInvalidRequest
	rows := []struct {
		name   string
		change func(*execv2.RunRequest)
		class  ErrorClass
		reason ErrorReason
	}{
		{name: "argv total of 256 KiB", change: func(r *execv2.RunRequest) { r.Argv = rqtArgvOfSize(256 << 10) }},
		{name: "argv total of 256 KiB + 1", change: func(r *execv2.RunRequest) { r.Argv = rqtArgvOfSize(256<<10 + 1) }, class: invalid},
		{name: "env total of 256 KiB", change: func(r *execv2.RunRequest) { r.Env = rqtListOf(rqtEnvOfSize(256 << 10)) }},
		{name: "env total of 256 KiB + 1", change: func(r *execv2.RunRequest) { r.Env = rqtListOf(rqtEnvOfSize(256<<10 + 1)) }, class: invalid},
		{name: "stdin of 1 MiB", change: func(r *execv2.RunRequest) { r.Stdin = make([]byte, 1<<20) }},
		{name: "stdin of 1 MiB + 1", change: func(r *execv2.RunRequest) { r.Stdin = make([]byte, 1<<20+1) }, class: ErrorClassLimit, reason: ErrorReasonInputTooLarge},
		{name: "a timeout of an hour", change: func(r *execv2.RunRequest) { r.TimeoutMs = 3_600_000 }},
		{name: "a timeout of an hour and a millisecond", change: func(r *execv2.RunRequest) { r.TimeoutMs = 3_600_001 }, class: invalid},
		{name: "stdout of 1 MiB", change: func(r *execv2.RunRequest) { r.Output.MaxStdoutBytes = 1 << 20 }},
		{name: "stdout of 1 MiB + 1", change: func(r *execv2.RunRequest) { r.Output.MaxStdoutBytes = 1<<20 + 1 }, class: invalid},
		{name: "stderr of 1 MiB", change: func(r *execv2.RunRequest) { r.Output.MaxStderrBytes = 1 << 20 }},
		{name: "stderr of 1 MiB + 1", change: func(r *execv2.RunRequest) { r.Output.MaxStderrBytes = 1<<20 + 1 }, class: invalid},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			reg, target := rqtTarget(t, rqtMaxRun)
			wire := rqtWireRun()
			wire.Stdin, wire.TimeoutMs = nil, 0
			wire.Output = &execv2.OutputLimits{}
			row.change(wire)
			got, admitted, refusal := admitRunRequest(wire, reg, rqtServed())
			if row.class == ErrorClassUnspecified {
				require.Nil(t, refusal)
				require.Same(t, target, admitted)
				require.Empty(t, rqtRunViolation(got, target))
				return
			}
			require.Zero(t, got)
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, row.class, row.reason)
		})
	}

	// The ceiling of the contract is a bound of the request itself: it is read before anything the target serves, so
	// a target that serves no runs does not turn a request that no target could serve into an unsupported one.
	t.Run("the ceilings come before what the target serves", func(t *testing.T) {
		reg, _ := rqtTarget(t, func(tg *Target) { tg.Info.Run = RunLimits{} })
		for name, change := range map[string]func(*execv2.RunRequest){
			"argv": func(r *execv2.RunRequest) { r.Argv = rqtArgvOfSize(256<<10 + 1) },
			"env":  func(r *execv2.RunRequest) { r.Env = rqtListOf(rqtEnvOfSize(256<<10 + 1)) },
		} {
			t.Run(name, func(t *testing.T) {
				wire := rqtWireRun()
				change(wire)
				_, _, refusal := admitRunRequest(wire, reg, rqtServed())
				rqtRequireRefusal(t, refusal, invalid, ErrorReasonUnspecified)
			})
		}
	})
	t.Run("a Start has the smaller ceilings of its own, also before what the target serves", func(t *testing.T) {
		_, target := rqtTarget(t, func(tg *Target) { tg.Info.Session = SessionLimits{} })
		for name, change := range map[string]func(*execv2.Start){
			"argv": func(s *execv2.Start) { s.Argv = rqtArgvOfSize(64<<10 + 1) },
			"env":  func(s *execv2.Start) { s.Env = rqtListOf(rqtEnvOfSize(64<<10 + 1)) },
		} {
			t.Run(name, func(t *testing.T) {
				wire := rqtWireStart()
				change(wire)
				_, refusal := admitStart(wire, target, rqtServed())
				rqtRequireRefusal(t, refusal, invalid, ErrorReasonUnspecified)
			})
		}
	})
}

// TestRequestsRoundTrip proves a fully populated request, and the smallest one, written by the test's own encoder,
// carried through bytes and admitted, gives back what was put in: admission reads every field.
func TestRequestsRoundTrip(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	t.Run("start", func(t *testing.T) {
		minimal := startRequest{Target: rqtGoTarget()}
		for name, start := range map[string]startRequest{"fully populated": rqtGoStart(), "minimal": minimal} {
			raw, err := proto.Marshal(rqtWireOfStart(start))
			require.NoError(t, err, name)
			decoded := new(execv2.Start)
			require.NoError(t, proto.Unmarshal(raw, decoded), name)
			got, refusal := admitStart(decoded, target, served)
			require.Nil(t, refusal, name)
			require.Equal(t, start, got, name)
		}
	})

	t.Run("run", func(t *testing.T) {
		minimal := runRequest{Target: rqtGoTarget(), Argv: []string{"true"}}
		for name, run := range map[string]runRequest{"fully populated": rqtGoRun(), "minimal": minimal} {
			raw, err := proto.Marshal(rqtWireOfRun(run))
			require.NoError(t, err, name)
			decoded := new(execv2.RunRequest)
			require.NoError(t, proto.Unmarshal(raw, decoded), name)
			got, admitted, refusal := admitRunRequest(decoded, reg, served)
			require.Nil(t, refusal, name)
			require.Same(t, target, admitted, name)
			require.Equal(t, run, got, name)
		}
	})

	t.Run("open with a start", func(t *testing.T) {
		start := rqtGoStart()
		open := &execv2.OpenRequest{SessionId: "session-1", Kind: &execv2.OpenRequest_Start{Start: rqtWireOfStart(start)}}
		raw, err := proto.Marshal(open)
		require.NoError(t, err)
		decoded := new(execv2.OpenRequest)
		require.NoError(t, proto.Unmarshal(raw, decoded))
		got, admitted, refusal := admitOpenRequest(decoded, reg, served)
		require.Nil(t, refusal)
		require.Same(t, target, admitted)
		require.Equal(t, openRequest{SessionID: "session-1", Start: &start}, got)
	})

	t.Run("open with an attach", func(t *testing.T) {
		open := &execv2.OpenRequest{SessionId: "session-1", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: 1 << 33}}}
		raw, err := proto.Marshal(open)
		require.NoError(t, err)
		decoded := new(execv2.OpenRequest)
		require.NoError(t, proto.Unmarshal(raw, decoded))
		got, admitted, refusal := admitOpenRequest(decoded, reg, served)
		require.Nil(t, refusal)
		require.Nil(t, admitted, "an attach names no target: the engine finds the session")
		require.Equal(t, openRequest{SessionID: "session-1", Attach: &attachRequest{FromOffset: 1 << 33}}, got)
	})
}

// TestWireFirstRoundTrip is the other direction of the round trip: a hand-built wire message, admitted and encoded
// again by the test's encoder, is the message that was sent.
func TestWireFirstRoundTrip(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	start, refusal := admitStart(rqtWireStart(), target, served)
	require.Nil(t, refusal)
	require.True(t, proto.Equal(rqtWireStart(), rqtWireOfStart(start)), "got %v", rqtWireOfStart(start))

	run, _, refusal := admitRunRequest(rqtWireRun(), reg, served)
	require.Nil(t, refusal)
	require.True(t, proto.Equal(rqtWireRun(), rqtWireOfRun(run)), "got %v", rqtWireOfRun(run))
}

// TestRefusalsNeverEchoRequestContent puts a canary in every kind of value a bound refuses: no refusal may carry the
// canary in its error text. The local cause is logged by the SDK; argv, env values, names and ids can hold secrets
// and must never reach a log through a refusal.
func TestRefusalsNeverEchoRequestContent(t *testing.T) {
	const canary = "CANARY-SECRET"
	reg, target := rqtTarget(t, nil)
	served := rqtServed()
	// fit returns a string of exactly n bytes that starts with the canary.
	fit := func(n int) string { return canary + rqtBytes(n-len(canary)) }

	starts := map[string]func(*startRequest){
		"argv element with a NUL":    func(s *startRequest) { s.Argv = []string{"sh", canary + "\x00"} },
		"argv element not UTF-8":     func(s *startRequest) { s.Argv = []string{"sh", canary + "\xff"} },
		"argv element too long":      func(s *startRequest) { s.Argv = []string{fit(32<<10 + 1)} },
		"argv too large in total":    func(s *startRequest) { s.Argv = []string{fit(32 << 10), fit(32 << 10), canary} },
		"argv[0] empty":              func(s *startRequest) { s.Argv = []string{"", canary} },
		"env value with a NUL":       func(s *startRequest) { s.Env = map[string]string{"A": canary + "\x00"} },
		"env value not UTF-8":        func(s *startRequest) { s.Env = map[string]string{"A": canary + "\xff"} },
		"env value too long":         func(s *startRequest) { s.Env = map[string]string{"A": fit(32<<10 + 1)} },
		"env name that is a pair":    func(s *startRequest) { s.Env = map[string]string{canary + "=1": "x"} },
		"env name too long":          func(s *startRequest) { s.Env = map[string]string{"A" + fit(256): "x"} },
		"cwd with a NUL":             func(s *startRequest) { s.Cwd = canary + "\x00" },
		"cwd not UTF-8":              func(s *startRequest) { s.Cwd = canary + "\xff" },
		"cwd too long":               func(s *startRequest) { s.Cwd = fit(4097) },
		"label key too long":         func(s *startRequest) { s.Labels = map[string]string{fit(65): "v"} },
		"label value with a control": func(s *startRequest) { s.Labels = map[string]string{"k": canary + "\n"} },
		"label value too long":       func(s *startRequest) { s.Labels = map[string]string{"k": fit(257)} },
		"selection value too long":   func(s *startRequest) { s.Selection = map[string]string{"container": fit(257)} },
		"selection value not UTF-8":  func(s *startRequest) { s.Selection = map[string]string{"container": canary + "\xff"} },
		"selection name not a code":  func(s *startRequest) { s.Selection = map[string]string{canary: "app"} },
	}
	for name, change := range starts {
		t.Run("start/"+name, func(t *testing.T) {
			start := rqtGoStart()
			change(&start)
			_, refusal := admitStart(rqtWireOfStart(start), target, served)
			require.NotNil(t, refusal)
			rqtRequireNoCanary(t, refusal, canary)
		})
	}

	// What only a list can carry: a name that appears twice, where the name is the secret, and an unknown field
	// beside a secret value. The names and the values of an entry are the caller's text.
	unknown := rqtUnknownBytes(canary)
	wireStarts := map[string]func(*execv2.Start){
		"env name twice": func(s *execv2.Start) {
			s.Env = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"label name twice": func(s *execv2.Start) {
			s.Labels = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"selection name twice": func(s *execv2.Start) {
			s.Target.Selection = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"env entry with an unknown field": func(s *execv2.Start) {
			s.Env = []*execv2.NameValue{{Name: "A", Value: canary}}
			s.Env[0].ProtoReflect().SetUnknown(unknown)
		},
		"label entry with an unknown field": func(s *execv2.Start) {
			s.Labels = []*execv2.NameValue{{Name: canary, Value: canary}}
			s.Labels[0].ProtoReflect().SetUnknown(unknown)
		},
		"selection entry with an unknown field": func(s *execv2.Start) {
			s.Target.Selection = []*execv2.NameValue{{Name: canary, Value: canary}}
			s.Target.Selection[0].ProtoReflect().SetUnknown(unknown)
		},
		"scope entry with an unknown field": func(s *execv2.Start) {
			s.Target.Reference.Address.Scope[0].Value = canary
			s.Target.Reference.Address.Scope[0].ProtoReflect().SetUnknown(unknown)
		},
	}
	for name, change := range wireStarts {
		t.Run("start/"+name, func(t *testing.T) {
			wire := rqtWireStart()
			change(wire)
			_, refusal := admitStart(wire, target, served)
			require.NotNil(t, refusal)
			rqtRequireNoCanary(t, refusal, canary)
		})
	}

	runs := map[string]func(*runRequest){
		"request id with a space": func(r *runRequest) { r.RequestID = canary + " id" },
		"request id too long":     func(r *runRequest) { r.RequestID = fit(129) },
		"argv element too long":   func(r *runRequest) { r.Argv = []string{fit(32<<10 + 1)} },
		"argv with a NUL":         func(r *runRequest) { r.Argv = []string{"sh", canary + "\x00"} },
		"argv not UTF-8":          func(r *runRequest) { r.Argv = []string{"sh", canary + "\xff"} },
		"env value with a NUL":    func(r *runRequest) { r.Env = map[string]string{"A": canary + "\x00"} },
		"env value not UTF-8":     func(r *runRequest) { r.Env = map[string]string{"A": canary + "\xff"} },
		"env name that is a pair": func(r *runRequest) { r.Env = map[string]string{canary + "=1": "x"} },
		"cwd with a NUL":          func(r *runRequest) { r.Cwd = canary + "\x00" },
		"cwd not UTF-8":           func(r *runRequest) { r.Cwd = canary + "\xff" },
		"label value too long":    func(r *runRequest) { r.Labels = map[string]string{"k": fit(257)} },
		"argv above the target's": func(r *runRequest) { r.Argv = []string{fit(32 << 10), fit(32 << 10), fit(32 << 10), fit(32 << 10)} },
		"env above the target's": func(r *runRequest) {
			r.Env = map[string]string{"A": fit(32 << 10), "B": fit(32 << 10), "C": fit(32 << 10), "D": fit(32 << 10)}
		},
		"stdin above the target's": func(r *runRequest) {
			r.Stdin = []byte(fit(rqtRunStdinBytes + 1))
		},
	}
	for name, change := range runs {
		t.Run("run/"+name, func(t *testing.T) {
			run := rqtGoRun()
			change(&run)
			_, _, refusal := admitRunRequest(rqtWireOfRun(run), reg, served)
			require.NotNil(t, refusal)
			rqtRequireNoCanary(t, refusal, canary)
		})
	}

	wireRuns := map[string]func(*execv2.RunRequest){
		"env name twice": func(r *execv2.RunRequest) {
			r.Env = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"label name twice": func(r *execv2.RunRequest) {
			r.Labels = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"selection name twice": func(r *execv2.RunRequest) {
			r.Target.Selection = []*execv2.NameValue{{Name: canary, Value: "1"}, {Name: canary, Value: "2"}}
		},
		"env entry with an unknown field": func(r *execv2.RunRequest) {
			r.Env = []*execv2.NameValue{{Name: "A", Value: canary}}
			r.Env[0].ProtoReflect().SetUnknown(unknown)
		},
	}
	for name, change := range wireRuns {
		t.Run("run/"+name, func(t *testing.T) {
			wire := rqtWireRun()
			change(wire)
			_, _, refusal := admitRunRequest(wire, reg, served)
			require.NotNil(t, refusal)
			rqtRequireNoCanary(t, refusal, canary)
		})
	}

	t.Run("open/session id", func(t *testing.T) {
		wire := &execv2.OpenRequest{SessionId: canary + " id", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}}
		_, _, refusal := admitOpenRequest(wire, reg, served)
		require.NotNil(t, refusal)
		rqtRequireNoCanary(t, refusal, canary)
	})
}

// rqtUnknownBytes is a field no version of any message of the contract has (number 1000, length-delimited), carrying
// payload, written with protowire: what a request of a newer peer holds that this build does not know.
func rqtUnknownBytes(payload string) []byte {
	return protowire.AppendString(protowire.AppendTag(nil, 1000, protowire.BytesType), payload)
}

// rqtRequireNoCanary requires that neither the error nor its local cause carries the canary.
func rqtRequireNoCanary(t *testing.T, err error, canary string) {
	t.Helper()
	require.Error(t, err)
	require.NotContains(t, err.Error(), canary)
	var refusal *Error
	require.ErrorAs(t, err, &refusal)
	require.NotNil(t, refusal)
	require.Error(t, refusal.Err)
	require.NotContains(t, refusal.Err.Error(), canary)
	require.NotContains(t, refusal.Message, canary)
}

// The validators of the bounds read the count of their list first, and refuse a list over its bound on their own, though
// admission bounds every list before it calls them: they are the contract's bounds, written once, and hold for any
// caller.
func TestRequestValidatorsBoundTheirCountsOnTheirOwn(t *testing.T) {
	invalid := ErrorClassInvalidRequest
	t.Run("argv", func(t *testing.T) {
		total, refusal := validateRequestArgv(rqtRepeat(1024, "a"), 64<<10, false)
		require.Nil(t, refusal)
		require.Equal(t, 1024, total)
		total, refusal = validateRequestArgv(rqtRepeat(1025, "a"), 64<<10, false)
		require.Zero(t, total)
		rqtRequireRefusal(t, refusal, invalid, ErrorReasonUnspecified)
		_, refusal = validateRequestArgv(nil, 64<<10, true)
		rqtRequireRefusal(t, refusal, invalid, ErrorReasonUnspecified)
		_, refusal = validateRequestArgv(nil, 64<<10, false)
		require.Nil(t, refusal, "a Start's empty argv is the target's default")
	})
	t.Run("env", func(t *testing.T) {
		total, refusal := validateRequestEnv(rqtEnvOfCount(256), 64<<10)
		require.Nil(t, refusal)
		require.Equal(t, rqtEnvSize(rqtEnvOfCount(256)), total)
		total, refusal = validateRequestEnv(rqtEnvOfCount(257), 64<<10)
		require.Zero(t, total)
		rqtRequireRefusal(t, refusal, invalid, ErrorReasonUnspecified)
	})
	t.Run("labels", func(t *testing.T) {
		require.Nil(t, validateRequestLabels(rqtLabelsOfCount(16)))
		rqtRequireRefusal(t, validateRequestLabels(rqtLabelsOfCount(17)), invalid, ErrorReasonUnspecified)
	})
}

// The decision on an integration request is three values and nothing else: a request for no mode needs nothing, a
// request for a mode needs the host's consent before anything else is said about the mode, and a mode this build does
// not know, with consent, is unsupported.
func TestValidateRequestIntegration(t *testing.T) {
	rows := []struct {
		name                      string
		requested, known, consent bool
		class                     ErrorClass
		reason                    ErrorReason
	}{
		{name: "no mode, no consent", requested: false, known: false, consent: false},
		{name: "no mode, with consent", requested: false, known: false, consent: true},
		{name: "a known mode with consent", requested: true, known: true, consent: true},
		{name: "a known mode without consent", requested: true, known: true, consent: false, class: ErrorClassInvalidRequest, reason: ErrorReasonConsentRequired},
		{name: "an unknown mode with consent", requested: true, known: false, consent: true, class: ErrorClassUnsupported},
		{name: "an unknown mode without consent: consent first", requested: true, known: false, consent: false, class: ErrorClassInvalidRequest, reason: ErrorReasonConsentRequired},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			refusal := validateRequestIntegration(row.requested, row.known, row.consent)
			if row.class == ErrorClassUnspecified {
				require.Nil(t, refusal)
				return
			}
			rqtRequireRefusal(t, refusal, row.class, row.reason)
		})
	}
}
