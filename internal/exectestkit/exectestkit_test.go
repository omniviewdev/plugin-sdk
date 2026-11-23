package exectestkit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

func TestConnectionsLeaseCheckAndRetire(t *testing.T) {
	c := exectestkit.NewConnectionsT(t, "k8s")
	entered, released := make(chan struct{}), make(chan error, 1)
	go func() {
		released <- c.WithConnection(context.Background(), c.Route(), func(lease context.Context, conn types.Connection) error {
			close(entered)
			<-lease.Done()
			return lease.Err()
		})
	}()
	<-entered
	c.Retire() // returns only when the callback that holds the lease has returned
	if err := <-released; !errors.Is(err, context.Canceled) {
		t.Errorf("the lease ended with %v", err)
	}
	err := c.WithConnection(context.Background(), c.Route(), func(context.Context, types.Connection) error { return nil })
	if resourcev2.ErrorCode(err) != "CONNECTION_ERROR" {
		t.Errorf("a lease after retirement: %v", err)
	}
	if err := c.WithConnection(context.Background(), resourcev2.AccessRoute{Connection: "other"}, nil); resourcev2.ErrorCode(err) != "NOT_FOUND" {
		t.Errorf("an unknown route: %v", err)
	}
	catalog, err := exectestkit.NewConnections("k8s").Catalog(context.Background(), resourcev2.CatalogRequest{Route: c.Route()})
	if err != nil || catalog.Provider != "k8s" || catalog.Revision == "" {
		t.Errorf("catalog = %+v, %v", catalog, err)
	}
}

// The wire form of a target carries what a test puts in it, the selection included: it is how a test sends the
// request the SDK's own client has no way to build.
func TestWireTargetCarriesTheReferenceTheRouteAndTheSelection(t *testing.T) {
	c := exectestkit.NewConnectionsT(t, "k8s")
	got := c.WireTarget(t, "pods", "pod-1", &execv2.NameValue{Name: "container", Value: "app"})
	if got.GetReference().GetAddress().GetProvider() != "k8s" || got.GetReference().GetAddress().GetType() != "pods" ||
		got.GetReference().GetAddress().GetId() != "pod-1" || got.GetRoute().GetConnection() != c.ConnectionID() {
		t.Errorf("target = %v", got)
	}
	if sel := got.GetSelection(); len(sel) != 1 || sel[0].GetName() != "container" || sel[0].GetValue() != "app" {
		t.Errorf("selection = %v", sel)
	}
	if bare := c.WireTarget(t, "pods", "pod-1"); len(bare.GetSelection()) != 0 {
		t.Errorf("a target with no selection carries %v", bare.GetSelection())
	}
}

// Serve serves what it is given over a real transport: the client of the server is the SDK's own, and a plugin that
// is given no target has no v2 lane.
func TestServeServesTheTargetsOverARealTransport(t *testing.T) {
	target := exec.Target{
		Info: exec.TargetInfo{Provider: "k8s", Type: "pods", DisplayName: "Pod"},
		Exec: func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitCode(0), nil },
	}
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}})
	got, err := server.V2(t).Describe(context.Background())
	if err != nil || len(got.GetTargets()) != 1 || got.GetTargets()[0].GetType() != "pods" {
		t.Fatalf("Describe = %v, %v", got, err)
	}

	bare := exectestkit.Serve(t, exectestkit.ServeConfig{})
	_, err = bare.V2(t).Describe(context.Background())
	var refusal *exec.Error
	if !errors.As(err, &refusal) || refusal.Class != exec.ErrorClassUnsupported {
		t.Errorf("Describe of a plugin with no targets = %v, want the typed refusal of a plugin that does not serve the lane", err)
	}
}
