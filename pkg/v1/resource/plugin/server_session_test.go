package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/v1/resource"
)

// requireSession is a test helper that asserts a valid Session with the
// expected connection ID is present in the context.
func requireSession(t *testing.T, ctx context.Context, wantConnID string) {
	t.Helper()
	sess := resource.SessionFromContext(ctx)
	if sess == nil {
		t.Fatal("expected session in context, got nil")
	}
	if sess.Connection == nil {
		t.Fatal("expected session.Connection, got nil")
	}
	if sess.Connection.ID != wantConnID {
		t.Fatalf("connection ID = %q, want %q", sess.Connection.ID, wantConnID)
	}
}

// TestServer_SessionInjection verifies that server methods which receive a
// connection_id inject a Session into the context before calling the provider.
func TestServer_SessionInjection(t *testing.T) {
	const connID = "test-conn-1"
	const resKey = "core::v1::Pod"

	tests := []struct {
		name string
		call func(s resourcepb.ResourcePluginServer) error
		// setup returns a TestProvider option that records whether the session
		// was injected.
		setup func(called *bool) resourcetest.Option
	}{
		{
			name: "GetEditorSchemas",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetEditorSchemasFunc = func(ctx context.Context, cid string) ([]resource.EditorSchema, error) {
						requireSession(t, ctx, connID)
						*called = true
						return nil, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetEditorSchemas(context.Background(), &resourcepb.EditorSchemasRequest{ConnectionId: connID})
				return err
			},
		},
		{
			name: "GetResourceGroups",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetResourceGroupsFunc = func(ctx context.Context, cid string) map[string]resource.ResourceGroup {
						requireSession(t, ctx, connID)
						*called = true
						return nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetResourceGroups(context.Background(), &resourcepb.ResourceGroupsRequest{ConnectionId: connID})
				return err
			},
		},
		{
			name: "GetResourceTypes",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetResourceTypesFunc = func(ctx context.Context, cid string) map[string]resource.ResourceMeta {
						requireSession(t, ctx, connID)
						*called = true
						return nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetResourceTypes(context.Background(), &resourcepb.ResourceTypesRequest{ConnectionId: connID})
				return err
			},
		},
		{
			name: "GetFilterFields",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetFilterFieldsFunc = func(ctx context.Context, cid, key string) ([]resource.FilterField, error) {
						requireSession(t, ctx, connID)
						*called = true
						return nil, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetFilterFields(context.Background(), &resourcepb.FilterFieldsRequest{ConnectionId: connID, ResourceKey: resKey})
				return err
			},
		},
		{
			name: "GetResourceSchema",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetResourceSchemaFunc = func(ctx context.Context, cid, key string) (json.RawMessage, error) {
						requireSession(t, ctx, connID)
						*called = true
						return nil, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetResourceSchema(context.Background(), &resourcepb.ResourceSchemaRequest{ConnectionId: connID, ResourceKey: resKey})
				return err
			},
		},
		{
			name: "ResolveRelationships",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.ResolveRelationshipsFunc = func(ctx context.Context, cid, key, id, ns string) ([]resource.ResolvedRelationship, error) {
						requireSession(t, ctx, connID)
						*called = true
						return nil, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.ResolveRelationships(context.Background(), &resourcepb.ResolveRelationshipsRequest{
					ConnectionId: connID, ResourceKey: resKey, Id: "pod-1", Namespace: "default",
				})
				return err
			},
		},
		{
			name: "GetHealth",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetHealthFunc = func(ctx context.Context, cid, key string, data json.RawMessage) (*resource.ResourceHealth, error) {
						requireSession(t, ctx, connID)
						*called = true
						return &resource.ResourceHealth{}, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetHealth(context.Background(), &resourcepb.HealthRequest{
					ConnectionId: connID, ResourceKey: resKey, Data: []byte(`{}`),
				})
				return err
			},
		},
		{
			name: "GetResourceEvents",
			setup: func(called *bool) resourcetest.Option {
				return func(p *resourcetest.TestProvider) {
					p.GetResourceEventsFunc = func(ctx context.Context, cid, key, id, ns string, limit int32) ([]resource.ResourceEvent, error) {
						requireSession(t, ctx, connID)
						*called = true
						return nil, nil
					}
				}
			},
			call: func(s resourcepb.ResourcePluginServer) error {
				_, err := s.GetResourceEvents(context.Background(), &resourcepb.ResourceEventsRequest{
					ConnectionId: connID, ResourceKey: resKey, Id: "pod-1", Namespace: "default", Limit: 10,
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called bool
			tp := resourcetest.NewTestProvider(t, tt.setup(&called))
			srv := NewServer(tp, nil)
			if err := tt.call(srv); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Fatal(fmt.Sprintf("provider.%s was not called", tt.name))
			}
		})
	}
}
