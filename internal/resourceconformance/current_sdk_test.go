package resourceconformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/config"
	resource "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const fixturePluginID = "p45-native-verification"

// The same test binary serves a genuine native Provider in a disposable child.
func TestCurrentSDKChild(t *testing.T) {
	if os.Getenv("P45_NATIVE_CHILD") != "1" {
		return
	}
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "KUBECONFIG", "OMNIVIEW_WORKSPACE_FILE"} {
		_, inherited := os.LookupEnv(key)
		require.False(t, inherited, "disposable child inherited %s", key)
	}
	base := newNativeProvider(os.Getenv("P45_NATIVE_RECEIPTS"), os.Getenv("P45_NATIVE_PROFILE"))
	var impl resource.Provider
	switch base.profile {
	case "catalog-only":
		impl = base
	case "get-only":
		impl = &nativeReader{base}
	case "enumerate-only":
		impl = &nativeEnumerationOnly{base}
	case "native-query-only":
		impl = &nativeQueryOnly{base}
	default:
		impl = newNativeEventProvider(base)
	}
	defer impl.(resource.Closer).Close()
	p := sdk.NewPlugin(sdk.PluginOpts{ID: fixturePluginID})
	p.RegisterCapability("resource", &resourceplugin.GRPCPlugin{Impl: impl})
	p.Serve()
}

func startNativeProcess(t *testing.T) (resource.Provider, string, func()) {
	return startNativeProfile(t, "read-only")
}
func startNativeProfile(t *testing.T, profile string) (resource.Provider, string, func()) {
	t.Helper()
	root, home, receipts := t.TempDir(), t.TempDir(), t.TempDir()
	socketDir, err := os.MkdirTemp("", "p45-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	pluginDir := filepath.Join(root, "plugins", fixturePluginID)
	require.NoError(t, os.MkdirAll(pluginDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("id: "+fixturePluginID+"\nversion: 0.0.0\n"), 0600))
	meta := config.PluginMeta{ID: fixturePluginID}
	handshake := meta.GenerateHandshakeConfig()
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "TMPDIR=" + socketDir, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "GOWORK=off", "OMNIVIEW_STATE_DIR=" + root, "OMNIVIEW_TELEMETRY_ENABLED=false", "OMNIVIEW_TELEMETRY_PROFILING=false", "P45_NATIVE_CHILD=1", "P45_NATIVE_RECEIPTS=" + receipts, "P45_NATIVE_PROFILE=" + profile, handshake.MagicCookieKey + "=" + handshake.MagicCookieValue, "PLUGIN_PROTOCOL_VERSIONS=1"}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCurrentSDKChild$")
	cmd.Env = env
	if launcher := os.Getenv("P44_CLEAN_ENV_LAUNCHER"); launcher != "" {
		args := append([]string{"/usr/bin/env"}, env...)
		args = append(args, os.Args[0], "-test.run=^TestCurrentSDKChild$")
		cmd = exec.Command(launcher, args...)
		cmd.Env = []string{"GOWORK=off"}
	}
	client := goplugin.NewClient(&goplugin.ClientConfig{HandshakeConfig: handshake, SkipHostEnv: true, VersionedPlugins: map[int]goplugin.PluginSet{sdk.CurrentProtocolVersion: {"resource": &resourceplugin.GRPCPlugin{}}}, Cmd: cmd, AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC}, StartTimeout: 15 * time.Second})
	t.Cleanup(client.Kill)
	rpc, err := client.Client()
	require.NoError(t, err)
	capability, err := rpc.Dispense("resource")
	require.NoError(t, err)
	provider, ok := capability.(resource.Provider)
	require.True(t, ok)
	return provider, receipts, func() { client.Kill(); require.True(t, client.Exited(), "disposable plugin process must exit") }
}
func TestCurrentSDKChildIsolation(t *testing.T) {
	t.Setenv("P44_CLEAN_ENV_LAUNCHER", "")
	for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "KUBECONFIG", "OMNIVIEW_WORKSPACE_FILE"} {
		t.Setenv(key, "p45-unused-sentinel")
	}
	provider, _, stop := startNativeProcess(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connections, err := provider.(resource.ConnectionLifecycle).LoadConnections(ctx)
	require.NoError(t, err)
	require.Len(t, connections, len(NativeCorpus().Scenarios))
}
func target(s Scenario, c Corpus) resource.ReadTarget {
	r := c.Record(s.Pages[0].Records[0])
	return resource.ReadTarget{Route: resource.AccessRoute{Connection: s.Name}, Provider: r.Provider, Authority: r.Authority, Type: s.Type, Scope: queryScope(s), Representation: "native"}
}
func TestCurrentSDKNativeBoundary(t *testing.T) {
	c := NativeCorpus()
	require.NoError(t, c.Validate())
	provider, receipts, stop := startNativeProcess(t)
	defer stop()
	lifecycle := provider.(resource.ConnectionLifecycle)
	enumerator := provider.(resource.Enumerator)
	getter := provider.(resource.Getter)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	connections, err := lifecycle.LoadConnections(ctx)
	require.NoError(t, err)
	require.Len(t, connections, len(c.Scenarios))
	bodies := map[string]json.RawMessage{}
	for _, s := range c.Scenarios {
		t.Run(s.Name, func(t *testing.T) {
			_, err := lifecycle.StartConnection(ctx, s.Name)
			require.NoError(t, err)
			catalog, err := provider.Catalog(ctx, resource.CatalogRequest{Route: resource.AccessRoute{Connection: s.Name}})
			require.NoError(t, err)
			require.Equal(t, s.Type, catalog.Types[0].Type)
			require.Equal(t, representation(s), catalog.Types[0].Representations[0])
			expectedCaps := []resource.Capability{{Operation: resource.OperationGet, Scope: queryScope(s)}, {Operation: resource.OperationEnumerate, Scope: queryScope(s)}}
			if len(s.Updates) > 0 {
				expectedCaps = append(expectedCaps, resource.Capability{Operation: resource.OperationUpdate, Scope: queryScope(s), Guarantee: resource.BestEffortGuarantee})
			}
			require.Equal(t, expectedCaps, catalog.Types[0].Capabilities)
			require.Equal(t, resource.Coverage{Kind: "complete", Paths: []string{}}, catalog.Coverage)
			require.Nil(t, catalog.NativeAuthorityHint)
			var observed []Observation
			var cursor *string
			for _, page := range s.Pages {
				require.Equal(t, token(page.Cursor), cursor)
				result, err := enumerator.Enumerate(ctx, resource.EnumerateRequest{Target: target(s, c), PageSize: 2, Cursor: cursor})
				require.NoError(t, err)
				require.Equal(t, token(page.Next), result.NextCursor)
				require.Equal(t, resource.Count{Kind: "unknown"}, result.Count)
				require.Equal(t, s.Coverage.Consistency, result.Consistency)
				require.Nil(t, result.SourceRevision)
				wantCoverage := resource.Coverage{Kind: "complete", Paths: []string{}}
				if s.Coverage.Enumeration == "partial" {
					wantCoverage = resource.Coverage{Kind: "partial", Paths: []string{}, Reason: "selected listing truncated; eventual visibility"}
				}
				require.Equal(t, wantCoverage, result.Coverage)
				require.Len(t, result.Records, len(page.Records))
				for i, got := range result.Records {
					r := c.Record(page.Records[i])
					require.Equal(t, nativeRecord(r, s), got, "transport changed canonical record %s", r.Name)
					get, err := getter.Get(ctx, resource.GetRequest{Route: resource.AccessRoute{Connection: s.Name}, Reference: got.Reference, Representation: "native"})
					require.NoError(t, err)
					require.Equal(t, got, get, "exact scope must disambiguate colliding native IDs")
					body := nativeBodyJSON(got.Body)
					observed = append(observed, Observation{Record: r, Body: body})
					bodies[r.Name] = body
				}
				cursor = result.NextCursor
			}
			require.NoError(t, c.CheckSnapshot(s, observed, s.Coverage, s.Freshness))
			missing := nativeRecord(c.Record(s.Pages[0].Records[0]), s).Reference
			missing.Address.ID = "not-observed"
			_, err = getter.Get(ctx, resource.GetRequest{Route: resource.AccessRoute{Connection: s.Name}, Reference: missing, Representation: "native"})
			requireNativeError(t, err, "NOT_FOUND")
			_, err = enumerator.Enumerate(ctx, resource.EnumerateRequest{Target: target(s, c), Cursor: token("invalid-cursor")})
			requireNativeError(t, err, "INVALID_REQUEST")
			wrong := target(s, c)
			wrong.Type = "unregistered/native/type"
			_, err = enumerator.Enumerate(ctx, resource.EnumerateRequest{Target: wrong})
			requireNativeError(t, err, "UNSUPPORTED")
			ref := nativeRecord(c.Record(s.Pages[0].Records[0]), s).Reference
			_, err = provider.(resource.Mutator).Mutate(ctx, resource.MutationRequest{Route: resource.AccessRoute{Connection: s.Name}, Reference: ref, Operation: "delete", Input: resource.Value{Kind: "map", Map: map[string]resource.Value{}}})
			requireNativeError(t, err, "UNSUPPORTED")
			_, err = provider.(resource.NativeQuerier).NativeQuery(ctx, resource.NativeQueryRequest{Target: target(s, c), Language: "fixture.compute.v1", Query: resource.Value{Kind: "int64", Int: "1"}})
			requireNativeError(t, err, "UNSUPPORTED")
			if len(s.Updates) > 0 {
				probeNativeEvents(t, provider, ctx, receipts, s, c)
			}
			probeCancellation(t, enumerator, ctx, receipts, s, c)
			_, err = lifecycle.StopConnection(ctx, s.Name)
			require.NoError(t, err)
			closed, err := os.ReadFile(filepath.Join(receipts, s.Name+".closed"))
			require.NoError(t, err)
			require.Equal(t, "closed", string(closed))
			_, err = enumerator.Enumerate(ctx, resource.EnumerateRequest{Target: target(s, c)})
			requireNativeError(t, err, "INVALID_REQUEST")
		})
	}
	require.NoError(t, c.CheckLinks(bodies), "links must resolve from actually transported tagged bodies")
}

// Convert only already classified fixture values for the independent native
// JSON body oracle. Integers never pass through float64.
func nativeBodyJSON(v resource.Value) json.RawMessage {
	var plain func(resource.Value) any
	plain = func(v resource.Value) any {
		switch v.Kind {
		case "null":
			return nil
		case "bool":
			return v.Bool
		case "string":
			return v.String
		case "timestamp":
			return v.Timestamp
		case "int64":
			return json.Number(v.Int)
		case "uint64":
			return json.Number(v.Uint)
		case "double":
			return v.Double
		case "list":
			out := make([]any, 0, len(v.List))
			for _, x := range v.List {
				out = append(out, plain(x))
			}
			return out
		case "map":
			out := map[string]any{}
			for k, x := range v.Map {
				out[k] = plain(x)
			}
			return out
		default:
			panic("unsupported fixture kind")
		}
	}
	raw, err := json.Marshal(plain(v))
	if err != nil {
		panic(err)
	}
	return raw
}
func requireNativeError(t *testing.T, err error, code string) {
	t.Helper()
	var native *resource.ResourceOperationError
	require.True(t, errors.As(err, &native), "expected typed error, got %v", err)
	require.Equal(t, code, native.Code)
}
func probeCancellation(t *testing.T, p resource.Enumerator, session context.Context, receipts string, s Scenario, c Corpus) {
	t.Helper()
	ctx, cancel := context.WithCancel(session)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := p.Enumerate(ctx, resource.EnumerateRequest{Target: target(s, c), Cursor: token("verification:wait-for-cancel")})
		done <- err
	}()
	waitReceipt(t, filepath.Join(receipts, s.Name+".waiting"))
	cancel()
	select {
	case err := <-done:
		var op *resource.ResourceOperationError
		if errors.As(err, &op) {
			require.Equal(t, "CANCELLED", op.Code)
		} else {
			require.Equal(t, codes.Canceled, status.Code(err))
		}
	case <-session.Done():
		t.Fatal("canceled service request did not finish")
	}
	waitReceipt(t, filepath.Join(receipts, s.Name+".cancelled"))
	_, err := p.Enumerate(session, resource.EnumerateRequest{Target: target(s, c)})
	require.NoError(t, err, "request cancellation must not stop source")
}
func waitReceipt(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(path); return err == nil }, 5*time.Second, 5*time.Millisecond, "missing child lifecycle receipt: %s", path)
}

func TestCurrentSDKIndependentProfiles(t *testing.T) {
	for _, profile := range []string{"catalog-only", "get-only", "enumerate-only", "native-query-only", "read-only"} {
		t.Run(profile, func(t *testing.T) {
			// Provider shapes genuinely omit unsupported methods; transport capability
			// discovery remains catalog-qualified, never client method-set inference.
			base := newNativeProvider(t.TempDir(), profile)
			var shape resource.Provider = base
			switch profile {
			case "get-only":
				shape = &nativeReader{base}
			case "enumerate-only":
				shape = &nativeEnumerationOnly{base}
			case "native-query-only":
				shape = &nativeQueryOnly{base}
			case "read-only":
				shape = &nativeEnumerable{&nativeReader{base}}
			}
			_, hasGet := shape.(resource.Getter)
			_, hasEnum := shape.(resource.Enumerator)
			_, hasQuery := shape.(resource.NativeQuerier)
			require.Equal(t, profile == "get-only" || profile == "read-only", hasGet)
			require.Equal(t, profile == "enumerate-only" || profile == "read-only", hasEnum)
			require.Equal(t, profile == "native-query-only", hasQuery)
			provider, _, stop := startNativeProfile(t, profile)
			defer stop()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := NativeCorpus()
			s := c.Scenarios[0]
			_, err := provider.(resource.ConnectionLifecycle).StartConnection(ctx, s.Name)
			require.NoError(t, err)
			catalog, err := provider.Catalog(ctx, resource.CatalogRequest{Route: resource.AccessRoute{Connection: s.Name}})
			require.NoError(t, err)
			ops := []resource.Operation{}
			for _, cap := range catalog.Types[0].Capabilities {
				ops = append(ops, cap.Operation)
			}
			expected := []resource.Operation{}
			if hasGet {
				expected = append(expected, resource.OperationGet)
			}
			if hasEnum {
				expected = append(expected, resource.OperationEnumerate)
			}
			if hasQuery {
				expected = append(expected, resource.OperationNativeQuery)
			}
			require.Equal(t, expected, ops)
			ref := nativeRecord(c.Record(s.Pages[0].Records[0]), s).Reference
			got, getErr := provider.(resource.Getter).Get(ctx, resource.GetRequest{Route: resource.AccessRoute{Connection: s.Name}, Reference: ref, Representation: "native"})
			if hasGet {
				require.NoError(t, getErr)
				require.Equal(t, nativeRecord(c.Record(s.Pages[0].Records[0]), s), got)
			} else {
				requireNativeError(t, getErr, "UNSUPPORTED")
			}
			_, enumErr := provider.(resource.Enumerator).Enumerate(ctx, resource.EnumerateRequest{Target: target(s, c)})
			if hasEnum {
				require.NoError(t, enumErr)
			} else {
				requireNativeError(t, enumErr, "UNSUPPORTED")
			}
			input := resource.Value{Kind: "int64", Int: "9007199254740993"}
			page, queryErr := provider.(resource.NativeQuerier).NativeQuery(ctx, resource.NativeQueryRequest{Target: target(s, c), Language: "fixture.compute.v1", Query: input})
			if !hasQuery {
				requireNativeError(t, queryErr, "UNSUPPORTED")
				return
			}
			require.NoError(t, queryErr)
			computed := page.Records[0].Body
			require.Equal(t, resource.Value{Kind: "int64", Int: "9007199254740994"}, computed.Map["computed"])
			require.NotEqual(t, input, computed)
			require.Equal(t, resource.Value{Kind: "timestamp", Timestamp: "1969-12-31T23:59:59.999999999Z"}, computed.Map["beforeEpoch"])
			require.Equal(t, []resource.Value{{Kind: "uint64", Uint: "18446744073709551615"}, {Kind: "null"}, {Kind: "bool", Bool: true}}, computed.Map["nested"].List)
			raw, err := json.Marshal(computed)
			require.NoError(t, err)
			var decoded resource.Value
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.Equal(t, computed, decoded)
		})
	}
}

func TestCurrentSDKHostIssuedAuthority(t *testing.T) {
	for _, profile := range []string{"read-only", "native-query-only"} {
		t.Run(profile, func(t *testing.T) {
			provider, _, stop := startNativeProfile(t, profile)
			defer stop()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := NativeCorpus()
			s := c.Scenarios[0]
			_, err := provider.(resource.ConnectionLifecycle).StartConnection(ctx, s.Name)
			require.NoError(t, err)
			admitted := target(s, c)
			admitted.Authority = "host-issued:isolated/雪"
			var page resource.SourcePage
			if profile == "read-only" {
				page, err = provider.(resource.Enumerator).Enumerate(ctx, resource.EnumerateRequest{Target: admitted})
			} else {
				page, err = provider.(resource.NativeQuerier).NativeQuery(ctx, resource.NativeQueryRequest{Target: admitted, Language: "fixture.compute.v1", Query: resource.Value{Kind: "int64", Int: "1"}})
			}
			require.NoError(t, err)
			require.NotEmpty(t, page.Records)
			for _, record := range page.Records {
				require.Equal(t, admitted.Authority, record.Reference.Address.Authority)
			}
			if profile == "read-only" {
				record := page.Records[0]
				got, err := provider.(resource.Getter).Get(ctx, resource.GetRequest{Route: admitted.Route, Reference: record.Reference, Representation: "native"})
				require.NoError(t, err)
				require.Equal(t, record, got)
			}
		})
	}
}
