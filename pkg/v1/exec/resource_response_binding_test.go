package exec

import (
	"context"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type changedResourceSessionProvider struct {
	Provider
	id string
}

func (p *changedResourceSessionProvider) CreateResourceSession(*types.PluginContext, SessionOptions) (*Session, error) {
	return &Session{ID: p.id, CreatedAt: time.Now().UTC()}, nil
}

type changedResourceSessionServer struct {
	execpb.UnimplementedExecPluginServer
	id string
}

func (p *changedResourceSessionServer) CreateResourceSession(context.Context, *execpb.SessionOptions) (*execpb.ResourceSessionResponse, error) {
	return &execpb.ResourceSessionResponse{ResourceWireVersion: ResourceSessionWireVersion, Session: &execpb.Session{Id: p.id, CreatedAt: timestamppb.Now()}}, nil
}

func TestResourceExecActualGRPCResponseIDBinding(t *testing.T) {
	cases := []struct {
		name, requested, returned string
		allowed                   bool
	}{
		{"same-explicit", "requested-session", "requested-session", true},
		{"changed-explicit", "requested-session", "different-session", false},
		{"empty-explicit", "requested-session", "", false},
		{"generated", "", "native-generated-session", true},
		{"missing-generated", "", "", false},
	}
	for _, boundary := range []string{"sdk-server", "malicious-server"} {
		for _, tc := range cases {
			t.Run(boundary+"/"+tc.name, func(t *testing.T) {
				server := grpc.NewServer()
				if boundary == "sdk-server" {
					RegisterExecPluginServer(server, &PluginServer{Impl: &changedResourceSessionProvider{id: tc.returned}})
				} else {
					// Generated registration intentionally bypasses SDK server checks. The
					// client must bind the actual peer response independently.
					execpb.RegisterExecPluginServer(server, &changedResourceSessionServer{id: tc.returned})
				}
				client := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, server))}
				session, err := client.CreateSession(&types.PluginContext{}, SessionOptions{ID: tc.requested, ResourcePlugin: "provider", ResourceKey: "opaque-type"})
				if tc.allowed {
					if err != nil || session == nil || session.ID != tc.returned {
						t.Fatalf("valid creation identity rejected: %+v %v", session, err)
					}
				} else if session != nil || resourcev2.ErrorCode(err) != "PROVIDER_ERROR" {
					t.Fatalf("unbound session identity exposed: %+v %v", session, err)
				}
			})
		}
	}
}
