package exec_test

import (
	"context"
	"net"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

const (
	legacyService = "omniview.sdk.exec.v1.ExecPlugin"
	v2Service     = "omniview.sdk.exec.v2.Exec"
)

func legacyManager() exec.Provider {
	return exec.NewManager(exec.ManagerConfig{
		Logger:   logging.NewNop(),
		Handlers: map[string]exec.Handler{"k8s/pods": {Plugin: "k8s", Resource: "pods"}},
	})
}

// newTargetPlugin builds a Plugin that serves targets on the v2 lane.
func newTargetPlugin(t *testing.T, targets ...exec.Target) *exec.Plugin {
	t.Helper()
	p, err := exec.NewTargetPlugin(targets)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The services a plugin registers follow what it supplies: every plugin built before keeps exactly its legacy
// service; targets add the v2 service; a plugin that supplies only targets pays for one.
func TestGRPCServerRegistersTheServicesThePluginSupplies(t *testing.T) {
	target := exectest.NewFakeTarget()
	tests := []struct {
		name   string
		plugin func(t *testing.T) *exec.Plugin
		want   []string
	}{
		{"legacy only", func(*testing.T) *exec.Plugin { return &exec.Plugin{Impl: legacyManager()} }, []string{legacyService}},
		{"targets only", func(t *testing.T) *exec.Plugin { return newTargetPlugin(t, target) }, []string{v2Service}},
		{"both", func(t *testing.T) *exec.Plugin {
			p := newTargetPlugin(t, target)
			p.Impl = legacyManager()
			return p
		}, []string{legacyService, v2Service}},
		{"neither (a zero plugin, as before)", func(*testing.T) *exec.Plugin { return &exec.Plugin{} }, []string{legacyService}},
		{"no targets (a plugin built with none has no v2 lane)", func(t *testing.T) *exec.Plugin { return newTargetPlugin(t) }, []string{legacyService}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := grpc.NewServer()
			if err := tc.plugin(t).GRPCServer(nil, server); err != nil {
				t.Fatal(err)
			}
			got := server.GetServiceInfo()
			if len(got) != len(tc.want) {
				t.Errorf("services = %v, want %v", keys(got), tc.want)
			}
			for _, name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Errorf("service %s is not registered: %v", name, keys(got))
				}
			}
		})
	}
	t.Run("an invalid target is an error of the constructor, not a panic at serve time", func(t *testing.T) {
		bad := exec.Target{Info: exec.TargetInfo{Provider: "k8s", Type: "pods"}} // no Exec
		if p, err := exec.NewTargetPlugin([]exec.Target{bad}); err == nil || p != nil {
			t.Errorf("NewTargetPlugin accepted a target with no Exec: %+v, %v", p, err)
		}
	})
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A plugin built before the lane existed answers Unimplemented to every v2 method; the v2 client of a host that asks
// first says so with a typed error, and the legacy lane through the same client is untouched.
func TestAPluginThatServesOnlyTheLegacyLaneRefusesTheV2ClientWithAnUnsupportedError(t *testing.T) {
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Legacy: legacyManager()})
	client := server.Client(t)
	ctx := context.Background()

	if _, err := client.V2().Describe(ctx); err == nil {
		t.Fatal("Describe of a plugin that does not serve the lane succeeded")
	} else if got := asExecError(t, err); got.Class != exec.ClassUnsupported {
		t.Errorf("Describe: class = %s, want UNSUPPORTED", got.Class)
	}
	connections := exectestkit.NewConnectionsT(t, "k8s")
	if _, err := client.V2().Options(connections.Context(ctx), connections.TargetRef("pods", "p")); err == nil {
		t.Fatal("Options of a plugin that does not serve the lane succeeded")
	} else if got := asExecError(t, err); got.Class != exec.ClassUnsupported {
		t.Errorf("Options: class = %s, want UNSUPPORTED", got.Class)
	}
	if handlers := client.GetSupportedResources(nil); len(handlers) != 1 || handlers[0].ID() != "k8s/pods" {
		t.Errorf("the legacy lane through the same client: %+v", handlers)
	}
}

func TestAPluginThatServesBothLanesAnswersBoth(t *testing.T) {
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Legacy: legacyManager(), Targets: []exec.Target{exectest.NewFakeTarget()}})
	client := server.Client(t)
	if handlers := client.GetSupportedResources(nil); len(handlers) != 1 {
		t.Errorf("the legacy lane: %+v", handlers)
	}
	description, err := client.V2().Describe(context.Background())
	if err != nil || len(description.GetTargets()) != 1 {
		t.Errorf("the v2 lane: %v, %v", description, err)
	}
}

// A binding that arrives after the server was built (the SDK binds the connection registry when the resource
// capability is registered, which can come after exec) is used by the next call.
func TestTheConnectionRegistryMayBeBoundAfterTheServerIsBuilt(t *testing.T) {
	target := exectest.NewFakeTarget(exectest.FakeRouting("k8s", "pods"), exectest.FakeStaticOptions(podContainers()...))
	connections := exectestkit.NewConnectionsT(t, "k8s")
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}})
	ctx := connections.Context(context.Background())
	ref := connections.TargetRef("pods", "p")

	if _, err := server.V2(t).Options(ctx, ref); err == nil {
		t.Fatal("Options succeeded with no connection registry bound")
	}
	server.Plugin.BindConnectionContextProvider(connections)
	if _, err := server.V2(t).Options(ctx, ref); err != nil {
		t.Fatalf("Options after binding: %v", err)
	}
}

// Responses and frames are lenient: a field a reader does not know is ignored, and a value it does not know is not
// an error. A newer plugin must never break an older host. Describe hands the host the plugin's message as it is, so a
// value this build does not know stays the number the plugin sent, for the host to read as unspecified.
func TestResponsesAreLenientAndAnUnknownValueIsNotAnError(t *testing.T) {
	unknown := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)
	described := &execv2.DescribeResponse{
		ContractVersion: 7,
		Features:        []execv2.Feature{execv2.Feature(9999)},
		Targets: []*execv2.TargetInfo{{
			Provider: "k8s", Type: "pods", DisplayName: "Pod",
			Capabilities: &execv2.Capabilities{Kill: execv2.KillBehavior(9999), Stall: execv2.StallBehavior(9999), ExitReport: execv2.ExitReport(9999)},
		}},
	}
	described.ProtoReflect().SetUnknown(unknown)
	described.Targets[0].ProtoReflect().SetUnknown(unknown)
	described.Targets[0].Capabilities.ProtoReflect().SetUnknown(unknown)
	options := &execv2.OptionsResponse{Options: []*execv2.Option{{Name: "container", Choices: []*execv2.Choice{{Value: "app", State: execv2.ChoiceState(9999)}}}}}
	options.ProtoReflect().SetUnknown(unknown)
	options.Options[0].ProtoReflect().SetUnknown(unknown)

	client := dialFake(t, &fakeV2{describe: described, options: options})
	description, err := client.Describe(context.Background())
	if err != nil {
		t.Fatalf("a response with fields this build does not know was refused: %v", err)
	}
	if description.GetContractVersion() != 7 || !slices.Equal(description.GetFeatures(), []execv2.Feature{9999}) {
		t.Errorf("description = %v: the plugin's own message, with the feature this build does not know as it was sent", description)
	}
	caps := description.GetTargets()[0].GetCapabilities()
	if caps.GetKill() != 9999 || caps.GetStall() != 9999 || caps.GetExitReport() != 9999 {
		t.Errorf("unknown enum values were changed: %v", caps)
	}
	if caps.Tty != nil || caps.Signals != nil || caps.Integration != nil {
		t.Errorf("a field the plugin did not send is present: %v", caps)
	}
	result, err := client.Options(context.Background(), exectestkit.NewConnections("k8s").TargetRef("pods", "p"))
	if err != nil {
		t.Fatalf("an options response with fields this build does not know was refused: %v", err)
	}
	if result.Options[0].Choices[0].State != exec.ChoiceStateUnspecified {
		t.Errorf("an unknown state read as %v", result.Options[0].Choices[0].State)
	}
}

// A plugin that answers outside the contract is not trusted: its answer is an internal error, never partly used.
func TestAResponseThatBreaksTheContractIsNotUsed(t *testing.T) {
	client := dialFake(t, &fakeV2{
		describe: &execv2.DescribeResponse{Targets: []*execv2.TargetInfo{{Provider: "k8s"}}}, // no type
		options:  &execv2.OptionsResponse{Options: []*execv2.Option{{Name: "Not A Code"}}},
	})
	if _, err := client.Describe(context.Background()); err == nil {
		t.Error("Describe accepted a target with no type")
	} else if got := asExecError(t, err); got.Class != exec.ClassInternal {
		t.Errorf("Describe: class = %s", got.Class)
	}
	if _, err := client.Options(context.Background(), exectestkit.NewConnections("k8s").TargetRef("pods", "p")); err == nil {
		t.Error("Options accepted an option that is not a closed code")
	} else if got := asExecError(t, err); got.Class != exec.ClassInternal {
		t.Errorf("Options: class = %s", got.Class)
	}
}

// fakeV2 is a plugin that serves canned answers: what a newer or a faulty plugin would send.
type fakeV2 struct {
	execv2.UnimplementedExecServer
	describe *execv2.DescribeResponse
	options  *execv2.OptionsResponse
}

func (f *fakeV2) Describe(context.Context, *execv2.DescribeRequest) (*execv2.DescribeResponse, error) {
	return f.describe, nil
}

func (f *fakeV2) Options(context.Context, *execv2.OptionsRequest) (*execv2.OptionsResponse, error) {
	return f.options, nil
}

func dialFake(t *testing.T, service execv2.ExecServer) exec.Client {
	t.Helper()
	server := grpc.NewServer()
	execv2.RegisterExecServer(server, service)
	listener := bufconn.Listen(1 << 20)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	conn, err := grpc.NewClient("passthrough:///fake",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithChainUnaryInterceptor(sdk.ClientPluginContextInterceptor(nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	plugin, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.(*exec.PluginClient).V2()
}
