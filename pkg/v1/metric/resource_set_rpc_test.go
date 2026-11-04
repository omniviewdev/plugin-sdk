package metric

import (
	"context"
	metricpb "github.com/omniviewdev/plugin-sdk/proto/v1/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"testing"
	"time"
)

func TestResourceSetExternalTransportAndStrictAdmission(t *testing.T) {
	adapter := &resourceSetRecorder{}
	manager := NewManager(nil, nil, ProviderInfo{ID: "grafana"}, nil, nil, nil)
	manager.resourceSetProvider = adapter
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	if err := (&Plugin{Impl: manager}).GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := &PluginClient{resourceSetClient: metricpb.NewMetricResourceSetClient(conn)}
	request := resourceSetFixture()
	response, err := client.QueryResources(&QueryContext{Context: ctx}, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].ResourceToken != "pod-a" || response.Results[0].Status != MetricStatusMissing {
		t.Fatalf("lost explicit identity/status: %#v", response)
	}
	wire := metricpb.NewMetricResourceSetClient(conn)
	for _, payload := range []string{`{"resources":[],"private_connection":{}}`, `{"resource_plugin_id":"a","resource_plugin_id":"b"}`} {
		if _, err = wire.QueryResources(ctx, &metricpb.MetricContractRequest{Version: MetricContractVersion, Json: []byte(payload)}); err == nil {
			t.Fatal("unsafe external JSON reached provider")
		}
	}
	if adapter.calls != 1 {
		t.Fatal("rejected external input reached adapter")
	}
}

func TestCredentialCannotReturnThroughPublicDTO(t *testing.T) {
	var public ProviderConnection
	if DecodeMetricContract([]byte(`{"id":"provider","credential":"private"}`), &public) == nil {
		t.Fatal("public DTO accepted credential material")
	}
	var input ProviderConnectionInput
	if err := DecodeMetricContract([]byte(`{"connection":{"id":"provider"},"credential":"private"}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.Credential == nil || *input.Credential != "private" {
		t.Fatal("write-only credential lost")
	}
}
