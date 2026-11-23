package exec_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What the request says reaches the function as the request says it: the argv, the environment, the working directory and the
// labels, validated and copied, and a command that has no terminal.
func TestServedRunGivesTheFunctionTheRequestAsItWasAdmitted(t *testing.T) {
	type seen struct {
		Argv   []string
		Env    map[string]string
		Cwd    string
		Labels map[string]string
		TTY    bool
		Size   exec.Size
		ID     string
	}
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		out, err := json.Marshal(seen{
			Argv: p.Request.Argv, Env: p.Request.Env, Cwd: p.Request.Cwd, Labels: p.Request.Labels,
			TTY: p.Request.TTY, Size: p.Request.Size, ID: p.Request.Ref.Address.ID,
		})
		if err != nil {
			return exec.Exit{}, err
		}
		_, _ = p.Stdout.Write(out)
		return exec.ExitCode(0), nil
	}))
	res := rig.mustRun(rig.request(
		runSrvArgv("sh", "-c", "echo $A"),
		func(r *execv2.RunRequest) {
			r.Env = []*execv2.NameValue{{Name: "A", Value: "1"}, {Name: "B", Value: ""}}
			r.Cwd = "/work"
		},
		runSrvLabel("audit", "x-1"),
	))
	var got seen
	if err := json.Unmarshal(res.GetStdout(), &got); err != nil {
		t.Fatalf("the function's report %q: %v", res.GetStdout(), err)
	}
	if len(got.Argv) != 3 || got.Argv[0] != "sh" || got.Argv[2] != "echo $A" || got.Env["A"] != "1" || len(got.Env) != 2 ||
		got.Cwd != "/work" || got.Labels["audit"] != "x-1" || got.TTY || (got.Size != exec.Size{}) || got.ID != "thing-1" {
		t.Errorf("the function saw %+v", got)
	}
}

// A run is its own kind of command: the function of a target that serves sessions and runs is called for each, and a run is
// never given a terminal even though the target could give one.
func TestServedRunOfATargetThatCouldGiveATerminalIsNeverGivenOne(t *testing.T) {
	rig := newRunSrvRig(t, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		if p.Request.TTY || p.Stderr == nil || p.Capabilities.TTY != true {
			_, _ = io.WriteString(p.Stdout, "unexpected")
		}
		return exec.ExitCode(0), nil
	}))
	res := rig.mustRun(rig.request())
	if len(res.GetStdout()) != 0 {
		t.Errorf("the function was given a terminal, or no stderr: %q", res.GetStdout())
	}
}
