// Command oldhost is the host of the exec compatibility proof as SDK v0.21.0 builds it: it launches a plugin
// binary with go-plugin, dispenses its exec and lifecycle capabilities, runs the legacy scenario against the exec
// capability, asks the lifecycle service who the plugin is and what it serves, stops the plugin, and prints one JSON
// document (scenario.Report) on its standard output.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omniviewdev/plugin-sdk/testdata/compat-v0.21.0/helpers/scenario"
)

func main() {
	pluginPath := flag.String("plugin", "", "path of the plugin binary to launch")
	id := flag.String("id", scenario.PluginID, "id of the plugin")
	flag.Parse()
	if *pluginPath == "" {
		fmt.Fprintln(os.Stderr, "oldhost: -plugin is required")
		os.Exit(2)
	}
	if err := run(*pluginPath, *id); err != nil {
		fmt.Fprintln(os.Stderr, "oldhost:", err)
		os.Exit(1)
	}
}

// run launches the plugin, runs the scenario and prints the report. The plugin process is stopped on every path,
// an interrupt or a termination signal included.
func run(pluginPath, id string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	host, err := scenario.Launch(pluginPath, id)
	if err != nil {
		return err
	}
	defer host.Stop()

	transcript, err := scenario.Run(ctx, host.Exec, host.Recorder)
	if err != nil {
		return err
	}
	lifecycle, err := host.Info(ctx)
	if err != nil {
		return err
	}
	report := scenario.Report{Transcript: transcript, Lifecycle: lifecycle, Child: host.Stop()}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
