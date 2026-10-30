package settings

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	settingspb "github.com/omniviewdev/plugin-sdk/proto/v1/settings"
)

type settingValueWireServer struct {
	settingspb.UnimplementedSettingsPluginServer

	values map[string]*anypb.Any
}

func (s *settingValueWireServer) GetSettingValue(
	_ context.Context,
	request *settingspb.GetSettingValueRequest,
) (*settingspb.GetSettingValueResponse, error) {
	value, ok := s.values[request.GetId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "setting does not exist")
	}
	return &settingspb.GetSettingValueResponse{Value: value}, nil
}

func TestClientGetSettingValueRPCDecodesValuesAndErrors(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  any
	}{
		{name: "integer", value: 23, want: float64(23)},
		{name: "string", value: "ready", want: "ready"},
		{name: "empty string", value: "", want: ""},
		{name: "boolean", value: false, want: false},
		{
			name:  "structured",
			value: map[string]any{"limit": 7, "names": []string{"a", "b"}},
			want:  map[string]any{"limit": float64(7), "names": []any{"a", "b"}},
		},
		{name: "null", value: nil, want: nil},
	}
	values := make(map[string]*anypb.Any, len(cases)+3)
	for _, tc := range cases {
		encoded, err := ConvertInterfaceToAny(tc.value)
		require.NoError(t, err)
		values[tc.name] = encoded
	}
	invalidJSON, err := anypb.New(wrapperspb.Bytes([]byte("{")))
	require.NoError(t, err)
	wrongEnvelope, err := anypb.New(wrapperspb.String("not a settings envelope"))
	require.NoError(t, err)
	values["invalid JSON"] = invalidJSON
	values["wrong envelope"] = wrongEnvelope
	values["missing envelope"] = nil

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	settingspb.RegisterSettingsPluginServer(server, &settingValueWireServer{values: values})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			t.Errorf("owned settings RPC server: %v", serveErr)
		}
	})
	connection, err := grpc.NewClient(
		"passthrough:///settings-contract",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	waitForSettingsConnection(t, connection)
	client := &Client{client: settingspb.NewSettingsPluginClient(connection)}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := client.GetSettingValue(tc.name)
			require.NoError(t, err)
			require.Equal(t, tc.want, value)
		})
	}
	for _, name := range []string{"invalid JSON", "wrong envelope", "missing envelope"} {
		t.Run(name, func(t *testing.T) {
			_, err := client.GetSettingValue(name)
			require.Error(t, err)
		})
	}
	t.Run("transport error", func(t *testing.T) {
		_, err := client.GetSettingValue("unknown setting")
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// waitForSettingsConnection preserves a bounded readiness wait for in-memory RPC fixtures.
func waitForSettingsConnection(t *testing.T, connection *grpc.ClientConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection.Connect()
	for {
		state := connection.GetState()
		if state == connectivity.Ready {
			return
		}
		require.True(t, connection.WaitForStateChange(ctx, state), "settings RPC connection did not become ready")
	}
}
