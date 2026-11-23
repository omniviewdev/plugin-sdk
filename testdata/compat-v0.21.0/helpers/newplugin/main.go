// Command newplugin is the helper plugin of the exec compatibility proof as the current tree builds it: the same
// legacy handler as oldplugin, compat/echo, and a v2 target for the same resource on the same capability. A host
// that knows only the legacy lane must see exactly what oldplugin shows it.
package main

import (
	"fmt"
	"os"

	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/echotarget"
	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

func main() {
	p := sdk.NewPlugin(sdk.PluginOpts{ID: scenario.PluginID})
	err := exec.RegisterPlugin(p, exec.PluginOpts{
		Handlers: []exec.Handler{scenario.EchoHandler()},
		Targets:  echotarget.Targets(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "newplugin:", err)
		os.Exit(1)
	}
	p.Serve()
}
