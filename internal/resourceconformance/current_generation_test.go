package resourceconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"

	"github.com/omniviewdev/plugin-sdk/pkg/config"
	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// generationCommand is a test-only source refresh file, never a plugin RPC.
// applied-N acknowledges ApplyConnections and a newly started native client.
type generationCommand struct {
	Sequence int    `json:"sequence"`
	Endpoint string `json:"endpoint"`
}

func generationConnection(endpoint string) types.Connection {
	return types.Connection{ID: "generation", Data: map[string]any{"endpoint": endpoint}}
}

func generationRepresentation() resource.Representation {
	return resource.Representation{
		ID:       "document",
		Revision: "document-1",
		Schema:   resource.SchemaBinding{State: "absent", Validation: "none", Unvalidated: []string{}},
	}
}

func generationReference() resource.ResourceReference {
	return resource.ResourceReference{
		Address: resource.ResourceAddress{
			Provider:  "generation",
			Authority: "native",
			Type:      "native/object",
			Scope:     []resource.ScopeEntry{},
			ID:        "object",
		},
	}
}

// TestGenerationChild serves the canonical SDK controller in a disposable process.
// Its source independently refreshes native configuration from private control files.
func TestGenerationChild(t *testing.T) {
	if os.Getenv("P45_GENERATION_CHILD") != "1" {
		return
	}
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "KUBECONFIG", "OMNIVIEW_WORKSPACE_FILE"} {
		_, inherited := os.LookupEnv(key)
		require.False(t, inherited, "child inherited %s", key)
	}
	control, initial := os.Getenv("P45_GENERATION_CONTROL"), os.Getenv("P45_GENERATION_ENDPOINT")
	var reads atomic.Int64
	controller, err := resource.NewController(resource.Config[string]{
		Catalog: func(context.Context, resource.CatalogRequest) (resource.Catalog, error) {
			return resource.Catalog{
				Provider: "generation",
				Revision: "native-catalog",
				Coverage: resource.Coverage{Kind: "complete", Paths: []string{}},
				Types: []resource.ResourceType{
					{
						Type:               "native/object",
						IdentityDimensions: []resource.Dimension{},
						QueryDimensions:    []resource.Dimension{},
						Representations:    []resource.Representation{generationRepresentation()},
						Capabilities: []resource.Capability{
							{Operation: resource.OperationGet, Scope: resource.QueryScope{}},
						},
					},
				},
			}, nil
		},
		Lifecycle: &resource.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) {
				return []types.Connection{generationConnection(initial)}, nil
			},
			CreateClient: func(_ context.Context, c types.Connection) (*string, error) {
				endpoint, ok := c.Data["endpoint"].(string)
				if !ok {
					return nil, fmt.Errorf("generation fixture endpoint must be a string")
				}
				return &endpoint, nil
			},
			DestroyClient: func(context.Context, *string) error { return nil },
		},
		Registrations: []resource.Registration[string]{
			{
				Type: "native/object",
				Get: func(ctx context.Context, endpoint *string, request resource.GetRequest) (resource.Record, error) {
					return readGeneration(ctx, control, endpoint, request, reads.Add(1))
				},
			},
		},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, controller.Close()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		refreshGeneration(ctx, controller, control)
	}()
	p := sdk.NewPlugin(sdk.PluginOpts{ID: fixturePluginID})
	p.RegisterCapability("resource", &resourceplugin.GRPCPlugin{Impl: controller})
	p.Serve()
}

func readGeneration(
	ctx context.Context,
	control string,
	endpoint *string,
	request resource.GetRequest,
	n int64,
) (resource.Record, error) {
	if err := os.WriteFile(
		filepath.Join(control, fmt.Sprintf("entered-%d", n)),
		[]byte(*endpoint),
		0o600,
	); err != nil {
		return resource.Record{}, err
	}
	stop := context.AfterFunc(ctx, func() {
		_ = os.WriteFile(
			filepath.Join(control, fmt.Sprintf("canceled-%d", n)),
			[]byte("canceled"),
			0o600,
		)
	})
	defer stop()
	// Deliberately ignore cancellation: the controller must fence this result.
	response, err := (&http.Client{Timeout: 10 * time.Second}).Get(*endpoint)
	if err != nil {
		return resource.Record{}, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	closeErr := response.Body.Close()
	if err != nil {
		return resource.Record{}, err
	}
	if closeErr != nil {
		return resource.Record{}, closeErr
	}
	if err := os.WriteFile(
		filepath.Join(control, fmt.Sprintf("returned-%d", n)),
		body,
		0o600,
	); err != nil {
		return resource.Record{}, err
	}
	return resource.Record{
		Reference: request.Reference,
		Body: resource.Value{
			Kind: "map",
			Map:  map[string]resource.Value{"native": {Kind: "string", String: string(body)}},
		},
		Representation: generationRepresentation(),
		Coverage:       resource.Coverage{Kind: "complete", Paths: []string{}},
	}, nil
}

func refreshGeneration(ctx context.Context, controller *resource.Controller[string], control string) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	last := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		data, err := os.ReadFile(filepath.Join(control, "command.json"))
		if err != nil {
			continue
		}
		var command generationCommand
		if json.Unmarshal(data, &command) != nil || command.Sequence <= last {
			continue
		}
		last = command.Sequence
		err = controller.ApplyConnections(ctx, []types.Connection{generationConnection(command.Endpoint)})
		if err == nil {
			_, err = controller.StartConnection(ctx, "generation")
		}
		if err != nil {
			_ = os.WriteFile(filepath.Join(control, "control-error"), []byte(err.Error()), 0o600)
			return
		}
		if err = os.WriteFile(
			filepath.Join(control, fmt.Sprintf("applied-%d", last)),
			[]byte(command.Endpoint),
			0o600,
		); err != nil {
			return
		}
	}
}

func startGenerationProcess(t *testing.T, endpoint string) (resource.Provider, string) {
	t.Helper()
	root, home, control := t.TempDir(), t.TempDir(), t.TempDir()
	socket, err := os.MkdirTemp("", "p45-gen-") //nolint:usetesting // Keep Unix-domain socket paths below OS limits.
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socket)) })
	dir := filepath.Join(root, "plugins", fixturePluginID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte("id: "+fixturePluginID+"\nversion: 0.0.0\n"), 0o600),
	)
	meta := config.PluginMeta{ID: fixturePluginID}
	handshake := meta.GenerateHandshakeConfig()
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"TMPDIR=" + socket,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"GOWORK=off",
		"OMNIVIEW_STATE_DIR=" + root,
		"OMNIVIEW_TELEMETRY_ENABLED=false",
		"OMNIVIEW_TELEMETRY_PROFILING=false",
		"P45_GENERATION_CHILD=1",
		"P45_GENERATION_CONTROL=" + control,
		"P45_GENERATION_ENDPOINT=" + endpoint,
		handshake.MagicCookieKey + "=" + handshake.MagicCookieValue,
		"PLUGIN_PROTOCOL_VERSIONS=1",
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGenerationChild$")
	cmd.Env = env
	if launcher := os.Getenv("P44_CLEAN_ENV_LAUNCHER"); launcher != "" {
		args := append([]string{"/usr/bin/env"}, env...)
		args = append(args, os.Args[0], "-test.run=^TestGenerationChild$")
		cmd = exec.Command(launcher, args...)
		cmd.Env = []string{"GOWORK=off"}
	}
	client := goplugin.NewClient(
		&goplugin.ClientConfig{
			HandshakeConfig: handshake,
			SkipHostEnv:     true,
			VersionedPlugins: map[int]goplugin.PluginSet{
				sdk.CurrentProtocolVersion: {"resource": &resourceplugin.GRPCPlugin{}},
			},
			Cmd:              cmd,
			AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
			StartTimeout:     15 * time.Second,
		},
	)
	t.Cleanup(func() { client.Kill(); require.True(t, client.Exited()) })
	rpc, err := client.Client()
	require.NoError(t, err)
	capability, err := rpc.Dispense("resource")
	require.NoError(t, err)
	provider, ok := capability.(resource.Provider)
	require.True(t, ok)
	return provider, control
}

func generationReceipt(t *testing.T, control, name string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(filepath.Join(control, "control-error")); err == nil {
			t.Fatalf("source refresh: %s", body)
		}
		if body, err := os.ReadFile(filepath.Join(control, name)); err == nil {
			return string(body)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing child receipt %s", name)
	return ""
}

func generationRefresh(t *testing.T, control string, sequence int, endpoint string) {
	t.Helper()
	body, err := json.Marshal(generationCommand{Sequence: sequence, Endpoint: endpoint})
	require.NoError(t, err)
	temporary := filepath.Join(control, "command.tmp")
	require.NoError(t, os.WriteFile(temporary, body, 0o600))
	require.NoError(t, os.Rename(temporary, filepath.Join(control, "command.json")))
}

func TestCurrentSDKGenerationFence(t *testing.T) {
	var aReads, bReads atomic.Int64
	var block atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		aReads.Add(1)
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
		_, _ = io.WriteString(w, "native-A")
	}))
	defer a.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	b := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { bReads.Add(1); _, _ = io.WriteString(w, "native-B") },
		),
	)
	defer b.Close()
	provider, control := startGenerationProcess(t, a.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lifecycle, ok := provider.(resource.ConnectionLifecycle)
	require.True(t, ok)
	getter, ok := provider.(resource.Getter)
	require.True(t, ok)
	_, err := lifecycle.LoadConnections(ctx)
	require.NoError(t, err)
	_, err = lifecycle.StartConnection(ctx, "generation")
	require.NoError(t, err)
	route := resource.AccessRoute{Connection: "generation"}
	catalog := func() resource.Catalog {
		c, catalogErr := provider.Catalog(ctx, resource.CatalogRequest{Route: route})
		require.NoError(t, catalogErr)
		return c
	}
	get := func(revision string) (resource.Record, error) {
		return getter.Get(
			resource.WithCatalogRevision(ctx, revision),
			resource.GetRequest{Route: route, Reference: generationReference(), Representation: "document"},
		)
	}
	acceptedA := catalog()
	generationRefresh(t, control, 1, b.URL)
	require.Equal(t, b.URL, generationReceipt(t, control, "applied-1"))
	_, err = get(acceptedA.Revision)
	requireNativeError(t, err, "RESET_REQUIRED")
	require.Zero(t, aReads.Load())
	require.Zero(t, bReads.Load(), "stale A token must not dispatch B")
	acceptedB := catalog()
	require.NotEqual(t, acceptedA.Revision, acceptedB.Revision)
	record, err := get(acceptedB.Revision)
	require.NoError(t, err)
	require.Equal(t, "native-B", record.Body.Map["native"].String)
	require.EqualValues(t, 1, bReads.Load())
	generationRefresh(t, control, 2, a.URL)
	require.Equal(t, a.URL, generationReceipt(t, control, "applied-2"))
	_, err = get(acceptedA.Revision)
	requireNativeError(t, err, "RESET_REQUIRED")
	require.Zero(t, aReads.Load(), "restored configuration must not resurrect retired nonce")
	restoredA := catalog()
	require.NotEqual(t, acceptedA.Revision, restoredA.Revision)
	record, err = get(restoredA.Revision)
	require.NoError(t, err)
	require.Equal(t, "native-A", record.Body.Map["native"].String)
	block.Store(true)
	type readResult struct {
		record resource.Record
		err    error
	}
	result := make(chan readResult, 1)
	go func() {
		resultRecord, resultErr := get(restoredA.Revision)
		result <- readResult{record: resultRecord, err: resultErr}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native read did not enter")
	}
	generationRefresh(t, control, 3, b.URL)
	// Native reads: B=1, restored A=2, blocked A=3.
	require.Equal(t, "canceled", generationReceipt(t, control, "canceled-3"))
	close(release)
	select {
	case got := <-result:
		require.Error(t, got.err)
		require.Empty(t, got.record.Body.Kind, "retired native body must not cross RPC boundary")
	case <-ctx.Done():
		t.Fatal("retired native result escaped completion fence")
	}
	require.Equal(
		t,
		"native-A",
		generationReceipt(t, control, "returned-3"),
		"native provider returned data despite cancellation",
	)
	require.Equal(t, b.URL, generationReceipt(t, control, "applied-3"))
	require.EqualValues(t, 2, aReads.Load())
	currentB := catalog()
	record, err = get(currentB.Revision)
	require.NoError(t, err)
	require.Equal(t, "native-B", record.Body.Map["native"].String)
	require.EqualValues(t, 2, bReads.Load())
	hostReference := generationReference()
	hostReference.Address.Authority = "host-issued/opaque:α"
	record, err = getter.Get(
		resource.WithCatalogRevision(ctx, currentB.Revision),
		resource.GetRequest{Route: route, Reference: hostReference, Representation: "document"},
	)
	require.NoError(t, err)
	require.Equal(t, hostReference, record.Reference)
	require.Equal(
		t,
		"native-B",
		record.Body.Map["native"].String,
		"admitted host authority must retain actual selected native data",
	)
	require.EqualValues(t, 3, bReads.Load())
}
