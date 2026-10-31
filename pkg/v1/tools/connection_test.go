package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

func toolRegistry(t *testing.T, destroy func(context.Context, *string) error) *resourcev2.Controller[string] {
	t.Helper()
	if destroy == nil {
		destroy = func(context.Context, *string) error { return nil }
	}
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			t.Error("opaque tool must not request a catalog pin")
			return resourcev2.Catalog{}, errors.New("unexpected catalog call")
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{
					{ID: "conn-1", Data: map[string]any{"endpoint": "configured"}},
					{ID: "unstarted"},
				}, nil
			},
			CreateClient: func(_ context.Context, c types.Connection) (*string, error) {
				endpoint, _ := c.Data["endpoint"].(string)
				return &endpoint, nil
			},
			DestroyClient: destroy,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, controller.Close()) })
	_, err = controller.LoadConnections(context.Background())
	require.NoError(t, err)
	_, err = controller.StartConnection(context.Background(), "conn-1")
	require.NoError(t, err)
	return controller
}

func TestToolNativeAdmission(t *testing.T) {
	controller := toolRegistry(t, nil)
	calls := 0
	tool := validTool("scoped", ScopeConnection)
	tool.Handler = func(pc *types.PluginContext, _ json.RawMessage) (*Result, error) {
		calls++
		require.Equal(t, "configured", pc.Connection.Data["endpoint"])
		err := controller.WithClient(
			pc.Context,
			resourcev2.AccessRoute{Connection: pc.Connection.ID},
			func(_ context.Context, client *string) error {
				require.Equal(t, "configured", *client)
				return nil
			},
		)
		return TextResult("native"), err
	}
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool, validTool("plain", ScopePlugin)}})
	require.NoError(t, err)
	(&Plugin{Impl: manager}).BindConnectionContextProvider(controller)
	client := newWireClient(t, manager)
	for _, wire := range []bool{false, true} {
		call := manager.CallTool
		if wire {
			call = client.CallTool
		}
		for _, tc := range []struct {
			name       string
			input      CallInput
			connection *types.Connection
			ok         bool
		}{
			{"body only", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "conn-1"}}, nil, true},
			{"forged profile", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "conn-1"}}, &types.Connection{ID: "conn-1", Data: map[string]any{"endpoint": "forged"}}, true},
			{"missing", CallInput{Name: "scoped"}, &types.Connection{ID: "conn-1"}, false},
			{"empty route", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{}}, nil, false},
			{"conflict", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "conn-1"}}, &types.Connection{ID: "other"}, false},
			{"empty selector", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "conn-1"}}, &types.Connection{}, false},
			{"unknown", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "unknown"}}, nil, false},
			{"unstarted", CallInput{Name: "scoped", Route: &resourcev2.AccessRoute{Connection: "unstarted"}}, nil, false},
			{"plugin route", CallInput{Name: "plain", Route: &resourcev2.AccessRoute{Connection: "conn-1"}}, nil, false},
			{"plugin metadata", CallInput{Name: "plain"}, &types.Connection{ID: "conn-1"}, false},
		} {
			t.Run(tc.name+map[bool]string{false: "/direct", true: "/grpc"}[wire], func(t *testing.T) {
				before := calls
				tc.input.Arguments = json.RawMessage(`{}`)
				pc := types.NewPluginContextWithConnection(context.Background(), "tools-test", nil, nil, tc.connection)
				result, callErr := call(pc, tc.input)
				if tc.ok {
					require.NoError(t, callErr)
					require.Equal(t, "native", result.Content[0].Text)
					require.Equal(t, before+1, calls)
				} else {
					require.Error(t, callErr)
					require.Nil(t, result)
					require.Equal(t, before, calls)
				}
			})
		}
	}
	// Raw RPC bypasses client validation and proves present-empty routes reach server admission.
	_, err = client.client.CallTool(
		context.Background(),
		&toolspb.CallToolRequest{
			ContractVersion: ContractVersion,
			Arguments:       []byte(`{}`),
			Name:            "scoped",
			Route:           &resourcepb.AccessRoute{},
		},
	)
	require.Error(t, err)
	_, err = client.client.CallTool(
		metadata.NewOutgoingContext(context.Background(), metadata.Pairs("omniview-connection-id", "")),
		&toolspb.CallToolRequest{
			ContractVersion: ContractVersion,
			Arguments:       []byte(`{}`),
			Name:            "scoped",
			Route:           &resourcepb.AccessRoute{Connection: "conn-1"},
		},
	)
	require.Error(t, err)
	unbound, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool, validTool("plain", ScopePlugin)}})
	require.NoError(t, err)
	result, err := unbound.CallTool(
		nil,
		CallInput{
			Name:      "scoped",
			Arguments: json.RawMessage(`{}`),
			Route:     &resourcev2.AccessRoute{Connection: "conn-1"},
		},
	)
	require.Error(t, err)
	require.Nil(t, result)
	_, err = unbound.CallTool(nil, CallInput{Name: "plain", Arguments: json.RawMessage(`{}`)})
	require.NoError(t, err)
}

func TestToolLeaseRejectsLateSuccess(t *testing.T) {
	for _, retire := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller cancellation", true: "generation retirement"}[retire], func(t *testing.T) {
			destroyed := make(chan struct{})
			controller := toolRegistry(t, func(context.Context, *string) error { close(destroyed); return nil })
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseHandler := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseHandler)
			tool := validTool("scoped", ScopeConnection)
			tool.Handler = func(pc *types.PluginContext, _ json.RawMessage) (*Result, error) {
				close(entered)
				<-pc.Context.Done()
				close(canceled)
				<-release
				return TextResult("must not escape"), nil
			}
			manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
			require.NoError(t, err)
			manager.BindConnectionContextProvider(controller)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result *Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := manager.CallTool(
					&types.PluginContext{Context: ctx},
					CallInput{
						Name:      "scoped",
						Arguments: json.RawMessage(`{}`),
						Route:     &resourcev2.AccessRoute{Connection: "conn-1"},
					},
				)
				done <- outcome{result, err}
			}()
			awaitToolSignal(t, entered)
			stopped := make(chan error, 1)
			if retire {
				go func() { _, err := controller.StopConnection(context.Background(), "conn-1"); stopped <- err }()
			} else {
				cancel()
			}
			awaitToolSignal(t, canceled)
			select {
			case <-destroyed:
				t.Fatal("native client destroyed before handler released")
			default:
			}
			releaseHandler()
			select {
			case got := <-done:
				require.Nil(t, got.result)
				require.Error(t, got.err)
			case <-time.After(5 * time.Second):
				t.Fatal("call did not finish")
			}
			if retire {
				select {
				case err := <-stopped:
					require.NoError(t, err)
				case <-time.After(5 * time.Second):
					t.Fatal("retirement did not finish")
				}
				awaitToolSignal(t, destroyed)
			}
		})
	}
}

func TestToolHandlerErrorDiscardsResult(t *testing.T) {
	controller := toolRegistry(t, nil)
	for _, scope := range []Scope{ScopePlugin, ScopeConnection} {
		tool := validTool("failure", scope)
		failure := errors.New("handler failed")
		tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) {
			return TextResult("must not escape"), failure
		}
		manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.NoError(t, err)
		manager.BindConnectionContextProvider(controller)
		input := CallInput{Name: "failure", Arguments: json.RawMessage(`{}`)}
		if scope == ScopeConnection {
			input.Route = &resourcev2.AccessRoute{Connection: "conn-1"}
		}
		result, err := manager.CallTool(nil, input)
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Equal(t, ErrorExecutionFailed, result.ErrorCode)
		require.NotContains(t, result.Content[0].Text, failure.Error())
	}
}

func TestRawToolRPCWithoutMetadataPreservesLifetime(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		cancelNow  bool
		wantCode   codes.Code
	}{
		{
			name:       "cancellation",
			newContext: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			cancelNow:  true,
			wantCode:   codes.Canceled,
		},
		{
			name: "deadline",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 2*time.Second)
			},
			wantCode: codes.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := startRawToolLifetime(t, tc.newContext)
			defer probe.cancel()
			awaitToolSignal(t, probe.entered)
			if tc.cancelNow {
				probe.cancel()
			}
			// Observe RPC cancellation before retirement can independently cancel the lease.
			awaitToolSignal(t, probe.canceled)
			select {
			case err := <-probe.done:
				require.Equal(t, tc.wantCode, status.Code(err))
			case <-time.After(5 * time.Second):
				t.Fatal("raw RPC did not finish")
			}
			stopped := make(chan error, 1)
			go func() {
				_, err := probe.controller.StopConnection(context.Background(), "conn-1")
				stopped <- err
			}()
			select {
			case <-probe.destroyed:
				t.Fatal("native client destroyed while handler holds lease")
			default:
			}
			close(probe.release)
			select {
			case err := <-stopped:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("raw RPC did not release native lease")
			}
			awaitToolSignal(t, probe.destroyed)
		})
	}
}

type rawToolLifetime struct {
	controller *resourcev2.Controller[string]
	cancel     context.CancelFunc
	entered    chan struct{}
	canceled   chan struct{}
	release    chan struct{}
	destroyed  chan struct{}
	done       chan error
}

func startRawToolLifetime(
	t *testing.T,
	newContext func() (context.Context, context.CancelFunc),
) *rawToolLifetime {
	t.Helper()
	probe := &rawToolLifetime{
		entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
		destroyed: make(chan struct{}), done: make(chan error, 1),
	}
	probe.controller = toolRegistry(t, func(context.Context, *string) error {
		close(probe.destroyed)
		return nil
	})
	t.Cleanup(func() {
		select {
		case <-probe.release:
		default:
			close(probe.release)
		}
	})
	tool := validTool("scoped", ScopeConnection)
	tool.Handler = func(pc *types.PluginContext, _ json.RawMessage) (*Result, error) {
		require.Empty(t, pc.RequestID)
		require.Equal(t, "configured", pc.Connection.Data["endpoint"])
		close(probe.entered)
		select {
		case <-pc.Context.Done():
			close(probe.canceled)
		case <-probe.release:
			return nil, errors.New("handler released before RPC cancellation")
		}
		<-probe.release
		return TextResult("late native success"), nil
	}
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	manager.BindConnectionContextProvider(probe.controller)
	client := newWireClient(t, manager)
	ctx, cancel := newContext()
	probe.cancel = cancel
	go func() {
		// Bypass PluginClient: no PluginContext or request-ID metadata.
		result, callErr := client.client.CallTool(ctx, &toolspb.CallToolRequest{
			ContractVersion: ContractVersion, Arguments: []byte(`{}`),
			Name: "scoped", Route: &resourcepb.AccessRoute{Connection: "conn-1"},
		})
		if result != nil {
			probe.done <- errors.New("canceled RPC accepted a result")
			return
		}
		probe.done <- callErr
	}()
	return probe
}

func awaitToolSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("native lifecycle signal missing")
	}
}
