package exec_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A choice that runs no program: a Kubernetes attach joins the container's own process, so a session in it has no program, and
// the SDK must not say it has one. Before this, Opened carried the target's DefaultArgv ("/bin/sh") for an attach, because that
// is what fills a request that names no argv.

// noProgramOptions are the options of a Pod: a mode that is an exec by default and an attach that runs no program.
func noProgramOptions() []exec.Option {
	return []exec.Option{{Name: "mode", Choices: []exec.Choice{
		{Value: "exec", State: exec.ChoiceStateReady, IsDefault: true},
		{Value: "attach", State: exec.ChoiceStateReady, NoProgram: true},
	}}}
}

// seenProc is what a function tells a test of the Proc it was given.
type seenProc struct{ argv []string }

// programReporter is a function that tells the test its argv and writes a byte, which gives the session the Started that the SDK
// makes of the facts of Opened, and then runs until it is ended.
func programReporter(seen chan<- seenProc) exec.ExecFunc {
	return func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		seen <- seenProc{argv: slices.Clone(p.Request.Argv)}
		_, _ = p.Stdout.Write([]byte("x"))
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}
}

// withArgv gives a Start an argv of its own.
func withArgv(argv ...string) func(*execv2.Start) {
	return func(s *execv2.Start) { s.Argv = argv }
}

// The program of an exec is the one that was asked for, or the target's default; the program of a choice that runs none is absent,
// in Opened and in Started, and the function sees no argv.
func TestAChoiceThatRunsNoProgramHasNoProgramInOpenedAndStartedAndTheFunctionSeesNoArgv(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mods     []func(*execv2.Start)
		wantArgv []string // nil: no program
	}{
		{"the exec, which runs the program of the target's default", []func(*execv2.Start){withSelection("mode", "exec")}, []string{"/bin/sh"}},
		{"the default choice, which is the exec", nil, []string{"/bin/sh"}},
		{"the exec with a program of its own", []func(*execv2.Start){withSelection("mode", "exec"), withArgv("ls", "-l")}, []string{"ls", "-l"}},
		{"the attach", []func(*execv2.Start){withSelection("mode", "attach")}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan seenProc, 1)
			rig := newWireRig(t, staticOptionsTarget(programReporter(seen), noProgramOptions()))
			conn := rig.mustOpen("s1", append([]func(*execv2.Start){withoutTTY}, tc.mods...)...)

			program := conn.Opened().GetFacts().GetProgram()
			if tc.wantArgv == nil && program != nil {
				t.Errorf("Opened carries the program %v for a choice that runs none, want it absent", program)
			}
			if tc.wantArgv != nil && !slices.Equal(program.GetArgv(), tc.wantArgv) {
				t.Errorf("Opened carries the argv %v, want %v", program.GetArgv(), tc.wantArgv)
			}
			got := awaitValue(t, seen, "the function to start")
			if !slices.Equal(got.argv, tc.wantArgv) {
				t.Errorf("the function sees the argv %v, want %v", got.argv, tc.wantArgv)
			}

			var started *execv2.SessionFacts
			for started == nil { // the first byte of output makes the Started that the SDK makes of the facts of Opened
				frame, err := recvFrame(t, conn)
				if err != nil {
					t.Fatalf("recv: %v", err)
				}
				started = frame.GetStarted().GetFacts()
			}
			if tc.wantArgv == nil && started.GetProgram() != nil {
				t.Errorf("Started carries the program %v for a choice that runs none, want it absent", started.GetProgram())
			}
			if tc.wantArgv != nil && !slices.Equal(started.GetProgram().GetArgv(), tc.wantArgv) {
				t.Errorf("Started carries the argv %v, want %v", started.GetProgram().GetArgv(), tc.wantArgv)
			}
		})
	}
}

// A request that names a program of its own cannot be honored by a choice that runs none, and is never ignored: it is refused
// UNSUPPORTED, naming the choice, before the function runs. The argv that a request with none gets is the target's default, which
// the SDK does not tell apart from one a host sent: it is dropped.
func TestARequestThatNamesAnArgvIsRefusedByAChoiceThatRunsNoProgram(t *testing.T) {
	var ran bool
	rig := newEngineRig(t, engineRigConfig{}, staticOptionsTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		ran = true
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, noProgramOptions()))

	_, err := rig.open("s1", withSelection("mode", "attach"), withArgv("ls", "-l"))
	var refusal *exec.Error
	if !errors.As(err, &refusal) || refusal.Class != exec.ErrorClassUnsupported ||
		refusal.Facts.SelectionName != "mode" || refusal.Facts.SelectionValue != "attach" {
		t.Fatalf("a request with an argv for an attach: %v, want UNSUPPORTED naming mode and attach", err)
	}
	if ran {
		t.Error("the function ran for a refused request")
	}
	if live := rig.eng.Live(); len(live) != 0 {
		t.Errorf("a refused request left %v", live)
	}

	cmd, err := rig.open("s2", withSelection("mode", "attach"), withArgv("/bin/sh"))
	if err != nil {
		t.Fatalf("the target's default argv for an attach: %v, want it dropped", err)
	}
	cmd.Close(false)
	awaitDone(t, cmd)
}

// A bounded run is a program to run: a choice that runs none cannot serve one, and nothing ran.
func TestARunIsRefusedByAChoiceThatRunsNoProgram(t *testing.T) {
	var ran bool
	rig := newRunSrvRig(t, staticOptionsTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
		ran = true
		return exec.ExitCode(0), nil
	}, noProgramOptions()))
	req := rig.request()
	req.Target.Selection = []*execv2.NameValue{{Name: "mode", Value: "attach"}}
	_, err := rig.run(req)
	if refusal := decodeRunSrvRefusal(t, err); refusal.class != "UNSUPPORTED" || refusal.code.String() != "Unimplemented" {
		t.Fatalf("a run in an attach: %+v, want UNSUPPORTED", refusal)
	}
	req.Argv = []string{"/bin/sh"} // the target's own default: a run names its program, so it is refused all the same
	_, err = rig.run(req)
	if refusal := decodeRunSrvRefusal(t, err); refusal.class != "UNSUPPORTED" {
		t.Fatalf("a run of the default argv in an attach: %+v, want UNSUPPORTED", refusal)
	}
	if ran {
		t.Error("the function ran for a refused run")
	}
	req.Target.Selection = []*execv2.NameValue{{Name: "mode", Value: "exec"}}
	if res := rig.mustRun(req); res.GetOutcome() != execv2.Outcome_OUTCOME_EXITED {
		t.Errorf("a run in an exec ended %v", res.GetOutcome())
	}
}

// The flag is the author's declaration, and a host reads it with the options before it opens anything.
func TestTheNoProgramFlagOfAChoiceReachesAHostThatAsksForTheOptions(t *testing.T) {
	rig := newOptionsRig(t, exechook.EngineConfig{}, staticOptionsTarget(blockUntilCancelled, noProgramOptions()))
	result, err := rig.options(context.Background(), "thing-1")
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, option := range result.Options {
		for _, choice := range option.Choices {
			flags[option.Name+"/"+choice.Value] = choice.NoProgram
		}
	}
	if want := map[string]bool{"mode/exec": false, "mode/attach": true}; len(flags) != 2 || flags["mode/exec"] || !flags["mode/attach"] {
		t.Errorf("the host reads %v, want %v", flags, want)
	}
}

// A function that reports a program for a session that runs none has the report dropped and told, as any fact that breaks its
// bounds is: the facts of such a session never carry a program.
func TestAProgramReportedForAChoiceThatRunsNoProgramIsDroppedAndTold(t *testing.T) {
	reported := make(chan struct{})
	rig := newEngineRig(t, engineRigConfig{}, staticOptionsTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		p.Started(exec.SessionFacts{Program: exec.Program{Argv: []string{"bash"}, Name: "bash", Path: "/bin/bash"}, Transport: "k8s-ws-v5"})
		close(reported)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}, noProgramOptions()))
	cmd := rig.mustOpen("s1", withoutTTY, withSelection("mode", "attach"))
	awaitChannel(t, reported, "the function to report its start")
	started, ok := cmd.Started()
	if !ok {
		t.Fatal("the function reported its start and there is no Started")
	}
	if started.Argv != nil || started.Name != "" || started.Path != "" {
		t.Errorf("Started carries the program %+v, want none", started)
	}
	if started.Transport != "k8s-ws-v5" {
		t.Errorf("Started carries the transport %q, want the one reported: only the program is dropped", started.Transport)
	}
	told := false
	for _, v := range cmd.Violations() {
		told = told || strings.Contains(v, "Program")
	}
	if !told {
		t.Errorf("violations %v, want one that says a program was reported for a choice that runs none", cmd.Violations())
	}
	cmd.Close(false)
	awaitDone(t, cmd)
}
