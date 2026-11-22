// Command exec-plugin registers one exec target, an echo host. It has no resource provider, so a host can Describe
// it but cannot reach it: docs/exec.md says what a reachable plugin adds.
package main

import (
	"context"
	"io"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func main() {
	// NewPlugin reads the installed plugin.yaml of this ID and panics without it.
	plugin := sdk.NewPlugin(sdk.PluginOpts{ID: "echo-exec"})
	if err := exec.RegisterPlugin(plugin, exec.PluginOpts{Targets: []exec.Target{echoHost()}}); err != nil {
		panic(err)
	}
	plugin.Serve()
}

// echoHost is the one kind of thing this plugin runs commands in. Its "command" copies its input back.
func echoHost() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider: "echo", Type: "host", // the routing key: the resource provider id and type of a request's reference
			DisplayName: "Echo host",
			DefaultArgv: []string{"cat"},
			Capabilities: exec.Capabilities{ // declared, never guessed: what closing does, how it ends and stalls
				Kill: exec.KillTerminates, ExitReport: exec.ExitReportCode, Stall: exec.StallPropagates,
			},
			Session: exec.DefaultSessionLimits(), // a target with neither Session nor Run serves only Options
		},
		Exec: echo,
	}
}

// echo is the whole command: copy the input back until it ends.
func echo(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	p.Started(exec.SessionFacts{Program: exec.Program{Argv: p.Request.Argv, Name: "cat"}})
	if _, err := io.Copy(p.Stdout, p.Stdin); err != nil && ctx.Err() == nil {
		return exec.Exit{}, err // the target failed: the SDK classifies the error
	}
	return exec.ExitCode(0), nil // the command ended with its own status
}
