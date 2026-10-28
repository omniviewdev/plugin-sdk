package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"google.golang.org/grpc"
	"testing"
	"time"
)

type streamProvider struct {
	qualifiedProvider
	mode   string
	joined chan struct{}
}

func (p *streamProvider) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	c, err := p.qualifiedProvider.Catalog(ctx, in)
	c.Types[0].Capabilities = append(c.Types[0].Capabilities, resourcev2.Capability{Operation: resourcev2.OperationAction, Scope: resourcev2.QueryScope{}})
	return c, err
}
func (p *streamProvider) StreamAction(ctx context.Context, _ resourcev2.ActionRequest, out chan<- resourcev2.ActionEvent) error {
	if p.joined != nil {
		defer close(p.joined)
	}
	switch p.mode {
	case "noReady":
		return nil
	case "failed":
		return resourcev2.NewError("UNAVAILABLE", "native establishment failed")
	case "earlyData":
		out <- resourcev2.ActionEvent{Type: "output", Data: &resourcev2.Value{Kind: "string", String: "x"}}
		return nil
	}
	select {
	case out <- resourcev2.ActionEvent{Type: "ready"}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if p.mode == "duplicate" {
		select {
		case out <- resourcev2.ActionEvent{Type: "ready"}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	if p.mode == "wait" {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case out <- resourcev2.ActionEvent{Type: "output", Data: &resourcev2.Value{Kind: "int64", Int: "9223372036854775807"}}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func actionInput() resourcev2.ActionRequest {
	r := testGet()
	return resourcev2.ActionRequest{Route: r.Route, Reference: r.Reference, Action: "test", Input: resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}}}
}
func TestActualGRPCActionReadiness(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"valid", ""}, {"noReady", "INVALID_REQUEST"}, {"duplicate", "INVALID_REQUEST"}, {"earlyData", "INVALID_REQUEST"}, {"failed", "UNAVAILABLE"}} {
		t.Run(tc.mode, func(t *testing.T) {
			p := &streamProvider{mode: tc.mode}
			s := grpc.NewServer()
			RegisterServer(s, p)
			c := NewClient(pipe(t, s))
			out := make(chan resourcev2.ActionEvent, 8)
			err := c.StreamAction(acceptedCatalogContext(t, context.Background(), c, actionInput().Route), actionInput(), out)
			if resourcev2.ErrorCode(err) != tc.code {
				t.Fatalf("class %s want %s: %v", resourcev2.ErrorCode(err), tc.code, err)
			}
			if tc.mode == "valid" {
				if len(out) != 2 {
					t.Fatal("missing action events")
				}
				if (<-out).Type != "ready" {
					t.Fatal("first event not ready")
				}
				if (<-out).Data.Int != "9223372036854775807" {
					t.Fatal("exact action value lost")
				}
			}
			select {
			case _, ok := <-out:
				if !ok {
					t.Fatal("transport closed caller channel")
				}
			default:
			}
			out <- resourcev2.ActionEvent{Type: "caller-still-owns-channel"}
		})
	}
}
func TestActionCancellationJoinsProducer(t *testing.T) {
	p := &streamProvider{mode: "wait", joined: make(chan struct{})}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx, cancel := context.WithCancel(acceptedCatalogContext(t, context.Background(), c, actionInput().Route))
	out := make(chan resourcev2.ActionEvent, 1)
	done := make(chan error, 1)
	go func() { done <- c.StreamAction(ctx, actionInput(), out) }()
	select {
	case v := <-out:
		if v.Type != "ready" {
			t.Fatal("missing readiness")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
	cancel()
	select {
	case err := <-done:
		if testErrorClass(err) != "CANCELLED" {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not stop")
	}
	select {
	case <-p.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("native producer did not join")
	}
}

type browserProvider struct{ qualifiedProvider }

func (*browserProvider) ListConnections(context.Context) ([]types.Connection, error) {
	return []types.Connection{}, nil
}
func (*browserProvider) ScopeValues(context.Context, resourcev2.AccessRoute, string) ([]string, error) {
	return []string{"one", "two"}, nil
}
func (*browserProvider) UpdateConnection(_ context.Context, in types.Connection) (types.Connection, error) {
	return in, nil
}
func (*browserProvider) DeleteConnection(context.Context, string) error { return nil }
func (*browserProvider) WatchConnections(ctx context.Context, out chan<- []types.Connection) error {
	select {
	case out <- []types.Connection{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func TestActualGRPCConnectionBrowser(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &browserProvider{})
	c := NewClient(pipe(t, s))
	if _, err := c.ListConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	values, err := c.ScopeValues(acceptedCatalogContext(t, context.Background(), c, resourcev2.AccessRoute{Connection: "explicit"}), resourcev2.AccessRoute{Connection: "explicit"}, "dimension")
	if err != nil || len(values) != 2 {
		t.Fatalf("scope values: %v %v", values, err)
	}
	if _, err = c.UpdateConnection(context.Background(), types.Connection{}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	out := make(chan []types.Connection, 1)
	if err = c.WatchConnections(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatal("connection event missing")
	}
	<-out
	select {
	case _, ok := <-out:
		if !ok {
			t.Fatal("transport closed caller channel")
		}
	default:
	}
}
