package resourceconformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/config"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/v1/resource/plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const fixturePluginID = "p44-native-verification"

// Only a disposable go-plugin child enters this test. This uses the actual SDK
// runtime, service client/server, protobuf byte codec and resource controller.
func TestCurrentSDKChild(t *testing.T) {
	if os.Getenv("P44_NATIVE_CHILD") != "1" {
		return
	}
	c := NativeCorpus()
	state := os.Getenv("P44_NATIVE_RECEIPTS")
	controller, err := resource.BuildResourceController(context.Background(), resource.ResourcePluginConfig[Scenario]{
		Connections: &nativeConnections{corpus: c, state: state},
		Patterns: map[string]resource.Resourcer[Scenario]{
			"compute/instance": &nativeSource{corpus: c, typeID: "compute/instance", state: state},
			"container":        &nativeEvents{&nativeSource{corpus: c, typeID: "container", state: state}},
			"vm":               &nativeSource{corpus: c, typeID: "vm", state: state},
			"host":             &nativeSource{corpus: c, typeID: "host", state: state},
			"disk":             &nativeSource{corpus: c, typeID: "disk", state: state},
		},
	})
	require.NoError(t, err)
	defer controller.Close()
	p := sdk.NewPlugin(sdk.PluginOpts{ID: fixturePluginID})
	p.RegisterCapability("resource", &resourceplugin.GRPCPlugin{Impl: controller, SettingsProvider: p.SettingsProvider})
	p.Serve()
}

func startNativeProcess(t *testing.T) (resource.Provider, string, func()) {
	t.Helper()
	root, home, receipts := t.TempDir(), t.TempDir(), t.TempDir()
	// Darwin's Unix socket path limit is shorter than testing's nested temp
	// names. Keep only the socket directory short, unique and cleanup-owned.
	socketDir, err := os.MkdirTemp("", "p44-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	pluginDir := filepath.Join(root, "plugins", fixturePluginID)
	require.NoError(t, os.MkdirAll(pluginDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("id: "+fixturePluginID+"\nversion: 0.0.0\n"), 0600))
	meta := config.PluginMeta{ID: fixturePluginID}
	handshake := meta.GenerateHandshakeConfig()
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home, "TMPDIR=" + socketDir,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, "data"),
		"GOWORK=off", "OMNIVIEW_STATE_DIR=" + root,
		"OMNIVIEW_TELEMETRY_ENABLED=false", "OMNIVIEW_TELEMETRY_PROFILING=false",
		"P44_NATIVE_CHILD=1", "P44_NATIVE_RECEIPTS=" + receipts,
		handshake.MagicCookieKey + "=" + handshake.MagicCookieValue,
		"PLUGIN_PROTOCOL_VERSIONS=1",
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCurrentSDKChild$")
	cmd.Env = env // no credential, kubeconfig or user plugin state inheritance
	if launcher := os.Getenv("P44_CLEAN_ENV_LAUNCHER"); launcher != "" {
		args := append([]string{"/usr/bin/env"}, env...)
		args = append(args, os.Args[0], "-test.run=^TestCurrentSDKChild$")
		cmd = exec.Command(launcher, args...)
		cmd.Env = []string{"GOWORK=off"}
	}
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: handshake,
		VersionedPlugins: map[int]goplugin.PluginSet{
			sdk.CurrentProtocolVersion: {"resource": &resourceplugin.GRPCPlugin{}},
		},
		Cmd: cmd, AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		StartTimeout: 15 * time.Second,
	})
	t.Cleanup(client.Kill)
	rpc, err := client.Client()
	require.NoError(t, err)
	capability, err := rpc.Dispense("resource")
	require.NoError(t, err)
	provider, ok := capability.(resource.Provider)
	require.True(t, ok)
	return provider, receipts, func() {
		client.Kill()
		require.True(t, client.Exited(), "disposable plugin process must exit")
	}
}

func TestCurrentSDKNativeBoundary(t *testing.T) {
	c := NativeCorpus()
	require.NoError(t, c.Validate())
	provider, receipts, stopProcess := startNativeProcess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	connections, err := provider.LoadConnections(ctx)
	require.NoError(t, err)
	require.Len(t, connections, len(c.Scenarios))
	nativeBodies := make(map[string]json.RawMessage)
	for _, s := range c.Scenarios {
		t.Run(s.Name, func(t *testing.T) {
			_, err := provider.StartConnection(ctx, s.Name)
			require.NoError(t, err)
			session := resource.WithSession(ctx, &resource.Session{Connection: &types.Connection{ID: s.Name}})
			var observed []Observation
			cursor := ""
			for _, page := range s.Pages {
				require.Equal(t, page.Cursor, cursor)
				result, err := provider.List(session, s.Type, resource.ListInput{Pagination: resource.PaginationParams{Cursor: cursor, PageSize: 2}})
				require.NoError(t, err)
				require.True(t, result.Success)
				require.Equal(t, page.Next, result.NextCursor)
				require.Len(t, result.Result, len(page.Records))
				// This is a limitation observation, NOT the native total oracle.
				// The source returns -1; the selected server clamps it to zero.
				t.Logf("LIMIT current-wire unknown total: native=-1 transported=%d; not inventory size", result.TotalCount)
				for i, body := range result.Result {
					r := c.Record(page.Records[i])
					require.True(t, equalJSON(body, r.Body), "native body/large integer changed for %s", r.Name)
					observed = append(observed, Observation{Record: r, Body: body})
					nativeBodies[r.Name] = body
				}
				cursor = result.NextCursor
			}
			// Coverage/freshness are native sidecar oracles. Old List has no
			// fields to carry them: this is not a transported guarantee claim.
			require.NoError(t, c.CheckSnapshot(s, observed, s.Coverage, s.Freshness))
			t.Logf("LIMIT current-wire has no coverage/freshness envelope: native=%+v %s", s.Coverage, s.Freshness)
			schema, err := provider.GetResourceSchema(session, s.Name, s.Type)
			require.NoError(t, err)
			if s.Schema == nil {
				require.Empty(t, schema, "absent schema must not be fabricated")
			} else {
				require.True(t, equalJSON(schema, s.Schema), "bundled schema changed")
			}
			ns, err := provider.GetConnectionNamespaces(session, s.Name)
			require.NoError(t, err)
			require.Empty(t, ns)
			for _, o := range observed {
				get, err := provider.Get(session, s.Type, resource.GetInput{ID: o.Record.ID})
				collides := false
				for _, other := range observed {
					collides = collides || o.Record.ID == other.Record.ID && !SameAddress(o.Record, other.Record)
				}
				if collides {
					requireNativeError(t, err, "INVALID_INPUT")
					t.Log("LIMIT current Get cannot express colliding exact native scopes; rejected, not namespace-encoded")
				} else {
					require.NoError(t, err)
					require.True(t, equalJSON(get.Result, o.Body))
				}
			}
			_, err = provider.Get(session, s.Type, resource.GetInput{ID: "not-observed"})
			requireNativeError(t, err, "NOT_FOUND")
			_, err = provider.Get(session, s.Type, resource.GetInput{ID: c.Record(s.Pages[0].Records[0]).ID, Namespace: "node/a::1"})
			requireNativeError(t, err, "INVALID_INPUT")
			_, err = provider.List(session, s.Type, resource.ListInput{Namespaces: []string{"111111111111"}})
			requireNativeError(t, err, "INVALID_INPUT")
			_, err = provider.List(session, s.Type, resource.ListInput{Pagination: resource.PaginationParams{Cursor: "invalid-cursor"}})
			requireNativeError(t, err, "INVALID_INPUT")
			_, err = provider.List(session, "unregistered/native/type", resource.ListInput{})
			require.Error(t, err)
			_, err = provider.Find(session, s.Type, resource.FindInput{TextQuery: "prod"})
			requireUnsupportedLimitation(t, err)
			_, err = provider.Create(session, s.Type, resource.CreateInput{})
			requireUnsupportedLimitation(t, err)
			_, err = provider.Update(session, s.Type, resource.UpdateInput{ID: "x"})
			requireUnsupportedLimitation(t, err)
			_, err = provider.Delete(session, s.Type, resource.DeleteInput{ID: "x"})
			requireUnsupportedLimitation(t, err)
			caps, err := provider.GetResourceCapabilities(session, s.Type)
			require.NoError(t, err)
			require.False(t, caps.Filterable)
			require.False(t, caps.Searchable)
			t.Logf("LIMIT legacy mandatory interface advertises unsupported operations: find=%v create=%v update=%v delete=%v", caps.CanFind, caps.CanCreate, caps.CanUpdate, caps.CanDelete)
			catalog := provider.GetResourceTypes(session, s.Name)
			require.Empty(t, catalog, "native patterns must not invent GVK catalog entries")
			if len(s.Updates) > 0 {
				probeNativeEvents(t, provider, session, receipts, s, c)
			}
			if s.Profile == "hypervisor" {
				require.False(t, caps.Watchable, "snapshot-only source must not have a fake watch")
			}
			probeCancellation(t, provider, session, receipts, s)
			_, err = provider.StopConnection(session, s.Name)
			require.NoError(t, err)
			closed, err := os.ReadFile(filepath.Join(receipts, s.Name+".closed"))
			require.NoError(t, err)
			require.Equal(t, "closed", string(closed))
			_, err = provider.List(session, s.Type, resource.ListInput{})
			require.Error(t, err, "stopped source must not keep serving fixture data")
		})
	}
	require.NoError(t, c.CheckLinks(nativeBodies), "host/VM/disk links must resolve from real transported bodies")
	stopProcess()
}

func requireNativeError(t *testing.T, err error, code string) {
	t.Helper()
	var native *resource.ResourceOperationError
	require.True(t, errors.As(err, &native), "expected structured native error, got %v", err)
	require.Equal(t, code, native.Code)
}

func requireUnsupportedLimitation(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var native *resource.ResourceOperationError
	require.True(t, errors.As(err, &native))
	// Current codec has no UNSUPPORTED mapping. Do not assert UNKNOWN_0 is
	// correct semantics or enshrine this broken encoding as future acceptance.
	t.Logf("LIMIT native unsupported operation transported as %s: %s", native.Code, native.Message)
	require.NotEmpty(t, native.Message)
	require.True(t, strings.Contains(native.Message, "read-only") || strings.Contains(native.Message, "server filtering"), "wrong native failure: %s", native.Message)
}

func probeCancellation(t *testing.T, provider resource.Provider, session context.Context, receipts string, s Scenario) {
	t.Helper()
	ctx, cancel := context.WithCancel(session)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := provider.List(ctx, s.Type, resource.ListInput{Pagination: resource.PaginationParams{Cursor: "verification:wait-for-cancel"}})
		done <- err
	}()
	waitReceipt(t, filepath.Join(receipts, s.Name+".waiting"))
	cancel()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-session.Done():
		t.Fatal("canceled service request did not finish")
	}
	waitReceipt(t, filepath.Join(receipts, s.Name+".cancelled"))
	_, err := provider.List(session, s.Type, resource.ListInput{})
	require.NoError(t, err, "cancellation must not terminate another request/source")
}

func waitReceipt(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 5*time.Second, 5*time.Millisecond, "missing plugin-side lifecycle receipt: %s", path)
}
