package exec

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
)

func noopExec(context.Context, *Proc) (Exit, error) { return ExitCode(0), nil }

// commandCapabilities are the capabilities a target that runs commands must declare.
func commandCapabilities() Capabilities {
	return Capabilities{Kill: KillTerminates, ExitReport: ExitReportCode, Stall: StallPropagates}
}

func validTarget() Target {
	return Target{
		Exec: noopExec,
		Info: TargetInfo{
			Provider:     "k8s",
			Type:         "pods",
			DisplayName:  "Pod",
			DefaultArgv:  []string{"/bin/sh"},
			Capabilities: commandCapabilities(),
			Session:      DefaultSessionLimits(),
			Run:          DefaultRunLimits(),
		},
	}
}

func TestRegistryAcceptsTargetsAndHoldsEffectiveInfo(t *testing.T) {
	optionsOnly := Target{Exec: noopExec, Info: TargetInfo{Provider: "k8s", Type: "nodes"}}
	partial := validTarget()
	partial.Info.Type = "containers"
	partial.Info.Session = SessionLimits{MaxSessions: 4}
	partial.Info.Run = RunLimits{DefaultTimeout: 30 * time.Second, MaxTimeout: 5 * time.Minute}

	r, err := newTargetRegistry([]Target{validTarget(), optionsOnly, partial})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.targets) != 3 {
		t.Fatalf("%d targets", len(r.targets))
	}
	pods, ok := r.lookup("k8s", "pods")
	if !ok || pods.info.Provider != "k8s" || pods.info.Type != "pods" {
		t.Fatalf("lookup pods = %+v, %v", pods, ok)
	}
	if !pods.runsCommands() {
		t.Error("a target with sessions and runs runs commands")
	}
	nodes, _ := r.lookup("k8s", "nodes")
	if nodes.runsCommands() || !nodes.info.Session.IsZero() || !nodes.info.Run.IsZero() {
		t.Errorf("an options-only target runs no commands and gets no limits: %+v", nodes.info)
	}
	containers, _ := r.lookup("k8s", "containers")
	if err := containers.info.Session.validate(false); err != nil || containers.info.Session.MaxSessions != 4 {
		t.Errorf("partial session limits were not made effective: %+v, %v", containers.info.Session, err)
	}
	if err := containers.info.Run.validate(false); err != nil {
		t.Errorf("partial run limits were not made effective: %+v, %v", containers.info.Run, err)
	}
	if _, ok := r.lookup("k8s", "missing"); ok {
		t.Error("lookup found a target that is not registered")
	}
	if got := r.description().GetTargets(); len(got) != 3 || got[0].GetType() != "pods" || got[1].GetType() != "nodes" || got[2].GetType() != "containers" {
		t.Errorf("description does not list the targets in registration order: %v", got)
	}
}

func TestRegistryKeepsTheRoutingKeyAsAPairNotAString(t *testing.T) {
	a, b := validTarget(), validTarget()
	a.Info.Provider, a.Info.Type = "a/b", "c"
	b.Info.Provider, b.Info.Type = "a", "b/c"
	if a.key() != b.key() {
		t.Fatalf("the test needs two keys with the same string form: %q %q", a.key(), b.key())
	}
	r, err := newTargetRegistry([]Target{a, b})
	if err != nil {
		t.Fatalf("two different routing keys with one string form are two targets: %v", err)
	}
	if _, ok := r.lookup("a/b", "c"); !ok {
		t.Error("lookup (a/b, c) failed")
	}
	if _, ok := r.lookup("a", "b/c"); !ok {
		t.Error("lookup (a, b/c) failed")
	}
}

func TestRegistryRefusesWhatBreaksTheContract(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *Target)
		want   string
	}{
		{"no provider", func(t *Target) { t.Info.Provider = "" }, "Info.Provider must be"},
		{"no type", func(t *Target) { t.Info.Type = "" }, "Info.Type must be"},
		{"a control character in the key", func(t *Target) { t.Info.Type = "po\x00ds" }, "Info.Type must be"},
		{"a key that is too long", func(t *Target) { t.Info.Provider = strings.Repeat("p", 257) }, "Info.Provider must be"},
		{"no Exec", func(t *Target) { t.Exec = nil }, "Exec function is required"},
		{"a display name that is too long", func(t *Target) { t.Info.DisplayName = strings.Repeat("d", 129) }, "DisplayName"},
		{"a display name with control characters", func(t *Target) { t.Info.DisplayName = "Pod\n" }, "DisplayName"},
		{"too many default argv elements", func(t *Target) { t.Info.DefaultArgv = make([]string, 65) }, "DefaultArgv has 65"},
		{"an empty default program", func(t *Target) { t.Info.DefaultArgv = []string{"", "x"} }, "DefaultArgv[0]"},
		{"a NUL in the default argv", func(t *Target) { t.Info.DefaultArgv = []string{"sh", "a\x00b"} }, "NUL"},
		{"a default argv element that is too long", func(t *Target) { t.Info.DefaultArgv = []string{"sh", strings.Repeat("a", 32<<10+1)} }, "too long"},
		{"a default argv element that is not UTF-8", func(t *Target) { t.Info.DefaultArgv = []string{"sh", "\xff"} }, "UTF-8"},
		{"a first default argv element that is not UTF-8", func(t *Target) { t.Info.DefaultArgv = []string{"\xc3\x28"} }, "UTF-8"},
		{"a terminal limit below the default terminal", func(t *Target) { t.Info.Session.MaxDimension = 79 }, "default terminal"},
		{"a duration limit with a sub-millisecond part", func(t *Target) { t.Info.Session.MaxDetachTimeout = 24*time.Hour + time.Nanosecond }, "MaxDetachTimeout"},
		{"a capability value that is not one", func(t *Target) { t.Info.Capabilities.Kill = KillBehavior(42) }, "Capabilities.Kill"},
		{"a fact mode that is not one", func(t *Target) { t.Info.Capabilities.CwdFact = FactMode(42) }, "Capabilities.CwdFact"},
		{"a survival that is not one", func(t *Target) { t.Info.Capabilities.HostRestart = Survival(42) }, "Capabilities.HostRestart"},
		{"an unspecified signal", func(t *Target) { t.Info.Capabilities.Signals = []Signal{SignalUnspecified} }, "not a deliverable signal"},
		{"a signal listed twice", func(t *Target) { t.Info.Capabilities.Signals = []Signal{SignalInt, SignalInt} }, "twice"},
		{"too many signals", func(t *Target) { t.Info.Capabilities.Signals = make([]Signal, 17) }, "at most 16"},
		{"integration without a terminal", func(t *Target) { t.Info.Capabilities.Integration = []IntegrationMode{IntegrationArgvWrapper} }, "needs Capabilities.TTY"},
		{"the off mode listed as accepted", func(t *Target) {
			t.Info.Capabilities.TTY = true
			t.Info.Capabilities.Integration = []IntegrationMode{IntegrationOff}
		}, "not an integration mode a target accepts"},
		{"an integration mode listed twice", func(t *Target) {
			t.Info.Capabilities.TTY = true
			t.Info.Capabilities.Integration = []IntegrationMode{IntegrationArgvWrapper, IntegrationArgvWrapper}
		}, "twice"},
		{"session limits that break their bounds", func(t *Target) { t.Info.Session.MaxDimension = 100000 }, "MaxDimension"},
		{"a ring above its maximum", func(t *Target) { t.Info.Session.ReplayBytes, t.Info.Session.MaxReplayBytes = 2<<20, 1<<20 }, "exceeds MaxReplayBytes"},
		{"run limits that break their bounds", func(t *Target) { t.Info.Run.MaxStdinBytes = 2 << 20 }, "MaxStdinBytes"},
		{"negative limits", func(t *Target) { t.Info.Session.MaxSessions = -1 }, "MaxSessions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := validTarget()
			tc.change(&target)
			_, err := newTargetRegistry([]Target{target})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestRegistryAcceptsDefaultArgvOfAnyValidText(t *testing.T) {
	target := validTarget()
	target.Info.DefaultArgv = []string{"/bin/sh", "-c", "echo hello world", "caf\u00e9 \u65e5\u672c\u8a9e", "tab\there"}
	target.Info.Session.MaxDimension = int(defaultStartCols)
	if _, err := newTargetRegistry([]Target{target}); err != nil {
		t.Errorf("a valid default argv was refused: %v", err)
	}
}

func TestRegistryRefusesDuplicatesAndTooManyTargets(t *testing.T) {
	if _, err := newTargetRegistry([]Target{validTarget(), validTarget()}); err == nil || !strings.Contains(err.Error(), "duplicate exec target key") {
		t.Errorf("duplicates: %v", err)
	}
	many := make([]Target, maxTargets+1)
	if _, err := newTargetRegistry(many); err == nil || !strings.Contains(err.Error(), "exceed the limit") {
		t.Errorf("too many: %v", err)
	}
	if r, err := newTargetRegistry(nil); err != nil || len(r.targets) != 0 {
		t.Errorf("no targets: %+v, %v", r, err)
	}
}

// A target that can run commands must declare the three behaviors that decide how its end is read, what a stalled
// reader does to it and what closing it does to the process: an author declares them instead of inheriting a default.
func TestATargetThatRunsCommandsMustDeclareHowItEndsStallsAndCloses(t *testing.T) {
	missing := []struct {
		name   string
		change func(c *Capabilities)
		want   string
	}{
		{"exit report", func(c *Capabilities) { c.ExitReport = ExitReportUnspecified }, "ExitReport"},
		{"stall", func(c *Capabilities) { c.Stall = StallUnspecified }, "Stall"},
		{"kill", func(c *Capabilities) { c.Kill = KillUnspecified }, "Kill"},
		{"all three", func(c *Capabilities) { *c = Capabilities{} }, "ExitReport, Stall, Kill"},
	}
	for _, shape := range []struct {
		name  string
		limit func(t *Target)
	}{
		{"sessions", func(t *Target) { t.Info.Run = RunLimits{} }},
		{"runs", func(t *Target) { t.Info.Session = SessionLimits{} }},
		{"sessions and runs", func(*Target) {}},
	} {
		for _, tc := range missing {
			t.Run(shape.name+" without "+tc.name, func(t *testing.T) {
				target := validTarget()
				shape.limit(&target)
				tc.change(&target.Info.Capabilities)
				_, err := newTargetRegistry([]Target{target})
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("error = %v, want one naming %q", err, tc.want)
				}
			})
		}
		t.Run(shape.name+" with them declared", func(t *testing.T) {
			target := validTarget()
			shape.limit(&target)
			if _, err := newTargetRegistry([]Target{target}); err != nil {
				t.Errorf("a complete declaration was refused: %v", err)
			}
		})
	}
	t.Run("a target that only offers Options declares nothing", func(t *testing.T) {
		if _, err := newTargetRegistry([]Target{{Exec: noopExec, Info: TargetInfo{Provider: "k8s", Type: "pods"}}}); err != nil {
			t.Errorf("an options-only target was refused: %v", err)
		}
	})
}

func TestRegistryHoldsACopyOfWhatTheAuthorGave(t *testing.T) {
	target := validTarget()
	target.Info.DefaultArgv = []string{"/bin/sh", "-l"}
	target.Info.Capabilities.TTY = true
	target.Info.Capabilities.Signals = []Signal{SignalInt, SignalTerm}
	target.Info.Capabilities.Integration = []IntegrationMode{IntegrationArgvWrapper}
	r, err := newTargetRegistry([]Target{target})
	if err != nil {
		t.Fatal(err)
	}
	target.Info.DefaultArgv[0] = "/bin/evil"
	target.Info.Capabilities.Signals[0] = SignalKill
	target.Info.Capabilities.Integration[0] = IntegrationOff
	after := r.description().GetTargets()[0]
	if after.GetDefaultArgv()[0] != "/bin/sh" || after.GetCapabilities().GetSignals().GetSignals()[0].String() != "SIGNAL_INT" ||
		after.GetCapabilities().GetIntegration().GetModes()[0].String() != "INTEGRATION_MODE_ARGV_WRAPPER" {
		t.Errorf("the author changed what was registered: %v", after)
	}
	// And a caller of description() cannot change it either.
	after.DefaultArgv[0] = "/bin/evil"
	after.Capabilities.Signals.Signals[0] = 9999
	if again := r.description().GetTargets()[0]; again.GetDefaultArgv()[0] != "/bin/sh" || again.GetCapabilities().GetSignals().GetSignals()[0].String() != "SIGNAL_INT" {
		t.Errorf("a description shares storage with the registry: %v", again)
	}
}

// Plugin is comparable, as it was before the v2 lane existed: a plugin that embeds it or holds it in a map key, or a
// test that compares two, keeps compiling and running. Everything that is not comparable (a mutex, the targets, the
// connection registry) lives behind the one pointer of the v2 lane.
func TestPluginIsComparableAndAZeroPluginHasNoV2Lane(t *testing.T) {
	// reflect.TypeOf and not reflect.TypeFor: Go 1.27.4's linker panics on TypeFor of a comparable struct with two
	// interface fields (R_USEIFACE ... .eqfunc.IIM8 is not a type or itab).
	if !reflect.TypeOf(Plugin{}).Comparable() {
		t.Fatal("exec.Plugin is not comparable")
	}
	zero := &Plugin{}
	if zero.v2 != nil {
		t.Error("a zero Plugin, which is what the host dispenses, has a v2 lane")
	}
	if any(*zero) != any(Plugin{}) {
		t.Error("two zero Plugins are not equal")
	}

	served, err := NewTargetPlugin([]Target{validTarget()})
	if err != nil {
		t.Fatal(err)
	}
	if served.v2 == nil {
		t.Fatal("a Plugin built with a target has no v2 lane")
	}
	copied := *served
	if any(copied) != any(*served) {
		t.Error("a copy of a Plugin is not equal to it")
	}
	if any(*served) == any(*zero) {
		t.Error("a Plugin that serves a target equals a zero one")
	}
}

func TestNewTargetPluginValidatesItsTargetsAndNoTargetsIsNoV2Lane(t *testing.T) {
	for name, targets := range map[string][]Target{"nil": nil, "empty": {}} {
		p, err := NewTargetPlugin(targets)
		if err != nil || p == nil || p.v2 != nil || p.Impl != nil {
			t.Errorf("%s targets: %+v, %v: want a Plugin with no lane at all", name, p, err)
		}
	}
	bad := validTarget()
	bad.Exec = nil
	if p, err := NewTargetPlugin([]Target{bad}); err == nil || p != nil || !strings.Contains(err.Error(), "Exec function is required") {
		t.Errorf("an invalid target: %+v, %v", p, err)
	}
	if p, err := NewTargetPlugin([]Target{validTarget(), validTarget()}); err == nil || p != nil || !strings.Contains(err.Error(), "duplicate exec target key") {
		t.Errorf("a duplicate key: %+v, %v", p, err)
	}
}

// RegisterPlugin keeps every existing registration working exactly as before, and adds the v2 lane for Targets.
func TestRegisterPluginInstallsTheLanesThePluginSupplies(t *testing.T) {
	newPlugin := func() *sdk.Plugin { return &sdk.Plugin{Log: logging.NewNop()} }
	handler := Handler{Plugin: "k8s", Resource: "pods"}

	tests := []struct {
		name       string
		opts       PluginOpts
		wantLegacy bool
		wantV2     bool
	}{
		{"handlers only (every plugin built before)", PluginOpts{Handlers: []Handler{handler}}, true, false},
		{"neither (as before: an empty legacy manager)", PluginOpts{}, true, false},
		{"targets only", PluginOpts{Targets: []Target{validTarget()}}, false, true},
		{"both", PluginOpts{Handlers: []Handler{handler}, Targets: []Target{validTarget()}}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPlugin()
			if err := RegisterPlugin(p, tc.opts); err != nil {
				t.Fatal(err)
			}
			capability, ok := p.GetPluginMap()["exec"].(*Plugin)
			if !ok {
				t.Fatalf("exec capability is %T", p.GetPluginMap()["exec"])
			}
			if (capability.Impl != nil) != tc.wantLegacy {
				t.Errorf("legacy lane installed = %v, want %v", capability.Impl != nil, tc.wantLegacy)
			}
			if (capability.v2 != nil) != tc.wantV2 {
				t.Errorf("v2 lane installed = %v, want %v", capability.v2 != nil, tc.wantV2)
			}
		})
	}

	t.Run("an invalid target is an error at registration", func(t *testing.T) {
		bad := validTarget()
		bad.Exec = nil
		if err := RegisterPlugin(newPlugin(), PluginOpts{Targets: []Target{bad}}); err == nil || !strings.Contains(err.Error(), "Exec function is required") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("duplicate legacy handlers are still refused", func(t *testing.T) {
		if err := RegisterPlugin(newPlugin(), PluginOpts{Handlers: []Handler{handler, handler}}); err == nil || !strings.Contains(err.Error(), "duplicate exec handler key") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("a nil plugin", func(t *testing.T) {
		if err := RegisterPlugin(nil, PluginOpts{}); err == nil {
			t.Error("a nil plugin was accepted")
		}
	})
}
