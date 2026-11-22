// Command oldplugin is the helper plugin of the exec compatibility proof as SDK v0.21.0 builds it: it serves the
// legacy exec lane with one handler, compat/echo, and nothing else. Command newplugin is the same plugin built
// against the current tree, with a v2 target beside the handler.
package main

import (
	"fmt"
	"os"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

func main() {
	p := sdk.NewPlugin(sdk.PluginOpts{ID: scenario.PluginID})
	if err := exec.RegisterPlugin(p, exec.PluginOpts{Handlers: []exec.Handler{scenario.EchoHandler()}}); err != nil {
		fmt.Fprintln(os.Stderr, "oldplugin:", err)
		os.Exit(1)
	}
	p.Serve()
}
