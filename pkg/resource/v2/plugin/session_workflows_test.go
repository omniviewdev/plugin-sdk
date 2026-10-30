package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

type observedSession struct {
	session      resourcev2.Session
	inconsistent bool
}
type workflowContextProvider struct {
	browserProvider

	mu   sync.Mutex
	seen map[string]observedSession
}

func (p *workflowContextProvider) GetActions(
	context.Context,
	resourcev2.AccessRoute,
	string,
) ([]resourcev2.ActionDescriptor, error) {
	return []resourcev2.ActionDescriptor{{ID: "test", Scope: resourcev2.ActionScopeInstance, Streaming: true}}, nil
}

func (p *workflowContextProvider) observe(ctx context.Context, method string) {
	s, _ := resourcev2.SessionFromContext(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen == nil {
		p.seen = make(map[string]observedSession)
	}
	observation, exists := p.seen[method]
	if !exists {
		observation.session = s
	} else if observation.session.RequestID != s.RequestID || observation.session.RequesterID != s.RequesterID {
		observation.inconsistent = true
	}
	p.seen[method] = observation
}

func TestWorkflowObservationDoesNotAssumeRPCCount(t *testing.T) {
	p := &workflowContextProvider{}
	ctx := resourcev2.WithSession(
		context.Background(),
		resourcev2.Session{RequestID: "request", RequesterID: "requester"},
	)
	for i := 0; i < 64; i++ {
		p.observe(ctx, "catalog")
	}
	p.observe(
		resourcev2.WithSession(
			context.Background(),
			resourcev2.Session{RequestID: "changed", RequesterID: "requester"},
		),
		"catalog",
	)
	p.observe(ctx, "catalog")
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) != 1 || !p.seen["catalog"].inconsistent {
		t.Fatal("repeated observations must retain diagnostic inconsistency without a call-count budget")
	}
}

func (p *workflowContextProvider) Catalog(
	ctx context.Context,
	in resourcev2.CatalogRequest,
) (resourcev2.Catalog, error) {
	p.observe(ctx, "catalog")
	c, err := p.qualifiedProvider.Catalog(ctx, in)
	c.Types[0].Capabilities = append(
		c.Types[0].Capabilities,
		resourcev2.Capability{Operation: resourcev2.OperationAction, Scope: resourcev2.QueryScope{}},
	)
	return c, err
}

func (p *workflowContextProvider) ListConnections(ctx context.Context) ([]types.Connection, error) {
	p.observe(ctx, "browser")
	return []types.Connection{}, nil
}

func (p *workflowContextProvider) WatchConnections(ctx context.Context, _ chan<- []types.Connection) error {
	p.observe(ctx, "connectionStream")
	return nil
}

func (p *workflowContextProvider) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	p.observe(ctx, "lifecycle")
	return []types.Connection{}, nil
}

func (p *workflowContextProvider) GetConnection(context.Context, string) (types.Connection, error) {
	return types.Connection{}, nil
}

func (p *workflowContextProvider) StartConnection(context.Context, string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	return out, nil
}

func (p *workflowContextProvider) StopConnection(context.Context, string) (types.Connection, error) {
	return types.Connection{}, nil
}

func (p *workflowContextProvider) CheckConnection(context.Context, string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	return out, nil
}

func (p *workflowContextProvider) StreamAction(
	ctx context.Context,
	_ resourcev2.ActionRequest,
	out chan<- resourcev2.ActionEvent,
) error {
	p.observe(ctx, "actionStream")
	select {
	case out <- resourcev2.ActionEvent{Type: "ready"}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *workflowContextProvider) GetRelationships(
	context.Context,
	resourcev2.AccessRoute,
	string,
) ([]resourcev2.RelationshipDescriptor, error) {
	return []resourcev2.RelationshipDescriptor{testRelationship()}, nil
}

func (p *workflowContextProvider) ResolveRelationships(
	ctx context.Context,
	_ resourcev2.RelationshipResolutionRequest,
) ([]resourcev2.ResolvedRelationship, error) {
	p.observe(ctx, "relationships")
	return relationshipFixtureResults(), nil
}

func TestDiagnosticsCrossAncillaryLifecycleAndActionStreams(t *testing.T) {
	p := &workflowContextProvider{}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx := resourcev2.WithSession(
		context.Background(),
		resourcev2.Session{RequestID: "workflow-request", RequesterID: "diagnostic-requester"},
	)
	ctx = acceptedRelationshipContext(ctx, t, c, relationshipFixtureRequest())
	if _, err := c.ListConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadConnections(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.WatchConnections(ctx, make(chan []types.Connection, 1)); err != nil {
		t.Fatal(err)
	}
	if err := c.StreamAction(ctx, actionInput(), make(chan resourcev2.ActionEvent, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveRelationships(ctx, relationshipFixtureRequest()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for method, observation := range p.seen {
		if observation.inconsistent || observation.session.RequestID != "workflow-request" ||
			observation.session.RequesterID != "diagnostic-requester" {
			t.Fatalf("%s lost diagnostics", method)
		}
	}
	for _, method := range []string{"browser", "lifecycle", "connectionStream", "actionStream", "relationships"} {
		if _, found := p.seen[method]; !found {
			t.Fatalf("missing %s invocation", method)
		}
	}
}

type updateContextProvider struct {
	updateProvider

	seen chan resourcev2.Session
}

func (p *updateContextProvider) ListenForEvents(
	ctx context.Context,
	sink resourcev2.WatchEventSink,
	ready func() error,
) error {
	s, _ := resourcev2.SessionFromContext(ctx)
	p.seen <- s
	return p.updateProvider.ListenForEvents(ctx, sink, ready)
}

func TestDiagnosticsCrossAdvisoryListener(t *testing.T) {
	p := &updateContextProvider{
		updateProvider: updateProvider{trigger: make(chan struct{}, 1)},
		seen:           make(chan resourcev2.Session, 1),
	}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx, cancel := context.WithCancel(
		resourcev2.WithSession(
			context.Background(),
			resourcev2.Session{RequestID: "updates-request", RequesterID: "not-a-principal"},
		),
	)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.ListenForEvents(ctx, &observedSink{}, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listener readiness timeout")
	}
	v := <-p.seen
	if v.RequestID != "updates-request" || v.RequesterID != "not-a-principal" {
		t.Fatal("update listener lost diagnostics")
	}
	cancel()
	select {
	case err := <-done:
		if testErrorClass(err) != "CANCELLED" {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener cancellation timeout")
	}
}
