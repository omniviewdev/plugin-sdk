package plugin

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

type updateProvider struct {
	qualifiedProvider

	mode    string
	trigger chan struct{}
	joined  chan struct{}
	starts  atomic.Int32
}

func (p *updateProvider) Catalog(ctx context.Context, in resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	c, err := p.qualifiedProvider.Catalog(ctx, in)
	c.Types[0].Capabilities = append(
		c.Types[0].Capabilities,
		resourcev2.Capability{
			Operation: resourcev2.OperationUpdate,
			Scope:     resourcev2.QueryScope{},
			Guarantee: resourcev2.BestEffortGuarantee,
		},
	)
	return c, err
}

func (p *updateProvider) StartConnectionWatch(context.Context, []resourcev2.ReadTarget) error {
	p.starts.Add(1)
	return nil
}

func (p *updateProvider) StopConnectionWatch(context.Context, resourcev2.AccessRoute) error {
	return nil
}
func (p *updateProvider) HasWatch(context.Context, resourcev2.AccessRoute) bool { return true }

//nolint:nilnil // A missing watch summary is a legitimate successful result, distinct from an empty summary.
func (p *updateProvider) GetWatchState(
	context.Context,
	resourcev2.AccessRoute,
) (*resourcev2.WatchConnectionSummary, error) {
	return nil, nil
}

func (p *updateProvider) EnsureResourceWatch(ctx context.Context, _ resourcev2.ReadTarget) error {
	select {
	case p.trigger <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *updateProvider) StopResourceWatch(context.Context, resourcev2.AccessRoute, string) error {
	return nil
}

func (p *updateProvider) RestartResourceWatch(context.Context, resourcev2.ReadTarget) error {
	return nil
}

func (p *updateProvider) IsResourceWatchRunning(context.Context, resourcev2.AccessRoute, string) (bool, error) {
	return true, nil
}

func (p *updateProvider) ListenForEvents(
	ctx context.Context,
	sink resourcev2.WatchEventSink,
	ready func() error,
) error {
	if p.joined != nil {
		defer close(p.joined)
	}
	if p.mode == "noReady" {
		return nil
	}
	if err := ready(); err != nil {
		return err
	}
	if p.mode == "duplicate" {
		return ready()
	}
	select {
	case <-p.trigger:
	case <-ctx.Done():
		return ctx.Err()
	}
	record, err := p.qualifiedProvider.Get(ctx, testGet())
	if err != nil {
		return err
	}
	sink.OnUpdate(
		resourcev2.WatchUpdatePayload{
			Route:     testGet().Route,
			Record:    record,
			Guarantee: resourcev2.BestEffortGuarantee,
		},
	)
	<-ctx.Done()
	return ctx.Err()
}

type observedSink struct {
	updates chan resourcev2.WatchUpdatePayload
}

func (*observedSink) OnAdd(resourcev2.WatchAddPayload)           {}
func (s *observedSink) OnUpdate(v resourcev2.WatchUpdatePayload) { s.updates <- v }
func (*observedSink) OnDelete(resourcev2.WatchDeletePayload)     {}
func (*observedSink) OnStateChange(resourcev2.WatchStateEvent)   {}
func updateTarget() resourcev2.ReadTarget {
	r := testGet()
	return resourcev2.ReadTarget{
		Route:          r.Route,
		Provider:       r.Reference.Address.Provider,
		Authority:      r.Reference.Address.Authority,
		Type:           r.Reference.Address.Type,
		Scope:          resourcev2.QueryScope{},
		Representation: r.Representation,
	}
}

func TestActualGRPCBestEffortReadinessAndCancel(t *testing.T) {
	p := &updateProvider{trigger: make(chan struct{}, 1), joined: make(chan struct{})}
	s := grpc.NewServer()
	RegisterServer(s, p)
	c := NewClient(pipe(t, s))
	ctx, cancel := context.WithCancel(acceptedCatalogContext(context.Background(), t, c, updateTarget().Route))
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	sink := &observedSink{updates: make(chan resourcev2.WatchUpdatePayload, 1)}
	go func() { done <- c.ListenForEvents(ctx, sink, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listener readiness timeout")
	}
	if err := c.EnsureResourceWatch(ctx, updateTarget()); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-sink.updates:
		if event.Guarantee != resourcev2.BestEffortGuarantee ||
			event.Record.Reference.Address.Authority != "authority" ||
			event.Record.Reference.Address.ID != "opaque/id" {
			t.Fatal("canonical advisory update lost")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("update timeout")
	}
	cancel()
	select {
	case err := <-done:
		if testErrorClass(err) != "CANCELLED" {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not stop")
	}
	select {
	case <-p.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not retire sink")
	}
}

func TestActualGRPCUpdateReadinessFailures(t *testing.T) {
	for _, mode := range []string{"noReady", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			p := &updateProvider{mode: mode}
			s := grpc.NewServer()
			RegisterServer(s, p)
			c := NewClient(pipe(t, s))
			err := c.ListenForEvents(context.Background(), &observedSink{}, func() error { return nil })
			if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("readiness class: %v", err)
			}
		})
	}
}

type depthUpdateServer struct {
	resourcepb.UnimplementedResourceUpdatesServer
	update *resourcepb.WatchUpdatePayload
}

func (s *depthUpdateServer) Listen(_ *resourcepb.UpdateRequest, stream resourcepb.ResourceUpdates_ListenServer) error {
	if err := stream.Send(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion,
		Event:   &resourcepb.UpdateStreamEvent_Ready{Ready: true},
	}); err != nil {
		return err
	}
	return stream.Send(&resourcepb.UpdateStreamEvent{
		Version: resourcev2.WireVersion,
		Event:   &resourcepb.UpdateStreamEvent_Update{Update: s.update},
	})
}

func TestActualGRPCUpdateValueDepth(t *testing.T) {
	for _, depth := range []int{21, resourcev2.MaxValueDepth - 1, resourcev2.MaxValueDepth, resourcev2.MaxValueDepth + 1} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			value := resourcev2.Value{Kind: "bool"}
			wireValue := &resourcepb.Value{Selection: &resourcepb.Value_BoolValue{}}
			for range depth {
				value = resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{"x": value}}
				wireValue = &resourcepb.Value{Selection: &resourcepb.Value_MapValue{
					MapValue: &resourcepb.MapValue{Entries: []*resourcepb.ValueEntry{{Key: "x", Value: wireValue}}},
				}}
			}
			record, err := (&qualifiedProvider{}).Get(context.Background(), testGet())
			if err != nil {
				t.Fatal(err)
			}
			update, err := resourcev2.ToProtoWatchUpdatePayload(resourcev2.WatchUpdatePayload{
				Route: testGet().Route, Record: record, Guarantee: resourcev2.BestEffortGuarantee,
			})
			if err != nil {
				t.Fatal(err)
			}
			update.Record.Body = wireValue
			event := &resourcepb.UpdateStreamEvent{
				Version: resourcev2.WireVersion,
				Event:   &resourcepb.UpdateStreamEvent_Update{Update: update},
			}
			data, err := proto.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err = ValidateWire(data, event.ProtoReflect().Descriptor()); err != nil {
				t.Fatalf("wire rejected depth %d: %v", depth, err)
			}
			server := grpc.NewServer()
			resourcepb.RegisterResourceUpdatesServer(server, &depthUpdateServer{update: update})
			client := NewClient(pipe(t, server))
			sink := &observedSink{updates: make(chan resourcev2.WatchUpdatePayload, 1)}
			err = client.ListenForEvents(context.Background(), sink, func() error { return nil })
			if depth > resourcev2.MaxValueDepth {
				if resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" || !strings.Contains(err.Error(), "value complexity limit") {
					t.Fatalf("expected semantic depth rejection, got %v", err)
				}
				if len(sink.updates) != 0 {
					t.Fatal("delivered over-depth update")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-sink.updates:
				if !reflect.DeepEqual(got.Record.Body, value) {
					t.Fatal("update body changed in transit")
				}
			default:
				t.Fatal("valid update was not delivered")
			}
		})
	}
}
