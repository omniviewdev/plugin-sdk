//go:build unix

package exec_test

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/echotarget"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

const compatStubPluginEnv = "OMNIVIEW_COMPAT_STUB_PLUGIN"

// TestCompatStubPlugin is not a test: it is the plugin process of TestCompatHostStopKillsAHungPlugin, this test
// binary run again with compatStubPluginEnv set. It serves the same plugin as the newplugin helper.
func TestCompatStubPlugin(t *testing.T) {
	if os.Getenv(compatStubPluginEnv) != "1" {
		t.Skip("the plugin process of TestCompatHostStopKillsAHungPlugin")
	}
	p := sdk.NewPlugin(sdk.PluginOpts{ID: scenario.PluginID})
	err := exec.RegisterPlugin(p, exec.PluginOpts{
		Handlers: []exec.Handler{scenario.EchoHandler()},
		Targets:  echotarget.Targets(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "stub plugin:", err)
		os.Exit(1)
	}
	p.Serve()
}

// TestCompatHostStopKillsAHungPlugin requires Host.Stop to return, with the plugin process killed and reaped, when
// the plugin stopped answering after its start: a stopped process cannot answer the shutdown request, which
// go-plugin makes under a context that lasts as long as the child does.
func TestCompatHostStopKillsAHungPlugin(t *testing.T) {
	compatIsolateEnvironment(t, compatTempDir(t))
	t.Setenv(compatStubPluginEnv, "1")

	cmd := osexec.Command(os.Args[0], "-test.run=^TestCompatStubPlugin$", "-test.count=1")
	host, err := scenario.LaunchCommand(cmd, scenario.PluginID)
	if err != nil {
		t.Fatalf("launch the stub plugin: %v", err)
	}
	t.Cleanup(func() { host.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), compatSafetyNet)
	defer cancel()
	if _, err := host.Info(ctx); err != nil {
		t.Fatalf("the stub plugin does not answer while it runs: %v", err)
	}

	pid := host.PID()
	if pid <= 0 {
		t.Fatal("the host did not report the plugin's process id")
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("stop the plugin process: %v", err)
	}
	// The grace period only decides when the host stops waiting: the stopped process cannot shut down in any time.
	host.StopGrace = 10 * time.Millisecond

	stopped := make(chan scenario.Child, 1)
	go func() { stopped <- host.Stop() }()
	select {
	case child := <-stopped:
		assertCompatChildGone(t, "a hung plugin", child)
	case <-time.After(compatSafetyNet):
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("Host.Stop did not return: it waits for ever for a plugin that stopped answering")
	}
}
