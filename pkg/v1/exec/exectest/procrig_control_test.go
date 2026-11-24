package exectest_test

import (
	"context"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// The rig gives a function the control channels of its Proc as the SDK does, and holds what the test does with them to what the SDK
// does with the frames of a host.

func TestTheRigsProcHasSignalsAndFactAndQueriesOnlyForATargetThatProvidesAFact(t *testing.T) {
	plain := exectest.NewProcRig(t, exec.Request{}).Proc()
	if plain.Signals == nil || plain.Fact == nil || plain.Queries != nil {
		t.Errorf("Signals %v, Fact %v, Queries %v for a target that provides no fact: signals and fact are never nil, queries is nil", plain.Signals != nil, plain.Fact != nil, plain.Queries != nil)
	}
	caps := exec.Capabilities{TTY: true, Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates, CwdFact: exec.FactModePull}
	if withFacts := exectest.NewProcRig(t, exec.Request{}, exectest.RigCapabilities(caps)).Proc(); withFacts.Queries == nil {
		t.Error("Queries is nil for a target that provides the directory")
	}
}

func TestTheRigDeliversTheSignalsTheSessionDeclaresAndTellsTheTestOfAnyOther(t *testing.T) {
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{}) // the default target declares INT, TERM and KILL
	rig.Signal(exec.SignalTerm)
	rig.Signal(exec.SignalHup) // not declared: the SDK would end the stream of a host that sent it
	var got exec.Signal
	if _, err := rig.Run(context.Background(), func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		got = <-p.Signals
		return exec.ExitCode(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got != exec.SignalTerm {
		t.Errorf("the function read %v, want TERM", got)
	}
	if told := told.told(); len(told) != 1 {
		t.Errorf("the test was told %q, want one report, for the signal that is not declared", told)
	}
	rig.Signal(exec.SignalInt) // after the command ended: nothing, and no panic
}

func TestTheRigRoutesAQueryToTheFunctionRecordsItsPushesAndTellsTheTestWhatTheSDKWouldRefuse(t *testing.T) {
	caps := exec.Capabilities{TTY: true, Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates,
		CwdFact: exec.FactModePull, ForegroundFact: exec.FactModePush}
	told := &recordedT{TB: t}
	rig := exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(caps))
	answer := rig.Ask(exec.FactKindCwd)
	if _, err := rig.Run(context.Background(), func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		q := <-p.Queries
		q.Answer(exec.Fact{Cwd: "/srv"})
		q.Answer(exec.Fact{Cwd: "/ignored"}) // an answer is the first
		p.Fact(exec.Fact{Kind: exec.FactKindForeground, Foreground: exec.Foreground{State: exec.ForegroundBusy, Name: "vim"}})
		p.Fact(exec.Fact{Kind: exec.FactKindCwd, Cwd: "/pull"}) // the directory is pulled, not pushed
		return exec.ExitCode(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := <-answer; got.Cwd != "/srv" {
		t.Errorf("the answer is %+v, want the directory /srv", got)
	}
	if facts := rig.Facts(); len(facts) != 1 || facts[0].Foreground.Name != "vim" {
		t.Errorf("the rig recorded %+v, want the one foreground fact that was pushed", facts)
	}
	if told := told.told(); len(told) != 1 {
		t.Errorf("the test was told %q, want one report, for the push of a fact that is not PUSH", told)
	}
	rig.Ask(exec.FactKindForeground) // a pushed fact can be asked for too: no report
	if n := len(told.told()); n != 1 {
		t.Errorf("a query for a fact the target provides was reported: %d reports", n)
	}
	exectest.NewProcRig(told, exec.Request{}).Ask(exec.FactKindCwd) // the default target provides none: the SDK answers it itself
	if n := len(told.told()); n != 2 {
		t.Errorf("a query for a fact the target does not provide was not reported: %d reports", n)
	}
	pullOnly := exec.Capabilities{TTY: true, Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates, CwdFact: exec.FactModePull}
	exectest.NewProcRig(told, exec.Request{}, exectest.RigCapabilities(pullOnly)).Ask(exec.FactKindForeground) // Proc.Queries exists, this fact is not provided
	if n := len(told.told()); n != 3 {
		t.Errorf("a query for a fact the target does not provide, beside one it provides, was not reported: %d reports", n)
	}
}
