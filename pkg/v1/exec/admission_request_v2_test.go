package exec

import (
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// TestFeatureSet covers the set of features a build serves.
func TestFeatureSet(t *testing.T) {
	var absent featureSet
	for _, f := range rqtAllFeatures {
		require.False(t, absent.has(f), "a nil set serves nothing: %s", f)
		require.False(t, newFeatureSet(nil).has(f), "an empty set serves nothing: %s", f)
	}

	set := newFeatureSet([]execv2.Feature{rqtFeatureSession, rqtFeatureRun, rqtFeatureSession})
	require.True(t, set.has(rqtFeatureSession))
	require.True(t, set.has(rqtFeatureRun))
	require.False(t, set.has(rqtFeatureEnv))
	require.False(t, set.has(rqtFeatureMarks))

	for _, f := range rqtAllFeatures {
		require.True(t, rqtServed().has(f), "a whole build serves %s", f)
		require.False(t, rqtServed(f).has(f), "a build without %s does not serve it", f)
	}

	unlisted := newFeatureSet([]execv2.Feature{execv2.Feature_FEATURE_UNSPECIFIED, 99, -1})
	require.False(t, unlisted.has(execv2.Feature_FEATURE_UNSPECIFIED), "an unknown feature is never listed")
	require.False(t, unlisted.has(99))
	require.False(t, unlisted.has(-1))

	list := []execv2.Feature{rqtFeatureSession}
	owned := newFeatureSet(list)
	list[0] = rqtFeatureRun
	require.True(t, owned.has(rqtFeatureSession), "the set does not alias the slice it was built from")
	require.False(t, owned.has(rqtFeatureRun))
}

// TestEveryFeatureOfTheContractIsInTheTables keeps rqtAllFeatures complete. The tables list the features by hand, so
// that a feature added to the contract is a row to write, never one that no table has: this test fails until the
// feature is in the list, and the tests that loop over the list then cover it.
func TestEveryFeatureOfTheContractIsInTheTables(t *testing.T) {
	var named []execv2.Feature
	for value := range execv2.Feature_name {
		if f := execv2.Feature(value); f != execv2.Feature_FEATURE_UNSPECIFIED {
			named = append(named, f)
		}
	}
	require.ElementsMatch(t, named, rqtAllFeatures)
}

// rqtStartRow is one rule of a Start that depends on the registered target or on what the build serves.
type rqtStartRow struct {
	name    string
	target  func(*Target)       // changes the registered target
	without []execv2.Feature    // the features the build does not serve
	wire    func(*execv2.Start) // changes the valid message
	class   ErrorClass          // zero: admitted
	reason  ErrorReason
}

// rqtNoTTY makes the target unable to give a command a terminal; shell integration needs one, so it goes too.
func rqtNoTTY(tg *Target) {
	tg.Info.Capabilities.TTY = false
	tg.Info.Capabilities.Integration = nil
}

// rqtStartRows returns the rows of the rules a Start has against its target and its build. The boundary value is
// accepted and the first value beyond it is refused; the last rows break two rules at once and show which refusal
// the documented order of the checks gives.
func rqtStartRows() []rqtStartRow {
	invalid, unsupported := ErrorClassInvalidRequest, ErrorClassUnsupported
	noEnv := func(tg *Target) { tg.Info.Capabilities.Env = false }
	noCwd := func(tg *Target) { tg.Info.Capabilities.Cwd = false }
	noSessions := func(tg *Target) { tg.Info.Session = SessionLimits{} }
	noDefault := func(tg *Target) { tg.Info.DefaultArgv = nil }
	noIntegration := func(tg *Target) { tg.Info.Capabilities.Integration = nil }
	noTerminal := func(p *execv2.Start) { p.Tty = false; p.Size = nil; p.Integration = nil }
	integration := func(mode execv2.IntegrationMode, consent bool) func(*execv2.Start) {
		return func(p *execv2.Start) { p.Integration = &execv2.IntegrationRequest{Mode: mode, Consent: consent} }
	}
	wrapper := execv2.IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER
	none := execv2.IntegrationMode_INTEGRATION_MODE_NONE
	unspecified := execv2.IntegrationMode_INTEGRATION_MODE_UNSPECIFIED

	return []rqtStartRow{
		{name: "valid"},

		// the target serves sessions, and so does the build
		{name: "the target serves no sessions", target: noSessions, class: unsupported},
		{name: "the build does not serve sessions", without: []execv2.Feature{rqtFeatureSession}, class: unsupported},

		// the default argv
		{name: "empty argv with a default", wire: func(p *execv2.Start) { p.Argv = nil }},
		{name: "empty argv without a default", target: noDefault, wire: func(p *execv2.Start) { p.Argv = nil }, class: invalid},
		{name: "argv without a default", target: noDefault},

		// the terminal
		{name: "a terminal on a target that has none", target: rqtNoTTY, wire: func(p *execv2.Start) { p.Integration = nil }, class: unsupported, reason: ErrorReasonTTYUnsupported},
		{name: "no terminal on a target that has none", target: rqtNoTTY, wire: noTerminal},
		{name: "a size without a terminal", wire: func(p *execv2.Start) { p.Tty = false }, class: invalid},
		{name: "a terminal without a size", wire: func(p *execv2.Start) { p.Size = nil }},
		{name: "a terminal without a size, on a target of the smallest dimension limit a target may declare", target: func(tg *Target) { tg.Info.Session.MaxDimension = 80 }, wire: func(p *execv2.Start) { p.Size = nil }},
		{name: "a size of 80 x 80 on a target of the smallest dimension limit", target: func(tg *Target) { tg.Info.Session.MaxDimension = 80 }, wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 80, Rows: 80} }},
		{name: "a size of 81 x 24 on a target of the smallest dimension limit", target: func(tg *Target) { tg.Info.Session.MaxDimension = 80 }, wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 81, Rows: 24} }, class: invalid},
		{name: "a size of 500 x 500, the target's limit", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 500, Rows: 500} }},
		{name: "500 columns and 501 rows", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 500, Rows: 501} }, class: invalid},
		{name: "501 columns and 500 rows", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 501, Rows: 500} }, class: invalid},
		{name: "a size of 1 x 1", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 1, Rows: 1} }},
		{name: "a size that is present and zero is no size", wire: func(p *execv2.Start) { p.Size = &execv2.Size{} }},
		{name: "a size that is present and zero, without a terminal", wire: func(p *execv2.Start) { p.Tty = false; p.Size = &execv2.Size{} }},
		{name: "65536 columns", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 65536, Rows: 1} }, class: invalid},
		{name: "65536 rows", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 1, Rows: 65536} }, class: invalid},
		{name: "the largest columns and rows of the wire", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 1<<32 - 1, Rows: 1<<32 - 1} }, class: invalid},
		{name: "65537 columns, which would be 1 if truncated", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 65537, Rows: 1} }, class: invalid},
		{name: "no columns", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Rows: 24} }, class: invalid},
		{name: "no rows", wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 80} }, class: invalid},

		// env and cwd: the target and the build must both honor them, and are never silently ignored
		{name: "env on a target without env", target: noEnv, class: unsupported},
		{name: "cwd on a target without cwd", target: noCwd, class: unsupported},
		{name: "no env on a target without env", target: noEnv, wire: func(p *execv2.Start) { p.Env = nil }},
		{name: "no cwd on a target without cwd", target: noCwd, wire: func(p *execv2.Start) { p.Cwd = "" }},
		{name: "env when the build does not serve env", without: []execv2.Feature{rqtFeatureEnv}, class: unsupported},
		{name: "cwd when the build does not serve cwd", without: []execv2.Feature{rqtFeatureCwd}, class: unsupported},
		{name: "no env when the build does not serve env", without: []execv2.Feature{rqtFeatureEnv}, wire: func(p *execv2.Start) { p.Env = nil }},
		{name: "no cwd when the build does not serve cwd", without: []execv2.Feature{rqtFeatureCwd}, wire: func(p *execv2.Start) { p.Cwd = "" }},

		// the lease and the replay ring: parts of session management, so a build that does not serve it refuses a Start
		// that asks for either, and never ignores the ask
		{name: "no detach timeout", wire: func(p *execv2.Start) { p.DetachTimeoutMs = 0 }},
		{name: "a detach timeout of ten minutes, the target's maximum", wire: func(p *execv2.Start) { p.DetachTimeoutMs = rqtMaxDetachMillis }},
		{name: "a detach timeout of ten minutes and a millisecond", wire: func(p *execv2.Start) { p.DetachTimeoutMs = rqtMaxDetachMillis + 1 }, class: invalid},
		{name: "the largest detach timeout of the wire", wire: func(p *execv2.Start) { p.DetachTimeoutMs = 1<<32 - 1 }, class: invalid},
		{name: "no replay ring", wire: func(p *execv2.Start) { p.ReplayBytes = 0 }},
		{name: "a replay ring of the target's maximum", wire: func(p *execv2.Start) { p.ReplayBytes = rqtMaxReplayBytes }},
		{name: "a replay ring of the target's maximum and a byte", wire: func(p *execv2.Start) { p.ReplayBytes = rqtMaxReplayBytes + 1 }, class: invalid},
		{name: "the largest replay ring of the wire", wire: func(p *execv2.Start) { p.ReplayBytes = 1<<32 - 1 }, class: invalid},
		{name: "a lease and a replay ring when the build does not serve session management", without: []execv2.Feature{rqtFeatureSessionManagement}, class: unsupported},
		{name: "a lease alone when the build does not serve session management", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) { p.ReplayBytes = 0 }, class: unsupported},
		{name: "a replay ring alone when the build does not serve session management", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) { p.DetachTimeoutMs = 0 }, class: unsupported},
		{name: "neither when the build does not serve session management", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) {
			p.DetachTimeoutMs, p.ReplayBytes = 0, 0
		}},

		// the features the session asks for must be served
		{name: "marks and modes when the build serves neither", without: []execv2.Feature{rqtFeatureMarks, rqtFeatureModes}, class: unsupported},
		{name: "marks when the build does not serve them", without: []execv2.Feature{rqtFeatureMarks}, class: unsupported},
		{name: "modes when the build does not serve them", without: []execv2.Feature{rqtFeatureModes}, class: unsupported},
		{name: "marks alone when the build does not serve modes", without: []execv2.Feature{rqtFeatureModes}, wire: func(p *execv2.Start) {
			p.Features = []execv2.Feature{execv2.Feature_FEATURE_MARKS}
		}},
		{name: "no features when the build serves none", without: []execv2.Feature{rqtFeatureMarks, rqtFeatureModes}, wire: func(p *execv2.Start) { p.Features = nil }},

		// the integration the session asks for. No message, a message with no mode and a message with the mode none are
		// no integration: nothing is asked, so no consent, no listed mode and no served feature is needed. A mode needs
		// the host's consent, a target that lists it and a build that serves integration; a mode this build does not
		// know is unsupported.
		{name: "integration on a target that lists no mode", target: noIntegration, class: unsupported},
		{name: "integration when the build does not serve it", without: []execv2.Feature{rqtFeatureIntegration}, class: unsupported},
		{name: "no integration on a target that lists no mode", target: noIntegration, wire: func(p *execv2.Start) { p.Integration = nil }},
		{name: "no integration when the build does not serve it", without: []execv2.Feature{rqtFeatureIntegration}, wire: func(p *execv2.Start) { p.Integration = nil }},
		{name: "an integration with no mode when the build does not serve it", without: []execv2.Feature{rqtFeatureIntegration}, wire: func(p *execv2.Start) {
			p.Integration = &execv2.IntegrationRequest{Consent: true}
		}},
		{name: "mode none with consent, on a target that lists no mode", target: noIntegration, wire: integration(none, true)},
		{name: "mode none without consent, on a target that lists no mode", target: noIntegration, wire: integration(none, false)},
		{name: "mode none with consent, when the build does not serve integration", without: []execv2.Feature{rqtFeatureIntegration}, wire: integration(none, true)},
		{name: "mode none without consent, when the build does not serve integration", without: []execv2.Feature{rqtFeatureIntegration}, wire: integration(none, false)},
		{name: "mode unspecified with consent, on a target that lists no mode", target: noIntegration, wire: integration(unspecified, true)},
		{name: "mode unspecified without consent, on a target that lists no mode", target: noIntegration, wire: integration(unspecified, false)},
		{name: "mode unspecified without consent, when the build does not serve integration", without: []execv2.Feature{rqtFeatureIntegration}, wire: integration(unspecified, false)},
		{name: "mode none without consent, with no terminal", target: rqtNoTTY, wire: func(p *execv2.Start) {
			noTerminal(p)
			p.Integration = &execv2.IntegrationRequest{Mode: none}
		}},
		{name: "the argv wrapper with consent", wire: integration(wrapper, true)},
		{name: "the argv wrapper without consent", wire: integration(wrapper, false), class: invalid, reason: ErrorReasonConsentRequired},
		{name: "the argv wrapper with consent, on a target that lists no mode", target: noIntegration, wire: integration(wrapper, true), class: unsupported},
		{name: "the argv wrapper with consent, when the build does not serve integration", without: []execv2.Feature{rqtFeatureIntegration}, wire: integration(wrapper, true), class: unsupported},
		{name: "an unknown mode with consent", wire: integration(99, true), class: unsupported},
		{name: "an unknown mode without consent: consent comes first", wire: integration(99, false), class: invalid, reason: ErrorReasonConsentRequired},
		{name: "consent is checked before the target's modes", target: noIntegration, wire: integration(wrapper, false), class: invalid, reason: ErrorReasonConsentRequired},
		{name: "consent is checked before the build's features", without: []execv2.Feature{rqtFeatureIntegration}, wire: integration(wrapper, false), class: invalid, reason: ErrorReasonConsentRequired},

		// a request that breaks two rules is refused by the one the order of the checks puts first
		{name: "order: an unknown field comes before everything", wire: func(p *execv2.Start) {
			p.Argv = []string{"\x00"}
			rqtSetUnknown(p)
		}, class: unsupported},
		{name: "order: the target comes before the features", wire: func(p *execv2.Start) {
			p.Target.Route = nil
			p.Features = []execv2.Feature{99}
		}, class: invalid},
		{name: "order: the request's own bounds come before the target's capabilities", target: rqtNoTTY, wire: func(p *execv2.Start) {
			p.Argv = []string{"\x00"}
			p.Integration = nil
		}, class: invalid},
		{name: "order: consent comes before an unknown mode", wire: integration(99, false), class: invalid, reason: ErrorReasonConsentRequired},
		{name: "order: a target without sessions comes before the terminal", target: func(tg *Target) { noSessions(tg); rqtNoTTY(tg) }, wire: func(p *execv2.Start) { p.Integration = nil }, class: unsupported},
		{name: "order: a missing default argv comes before the terminal", target: func(tg *Target) { noDefault(tg); rqtNoTTY(tg) }, wire: func(p *execv2.Start) { p.Argv = nil; p.Integration = nil }, class: invalid},
		{name: "order: the terminal comes before the size limit", target: rqtNoTTY, wire: func(p *execv2.Start) {
			p.Size = &execv2.Size{Cols: 501, Rows: 40}
			p.Integration = nil
		}, class: unsupported, reason: ErrorReasonTTYUnsupported},
		{name: "order: a size without a terminal comes before env", target: noEnv, wire: func(p *execv2.Start) { p.Tty = false }, class: invalid},
		{name: "order: the size limit comes before env", target: noEnv, wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 501, Rows: 40} }, class: invalid},
		{name: "order: env comes before the lease", target: noEnv, wire: func(p *execv2.Start) { p.DetachTimeoutMs = rqtMaxDetachMillis + 1 }, class: unsupported},
		{name: "order: cwd comes before the replay ring", target: noCwd, wire: func(p *execv2.Start) { p.ReplayBytes = rqtMaxReplayBytes + 1 }, class: unsupported},
		{name: "order: the size limit comes before session management", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) { p.Size = &execv2.Size{Cols: 501, Rows: 40} }, class: invalid},
		{name: "order: session management comes before the lease's limit", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) { p.DetachTimeoutMs = rqtMaxDetachMillis + 1 }, class: unsupported},
		{name: "order: session management comes before the replay ring's limit", without: []execv2.Feature{rqtFeatureSessionManagement}, wire: func(p *execv2.Start) { p.ReplayBytes = rqtMaxReplayBytes + 1 }, class: unsupported},
		{name: "order: the lease comes before the features", without: []execv2.Feature{rqtFeatureMarks}, wire: func(p *execv2.Start) { p.DetachTimeoutMs = rqtMaxDetachMillis + 1 }, class: invalid},
		{name: "order: the replay ring comes before the features", without: []execv2.Feature{rqtFeatureMarks}, wire: func(p *execv2.Start) { p.ReplayBytes = rqtMaxReplayBytes + 1 }, class: invalid},
		{name: "order: the features come before the integration", without: []execv2.Feature{rqtFeatureMarks, rqtFeatureIntegration}, class: unsupported},
	}
}

// TestAdmitStartAgainstTheTarget is the table of what a Start must be against the registered target and the
// build's features: each limit's boundary value is accepted and the first value beyond it is refused.
func TestAdmitStartAgainstTheTarget(t *testing.T) {
	for _, row := range rqtStartRows() {
		t.Run(row.name, func(t *testing.T) {
			_, target := rqtTarget(t, row.target)
			wire := rqtWireStart()
			if row.wire != nil {
				row.wire(wire)
			}
			got, refusal := admitStart(wire, target, rqtServed(row.without...))
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

// TestAdmitStartReturnsTheRequestAsSent checks that admission does not fill defaults: the Start is what the host
// sent, and startRequest.request fills the defaults.
func TestAdmitStartReturnsTheRequestAsSent(t *testing.T) {
	_, target := rqtTarget(t, nil)
	wire := &execv2.Start{Target: rqtHandWireTarget(), Tty: true}
	got, refusal := admitStart(wire, target, rqtServed())
	require.Nil(t, refusal)
	require.Nil(t, got.Argv, "the default argv is not filled in")
	require.True(t, got.Size.IsZero(), "the default size is not filled in")
	require.True(t, got.TTY)
	require.Zero(t, got.DetachTimeout, "the default lease is not filled in")
	require.Zero(t, got.ReplayBytes, "the default replay ring is not filled in")
	require.Nil(t, got.Env)
	require.Nil(t, got.Labels)
	require.Nil(t, got.Features)
	require.Nil(t, got.Integration, "no integration message is no integration")
	require.Equal(t, map[string]string{"container": "app"}, got.Selection)

	t.Run("a mode of none or unspecified is no integration, however the flag is set", func(t *testing.T) {
		for _, mode := range []execv2.IntegrationMode{execv2.IntegrationMode_INTEGRATION_MODE_UNSPECIFIED, execv2.IntegrationMode_INTEGRATION_MODE_NONE} {
			for _, consent := range []bool{false, true} {
				wire := rqtWireStart()
				wire.Integration = &execv2.IntegrationRequest{Mode: mode, Consent: consent}
				got, refusal := admitStart(wire, target, rqtServed(rqtFeatureIntegration))
				require.Nil(t, refusal, "mode %s consent %v", mode, consent)
				require.Nil(t, got.Integration, "mode %s consent %v", mode, consent)
			}
		}
	})

	t.Run("a mode is carried with the consent that was given", func(t *testing.T) {
		wire := rqtWireStart()
		got, refusal := admitStart(wire, target, rqtServed())
		require.Nil(t, refusal)
		require.Equal(t, &integrationRequest{Mode: IntegrationArgvWrapper, Consent: true}, got.Integration)
	})

	t.Run("features keep the order they were sent in", func(t *testing.T) {
		wire := rqtWireStart()
		wire.Features = []execv2.Feature{execv2.Feature_FEATURE_MODES, execv2.Feature_FEATURE_MARKS}
		got, refusal := admitStart(wire, target, rqtServed())
		require.Nil(t, refusal)
		require.Equal(t, []execv2.Feature{rqtFeatureModes, rqtFeatureMarks}, got.Features)
	})
}

// rqtRunRow is one rule of a RunRequest that depends on the registered target or on what the build serves.
type rqtRunRow struct {
	name    string
	target  func(*Target)            // changes the registered target
	without []execv2.Feature         // the features the build does not serve
	wire    func(*execv2.RunRequest) // changes the valid message
	class   ErrorClass               // zero: admitted
	reason  ErrorReason
}

// rqtTightRun gives the target small run limits, below the Start's 64 KiB and the contract's ceilings.
func rqtTightRun(tg *Target) {
	tg.Info.Run = RunLimits{
		DefaultTimeout:     time.Second,
		MaxTimeout:         2 * time.Second,
		DefaultOutputBytes: 100,
		MaxOutputBytes:     1000,
		MaxStdinBytes:      1000,
		MaxConcurrent:      1,
		MaxArgvBytes:       1000,
		MaxEnvBytes:        1000,
		PostExitDrain:      time.Millisecond,
	}
}

// rqtRunRows returns the rows of the rules a RunRequest has against its target and its build, the way
// rqtStartRows does for a Start.
func rqtRunRows() []rqtRunRow {
	invalid, unsupported := ErrorClassInvalidRequest, ErrorClassUnsupported
	noEnv := func(tg *Target) { tg.Info.Capabilities.Env = false }
	noCwd := func(tg *Target) { tg.Info.Capabilities.Cwd = false }
	noRuns := func(tg *Target) { tg.Info.Run = RunLimits{} }
	withUnknownPolicy := func(p *execv2.RunRequest) { p.Output.Policy = execv2.TruncatePolicy(99) }
	// tight gives a run the small caps of a tight target, then applies change: the valid message of the base target
	// would break them (its timeout and output caps are above a tight target's maximum).
	tight := func(change func(*execv2.RunRequest)) func(*execv2.RunRequest) {
		return func(p *execv2.RunRequest) {
			p.Stdin = nil
			p.TimeoutMs = 1000
			p.Output = &execv2.OutputLimits{MaxStdoutBytes: 500, MaxStderrBytes: 500}
			change(p)
		}
	}

	return []rqtRunRow{
		{name: "valid"},

		// the target is registered, serves runs, and so does the build
		{name: "a provider that has no target", wire: func(p *execv2.RunRequest) { p.Target.Reference.Address.Provider = "other" }, class: unsupported},
		{name: "a type that has no target", wire: func(p *execv2.RunRequest) { p.Target.Reference.Address.Type = "services" }, class: unsupported},
		{name: "a target that serves no runs", target: noRuns, class: unsupported},
		{name: "a build that does not serve runs", without: []execv2.Feature{rqtFeatureRun}, class: unsupported},

		// argv, env: the target's totals
		{name: "argv total of the target's limit", wire: func(p *execv2.RunRequest) { p.Argv = rqtArgvOfSize(rqtRunArgvBytes) }},
		{name: "argv total of the target's limit + 1", wire: func(p *execv2.RunRequest) { p.Argv = rqtArgvOfSize(rqtRunArgvBytes + 1) }, class: invalid},
		{name: "argv total of a tight target's limit", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Argv = rqtArgvOfSize(1000) })},
		{name: "argv total of a tight target's limit + 1", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Argv = rqtArgvOfSize(1001) }), class: invalid},
		{name: "env total of the target's limit", wire: func(p *execv2.RunRequest) { p.Env = rqtListOf(rqtEnvOfSize(rqtRunEnvBytes)) }},
		{name: "env total of the target's limit + 1", wire: func(p *execv2.RunRequest) { p.Env = rqtListOf(rqtEnvOfSize(rqtRunEnvBytes + 1)) }, class: invalid},
		{name: "env total of a tight target's limit", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Env = rqtListOf(rqtEnvOfSize(1000)) })},
		{name: "env total of a tight target's limit + 1", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Env = rqtListOf(rqtEnvOfSize(1001)) }), class: invalid},

		// stdin: a limit, not an invalid request
		{name: "no stdin", wire: func(p *execv2.RunRequest) { p.Stdin = nil }},
		{name: "stdin of the target's limit", wire: func(p *execv2.RunRequest) { p.Stdin = make([]byte, rqtRunStdinBytes) }},
		{name: "stdin of the target's limit + 1", wire: func(p *execv2.RunRequest) { p.Stdin = make([]byte, rqtRunStdinBytes+1) }, class: ErrorClassLimit, reason: ErrorReasonInputTooLarge},
		{name: "stdin of a tight target's limit", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Stdin = make([]byte, 1000) })},
		{name: "stdin of a tight target's limit + 1", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Stdin = make([]byte, 1001) }), class: ErrorClassLimit, reason: ErrorReasonInputTooLarge},

		// env and cwd: never ignored
		{name: "env on a target without env", target: noEnv, class: unsupported},
		{name: "cwd on a target without cwd", target: noCwd, class: unsupported},
		{name: "no env on a target without env", target: noEnv, wire: func(p *execv2.RunRequest) { p.Env = nil }},
		{name: "no cwd on a target without cwd", target: noCwd, wire: func(p *execv2.RunRequest) { p.Cwd = "" }},
		{name: "env when the build does not serve env", without: []execv2.Feature{rqtFeatureEnv}, class: unsupported},
		{name: "cwd when the build does not serve cwd", without: []execv2.Feature{rqtFeatureCwd}, class: unsupported},
		{name: "no env when the build does not serve env", without: []execv2.Feature{rqtFeatureEnv}, wire: func(p *execv2.RunRequest) { p.Env = nil }},
		{name: "no cwd when the build does not serve cwd", without: []execv2.Feature{rqtFeatureCwd}, wire: func(p *execv2.RunRequest) { p.Cwd = "" }},

		// the timeout is never clamped
		{name: "the default timeout", wire: func(p *execv2.RunRequest) { p.TimeoutMs = 0 }},
		{name: "a timeout of the target's maximum", wire: func(p *execv2.RunRequest) { p.TimeoutMs = rqtRunTimeoutMillis }},
		{name: "a timeout of the target's maximum + 1 ms", wire: func(p *execv2.RunRequest) { p.TimeoutMs = rqtRunTimeoutMillis + 1 }, class: invalid},
		{name: "the largest timeout of the wire", wire: func(p *execv2.RunRequest) { p.TimeoutMs = 1<<32 - 1 }, class: invalid},
		{name: "a timeout of a tight target's maximum", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.TimeoutMs = 2000 })},
		{name: "a timeout of a tight target's maximum + 1 ms", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.TimeoutMs = 2001 }), class: invalid},

		// the output caps are never clamped
		{name: "no output limits", wire: func(p *execv2.RunRequest) { p.Output = nil }},
		{name: "default output caps", wire: func(p *execv2.RunRequest) { p.Output = &execv2.OutputLimits{} }},
		{name: "stdout at the target's maximum", wire: func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = rqtRunOutputBytes }},
		{name: "stdout at the target's maximum + 1", wire: func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = rqtRunOutputBytes + 1 }, class: invalid},
		{name: "stderr at the target's maximum", wire: func(p *execv2.RunRequest) { p.Output.MaxStderrBytes = rqtRunOutputBytes }},
		{name: "stderr at the target's maximum + 1", wire: func(p *execv2.RunRequest) { p.Output.MaxStderrBytes = rqtRunOutputBytes + 1 }, class: invalid},
		{name: "the largest stdout cap of the wire", wire: func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = 1<<32 - 1 }, class: invalid},
		{name: "stdout at a tight target's maximum", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = 1000 })},
		{name: "stdout at a tight target's maximum + 1", target: rqtTightRun, wire: tight(func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = 1001 }), class: invalid},

		// a policy this build does not know is a value of a newer peer: unsupported, never read as the nearest policy
		{name: "a policy this build does not know", wire: withUnknownPolicy, class: unsupported},
		{name: "a negative policy", wire: func(p *execv2.RunRequest) { p.Output.Policy = execv2.TruncatePolicy(-1) }, class: unsupported},
		{name: "the policy after the last this build has", wire: func(p *execv2.RunRequest) { p.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL + 1 }, class: unsupported},
		{name: "every policy this build has", wire: func(p *execv2.RunRequest) { p.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD }},

		// a request that breaks two rules is refused by the one the order of the checks puts first
		{name: "order: an unknown field comes before everything", wire: func(p *execv2.RunRequest) {
			p.Argv = nil
			rqtSetUnknown(p)
		}, class: unsupported},
		{name: "order: the request id comes before the target", wire: func(p *execv2.RunRequest) {
			p.RequestId = "a b"
			p.Target.Reference.Address.Provider = "other"
		}, class: invalid},
		{name: "order: the target's registration comes before the request's own bounds", wire: func(p *execv2.RunRequest) {
			p.Argv = []string{"\x00"}
			p.Target.Reference.Address.Provider = "other"
		}, class: unsupported},
		{name: "order: the request's own bounds come before what the target serves", target: noRuns, wire: func(p *execv2.RunRequest) { p.Argv = []string{"\x00"} }, class: invalid},
		{name: "order: the labels come before the policy", wire: func(p *execv2.RunRequest) {
			p.Labels = rqtListOf(rqtLabelsOfCount(17))
			withUnknownPolicy(p)
		}, class: invalid},
		{name: "order: the policy comes before what the target serves", wire: func(p *execv2.RunRequest) {
			p.Argv = rqtArgvOfSize(rqtRunArgvBytes + 1)
			withUnknownPolicy(p)
		}, class: unsupported},
		{name: "order: the argv total comes before stdin", wire: func(p *execv2.RunRequest) {
			p.Argv = rqtArgvOfSize(rqtRunArgvBytes + 1)
			p.Stdin = make([]byte, rqtRunStdinBytes+1)
		}, class: invalid},
		{name: "order: stdin comes before the env total", wire: func(p *execv2.RunRequest) {
			p.Stdin = make([]byte, rqtRunStdinBytes+1)
			p.Env = rqtListOf(rqtEnvOfSize(rqtRunEnvBytes + 1))
		}, class: ErrorClassLimit, reason: ErrorReasonInputTooLarge},
		{name: "order: the env total comes before the target's env capability", target: noEnv, wire: func(p *execv2.RunRequest) { p.Env = rqtListOf(rqtEnvOfSize(rqtRunEnvBytes + 1)) }, class: invalid},
		{name: "order: env comes before the timeout", target: noEnv, wire: func(p *execv2.RunRequest) { p.TimeoutMs = rqtRunTimeoutMillis + 1 }, class: unsupported},
		{name: "order: cwd comes before the output caps", target: noCwd, wire: func(p *execv2.RunRequest) { p.Output.MaxStdoutBytes = rqtRunOutputBytes + 1 }, class: unsupported},
	}
}

// TestAdmitRunRequestAgainstTheTarget is the table of what a RunRequest must be against the registered target and the
// build's features: each limit's boundary value is accepted and the first value beyond it is refused, never
// clamped.
func TestAdmitRunRequestAgainstTheTarget(t *testing.T) {
	for _, row := range rqtRunRows() {
		t.Run(row.name, func(t *testing.T) {
			reg, target := rqtTarget(t, row.target)
			wire := rqtWireRun()
			if row.wire != nil {
				row.wire(wire)
			}
			got, admitted, refusal := admitRunRequest(wire, reg, rqtServed(row.without...))
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

// TestAdmitRunRequestReturnsTheRequestAsSent checks that admission does not fill defaults.
func TestAdmitRunRequestReturnsTheRequestAsSent(t *testing.T) {
	reg, _ := rqtTarget(t, nil)
	wire := &execv2.RunRequest{Target: rqtHandWireTarget(), Argv: []string{"true"}}
	got, _, refusal := admitRunRequest(wire, reg, rqtServed())
	require.Nil(t, refusal)
	require.Zero(t, got.Timeout, "the default timeout is not filled in")
	require.Zero(t, got.Output, "the default output caps are not filled in")
	require.Nil(t, got.Stdin)
	require.Nil(t, got.Env)
	require.Nil(t, got.Labels)
	require.Empty(t, got.RequestID)
	require.Equal(t, map[string]string{"container": "app"}, got.Selection)

	t.Run("a timeout is in milliseconds", func(t *testing.T) {
		wire := rqtWireRun()
		wire.TimeoutMs = 1500
		got, _, refusal := admitRunRequest(wire, reg, rqtServed())
		require.Nil(t, refusal)
		require.Equal(t, 1500*time.Millisecond, got.Timeout)
	})

	t.Run("a policy this build knows is carried", func(t *testing.T) {
		for _, policy := range []execv2.TruncatePolicy{
			execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED,
			execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL,
			execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD,
			execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL,
		} {
			wire := rqtWireRun()
			wire.Output.Policy = policy
			got, _, refusal := admitRunRequest(wire, reg, rqtServed())
			require.Nil(t, refusal, "policy %s", policy)
			require.Equal(t, policy, got.Output.Policy)
		}
	})
}

// TestRequestSizeBounds covers the size of a Start at the boundaries of the Go type behind it: the Go Size holds
// uint16, so a wire value beyond 65535 is refused and never truncated, and a size is both dimensions or neither.
func TestRequestSizeBounds(t *testing.T) {
	rows := []struct {
		name string
		wire *execv2.Size
		want Size
		bad  bool
	}{
		{name: "absent", wire: nil},
		{name: "present and zero", wire: &execv2.Size{}},
		{name: "1 x 1", wire: &execv2.Size{Cols: 1, Rows: 1}, want: Size{Cols: 1, Rows: 1}},
		{name: "80 x 24", wire: &execv2.Size{Cols: 80, Rows: 24}, want: Size{Cols: 80, Rows: 24}},
		{name: "65535 x 65535", wire: &execv2.Size{Cols: 65535, Rows: 65535}, want: Size{Cols: 65535, Rows: 65535}},
		{name: "65535 x 1", wire: &execv2.Size{Cols: 65535, Rows: 1}, want: Size{Cols: 65535, Rows: 1}},
		{name: "1 x 65535", wire: &execv2.Size{Cols: 1, Rows: 65535}, want: Size{Cols: 1, Rows: 65535}},
		{name: "65536 columns", wire: &execv2.Size{Cols: 65536, Rows: 1}, bad: true},
		{name: "65536 rows", wire: &execv2.Size{Cols: 1, Rows: 65536}, bad: true},
		{name: "65537 columns", wire: &execv2.Size{Cols: 65537, Rows: 1}, bad: true},
		{name: "65536 x 65536", wire: &execv2.Size{Cols: 65536, Rows: 65536}, bad: true},
		{name: "the largest value of the wire", wire: &execv2.Size{Cols: 1<<32 - 1, Rows: 1<<32 - 1}, bad: true},
		{name: "no columns", wire: &execv2.Size{Rows: 24}, bad: true},
		{name: "no rows", wire: &execv2.Size{Cols: 80}, bad: true},
		{name: "no columns and too many rows", wire: &execv2.Size{Rows: 65536}, bad: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, refusal := requestSizeFromProto(row.wire)
			if row.bad {
				require.Zero(t, got)
				rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
				return
			}
			require.Nil(t, refusal)
			require.Equal(t, row.want, got)
		})
	}
}

// TestAdmitOpenRequest covers the first frame of an Open: the session id, exactly one of a start and an attach, an
// attach that names no target and a start that is admitted against the target its reference names.
func TestAdmitOpenRequest(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	invalid := ErrorClassInvalidRequest

	attach := func(id string, offset uint64) *execv2.OpenRequest {
		return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: offset}}}
	}
	start := func(id string, change func(*execv2.Start)) *execv2.OpenRequest {
		s := rqtWireStart()
		if change != nil {
			change(s)
		}
		return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: s}}
	}

	rows := []struct {
		name    string
		wire    *execv2.OpenRequest
		without []execv2.Feature
		class   ErrorClass
		reason  ErrorReason
	}{
		// the session id
		{name: "a session id of one byte", wire: attach("!", 0)},
		{name: "a session id of 128 bytes", wire: attach(rqtBytes(128), 0)},
		{name: "a session id of 129 bytes", wire: attach(rqtBytes(129), 0), class: invalid},
		{name: "an empty session id", wire: attach("", 0), class: invalid},
		{name: "the first printable byte", wire: attach("!", 0)},
		{name: "the last printable byte", wire: attach("~", 0)},
		{name: "a space", wire: attach(" ", 0), class: invalid},
		{name: "a session id with a space inside", wire: attach("a b", 0), class: invalid},
		{name: "a session id with DEL", wire: attach("a\x7f", 0), class: invalid},
		{name: "a session id with a control character", wire: attach("a\x1f", 0), class: invalid},
		{name: "a session id with a NUL", wire: attach("a\x00", 0), class: invalid},
		{name: "a session id with a non-ASCII letter", wire: attach("caf\u00e9", 0), class: invalid},
		{name: "a session id with a newline", wire: attach("a\n", 0), class: invalid},
		{name: "a session id of 128 bytes with a start", wire: start(rqtBytes(128), nil)},
		{name: "a session id of 129 bytes with a start", wire: start(rqtBytes(129), nil), class: invalid},

		// exactly one of a start and an attach
		{name: "neither a start nor an attach", wire: &execv2.OpenRequest{SessionId: "s-1"}, class: invalid},
		{name: "a start arm without a start", wire: &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{}}, class: invalid},
		{name: "an attach arm without an attach", wire: &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Attach{}}, class: invalid},
		{name: "an empty message", wire: &execv2.OpenRequest{}, class: invalid},

		// an attach names no target, so nothing of the registry is asked; the build must serve sessions and their
		// management, and nothing else of the lane
		{name: "an attach", wire: attach("s-1", 12345)},
		{name: "an attach from the beginning", wire: attach("s-1", 0)},
		{name: "an attach from the largest offset", wire: attach("s-1", 1<<64-1)},
		{name: "an attach in a build that serves no features", wire: attach("s-1", 0), without: rqtAllFeatures, class: ErrorClassUnsupported},
		{name: "an attach in a build that does not serve sessions", wire: attach("s-1", 0), without: []execv2.Feature{rqtFeatureSession}, class: ErrorClassUnsupported},
		{name: "an attach in a build that does not serve session management", wire: attach("s-1", 0), without: []execv2.Feature{rqtFeatureSessionManagement}, class: ErrorClassUnsupported},
		{name: "an attach in a build that serves only sessions and their management", wire: attach("s-1", 0), without: []execv2.Feature{
			rqtFeatureMarks, rqtFeatureRunStream, rqtFeatureEnv, rqtFeatureCwd, rqtFeatureStdinEOF, rqtFeatureModes, rqtFeatureIntegration, rqtFeatureRun,
		}},
		{name: "a bad session id comes before the build's features", wire: attach("a b", 0), without: rqtAllFeatures, class: invalid},

		// a start is admitted against the target its reference names
		{name: "a start", wire: start("s-1", nil)},
		{name: "a start of an unregistered provider", wire: start("s-1", func(s *execv2.Start) { s.Target.Reference.Address.Provider = "other" }), class: ErrorClassUnsupported},
		{name: "a start of an unregistered type", wire: start("s-1", func(s *execv2.Start) { s.Target.Reference.Address.Type = "services" }), class: ErrorClassUnsupported},
		{name: "a start without a target", wire: start("s-1", func(s *execv2.Start) { s.Target = nil }), class: invalid},
		{name: "a start that breaks a bound", wire: start("s-1", func(s *execv2.Start) { s.Argv = []string{"\x00"} }), class: invalid},
		{name: "a start that the target cannot serve", wire: start("s-1", func(s *execv2.Start) { s.Env = rqtListOf(rqtEnvOfCount(1)) }), without: []execv2.Feature{rqtFeatureEnv}, class: ErrorClassUnsupported},
		{name: "a start in a build that serves no sessions", wire: start("s-1", nil), without: []execv2.Feature{rqtFeatureSession}, class: ErrorClassUnsupported},
		{name: "a start that asks for a lease in a build that serves no session management", wire: start("s-1", nil), without: []execv2.Feature{rqtFeatureSessionManagement}, class: ErrorClassUnsupported},
		{name: "a start that asks for neither a lease nor a replay ring, in a build that serves no session management", wire: start("s-1", func(s *execv2.Start) {
			s.DetachTimeoutMs, s.ReplayBytes = 0, 0
		}), without: []execv2.Feature{rqtFeatureSessionManagement}},
		{name: "a start that asks for consent it does not give", wire: start("s-1", func(s *execv2.Start) { s.Integration.Consent = false }), class: invalid, reason: ErrorReasonConsentRequired},
		{name: "a start of mode none", wire: start("s-1", func(s *execv2.Start) {
			s.Integration = &execv2.IntegrationRequest{Mode: execv2.IntegrationMode_INTEGRATION_MODE_NONE}
		})},

		// the order of the checks
		{name: "order: a bad session id comes before an unregistered target", wire: start("a b", func(s *execv2.Start) { s.Target.Reference.Address.Provider = "other" }), class: invalid},
		{name: "order: a bad session id comes before a bad start", wire: start("a b", func(s *execv2.Start) { s.Argv = []string{"\x00"} }), class: invalid},
		{name: "order: an unregistered target comes before the start's bounds", wire: start("s-1", func(s *execv2.Start) {
			s.Target.Reference.Address.Provider = "other"
			s.Argv = []string{"\x00"}
		}), class: ErrorClassUnsupported},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, admitted, refusal := admitOpenRequest(row.wire, reg, rqtServed(row.without...))
			if row.class != ErrorClassUnspecified {
				require.Zero(t, got, "a refused request leaves nothing behind")
				require.Nil(t, admitted)
				rqtRequireRefusal(t, refusal, row.class, row.reason)
				return
			}
			require.Nil(t, refusal)
			require.Equal(t, row.wire.GetSessionId(), got.SessionID)
			if row.wire.GetStart() != nil {
				require.Nil(t, got.Attach)
				require.NotNil(t, got.Start)
				require.Same(t, target, admitted, "a start is admitted against the registered target")
				want, refusal := admitStart(row.wire.GetStart(), target, rqtServed(row.without...))
				require.Nil(t, refusal)
				require.Equal(t, want, *got.Start, "an Open admits its start exactly as admitStart does")
				return
			}
			require.Nil(t, got.Start)
			require.Equal(t, &attachRequest{FromOffset: row.wire.GetAttach().GetFromOffset()}, got.Attach)
			require.Nil(t, admitted, "an attach names no target: the engine finds the session")
		})
	}

	t.Run("an attach needs no registered target", func(t *testing.T) {
		empty, err := newTargetRegistry(nil)
		require.NoError(t, err)
		got, admitted, refusal := admitOpenRequest(attach("s-1", 7), empty, rqtServed())
		require.Nil(t, refusal)
		require.Nil(t, admitted)
		require.Equal(t, openRequest{SessionID: "s-1", Attach: &attachRequest{FromOffset: 7}}, got)
	})
}

// TestAdmitNilAndAbsent checks that a nil message, a nil registered target, a nil registry and a nil feature set are
// refusals and never panics.
func TestAdmitNilAndAbsent(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	t.Run("nil messages", func(t *testing.T) {
		start, refusal := admitStart(nil, target, served)
		require.Zero(t, start)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)

		var typedNil *execv2.Start
		start, refusal = admitStart(typedNil, target, served)
		require.Zero(t, start)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)

		run, admitted, refusal := admitRunRequest(nil, reg, served)
		require.Zero(t, run)
		require.Nil(t, admitted)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)

		open, admitted, refusal := admitOpenRequest(nil, reg, served)
		require.Zero(t, open)
		require.Nil(t, admitted)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
	})

	t.Run("empty messages", func(t *testing.T) {
		_, refusal := admitStart(&execv2.Start{}, target, served)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
		_, _, refusal = admitRunRequest(&execv2.RunRequest{}, reg, served)
		rqtRequireRefusal(t, refusal, ErrorClassInvalidRequest, ErrorReasonUnspecified)
	})

	t.Run("no registered target", func(t *testing.T) {
		_, refusal := admitStart(rqtWireStart(), nil, served)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
	})

	t.Run("no registry", func(t *testing.T) {
		_, _, refusal := admitRunRequest(rqtWireRun(), nil, served)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		start := &execv2.OpenRequest{SessionId: "s", Kind: &execv2.OpenRequest_Start{Start: rqtWireStart()}}
		_, _, refusal = admitOpenRequest(start, nil, served)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
	})

	t.Run("no feature set serves nothing", func(t *testing.T) {
		_, refusal := admitStart(rqtWireStart(), target, nil)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		_, _, refusal = admitRunRequest(rqtWireRun(), reg, nil)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		attach := &execv2.OpenRequest{SessionId: "s", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}}
		_, _, refusal = admitOpenRequest(attach, reg, nil)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
	})
}

// rqtSetUnknown gives m a field its descriptor does not know, the way a message of a newer peer arrives.
func rqtSetUnknown(m proto.Message) {
	raw := protowire.AppendTag(nil, 1000, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 1)
	m.ProtoReflect().SetUnknown(raw)
}

// rqtThroughTheWire marshals m and unmarshals the bytes into into: what a message looks like after the network.
func rqtThroughTheWire(t *testing.T, m, into proto.Message) {
	t.Helper()
	raw, err := proto.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, proto.Unmarshal(raw, into))
}

// TestAdmitRejectsUnknownFields puts a field the descriptor does not know in every message of a request, at the top
// and nested (a TargetRef's reference address, a size, an output limit, an attach): the plugin refuses the request
// UNSUPPORTED, because a request field can change what a command may do, whichever entry point it came through. The
// fields inside the elements of a list are the matrix of admission_namevalue_test.go.
func TestAdmitRejectsUnknownFields(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	starts := map[string]func(*execv2.Start){
		"start":                          func(s *execv2.Start) { rqtSetUnknown(s) },
		"start.target":                   func(s *execv2.Start) { rqtSetUnknown(s.Target) },
		"start.target.reference":         func(s *execv2.Start) { rqtSetUnknown(s.Target.Reference) },
		"start.target.reference.address": func(s *execv2.Start) { rqtSetUnknown(s.Target.Reference.Address) },
		"start.target.reference.address.scope[0]": func(s *execv2.Start) { rqtSetUnknown(s.Target.Reference.Address.Scope[0]) },
		"start.target.route":                      func(s *execv2.Start) { rqtSetUnknown(s.Target.Route) },
		"start.size":                              func(s *execv2.Start) { rqtSetUnknown(s.Size) },
		"start.integration":                       func(s *execv2.Start) { rqtSetUnknown(s.Integration) },
	}
	for name, set := range starts {
		t.Run("admitStart/"+name, func(t *testing.T) {
			wire := rqtWireStart()
			set(wire)
			got, refusal := admitStart(wire, target, served)
			require.Zero(t, got)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)

			decoded := new(execv2.Start)
			rqtThroughTheWire(t, wire, decoded)
			got, refusal = admitStart(decoded, target, served)
			require.Zero(t, got, "after the wire")
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		})
		t.Run("admitOpenRequest/"+name, func(t *testing.T) {
			wire := rqtWireStart()
			set(wire)
			open := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: wire}}
			got, admitted, refusal := admitOpenRequest(open, reg, served)
			require.Zero(t, got)
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)

			decoded := new(execv2.OpenRequest)
			rqtThroughTheWire(t, open, decoded)
			got, admitted, refusal = admitOpenRequest(decoded, reg, served)
			require.Zero(t, got, "after the wire")
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		})
	}

	runs := map[string]func(*execv2.RunRequest){
		"run":                                   func(r *execv2.RunRequest) { rqtSetUnknown(r) },
		"run.target":                            func(r *execv2.RunRequest) { rqtSetUnknown(r.Target) },
		"run.target.reference":                  func(r *execv2.RunRequest) { rqtSetUnknown(r.Target.Reference) },
		"run.target.reference.address":          func(r *execv2.RunRequest) { rqtSetUnknown(r.Target.Reference.Address) },
		"run.target.reference.address.scope[0]": func(r *execv2.RunRequest) { rqtSetUnknown(r.Target.Reference.Address.Scope[0]) },
		"run.target.route":                      func(r *execv2.RunRequest) { rqtSetUnknown(r.Target.Route) },
		"run.output":                            func(r *execv2.RunRequest) { rqtSetUnknown(r.Output) },
	}
	for name, set := range runs {
		t.Run("admitRunRequest/"+name, func(t *testing.T) {
			wire := rqtWireRun()
			set(wire)
			got, admitted, refusal := admitRunRequest(wire, reg, served)
			require.Zero(t, got)
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)

			decoded := new(execv2.RunRequest)
			rqtThroughTheWire(t, wire, decoded)
			got, admitted, refusal = admitRunRequest(decoded, reg, served)
			require.Zero(t, got, "after the wire")
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		})
	}

	opens := map[string]func() *execv2.OpenRequest{
		"open": func() *execv2.OpenRequest {
			open := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{}}}
			rqtSetUnknown(open)
			return open
		},
		"open.attach": func() *execv2.OpenRequest {
			attach := &execv2.Attach{FromOffset: 5}
			rqtSetUnknown(attach)
			return &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Attach{Attach: attach}}
		},
		"open (a start)": func() *execv2.OpenRequest {
			open := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: rqtWireStart()}}
			rqtSetUnknown(open)
			return open
		},
	}
	for name, build := range opens {
		t.Run("admitOpenRequest/"+name, func(t *testing.T) {
			got, admitted, refusal := admitOpenRequest(build(), reg, served)
			require.Zero(t, got)
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)

			decoded := new(execv2.OpenRequest)
			rqtThroughTheWire(t, build(), decoded)
			got, admitted, refusal = admitOpenRequest(decoded, reg, served)
			require.Zero(t, got, "after the wire")
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		})
	}

	t.Run("an unknown field after the known ones of a real encoding", func(t *testing.T) {
		raw, err := proto.Marshal(rqtWireStart())
		require.NoError(t, err)
		raw = protowire.AppendTag(raw, 1000, protowire.BytesType)
		raw = protowire.AppendBytes(raw, []byte("a field of a newer peer"))
		decoded := new(execv2.Start)
		require.NoError(t, proto.Unmarshal(raw, decoded))
		_, refusal := admitStart(decoded, target, served)
		rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
	})

	t.Run("the same message without the unknown field is admitted", func(t *testing.T) {
		_, refusal := admitStart(rqtWireStart(), target, served)
		require.Nil(t, refusal)
	})
}

// TestAdmitUnknownEnums covers the wire enum values this build does not know (a newer peer's): a feature, an
// integration mode or an output policy is UNSUPPORTED (an integration mode first needs consent, which is a separate
// refusal), never read as the nearest named value.
func TestAdmitUnknownEnums(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()
	marks, modes := execv2.Feature_FEATURE_MARKS, execv2.Feature_FEATURE_MODES

	t.Run("features", func(t *testing.T) {
		rows := []struct {
			name     string
			features []execv2.Feature
			class    ErrorClass
		}{
			{name: "99", features: []execv2.Feature{99}, class: ErrorClassUnsupported},
			{name: "unspecified", features: []execv2.Feature{execv2.Feature_FEATURE_UNSPECIFIED}, class: ErrorClassUnsupported},
			{name: "negative", features: []execv2.Feature{-1}, class: ErrorClassUnsupported},
			{name: "the largest value", features: []execv2.Feature{1<<31 - 1}, class: ErrorClassUnsupported},
			{name: "after a known one", features: []execv2.Feature{marks, 99}, class: ErrorClassUnsupported},
			{name: "before a known one", features: []execv2.Feature{99, modes}, class: ErrorClassUnsupported},
			{name: "between known ones", features: []execv2.Feature{marks, 99, modes}, class: ErrorClassUnsupported},
			{name: "twice", features: []execv2.Feature{99, 99}, class: ErrorClassUnsupported},
			{name: "a known value that cannot be requested, then 99", features: []execv2.Feature{execv2.Feature_FEATURE_SESSION_MANAGEMENT, 99}, class: ErrorClassInvalidRequest},
			{name: "the next value after the last this build has", features: []execv2.Feature{execv2.Feature_FEATURE_RUN + 1}, class: ErrorClassUnsupported},
		}
		for _, row := range rows {
			t.Run(row.name, func(t *testing.T) {
				wire := rqtWireStart()
				wire.Features = row.features
				got, refusal := admitStart(wire, target, served)
				require.Zero(t, got)
				rqtRequireRefusal(t, refusal, row.class, ErrorReasonUnspecified)

				decoded := new(execv2.Start)
				rqtThroughTheWire(t, wire, decoded)
				_, refusal = admitStart(decoded, target, served)
				rqtRequireRefusal(t, refusal, row.class, ErrorReasonUnspecified)
			})
		}
	})

	t.Run("integration modes", func(t *testing.T) {
		rows := []struct {
			name    string
			mode    execv2.IntegrationMode
			consent bool
			class   ErrorClass
			reason  ErrorReason
		}{
			{name: "99 with consent", mode: 99, consent: true, class: ErrorClassUnsupported},
			{name: "99 without consent: consent comes first", mode: 99, class: ErrorClassInvalidRequest, reason: ErrorReasonConsentRequired},
			{name: "negative with consent", mode: -1, consent: true, class: ErrorClassUnsupported},
			{name: "negative without consent: consent comes first", mode: -1, class: ErrorClassInvalidRequest, reason: ErrorReasonConsentRequired},
			{name: "the next value after the last this build has", mode: execv2.IntegrationMode_INTEGRATION_MODE_ARGV_WRAPPER + 1, consent: true, class: ErrorClassUnsupported},
			{name: "the largest value with consent", mode: 1<<31 - 1, consent: true, class: ErrorClassUnsupported},
		}
		for _, row := range rows {
			t.Run(row.name, func(t *testing.T) {
				wire := rqtWireStart()
				wire.Integration = &execv2.IntegrationRequest{Mode: row.mode, Consent: row.consent}
				got, refusal := admitStart(wire, target, served)
				require.Zero(t, got, "a mode this build does not know is never read as none")
				rqtRequireRefusal(t, refusal, row.class, row.reason)

				decoded := new(execv2.Start)
				rqtThroughTheWire(t, wire, decoded)
				_, refusal = admitStart(decoded, target, served)
				rqtRequireRefusal(t, refusal, row.class, row.reason)
			})
		}
	})

	t.Run("truncate policies", func(t *testing.T) {
		for _, policy := range []execv2.TruncatePolicy{99, -1, execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL + 1, 1<<31 - 1} {
			wire := rqtWireRun()
			wire.Output.Policy = policy
			got, admitted, refusal := admitRunRequest(wire, reg, served)
			require.Zero(t, got, "policy %d", policy)
			require.Nil(t, admitted)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)

			decoded := new(execv2.RunRequest)
			rqtThroughTheWire(t, wire, decoded)
			_, _, refusal = admitRunRequest(decoded, reg, served)
			rqtRequireRefusal(t, refusal, ErrorClassUnsupported, ErrorReasonUnspecified)
		}
	})
}

// TestAdmitCopiesTheWire admits a message, then changes every list, slice, byte slice and pointer of it: the Go value
// must not change, and admission must not have changed the message either.
func TestAdmitCopiesTheWire(t *testing.T) {
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	t.Run("start", func(t *testing.T) {
		wire := rqtWireStart()
		before := proto.Clone(wire)
		got, refusal := admitStart(wire, target, served)
		require.Nil(t, refusal)
		require.True(t, proto.Equal(before, wire), "admission changed the message")
		want, refusal := admitStart(rqtWireStart(), target, served)
		require.Nil(t, refusal)
		require.Equal(t, want, got)

		wire.Argv[0] = "mutated"
		wire.Env[0].Value = "mutated"
		wire.Env[1].Name = "MUTATED"
		wire.Labels[0].Value = "mutated"
		wire.Target.Selection[0].Value = "mutated"
		wire.Target.Reference.Address.Scope[0].Value = "mutated"
		wire.Target.Reference.Address.Provider = "mutated"
		*wire.Target.Reference.Incarnation = "mutated"
		wire.Target.Route.Connection = "mutated"
		wire.Features[0] = execv2.Feature_FEATURE_SESSION_MANAGEMENT
		wire.Integration.Consent = false
		wire.Integration.Mode = execv2.IntegrationMode_INTEGRATION_MODE_UNSPECIFIED
		wire.Size.Cols = 1
		wire.Tty = false
		wire.Cwd = "mutated"
		require.Equal(t, want, got, "the Go value changed with the message")
	})

	t.Run("run", func(t *testing.T) {
		wire := rqtWireRun()
		before := proto.Clone(wire)
		got, _, refusal := admitRunRequest(wire, reg, served)
		require.Nil(t, refusal)
		require.True(t, proto.Equal(before, wire), "admission changed the message")
		want, _, refusal := admitRunRequest(rqtWireRun(), reg, served)
		require.Nil(t, refusal)
		require.Equal(t, want, got)

		wire.Argv[0] = "mutated"
		wire.Stdin[0] ^= 0xff
		wire.Env[0].Value = "mutated"
		wire.Labels[0].Value = "mutated"
		wire.Target.Selection[0].Value = "mutated"
		wire.Target.Reference.Address.Scope[0].Value = "mutated"
		*wire.Target.Reference.Incarnation = "mutated"
		wire.Output.MaxStdoutBytes = 1
		wire.Output.Policy = execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL
		wire.RequestId = "mutated"
		require.Equal(t, want, got, "the Go value changed with the message")
	})

	t.Run("open with a start", func(t *testing.T) {
		wire := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: rqtWireStart()}}
		got, _, refusal := admitOpenRequest(wire, reg, served)
		require.Nil(t, refusal)
		want, _, refusal := admitOpenRequest(&execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: rqtWireStart()}}, reg, served)
		require.Nil(t, refusal)
		require.Equal(t, want, got)

		start := wire.GetStart()
		start.Argv[0] = "mutated"
		start.Env[0].Value = "mutated"
		start.Labels[0].Value = "mutated"
		start.Target.Selection[0].Value = "mutated"
		start.Features[0] = execv2.Feature_FEATURE_SESSION_MANAGEMENT
		require.Equal(t, want, got, "the Go value changed with the message")
	})

	t.Run("a stdin that is not copied is the aliasing this guards against", func(t *testing.T) {
		wire := rqtWireRun()
		got, _, refusal := admitRunRequest(wire, reg, served)
		require.Nil(t, refusal)
		require.Equal(t, []byte("input"), got.Stdin)
		got.Stdin[0] ^= 0xff
		require.Equal(t, []byte("input"), wire.GetStdin(), "changing the Go value must not change the message")
	})
}

// rqtStartTarget returns a registered target whose DefaultArgv is /bin/sh, for the request() tests.
func rqtStartTarget(t *testing.T) *registeredTarget {
	t.Helper()
	_, target := rqtTarget(t, nil)
	require.Equal(t, []string{"/bin/sh"}, target.info.DefaultArgv)
	return target
}

// TestStartRequest covers startRequest.request: the author's Request with the defaults filled in.
func TestStartRequest(t *testing.T) {
	target := rqtStartTarget(t)

	t.Run("the target's default argv fills an empty one, as a copy", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget()}
		got := start.request(target)
		require.Equal(t, []string{"/bin/sh"}, got.Argv)
		got.Argv[0] = "mutated"
		require.Equal(t, []string{"/bin/sh"}, target.info.DefaultArgv, "the target's default argv is not shared with the request")
		require.Equal(t, []string{"/bin/sh"}, start.request(target).Argv)
	})

	t.Run("a given argv is kept, as a copy", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget(), Argv: []string{"/bin/bash", "-l"}}
		got := start.request(target)
		require.Equal(t, []string{"/bin/bash", "-l"}, got.Argv)
		got.Argv[0] = "mutated"
		require.Equal(t, []string{"/bin/bash", "-l"}, start.Argv)
	})

	t.Run("a terminal without a size is 80 x 24", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget(), TTY: true}
		got := start.request(target)
		require.True(t, got.TTY)
		require.Equal(t, Size{Cols: 80, Rows: 24}, got.Size)
		require.True(t, start.Size.IsZero(), "the Start itself is not changed")
	})

	t.Run("the default size fits the smallest dimension limit a target may declare", func(t *testing.T) {
		_, smallest := rqtTarget(t, func(tg *Target) { tg.Info.Session.MaxDimension = 80 })
		got := startRequest{Target: rqtGoTarget(), TTY: true}.request(smallest)
		require.LessOrEqual(t, int(got.Size.Cols), smallest.info.Session.MaxDimension)
		require.LessOrEqual(t, int(got.Size.Rows), smallest.info.Session.MaxDimension)
	})

	t.Run("a terminal keeps the size it asked for", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget(), TTY: true, Size: Size{Cols: 132, Rows: 43}}
		require.Equal(t, Size{Cols: 132, Rows: 43}, start.request(target).Size)
	})

	t.Run("without a terminal there is no size", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget()}
		got := start.request(target)
		require.False(t, got.TTY)
		require.True(t, got.Size.IsZero())

		start.Size = Size{Cols: 100, Rows: 30}
		require.True(t, start.request(target).Size.IsZero(), "a size without a terminal is not carried")
	})

	t.Run("the other fields are copied", func(t *testing.T) {
		start := rqtGoStart()
		got := start.request(target)
		require.Equal(t, start.Target.Reference, got.Ref)
		require.Equal(t, start.Selection, got.Selection)
		require.Equal(t, start.Argv, got.Argv)
		require.Equal(t, start.TTY, got.TTY)
		require.Equal(t, start.Size, got.Size)
		require.Equal(t, start.Env, got.Env)
		require.Equal(t, start.Cwd, got.Cwd)
		require.Equal(t, start.Labels, got.Labels)
	})

	t.Run("maps and the reference are cloned", func(t *testing.T) {
		start := rqtGoStart()
		want := rqtGoStart()
		got := start.request(target)
		require.Equal(t, want.Env, got.Env, "the maps are copied before they can be mutated")
		require.Equal(t, want.Labels, got.Labels)
		require.Equal(t, want.Selection, got.Selection)
		require.Equal(t, want.Argv, got.Argv)
		require.Equal(t, want.Target.Reference, got.Ref)
		got.Env["LANG"] = "mutated"
		got.Env["NEW"] = "mutated"
		got.Labels["purpose"] = "mutated"
		got.Selection["container"] = "mutated"
		got.Argv[0] = "mutated"
		got.Ref.Address.Scope[0].Value = "mutated"
		got.Ref.Address.Provider = "mutated"
		*got.Ref.Incarnation = "mutated"
		require.Equal(t, want, start, "mutating the Request changed the Start")
	})

	t.Run("an empty map is no map", func(t *testing.T) {
		start := startRequest{Target: rqtGoTarget()}
		got := start.request(target)
		require.Nil(t, got.Env)
		require.Nil(t, got.Labels)
		require.Nil(t, got.Selection)
	})

	t.Run("a request without a default and without an argv is empty", func(t *testing.T) {
		_, bare := rqtTarget(t, func(tg *Target) { tg.Info.DefaultArgv = nil })
		start := startRequest{Target: rqtGoTarget()}
		require.Empty(t, start.request(bare).Argv, "admission refuses this Start; request() does not invent an argv")
	})
}

// TestRunRequestRequest covers runRequest.request: the author's Request for a bounded run.
func TestRunRequestRequest(t *testing.T) {
	target := rqtStartTarget(t)

	t.Run("a run has no terminal and no size", func(t *testing.T) {
		got := rqtGoRun().request(target)
		require.False(t, got.TTY)
		require.True(t, got.Size.IsZero())
	})

	t.Run("the fields are copied", func(t *testing.T) {
		run := rqtGoRun()
		got := run.request(target)
		require.Equal(t, run.Target.Reference, got.Ref)
		require.Equal(t, run.Selection, got.Selection)
		require.Equal(t, run.Argv, got.Argv)
		require.Equal(t, run.Env, got.Env)
		require.Equal(t, run.Cwd, got.Cwd)
		require.Equal(t, run.Labels, got.Labels)
	})

	t.Run("maps, argv and the reference are cloned", func(t *testing.T) {
		run := rqtGoRun()
		want := rqtGoRun()
		got := run.request(target)
		require.Equal(t, want.Env, got.Env, "the maps are copied before they can be mutated")
		require.Equal(t, want.Labels, got.Labels)
		require.Equal(t, want.Selection, got.Selection)
		require.Equal(t, want.Argv, got.Argv)
		require.Equal(t, want.Target.Reference, got.Ref)
		got.Env["LANG"] = "mutated"
		got.Labels["purpose"] = "mutated"
		got.Selection["container"] = "mutated"
		got.Argv[0] = "mutated"
		got.Ref.Address.Scope[0].Value = "mutated"
		*got.Ref.Incarnation = "mutated"
		require.Equal(t, want, run, "mutating the Request changed the RunRequest")
	})

	t.Run("an empty map is no map", func(t *testing.T) {
		run := runRequest{Target: rqtGoTarget(), Argv: []string{"true"}}
		got := run.request(target)
		require.Nil(t, got.Env)
		require.Nil(t, got.Labels)
		require.Nil(t, got.Selection)
		require.Equal(t, []string{"true"}, got.Argv)
	})
}

// TestAdmitRefusesHugeRequestsWithoutWork shows that a request whose size alone breaks a bound is refused after
// reading the size, with an amount of work and allocation that does not depend on how big the request is: the
// messages are built once, outside what is measured, and a refusal allocates a small constant number of times. The
// test holds a refusal to a count of allocations, with a margin: testing.AllocsPerRun runs the function alone with
// GOMAXPROCS(1) but reads the process-wide allocation counter, so another goroutine's allocations can move the count
// by a few, which is why the bound is generous (64) and the comparison between sizes has a noise margin, while work
// that depended on the size would cost thousands. How many bytes a refusal allocates is read by
// BenchmarkAdmitRefusesHugeRequests, which asserts nothing.
func TestAdmitRefusesHugeRequestsWithoutWork(t *testing.T) {
	const (
		huge          = 1_000_000
		manyEntries   = 100_000
		bigStdin      = 8 << 20
		maxAllocs     = 64 // a refusal's own errors and the target reference's decoding
		measuredCalls = 10
	)
	reg, target := rqtTarget(t, nil)
	served := rqtServed()

	hugeArgv := rqtRepeat(huge, "a")
	manyEnv := rqtListOf(rqtEnvOfCount(manyEntries))
	manyLabels := rqtListOf(rqtLabelsOfCount(manyEntries))
	manySelection := rqtListOf(rqtSelectionOfCount(manyEntries))
	hugeFeatures := slices.Repeat([]execv2.Feature{execv2.Feature_FEATURE_MARKS}, huge)
	hugeStdin := make([]byte, bigStdin)

	startWith := func(change func(*execv2.Start)) *execv2.Start {
		s := rqtWireStart()
		change(s)
		return s
	}
	runWith := func(change func(*execv2.RunRequest)) *execv2.RunRequest {
		r := rqtWireRun()
		change(r)
		return r
	}

	type hugeCase struct {
		name   string
		admit  func() *Error
		class  ErrorClass
		reason ErrorReason
	}
	var cases []hugeCase
	addStart := func(name string, s *execv2.Start) {
		cases = append(cases, hugeCase{
			name:  name,
			admit: func() *Error { _, refusal := admitStart(s, target, served); return refusal },
			class: ErrorClassInvalidRequest,
		})
		open := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: s}}
		cases = append(cases, hugeCase{
			name:  "open/" + name,
			admit: func() *Error { _, _, refusal := admitOpenRequest(open, reg, served); return refusal },
			class: ErrorClassInvalidRequest,
		})
	}
	addRun := func(name string, r *execv2.RunRequest, class ErrorClass, reason ErrorReason) {
		cases = append(cases, hugeCase{
			name:   name,
			admit:  func() *Error { _, _, refusal := admitRunRequest(r, reg, served); return refusal },
			class:  class,
			reason: reason,
		})
	}
	addStart("start with 1,000,000 argv elements", startWith(func(s *execv2.Start) { s.Argv = hugeArgv }))
	addStart("start with 100,000 env entries", startWith(func(s *execv2.Start) { s.Env = manyEnv }))
	addStart("start with 100,000 labels", startWith(func(s *execv2.Start) { s.Labels = manyLabels }))
	addStart("start with 100,000 selections", startWith(func(s *execv2.Start) { s.Target.Selection = manySelection }))
	addStart("start with 1,000,000 features", startWith(func(s *execv2.Start) { s.Features = hugeFeatures }))
	addRun("run with 1,000,000 argv elements", runWith(func(r *execv2.RunRequest) { r.Argv = hugeArgv }), ErrorClassInvalidRequest, ErrorReasonUnspecified)
	addRun("run with 100,000 env entries", runWith(func(r *execv2.RunRequest) { r.Env = manyEnv }), ErrorClassInvalidRequest, ErrorReasonUnspecified)
	addRun("run with 100,000 labels", runWith(func(r *execv2.RunRequest) { r.Labels = manyLabels }), ErrorClassInvalidRequest, ErrorReasonUnspecified)
	addRun("run with 100,000 selections", runWith(func(r *execv2.RunRequest) { r.Target.Selection = manySelection }), ErrorClassInvalidRequest, ErrorReasonUnspecified)
	addRun("run with 8 MiB of stdin", runWith(func(r *execv2.RunRequest) { r.Stdin = hugeStdin }), ErrorClassLimit, ErrorReasonInputTooLarge)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var refusal *Error
			allocs := testing.AllocsPerRun(measuredCalls, func() { refusal = c.admit() })
			rqtRequireRefusal(t, refusal, c.class, c.reason)
			require.LessOrEqual(t, allocs, float64(maxAllocs), "allocations per refusal")
		})
	}

	t.Run("a refusal costs the same whatever the size", func(t *testing.T) {
		smaller := rqtWireStart()
		smaller.Argv = rqtRepeat(1025, "a") // the first size beyond the bound
		bigger := rqtWireStart()
		bigger.Argv = hugeArgv
		var refusal *Error
		small := testing.AllocsPerRun(measuredCalls, func() { _, refusal = admitStart(smaller, target, served) })
		require.NotNil(t, refusal)
		large := testing.AllocsPerRun(measuredCalls, func() { _, refusal = admitStart(bigger, target, served) })
		require.NotNil(t, refusal)
		// Under the race detector sync.Pool drops items at random, so the same call allocates a few times more or less
		// from one run to the next. Work that depends on the size would put the two counts thousands apart.
		const noise = 8
		require.InDelta(t, small, large, noise, "a refusal of 1,000,000 elements allocated more than a refusal of 1,025")
	})

	// A list is read only up to the longest a request may carry (1,024 elements); a longer one is refused without being
	// read, so the cost of its refusal is the same at 1,025 elements and at 100,000.
	t.Run("a refusal for a list beyond the longest a request may carry costs the same whatever its length", func(t *testing.T) {
		smaller := rqtWireStart()
		smaller.Env = rqtListOf(rqtEnvOfCount(1025))
		bigger := rqtWireStart()
		bigger.Env = manyEnv
		var refusal *Error
		small := testing.AllocsPerRun(measuredCalls, func() { _, refusal = admitStart(smaller, target, served) })
		require.NotNil(t, refusal)
		large := testing.AllocsPerRun(measuredCalls, func() { _, refusal = admitStart(bigger, target, served) })
		require.NotNil(t, refusal)
		const noise = 8
		require.InDelta(t, small, large, noise, "a refusal of 100,000 entries allocated more than a refusal of 1,025")
	})
}

// BenchmarkAdmitRefusesHugeRequests reports the bytes and the allocations of the refusal of a request whose size alone
// breaks a bound, for a person who reads them: it asserts nothing, since the bytes of a process are shared with every
// other goroutine of it.
func BenchmarkAdmitRefusesHugeRequests(b *testing.B) {
	reg, target := rqtTarget(b, nil)
	served := rqtServed()
	hugeArgv := rqtWireStart()
	hugeArgv.Argv = rqtRepeat(1_000_000, "a")
	manyEnv := rqtWireStart()
	manyEnv.Env = rqtListOf(rqtEnvOfCount(100_000))
	for name, start := range map[string]*execv2.Start{"1,000,000 argv elements": hugeArgv, "100,000 env entries": manyEnv} {
		b.Run("start with "+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, refusal := admitStart(start, target, served); refusal == nil {
					b.Fatal("a request beyond a bound was admitted")
				}
			}
		})
		open := &execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{Start: start}}
		b.Run("open with "+name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if _, _, refusal := admitOpenRequest(open, reg, served); refusal == nil {
					b.Fatal("a request beyond a bound was admitted")
				}
			}
		})
	}
}

// rqtEnvNameRule is the contract's rule for an environment name, written as the regular expression of the proto.
var rqtEnvNameRule = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// rqtClosedCodeRule is the contract's rule for a closed code, the name of a selection.
var rqtClosedCodeRule = regexp.MustCompile(`^[a-z0-9_.-]{1,40}$`)

// rqtPlainText reports whether s is UTF-8 without control characters: the C0 controls (NUL to US), DEL and the C1
// controls (U+0080 to U+009F).
func rqtPlainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// rqtHasNUL reports whether s holds a NUL byte.
func rqtHasNUL(s string) bool {
	for i := range len(s) {
		if s[i] == 0 {
			return true
		}
	}
	return false
}

// rqtCommonViolation returns the first bound of the contract that the fields a Start and a RunRequest share break,
// or "". It is the test's own reading of the contract, written without the code under test.
func rqtCommonViolation(argv []string, argvMax int, env map[string]string, envMax int, cwd string, labels, selection map[string]string) string {
	if len(argv) > 1024 {
		return "argv has more than 1024 elements"
	}
	argvTotal := 0
	for i, a := range argv {
		switch {
		case len(a) > 32<<10:
			return "an argv element is longer than 32 KiB"
		case rqtHasNUL(a):
			return "an argv element holds a NUL"
		case !utf8.ValidString(a):
			return "an argv element is not UTF-8"
		case i == 0 && a == "":
			return "argv[0] is empty"
		}
		argvTotal += len(a)
	}
	if argvTotal > argvMax {
		return "argv is larger than its total"
	}
	if len(env) > 256 {
		return "env has more than 256 entries"
	}
	envTotal := 0
	for name, value := range env {
		switch {
		case len(name) > 256 || !rqtEnvNameRule.MatchString(name):
			return "an env name breaks its rule"
		case len(value) > 32<<10:
			return "an env value is longer than 32 KiB"
		case rqtHasNUL(value):
			return "an env value holds a NUL"
		case !utf8.ValidString(value):
			return "an env value is not UTF-8"
		}
		envTotal += len(name) + len(value)
	}
	if envTotal > envMax {
		return "env is larger than its total"
	}
	if len(cwd) > 4096 || rqtHasNUL(cwd) || !utf8.ValidString(cwd) {
		return "cwd is too long, holds a NUL or is not UTF-8"
	}
	if len(labels) > 16 {
		return "more than 16 labels"
	}
	for k, v := range labels {
		if len(k) > 64 || len(v) > 256 || !rqtPlainText(k) || !rqtPlainText(v) {
			return "a label breaks its rule"
		}
	}
	if len(selection) > 8 {
		return "more than 8 selections"
	}
	for k, v := range selection {
		if !rqtClosedCodeRule.MatchString(k) || len(v) > 256 || !rqtPlainText(v) {
			return "a selection breaks its rule"
		}
	}
	return ""
}

// rqtStartViolation returns the first bound of the contract that an admitted Start breaks against its target, or "".
func rqtStartViolation(s startRequest, target *registeredTarget) string {
	if v := rqtCommonViolation(s.Argv, 64<<10, s.Env, 64<<10, s.Cwd, s.Labels, s.Selection); v != "" {
		return v
	}
	info := target.info
	switch {
	case info.Session.IsZero():
		return "the target serves no sessions"
	case len(s.Argv) == 0 && len(info.DefaultArgv) == 0:
		return "no argv and no default"
	case s.TTY && !info.Capabilities.TTY:
		return "a terminal on a target without terminals"
	case !s.Size.IsZero() && (s.Size.Cols == 0 || s.Size.Rows == 0):
		return "a size with one dimension"
	case !s.Size.IsZero() && !s.TTY:
		return "a size without a terminal"
	case int(s.Size.Cols) > info.Session.MaxDimension || int(s.Size.Rows) > info.Session.MaxDimension:
		return "a size above the target's dimension limit"
	case len(s.Env) > 0 && !info.Capabilities.Env, s.Cwd != "" && !info.Capabilities.Cwd:
		return "env or cwd on a target that does not honor them"
	case s.DetachTimeout < 0 || s.DetachTimeout > info.Session.MaxDetachTimeout:
		return "a detach timeout beyond the target's"
	case s.ReplayBytes < 0 || s.ReplayBytes > info.Session.MaxReplayBytes:
		return "a replay ring beyond the target's"
	}
	seen := map[execv2.Feature]bool{}
	for _, f := range s.Features {
		if (f != rqtFeatureMarks && f != rqtFeatureModes) || seen[f] {
			return "a feature that cannot be requested, or twice"
		}
		seen[f] = true
	}
	if s.Integration != nil {
		if s.Integration.Mode != IntegrationArgvWrapper || !s.Integration.Consent {
			return "an integration that is not a known mode with consent"
		}
		if !slices.Contains(info.Capabilities.Integration, s.Integration.Mode) {
			return "an integration mode the target does not list"
		}
	}
	return ""
}

// rqtRunViolation returns the first bound of the contract that an admitted RunRequest breaks against its target, or "".
func rqtRunViolation(r runRequest, target *registeredTarget) string {
	info := target.info
	if info.Run.IsZero() {
		return "the target serves no runs"
	}
	if v := rqtCommonViolation(r.Argv, info.Run.MaxArgvBytes, r.Env, info.Run.MaxEnvBytes, r.Cwd, r.Labels, r.Selection); v != "" {
		return v
	}
	if len(r.RequestID) > 128 {
		return "a request id longer than 128 bytes"
	}
	for i := range len(r.RequestID) {
		if c := r.RequestID[i]; c < 0x21 || c > 0x7e {
			return "a request id that is not printable ASCII"
		}
	}
	if _, named := execv2.TruncatePolicy_name[int32(r.Output.Policy)]; !named {
		return "an output policy that is not a value of the enum"
	}
	switch {
	case len(r.Argv) == 0:
		return "an empty argv"
	case len(r.Stdin) > info.Run.MaxStdinBytes:
		return "stdin beyond the target's"
	case len(r.Env) > 0 && !info.Capabilities.Env, r.Cwd != "" && !info.Capabilities.Cwd:
		return "env or cwd on a target that does not honor them"
	case r.Timeout < 0 || r.Timeout > info.Run.MaxTimeout:
		return "a timeout beyond the target's"
	case r.Output.MaxStdoutBytes < 0 || r.Output.MaxStdoutBytes > info.Run.MaxOutputBytes:
		return "a stdout cap beyond the target's"
	case r.Output.MaxStderrBytes < 0 || r.Output.MaxStderrBytes > info.Run.MaxOutputBytes:
		return "a stderr cap beyond the target's"
	}
	return ""
}

// rqtCheckRefusal requires a refusal produced by admission to be one the host may see.
func rqtCheckRefusal(t *testing.T, refusal *Error) {
	t.Helper()
	if refusal.Err == nil {
		t.Fatalf("a refusal without a local cause: %v", refusal)
	}
	if !validPair(refusal.Class, refusal.Reason) {
		t.Fatalf("a refusal that may not cross the wire: %s/%s", refusal.Class, refusal.Reason)
	}
	switch refusal.Class {
	case ErrorClassInvalidRequest, ErrorClassUnsupported, ErrorClassLimit:
	default:
		t.Fatalf("admission refused with class %s: %v", refusal.Class, refusal.Err)
	}
}

// rqtHasUnknownField reports whether m, or a message nested in it, holds a field its descriptor does not know. It is
// the test's own reading of that and shares no code with hasUnknownFields: m is encoded and decoded again with the
// unknown fields discarded, and a message that came back smaller had some. ok is false for a message that cannot be
// encoded (a string that is not UTF-8), which has nothing to compare.
func rqtHasUnknownField(m proto.Message) (has, ok bool) {
	raw, err := proto.Marshal(m)
	if err != nil {
		return false, false
	}
	stripped := m.ProtoReflect().New().Interface()
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, stripped); err != nil {
		return false, false
	}
	return proto.Size(stripped) != len(raw), true
}

// rqtDuplicateName reports whether a list of NameValue holds a name twice.
func rqtDuplicateName(list []*execv2.NameValue) bool {
	seen := make(map[string]struct{}, len(list))
	for _, entry := range list {
		if _, dup := seen[entry.GetName()]; dup {
			return true
		}
		seen[entry.GetName()] = struct{}{}
	}
	return false
}

// rqtRequireStrictAdmission requires of a message that admission admitted what a strict request must hold: no field
// this build does not know anywhere in it, and no name twice in any of its lists.
func rqtRequireStrictAdmission(t *testing.T, m proto.Message, lists ...[]*execv2.NameValue) {
	t.Helper()
	if has, ok := rqtHasUnknownField(m); ok && has {
		t.Fatalf("admitted a message that carries a field this build does not know: %v", m)
	}
	for _, list := range lists {
		if rqtDuplicateName(list) {
			t.Fatalf("admitted a message with a name twice in a list: %v", m)
		}
	}
}

// rqtFuzzRegistry is the registry the fuzzers admit against, built once.
var rqtFuzzRegistry = sync.OnceValues(func() (*targetRegistry, error) {
	return newTargetRegistry([]Target{rqtBaseTarget()})
})

// rqtFuzzFixture returns the registry, its only target and the features of a build that serves everything.
func rqtFuzzFixture(t *testing.T) (*targetRegistry, *registeredTarget, featureSet) {
	t.Helper()
	reg, err := rqtFuzzRegistry()
	if err != nil {
		t.Fatal(err)
	}
	target, ok := reg.lookup("k8s", "pods")
	if !ok {
		t.Fatal("the fuzz target is not registered")
	}
	return reg, target, rqtServed()
}

// rqtMarshalSeed marshals m, or returns nil when the message cannot be marshaled (a string that is not UTF-8).
func rqtMarshalSeed(m proto.Message) []byte {
	raw, err := proto.Marshal(m)
	if err != nil {
		return nil
	}
	return raw
}

// rqtStartListSeeds returns Starts whose lists break what only a list can break: an entry with a field this build
// does not know, in each of the lists, and a name twice.
func rqtStartListSeeds() []*execv2.Start {
	unknownEntry := func(name, value string) *execv2.NameValue {
		entry := &execv2.NameValue{Name: name, Value: value}
		rqtSetUnknown(entry)
		return entry
	}
	var seeds []*execv2.Start
	add := func(change func(*execv2.Start)) {
		s := rqtWireStart()
		change(s)
		seeds = append(seeds, s)
	}
	add(func(s *execv2.Start) { s.Env[1] = unknownEntry("LANG", "C") })
	add(func(s *execv2.Start) { s.Env[0] = unknownEntry("EDITOR_2", "vi") })
	add(func(s *execv2.Start) { s.Labels[0] = unknownEntry("purpose", "debug") })
	add(func(s *execv2.Start) { s.Target.Selection[0] = unknownEntry("container", "app") })
	add(func(s *execv2.Start) { rqtSetUnknown(s.Target.Reference.Address.Scope[0]) })
	add(func(s *execv2.Start) {
		s.Env = rqtListOf(map[string]string{"A": "1"})
		s.Env = append(s.Env, &execv2.NameValue{Name: "A", Value: "2"})
	})
	add(func(s *execv2.Start) { s.Labels = append(s.Labels, &execv2.NameValue{Name: "purpose", Value: "other"}) })
	add(func(s *execv2.Start) {
		s.Target.Selection = append(s.Target.Selection, &execv2.NameValue{Name: "container", Value: "other"})
	})
	add(func(s *execv2.Start) { s.Env = append(s.Env, nil) })
	add(func(s *execv2.Start) { s.Env = []*execv2.NameValue{nil, nil} })
	return seeds
}

// rqtStartSeeds returns the corpus of FuzzAdmitStart: the valid message, one message per row of the tables and
// some hostile ones.
func rqtStartSeeds() [][]byte {
	seeds := [][]byte{nil, {0xff, 0xff, 0xff}, rqtMarshalSeed(rqtWireStart()), rqtMarshalSeed(&execv2.Start{})}
	for _, row := range rqtStartBounds() {
		s := rqtGoStart()
		if row.change != nil {
			row.change(&s)
		}
		seeds = append(seeds, rqtMarshalSeed(rqtWireOfStart(s)))
	}
	for _, row := range rqtStartRows() {
		wire := rqtWireStart()
		if row.wire != nil {
			row.wire(wire)
		}
		seeds = append(seeds, rqtMarshalSeed(wire))
	}
	for _, s := range rqtStartListSeeds() {
		seeds = append(seeds, rqtMarshalSeed(s))
	}
	hostile := rqtWireStart()
	hostile.Argv = rqtRepeat(2000, "x")
	hostile.Size = &execv2.Size{Cols: 1<<32 - 1, Rows: 1<<32 - 1}
	hostile.Features = []execv2.Feature{99, -1, 7, 7}
	hostile.Integration = &execv2.IntegrationRequest{Mode: -5, Consent: true}
	return append(seeds, rqtMarshalSeed(hostile))
}

// rqtRunSeeds returns the corpus of FuzzAdmitRunRequest.
func rqtRunSeeds() [][]byte {
	seeds := [][]byte{nil, {0xff, 0xff, 0xff}, rqtMarshalSeed(rqtWireRun()), rqtMarshalSeed(&execv2.RunRequest{})}
	for _, row := range rqtRunBounds() {
		r := rqtGoRun()
		if row.change != nil {
			row.change(&r)
		}
		seeds = append(seeds, rqtMarshalSeed(rqtWireOfRun(r)))
	}
	for _, row := range rqtRunRows() {
		wire := rqtWireRun()
		if row.wire != nil {
			row.wire(wire)
		}
		seeds = append(seeds, rqtMarshalSeed(wire))
	}
	unknownEntry := func(name, value string) *execv2.NameValue {
		entry := &execv2.NameValue{Name: name, Value: value}
		rqtSetUnknown(entry)
		return entry
	}
	for _, change := range []func(*execv2.RunRequest){
		func(r *execv2.RunRequest) { r.Env[0] = unknownEntry("LANG", "C") },
		func(r *execv2.RunRequest) { r.Labels[0] = unknownEntry("purpose", "diagnose") },
		func(r *execv2.RunRequest) { r.Target.Selection[0] = unknownEntry("container", "app") },
		func(r *execv2.RunRequest) { rqtSetUnknown(r.Target.Reference.Address.Scope[0]) },
		func(r *execv2.RunRequest) { r.Env = append(r.Env, &execv2.NameValue{Name: "LANG", Value: "other"}) },
		func(r *execv2.RunRequest) {
			r.Labels = append(r.Labels, &execv2.NameValue{Name: "purpose", Value: "other"})
		},
		func(r *execv2.RunRequest) {
			r.Target.Selection = append(r.Target.Selection, &execv2.NameValue{Name: "container", Value: "other"})
		},
	} {
		wire := rqtWireRun()
		change(wire)
		seeds = append(seeds, rqtMarshalSeed(wire))
	}
	hostile := rqtWireRun()
	hostile.Argv = rqtRepeat(2000, "x")
	hostile.TimeoutMs = 1<<32 - 1
	hostile.Output = &execv2.OutputLimits{MaxStdoutBytes: 1<<32 - 1, MaxStderrBytes: 1<<32 - 1, Policy: 99}
	return append(seeds, rqtMarshalSeed(hostile))
}

// rqtOpenSeeds returns the corpus of FuzzAdmitOpenRequest.
func rqtOpenSeeds() [][]byte {
	attach := func(id string, offset uint64) *execv2.OpenRequest {
		return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Attach{Attach: &execv2.Attach{FromOffset: offset}}}
	}
	start := func(id string, s *execv2.Start) *execv2.OpenRequest {
		return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: s}}
	}
	seeds := [][]byte{
		nil,
		{0xff, 0xff, 0xff},
		rqtMarshalSeed(&execv2.OpenRequest{}),
		rqtMarshalSeed(attach("s-1", 0)),
		rqtMarshalSeed(attach("s-1", 1<<64-1)),
		rqtMarshalSeed(attach("", 0)),
		rqtMarshalSeed(attach(rqtBytes(129), 0)),
		rqtMarshalSeed(attach("a b", 0)),
		rqtMarshalSeed(start("s-1", rqtWireStart())),
		rqtMarshalSeed(start(rqtBytes(128), rqtWireStart())),
		rqtMarshalSeed(start("s-1", &execv2.Start{})),
		rqtMarshalSeed(&execv2.OpenRequest{SessionId: "s-1", Kind: &execv2.OpenRequest_Start{}}),
	}
	for _, row := range rqtStartRows() {
		wire := rqtWireStart()
		if row.wire != nil {
			row.wire(wire)
		}
		seeds = append(seeds, rqtMarshalSeed(start("s-1", wire)))
	}
	for _, s := range rqtStartListSeeds() {
		seeds = append(seeds, rqtMarshalSeed(start("s-1", s)))
	}
	return seeds
}

// FuzzAdmitStart feeds arbitrary bytes, decoded as a Start, to the admission. The message is decoded whatever the
// decoding error says: admission must hold for a partial message too. It must not panic, a refusal must be one the
// host may see, and what it admits must be within every bound, carry no field this build does not know and no name
// twice, and must survive being encoded and admitted again.
func FuzzAdmitStart(f *testing.F) {
	for _, seed := range rqtStartSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, target, served := rqtFuzzFixture(t)
		p := new(execv2.Start)
		_ = proto.Unmarshal(data, p)
		got, refusal := admitStart(p, target, served)
		if refusal != nil {
			rqtCheckRefusal(t, refusal)
			return
		}
		if v := rqtStartViolation(got, target); v != "" {
			t.Fatalf("admitted a Start that breaks a bound: %s\n%+v", v, got)
		}
		rqtRequireStrictAdmission(t, p, p.GetEnv(), p.GetLabels(), p.GetTarget().GetSelection())
		again, refusal := admitStart(rqtWireOfStart(got), target, served)
		if refusal != nil {
			t.Fatalf("admitted a Start that, encoded again, is refused: %v", refusal.Err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("a Start changed through its own encoding:\n%+v\n%+v", got, again)
		}
	})
}

// FuzzAdmitRunRequest feeds arbitrary bytes, decoded as a RunRequest, to the admission, with the same demands as
// FuzzAdmitStart.
func FuzzAdmitRunRequest(f *testing.F) {
	for _, seed := range rqtRunSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		reg, target, served := rqtFuzzFixture(t)
		p := new(execv2.RunRequest)
		_ = proto.Unmarshal(data, p)
		got, admitted, refusal := admitRunRequest(p, reg, served)
		if refusal != nil {
			rqtCheckRefusal(t, refusal)
			if admitted != nil {
				t.Fatal("a refused request names a target")
			}
			return
		}
		if admitted != target {
			t.Fatal("an admitted request does not name the registered target")
		}
		if v := rqtRunViolation(got, target); v != "" {
			t.Fatalf("admitted a RunRequest that breaks a bound: %s\n%+v", v, got)
		}
		rqtRequireStrictAdmission(t, p, p.GetEnv(), p.GetLabels(), p.GetTarget().GetSelection())
		again, _, refusal := admitRunRequest(rqtWireOfRun(got), reg, served)
		if refusal != nil {
			t.Fatalf("admitted a RunRequest that, encoded again, is refused: %v", refusal.Err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("a RunRequest changed through its own encoding:\n%+v\n%+v", got, again)
		}
	})
}

// FuzzAdmitOpenRequest feeds arbitrary bytes, decoded as the first frame of an Open, to the admission, with the same
// demands as FuzzAdmitStart.
func FuzzAdmitOpenRequest(f *testing.F) {
	for _, seed := range rqtOpenSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		reg, target, served := rqtFuzzFixture(t)
		p := new(execv2.OpenRequest)
		_ = proto.Unmarshal(data, p)
		got, admitted, refusal := admitOpenRequest(p, reg, served)
		if refusal != nil {
			rqtCheckRefusal(t, refusal)
			if admitted != nil {
				t.Fatal("a refused request names a target")
			}
			return
		}
		if n := len(got.SessionID); n < 1 || n > 128 {
			t.Fatalf("admitted a session id of %d bytes", n)
		}
		for i := range len(got.SessionID) {
			if c := got.SessionID[i]; c < 0x21 || c > 0x7e {
				t.Fatalf("admitted a session id with the byte %#x", c)
			}
		}
		if (got.Start == nil) == (got.Attach == nil) {
			t.Fatalf("admitted a request that is not exactly one of a start and an attach: %+v", got)
		}
		if got.Attach != nil {
			if admitted != nil {
				t.Fatal("an attach names a target")
			}
			rqtRequireStrictAdmission(t, p)
			return
		}
		if admitted != target {
			t.Fatal("an admitted start does not name the registered target")
		}
		if v := rqtStartViolation(*got.Start, target); v != "" {
			t.Fatalf("admitted a Start that breaks a bound: %s\n%+v", v, got.Start)
		}
		start := p.GetStart()
		rqtRequireStrictAdmission(t, p, start.GetEnv(), start.GetLabels(), start.GetTarget().GetSelection())
		again, _, refusal := admitOpenRequest(&execv2.OpenRequest{
			SessionId: got.SessionID,
			Kind:      &execv2.OpenRequest_Start{Start: rqtWireOfStart(*got.Start)},
		}, reg, served)
		if refusal != nil {
			t.Fatalf("admitted an OpenRequest that, encoded again, is refused: %v", refusal.Err)
		}
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("an OpenRequest changed through its own encoding:\n%+v\n%+v", got, again)
		}
	})
}
