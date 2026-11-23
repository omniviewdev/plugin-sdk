// Package echotarget is the one definition of the v2 target of the exec compatibility proof, compat/echo. The
// in-process replay test (pkg/v1/exec/compat_frames_test.go) and the real-binary helper (../newplugin) both build
// their v2 lane from it, so that they cannot drift apart.
//
// Unlike ../scenario, which compiles against SDK v0.21.0 as well, this package uses the exec v2 API and builds only
// against the current tree.
package echotarget

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// Targets returns the targets of the compat plugin: the one target compat/echo.
func Targets() []exec.Target {
	return []exec.Target{Target()}
}

// Target is the v2 target compat/echo: it runs nothing (its Exec returns at once) and offers one option, the
// container, with a ready default choice and a stopped one.
func Target() exec.Target {
	return exec.Target{
		Info: exec.TargetInfo{
			Provider:    "compat",
			Type:        "echo",
			DisplayName: "Echo",
			DefaultArgv: []string{"/bin/echo"},
			Capabilities: exec.Capabilities{
				TTY:        true,
				Kill:       exec.KillTerminates,
				ExitReport: exec.ExitReportCode,
				Stall:      exec.StallPropagates,
			},
			Session: exec.DefaultSessionLimits(),
		},
		Exec: func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil },
		Options: func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			return []exec.Option{{
				Name: "container",
				Choices: []exec.Choice{
					{Value: "main", DisplayName: "main", State: exec.ChoiceStateReady, IsDefault: true},
					{Value: "sidecar", DisplayName: "sidecar", State: exec.ChoiceStateStopped},
				},
			}}, nil
		},
	}
}
