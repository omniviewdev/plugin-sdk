package exec

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/interceptors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

type nativeCommandTransportFixture struct {
	resourceExecFixture
	planCalls int
	command   *NativeCommand
	failure   error
	seen      SessionOptions
}

func (p *nativeCommandTransportFixture) GetSupportedResources(*types.PluginContext) []Handler {
	return []Handler{
		{Plugin: "test", Resource: "pod", NativeCommandHandler: p.PlanNativeCommand},
		{Plugin: "test", Resource: "legacy"},
	}
}

func (p *nativeCommandTransportFixture) PlanNativeCommand(_ *types.PluginContext, opts SessionOptions) (*NativeCommand, error) {
	p.planCalls++
	p.seen = opts
	return p.command, p.failure
}

func nativeCommandTransportOptions() SessionOptions {
	return SessionOptions{
		Reference: &resourcev2.ResourceReference{
			Address: resourcev2.ResourceAddress{
				Provider: "test", Authority: "cluster", Type: "pod", ID: "opaque/id",
				Scope: []resourcev2.ScopeEntry{},
			},
		},
		Route: &resourcev2.AccessRoute{Connection: "configured"},
	}
}

func nativeCommandTransportPlan() *NativeCommand {
	return &NativeCommand{
		Executable: "/usr/local/bin/kubectl",
		Args:       []string{"exec", "opaque/id", "--", "sh", "-c", "printf '%s' '$literal;$(not-expanded)'", "", "space value"},
		Env:        map[string]string{"KUBECONFIG": "/Users/example/config with spaces", "EMPTY": ""},
		Cwd:        "/Users/example/project with spaces",
	}
}

func nativeCommandTransportClient(t *testing.T, provider Provider) *PluginClient {
	t.Helper()
	options := append(resourceplugin.ServerOptions(), grpc.ChainUnaryInterceptor(interceptors.UnaryPluginContext()))
	server := grpc.NewServer(options...)
	RegisterExecPluginServer(server, &PluginServer{Impl: provider})
	return &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, server))}
}

func TestNativeCommandActualGRPCRoundtripAndCapabilities(t *testing.T) {
	provider := &nativeCommandTransportFixture{command: nativeCommandTransportPlan()}
	client := nativeCommandTransportClient(t, provider)
	handlers := client.GetSupportedResources(&types.PluginContext{})
	if len(handlers) != 2 || !handlers[0].SupportsNativeCommand || handlers[1].SupportsNativeCommand {
		t.Fatalf("capabilities lost: %#v", handlers)
	}
	if handlers[0].NativeCommandHandler != nil {
		t.Fatal("callback crossed the wire")
	}
	if provider.planCalls != 0 {
		t.Fatal("capability discovery invoked the planner")
	}
	opts := nativeCommandTransportOptions()
	out, err := client.PlanNativeCommand(&types.PluginContext{}, opts)
	if err != nil || !reflect.DeepEqual(out, provider.command) {
		t.Fatalf("plan changed: %#v err=%v", out, err)
	}
	if provider.planCalls != 1 || provider.calls != 0 || provider.oldCalls != 0 {
		t.Fatalf("planning created a session: %#v", provider)
	}
	if !reflect.DeepEqual(provider.seen.Reference, opts.Reference) || !reflect.DeepEqual(provider.seen.Route, opts.Route) ||
		provider.seen.ResourcePlugin != "test" || provider.seen.ResourceKey != "pod" {
		t.Fatalf("resource target changed: %#v", provider.seen)
	}
}

func TestNativeCommandClientRequiresExplicitTargetBeforeRPC(t *testing.T) {
	for _, opts := range []SessionOptions{
		{},
		{ResourcePlugin: "test", ResourceKey: "pod"},
		{Route: &resourcev2.AccessRoute{Connection: "configured"}, ResourcePlugin: "test", ResourceKey: "pod"},
	} {
		// A nil transport proves validation runs before any remote call.
		client := &PluginClient{}
		out, err := client.PlanNativeCommand(&types.PluginContext{}, opts)
		if out != nil || resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
			t.Fatalf("targetless planning accepted: %#v %v", out, err)
		}
	}
}

func TestNativeCommandActualGRPCLegacyProvidersAndOldServersUnsupported(t *testing.T) {
	t.Run("legacy provider", func(t *testing.T) {
		provider := &resourceExecFixture{}
		client := nativeCommandTransportClient(t, provider)
		out, err := client.PlanNativeCommand(&types.PluginContext{}, nativeCommandTransportOptions())
		if out != nil || resourcev2.ErrorCode(err) != "UNSUPPORTED" || provider.calls+provider.oldCalls != 0 {
			t.Fatalf("legacy fallback: %#v %v calls=%d", out, err, provider.calls+provider.oldCalls)
		}
	})
	t.Run("old server", func(t *testing.T) {
		server := grpc.NewServer()
		old := &oldExecServer{}
		execpb.RegisterExecPluginServer(server, old)
		client := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, server))}
		out, err := client.PlanNativeCommand(&types.PluginContext{}, nativeCommandTransportOptions())
		if out != nil || resourcev2.ErrorCode(err) != "UNSUPPORTED" || old.calls != 0 {
			t.Fatalf("old server fallback: %#v %v calls=%d", out, err, old.calls)
		}
	})
}

func TestNativeCommandActualGRPCTypedErrorsAndInvalidPlans(t *testing.T) {
	for _, code := range []string{"UNSUPPORTED", "INVALID_REQUEST", "RESET_REQUIRED", "FORBIDDEN", "UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			provider := &nativeCommandTransportFixture{failure: fmt.Errorf("planner: %w", resourcev2.NewError(code, "private detail"))}
			out, err := nativeCommandTransportClient(t, provider).PlanNativeCommand(&types.PluginContext{}, nativeCommandTransportOptions())
			if out != nil || resourcev2.ErrorCode(err) != code || provider.planCalls != 1 {
				t.Fatalf("typed error lost: %#v %v calls=%d", out, err, provider.planCalls)
			}
		})
	}
	for name, plan := range map[string]*NativeCommand{
		"nil":              nil,
		"empty executable": {},
		"nul argument":     {Executable: "kubectl", Args: []string{"bad\x00arg"}},
		"invalid env":      {Executable: "kubectl", Env: map[string]string{"BAD=NAME": "value"}},
		"nul cwd":          {Executable: "kubectl", Cwd: "bad\x00cwd"},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &nativeCommandTransportFixture{command: plan}
			out, err := nativeCommandTransportClient(t, provider).PlanNativeCommand(&types.PluginContext{}, nativeCommandTransportOptions())
			if out != nil || resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid plan accepted: %#v %v", out, err)
			}
		})
	}
}

func TestNativeCommandActualGRPCConnectionMismatchRejectsBeforeHandler(t *testing.T) {
	calls, terminals := 0, 0
	manager := NewManager(ManagerConfig{
		Handlers: map[string]Handler{
			"test/pod": {Plugin: "test", Resource: "pod", NativeCommandHandler: func(*types.PluginContext, SessionOptions) (*NativeCommand, error) {
				calls++
				return nativeCommandTransportPlan(), nil
			}},
		},
		TerminalFactory: func() (Terminal, error) { terminals++; return nil, fmt.Errorf("unexpected PTY") },
	})
	t.Cleanup(manager.Close)
	client := nativeCommandTransportClient(t, manager)
	out, err := client.PlanNativeCommand(&types.PluginContext{
		Context: context.Background(), RequestID: "native-mismatch", Connection: &types.Connection{ID: "other"},
	}, nativeCommandTransportOptions())
	if out != nil || resourcev2.ErrorCode(err) != "INVALID_REQUEST" || calls != 0 || terminals != 0 {
		t.Fatalf("mismatch reached handler: %#v %v handler=%d terminals=%d", out, err, calls, terminals)
	}
	if sessions, err := manager.ListSessions(nil); err != nil || len(sessions) != 0 {
		t.Fatalf("planning created sessions: %#v %v", sessions, err)
	}
}

func TestNativeCommandRawAdmissionRejectsMalformedRequest(t *testing.T) {
	opts := nativeCommandTransportOptions()
	request, err := opts.ToProto()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	entry := append(execBytes(1, []byte("key")), execBytes(2, []byte("value"))...)
	cases := []struct {
		name, code string
		data       []byte
	}{
		{"version absent", "UNSUPPORTED", nil},
		{"version duplicate", "INVALID_REQUEST", append(append([]byte{}, valid...), 80, 2)},
		{"map duplicate", "INVALID_REQUEST", append(append(append([]byte{}, valid...), execBytes(4, entry)...), execBytes(4, entry)...)},
		{"unknown", "UNSUPPORTED", append(append([]byte{}, valid...), execBytes(20, nil)...)},
		{"reserved", "UNSUPPORTED", append(append([]byte{}, valid...), execBytes(8, nil)...)},
		{"invalid utf8", "INVALID_REQUEST", append(append([]byte{}, valid...), execBytes(1, []byte{255})...)},
		{"missing reference", "INVALID_REQUEST", append([]byte{80, 2}, execBytes(6, []byte("test"))...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &nativeCommandTransportFixture{command: nativeCommandTransportPlan()}
			server := grpc.NewServer(resourceplugin.ServerOptions()...)
			RegisterExecPluginServer(server, &PluginServer{Impl: provider})
			var out execpb.PlanNativeCommandResponse
			err := execPipe(t, server).Invoke(
				context.Background(), "/"+execpb.ExecPlugin_ServiceDesc.ServiceName+"/PlanNativeCommand",
				&execpb.SessionOptions{}, &out, grpc.ForceCodecV2(execRawCodec{data: tc.data}),
			)
			if resourcev2.ErrorCode(resourceplugin.ResourceDomainError(err)) != tc.code || provider.planCalls != 0 {
				t.Fatalf("bad request admitted: %v calls=%d", err, provider.planCalls)
			}
		})
	}
}

func TestNativeCommandRawResponsesFailClosed(t *testing.T) {
	command := execBytes(1, []byte("kubectl"))
	entry := append(execBytes(1, []byte("KUBECONFIG")), execBytes(2, []byte("local-config"))...)
	duplicateEnv := append(append(append([]byte{}, command...), execBytes(3, entry)...), execBytes(3, entry)...)
	cases := map[string][]byte{
		"missing version":       execBytes(2, command),
		"duplicate version":     append([]byte{8, 2, 8, 2}, execBytes(2, command)...),
		"missing command":       {8, 2},
		"duplicate env":         append([]byte{8, 2}, execBytes(2, duplicateEnv)...),
		"invalid utf8":          append([]byte{8, 2}, execBytes(2, execBytes(1, []byte{255}))...),
		"unknown command field": append([]byte{8, 2}, execBytes(2, append(append([]byte{}, command...), execBytes(8, nil)...))...),
		"empty executable":      append([]byte{8, 2}, execBytes(2, nil)...),
		"nul argument":          append([]byte{8, 2}, execBytes(2, append(append([]byte{}, command...), execBytes(2, []byte{0})...))...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &nativeCommandTransportFixture{command: nativeCommandTransportPlan()}
			server := grpc.NewServer(grpc.ForceServerCodecV2(execRawCodec{data: data}))
			RegisterExecPluginServer(server, &PluginServer{Impl: provider})
			client := &PluginClient{client: execpb.NewExecPluginClient(execPipe(t, server))}
			out, err := client.PlanNativeCommand(&types.PluginContext{}, nativeCommandTransportOptions())
			if err == nil || out != nil {
				t.Fatalf("bad response materialized: %#v %v", out, err)
			}
		})
	}
}
