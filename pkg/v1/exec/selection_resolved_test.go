package exec_test

import (
	"context"
	"maps"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The function sees the choice of every option its request resolved to: the one the host selected, or the default of an option it
// omitted. A request that omits a required option whose choice is the default is admitted, and the function must not have to
// read the resource again to find the choice the SDK already used (a Pod with several containers fails at the API without the
// name of the container).

// resolvedOptions are the options of the thing: a required container with a default, a mode with a default, and an image that is
// neither required nor has a default.
func resolvedOptions() []exec.Option {
	return []exec.Option{
		{Name: "container", Required: true, Choices: []exec.Choice{
			{Value: "app", State: exec.ChoiceStateReady, IsDefault: true},
			{Value: "sidecar", State: exec.ChoiceStateReady},
		}},
		{Name: "mode", Choices: []exec.Choice{
			{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true},
			{Value: "attach", State: exec.ChoiceStateReady},
		}},
		{Name: "image", Choices: []exec.Choice{{Value: "debian", State: exec.ChoiceStateReady}}},
	}
}

// selectionReporter is a function that tells the test the selection it was given, a copy, and then runs until it is ended.
func selectionReporter(seen chan<- map[string]string) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- maps.Clone(p.Request.Selection)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

func staticOptionsTarget(fn exec.ExecFunc, options []exec.Option) exec.Target {
	target := engTarget(fn)
	target.Options = exectest.NewFakeTarget(exectest.FakeStaticOptions(options...)).Options
	return target
}

var resolvedSelections = []struct {
	name string
	mods []func(*execv2.Start)
	want map[string]string
}{
	{"nothing selected: the default of each option that has one", nil, map[string]string{"container": "app", "mode": "exec"}},
	{"one option selected: the others resolve to their defaults", []func(*execv2.Start){withSelection("mode", "attach")},
		map[string]string{"container": "app", "mode": "attach"}},
	{"a choice that is not the default is the choice", []func(*execv2.Start){withSelection("container", "sidecar")},
		map[string]string{"container": "sidecar", "mode": "exec"}},
	{"an option with no default that the request selects", []func(*execv2.Start){withSelection("image", "debian")},
		map[string]string{"container": "app", "mode": "exec", "image": "debian"}},
	{"everything selected", []func(*execv2.Start){withSelection("container", "sidecar"), withSelection("mode", "attach"), withSelection("image", "debian")},
		map[string]string{"container": "sidecar", "mode": "attach", "image": "debian"}},
}

func TestTheFunctionSeesTheResolvedChoiceOfEveryOptionOfItsSessionRequest(t *testing.T) {
	for _, tc := range resolvedSelections {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan map[string]string, 1)
			rig := newEngineRig(t, engineRigConfig{}, staticOptionsTarget(selectionReporter(seen), resolvedOptions()))
			cmd := rig.mustOpen("s1", tc.mods...)
			defer func() { cmd.Close(false); awaitDone(t, cmd) }()
			if got := awaitValue(t, seen, "the function to start"); !maps.Equal(got, tc.want) {
				t.Errorf("the function sees the selection %v, want %v", got, tc.want)
			}
		})
	}
}

// A run resolves its selection the same way: the engine starts both.
func TestTheFunctionSeesTheResolvedChoiceOfEveryOptionOfItsRunRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  []*execv2.NameValue
		want map[string]string
	}{
		{"nothing selected", nil, map[string]string{"container": "app", "mode": "exec"}},
		{"a choice that is not the default", []*execv2.NameValue{{Name: "container", Value: "sidecar"}}, map[string]string{"container": "sidecar", "mode": "exec"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan map[string]string, 1)
			rig := newRunSrvRig(t, staticOptionsTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
				seen <- maps.Clone(p.Request.Selection)
				return exec.ExitCode(0), nil
			}, resolvedOptions()))
			req := rig.request()
			req.Target.Selection = tc.sel
			if res := rig.mustRun(req); res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
				t.Fatalf("the run ended %v", res.GetOutcome())
			}
			if got := awaitValue(t, seen, "the function to start"); !maps.Equal(got, tc.want) {
				t.Errorf("the function sees the selection %v, want %v", got, tc.want)
			}
		})
	}
}

// What the host reads in Opened is what it sent: the target's selection echoes the request, and the defaults that the SDK
// resolved are the function's to read, not a claim about what the host asked for (conformance case 1 holds Opened to the request).
func TestOpenedEchoesTheSelectionTheHostSentAndNotTheDefaultsTheSDKResolved(t *testing.T) {
	seen := make(chan map[string]string, 1)
	rig := newWireRig(t, staticOptionsTarget(selectionReporter(seen), resolvedOptions()))
	conn := rig.mustOpen("s1", withoutTTY, withSelection("mode", "attach"))
	if got := awaitValue(t, seen, "the function to start"); !maps.Equal(got, map[string]string{"container": "app", "mode": "attach"}) {
		t.Errorf("the function sees %v", got)
	}
	if got := conn.Opened().GetFacts().GetTarget().GetSelection(); !maps.Equal(got, map[string]string{"mode": "attach"}) {
		t.Errorf("Opened carries the selection %v, want what the host sent: {mode: attach}", got)
	}
}

// The selection is the function's own: what it does to its copy changes nothing for the next session of the same request.
func TestTheFunctionsSelectionIsItsOwnCopy(t *testing.T) {
	seen := make(chan map[string]string, 2)
	rig := newEngineRig(t, engineRigConfig{}, staticOptionsTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- maps.Clone(p.Request.Selection)
		p.Request.Selection["container"] = "mutated"
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, resolvedOptions()))
	for _, id := range []string{"s1", "s2"} {
		cmd := rig.mustOpen(id)
		if got := awaitValue(t, seen, "the function to start"); got["container"] != "app" {
			t.Errorf("session %s sees the container %q, want app: a copy of the selection is shared between sessions", id, got["container"])
		}
		cmd.Close(false)
		awaitDone(t, cmd)
	}
}
