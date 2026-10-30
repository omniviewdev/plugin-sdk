// Command resource-plugin serves an immutable embedded notes provider.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	smoke := flag.Bool("smoke", false, "read an embedded note without starting the plugin transport")
	flag.Parse()
	provider, err := newProvider()
	if err != nil {
		return fmt.Errorf("create notes provider: %w", err)
	}
	defer func() {
		if closeErr := provider.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close notes provider: %w", closeErr))
		}
	}()
	if *smoke {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return smokeRead(ctx, provider, os.Stdout)
	}
	// The host installs plugin.yaml in its selected state root before launch.
	plugin := sdk.NewPlugin(sdk.PluginOpts{ID: pluginID})
	sdk.RegisterResourcePlugin(plugin, provider)
	plugin.Serve()
	return nil
}

func smokeRead(ctx context.Context, provider *resourcev2.Controller[struct{}], out io.Writer) error {
	route := resourcev2.AccessRoute{Connection: "local"}
	catalog, err := provider.Catalog(ctx, resourcev2.CatalogRequest{Route: route})
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	// Only this in-process harness stands in for the host. This string is not
	// a real grant. Production authorities and permissions come from the host.
	request := resourcev2.GetRequest{
		Route: route,
		Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
			Provider: pluginID, Authority: "smoke-host", Type: resourceTypeID, ID: "welcome",
		}},
		Representation: representationID,
	}
	ctx = resourcev2.WithCatalogRevisionForRoute(ctx, catalog.Provider, route, catalog.Revision)
	record, err := provider.Get(ctx, request)
	if err != nil {
		return fmt.Errorf("get welcome: %w", err)
	}
	_, err = fmt.Fprintf(out,
		"catalog: provider=%s type=%s representation=%s get=true\n"+
			"record: authority=%s id=%s title=%s\n"+
			"sequence: kind=%s value=%s\n",
		catalog.Provider, catalog.Types[0].Type, catalog.Types[0].DefaultRepresentation,
		record.Reference.Address.Authority, record.Reference.Address.ID, record.Body.Map["title"].String,
		record.Body.Map["sequence"].Kind, record.Body.Map["sequence"].Int,
	)
	return err
}
