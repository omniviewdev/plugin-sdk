package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestConfigFromEnv_Disabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test-plugin")

	cfg := configFromEnv()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, "test-plugin", cfg.PluginID)
}

func TestConfigFromEnv_Enabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT", "localhost:4318")
	t.Setenv("OMNIVIEW_TELEMETRY_AUTH_HEADER", "Authorization")
	t.Setenv("OMNIVIEW_TELEMETRY_AUTH_VALUE", "Bearer tok")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_PYROSCOPE_ENDPOINT", "localhost:4040")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "kubernetes")

	cfg := configFromEnv()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "localhost:4318", cfg.OTLPEndpoint)
	assert.Equal(t, "Authorization", cfg.AuthHeader)
	assert.Equal(t, "Bearer tok", cfg.AuthValue)
	assert.True(t, cfg.Profiling)
	assert.Equal(t, "localhost:4040", cfg.PyroscopeEndpoint)
	assert.Equal(t, "kubernetes", cfg.PluginID)
}

func TestConfigFromEnv_MissingEnv(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "")
	require.NoError(t, os.Unsetenv("OMNIVIEW_TELEMETRY_ENABLED"))
	require.NoError(t, os.Unsetenv("OMNIVIEW_PLUGIN_ID"))

	cfg := configFromEnv()
	assert.False(t, cfg.Enabled)
	assert.Empty(t, cfg.PluginID)
}

func TestInitFromEnv_Disabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "false")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test")

	provider, err := InitFromEnv()
	require.NoError(t, err)
	assert.Nil(t, provider)
}

func TestInitFromEnv_Enabled(t *testing.T) {
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT", "localhost:4318")
	t.Setenv("OMNIVIEW_PLUGIN_ID", "test-plugin")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "false")

	provider, err := InitFromEnv()
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.NotNil(t, provider.tracerProvider)
	assert.Nil(t, provider.profiler)

	// Shutdown should be idempotent
	require.NoError(t, provider.Shutdown(t.Context()))
	require.NoError(t, provider.Shutdown(t.Context()))
}

func TestClientIDReachesTraceAndProfileCollectors(t *testing.T) {
	traceRequests := make(chan []byte, 1)
	profileNames := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/traces":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid trace", http.StatusBadRequest)
				return
			}
			traceRequests <- body
		case "/ingest":
			select {
			case profileNames <- r.URL.Query().Get("name"):
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("OMNIVIEW_TELEMETRY_ENABLED", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_CLIENT_ID", "work-laptop")
	t.Setenv("OMNIVIEW_TELEMETRY_OTLP_ENDPOINT", server.URL+"/v1/traces")
	t.Setenv("OMNIVIEW_TELEMETRY_PROFILING", "true")
	t.Setenv("OMNIVIEW_TELEMETRY_PYROSCOPE_ENDPOINT", server.URL)
	t.Setenv("PYROSCOPE_ADHOC_SERVER_ADDRESS", server.URL)
	t.Setenv("OMNIVIEW_PLUGIN_ID", "kubernetes")
	previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(previousProvider)
	defer otel.SetTextMapPropagator(previousPropagator)
	provider, err := InitFromEnv()
	require.NoError(t, err)
	_, span := otel.Tracer("test").Start(t.Context(), "operation")
	span.End()
	require.NoError(t, provider.Shutdown(t.Context()))

	select {
	case body := <-traceRequests:
		var request collectortrace.ExportTraceServiceRequest
		require.NoError(t, proto.Unmarshal(body, &request))
		var found bool
		for _, entry := range request.GetResourceSpans()[0].GetResource().GetAttributes() {
			if entry.GetKey() == "omniview.client.id" && entry.GetValue().GetStringValue() == "work-laptop" {
				found = true
			}
		}
		require.True(t, found, "trace client attribute not exported")
	case <-time.After(5 * time.Second):
		t.Fatal("trace was not exported")
	}
	select {
	case name := <-profileNames:
		require.Contains(t, name, "omniview_client_id=work-laptop")
	case <-time.After(5 * time.Second):
		t.Fatal("profile was not exported")
	}
}
