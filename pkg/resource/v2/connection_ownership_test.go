package resourcev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

type ownershipEndpointClient struct {
	endpoint string
	http     *http.Client
}

type ownershipCredentials struct {
	Tokens []string          `json:"-"`
	Extra  map[string][]byte `json:"-"`
}

func ownershipToken(connection types.Connection) string {
	return connection.GetSensitiveData()["credentials"].(ownershipCredentials).Tokens[0]
}

func ownershipConnection(endpoint string) types.Connection {
	connection := types.Connection{ID: "route", Data: map[string]any{"transport": map[string]any{"urls": []any{endpoint}}}, Labels: map[string]any{"groups": []any{map[string]any{"name": "original"}}}}
	return connection
}

// Credential fixtures stay separate from display-only overlays, whose empty
// sensitive data intentionally preserves the native connection credentials.
func credentialOwnershipConnection(endpoint string) types.Connection {
	connection := ownershipConnection(endpoint)
	connection.SetSensitiveData(map[string]any{"credentials": ownershipCredentials{
		Tokens: []string{endpoint},
		Extra:  map[string][]byte{"secret": []byte(endpoint)},
	}})
	return connection
}
func ownershipEndpoint(connection types.Connection) string {
	return connection.Data["transport"].(map[string]any)["urls"].([]any)[0].(string)
}
func mutateOwnershipConnection(connection types.Connection, endpoint string) {
	connection.Data["transport"].(map[string]any)["urls"].([]any)[0] = endpoint
	connection.Labels["groups"].([]any)[0].(map[string]any)["name"] = "edited"
	credentials := connection.GetSensitiveData()["credentials"].(ownershipCredentials)
	credentials.Tokens[0] = endpoint
	credentials.Extra["secret"][0] = 'X'
}

// The old native HTTP request must actually be canceled, destroyed, and replaced;
// comparing a callback's echoed input would not prove source invalidation.
func TestConnectionOwnershipInvalidation(t *testing.T) {
	for _, boundary := range []string{"get", "list", "nativeSnapshot", "startStatus", "checkStatus", "nativeInput", "overlayInput", "updateInput", "updateResult"} {
		t.Run(boundary, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			nativeCanceled := make(chan struct{}, 1)
			oldSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/block" {
					entered <- struct{}{}
					<-r.Context().Done()
					nativeCanceled <- struct{}{}
					return
				}
				_, _ = io.WriteString(w, "old-source")
			}))
			t.Cleanup(oldSource.Close)
			newSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "new-source") }))
			t.Cleanup(newSource.Close)
			input := []types.Connection{credentialOwnershipConnection(oldSource.URL)}
			var destroyed atomic.Int32
			cfg := Config[ownershipEndpointClient]{Catalog: func(context.Context, CatalogRequest) (Catalog, error) { return Catalog{}, nil }, Lifecycle: &Lifecycle[ownershipEndpointClient]{
				LoadConnections: func(context.Context) ([]types.Connection, error) { return input, nil },
				CreateClient: func(_ context.Context, connection types.Connection) (*ownershipEndpointClient, error) {
					return &ownershipEndpointClient{endpoint: ownershipEndpoint(connection), http: oldSource.Client()}, nil
				},
				DestroyClient: func(context.Context, *ownershipEndpointClient) error { destroyed.Add(1); return nil },
			}}
			controller, err := NewController(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := controller.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := controller.LoadConnections(context.Background()); err != nil {
				t.Fatal(err)
			}
			overlay := ConnectionOverlay{Upserts: []types.Connection{credentialOwnershipConnection(oldSource.URL)}}
			var updateInput types.Connection
			var updateResult types.Connection
			if boundary == "overlayInput" {
				if err := controller.ApplyConnectionOverlay(context.Background(), overlay); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "updateInput" || boundary == "updateResult" {
				updateInput = credentialOwnershipConnection(oldSource.URL)
				updateResult, err = controller.UpdateConnection(context.Background(), updateInput)
				if err != nil {
					t.Fatal(err)
				}
			}
			started, err := controller.StartConnection(context.Background(), "route")
			if err != nil {
				t.Fatal(err)
			}
			blocked := make(chan error, 1)
			go func() {
				blocked <- controller.WithClient(context.Background(), AccessRoute{Connection: "route"}, func(ctx context.Context, client *ownershipEndpointClient) error {
					request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint+"/block", nil)
					if err != nil {
						return err
					}
					response, err := client.http.Do(request)
					if err != nil {
						return err
					}
					defer response.Body.Close()
					_, err = io.Copy(io.Discard, response.Body)
					return err
				})
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("native old-source request did not start")
			}
			var edit types.Connection
			switch boundary {
			case "get":
				edit, err = controller.GetConnection(context.Background(), "route")
			case "list":
				var list []types.Connection
				list, err = controller.ListConnections(context.Background())
				if len(list) > 0 {
					edit = list[0]
				}
			case "nativeSnapshot":
				var list []types.Connection
				list, err = controller.NativeConnections(context.Background())
				if len(list) > 0 {
					edit = list[0]
				}
			case "startStatus":
				edit = *started.Connection
			case "checkStatus":
				var checked types.ConnectionStatus
				checked, err = controller.CheckConnection(context.Background(), "route")
				if checked.Connection != nil {
					edit = *checked.Connection
				}
			case "nativeInput":
				edit = input[0]
			case "overlayInput":
				edit = overlay.Upserts[0]
			case "updateInput":
				edit = updateInput
			case "updateResult":
				edit = updateResult
			}
			if err != nil {
				t.Fatal(err)
			}
			mutateOwnershipConnection(edit, newSource.URL)
			before, err := controller.GetConnection(context.Background(), "route")
			if err != nil {
				t.Fatal(err)
			}
			if got := ownershipEndpoint(before); got != oldSource.URL {
				t.Errorf("%s leaked nested config mutation into inventory before apply: %s", boundary, got)
			}
			if ownershipToken(before) != oldSource.URL || string(before.GetSensitiveData()["credentials"].(ownershipCredentials).Extra["secret"]) != oldSource.URL {
				t.Errorf("%s leaked hidden credentials into inventory before apply", boundary)
			}
			if _, err := controller.UpdateConnection(context.Background(), edit); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-blocked:
				if err == nil {
					t.Error("retired native request unexpectedly succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Error("configuration apply did not cancel old native request")
			}
			select {
			case <-nativeCanceled:
			case <-time.After(3 * time.Second):
				t.Error("old HTTP source did not observe request cancellation")
			}
			if destroyed.Load() != 1 {
				t.Errorf("old client destroy count=%d, want1", destroyed.Load())
			}
			if _, err := controller.StartConnection(context.Background(), "route"); err != nil {
				t.Fatal(err)
			}
			var body string
			err = controller.WithClient(context.Background(), AccessRoute{Connection: "route"}, func(ctx context.Context, client *ownershipEndpointClient) error {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint, nil)
				if err != nil {
					return err
				}
				response, err := client.http.Do(request)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				data, err := io.ReadAll(response.Body)
				body = string(data)
				return err
			})
			if err != nil || body != "new-source" {
				t.Errorf("replacement read reached %q, want new-source: %v", body, err)
			}
		})
	}
}

func TestConnectionOwnershipPreservesNestedNativeValues(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	controller := newTestController(t, cfg)
	auto := &types.ConnectionAutoConnect{Enabled: true, Triggers: []types.ConnectionAutoConnectTrigger{types.ConnectionAutoConnectTriggerPluginStart}}
	typed := map[string][]string{"tokens": {"original"}}
	raw := json.RawMessage(`{"opaque":1}`)
	number := json.Number("18446744073709551615")
	input := types.Connection{ID: "explicit", Data: map[string]any{"typed": typed, "raw": raw, "signed": int64(-9223372036854775808), "unsigned": uint64(18446744073709551615), "number": number, "negativeZero": math.Copysign(0, -1), "nilMap": map[string]any(nil), "nilSlice": []any(nil)}, Labels: map[string]any{"array": [1]map[string]string{{"name": "original"}}}, Lifecycle: types.ConnectionLifecycle{AutoConnect: auto}}
	input.SetSensitiveData(map[string]any{"credentials": map[string]any{"token": "original"}})
	if err := controller.ApplyConnections(context.Background(), []types.Connection{input}); err != nil {
		t.Fatal(err)
	}
	typed["tokens"][0] = "caller-edited"
	raw[2] = 'X'
	auto.Triggers[0] = types.ConnectionAutoConnectTriggerConnectionDiscovered
	input.GetSensitiveData()["credentials"].(map[string]any)["token"] = "caller-edited"
	out, err := controller.GetConnection(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Data["typed"].(map[string][]string)["tokens"][0]; got != "original" {
		t.Errorf("typed nested slice aliased: %s", got)
	}
	if string(out.Data["raw"].(json.RawMessage)) != `{"opaque":1}` {
		t.Error("raw message aliased")
	}
	if out.Lifecycle.AutoConnect.Triggers[0] != types.ConnectionAutoConnectTriggerPluginStart {
		t.Error("lifecycle triggers aliased")
	}
	if out.GetSensitiveData()["credentials"].(map[string]any)["token"] != "original" {
		t.Error("private credentials aliased")
	}
	for _, key := range []string{"signed", "unsigned", "number"} {
		if !reflect.DeepEqual(out.Data[key], input.Data[key]) {
			t.Errorf("native scalar changed at %s: %v", key, out.Data[key])
		}
	}
	if !math.Signbit(out.Data["negativeZero"].(float64)) {
		t.Error("double negative zero lost")
	}
	if out.Data["nilMap"].(map[string]any) != nil || out.Data["nilSlice"].([]any) != nil {
		t.Error("nil container semantics changed")
	}
	array := out.Labels["array"].([1]map[string]string)
	array[0]["name"] = "returned-edited"
	out.Data["typed"].(map[string][]string)["tokens"][0] = "returned-edited"
	out.GetSensitiveData()["credentials"].(map[string]any)["token"] = "returned-edited"
	out.Lifecycle.AutoConnect.Triggers[0] = types.ConnectionAutoConnectTriggerConnectionDiscovered
	next, err := controller.GetConnection(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(next.Labels["array"]) != "[map[name:original]]" || next.Data["typed"].(map[string][]string)["tokens"][0] != "original" || next.GetSensitiveData()["credentials"].(map[string]any)["token"] != "original" || next.Lifecycle.AutoConnect.Triggers[0] != types.ConnectionAutoConnectTriggerPluginStart {
		t.Error("returned nested containers or lifecycle remain aliased")
	}
}

func TestConnectionOwnershipCallbackAndSessionIsolation(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Lifecycle.LoadConnections = func(context.Context) ([]types.Connection, error) {
		return []types.Connection{credentialOwnershipConnection("original")}, nil
	}
	var retainedCreate types.Connection
	cfg.Lifecycle.CreateClient = func(ctx context.Context, connection types.Connection) (*string, error) {
		endpoint := ownershipEndpoint(connection)
		retainedCreate = connection
		mutateOwnershipConnection(connection, "create-callback")
		session, _ := SessionFromContext(ctx)
		if session.Connection == nil {
			return nil, errors.New("native create omitted connection context")
		}
		mutateOwnershipConnection(*session.Connection, "create-session")
		return &endpoint, nil
	}
	cfg.Lifecycle.OnStart = func(_ context.Context, connection types.Connection, _ *string) error {
		mutateOwnershipConnection(connection, "start-callback")
		return nil
	}
	var retainedCheck *types.Connection
	cfg.Lifecycle.CheckConnection = func(ctx context.Context, connection types.Connection, _ *string) (types.ConnectionStatus, error) {
		mutateOwnershipConnection(connection, "check-callback")
		retainedCheck = &connection
		session, _ := SessionFromContext(ctx)
		if session.Connection == nil {
			return types.ConnectionStatus{}, errors.New("native check omitted connection context")
		}
		return types.ConnectionStatus{Connection: &connection, Status: types.ConnectionStatusConnected}, nil
	}
	cfg.Lifecycle.OnStop = func(ctx context.Context, connection types.Connection, _ *string) error {
		mutateOwnershipConnection(connection, "stop-callback")
		session, _ := SessionFromContext(ctx)
		if session.Connection == nil {
			return errors.New("native stop omitted connection context")
		}
		mutateOwnershipConnection(*session.Connection, "stop-session")
		return nil
	}
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, err := c.StartConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	mutateOwnershipConnection(retainedCreate, "retained-create")
	if ownershipEndpoint(*started.Connection) != "original" || ownershipToken(*started.Connection) != "original" {
		t.Fatal("start result shares native creation configuration")
	}
	for range 2 {
		if err := c.WithClient(context.Background(), AccessRoute{Connection: "route"}, func(ctx context.Context, client *string) error {
			session, _ := SessionFromContext(ctx)
			if *client != "original" || ownershipEndpoint(*session.Connection) != "original" || ownershipToken(*session.Connection) != "original" {
				return errors.New("callback mutation changed active slot configuration")
			}
			mutateOwnershipConnection(*session.Connection, "request-session")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	checked, err := c.CheckConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	mutateOwnershipConnection(*retainedCheck, "retained-check")
	if ownershipEndpoint(*checked.Connection) != "check-callback" || ownershipToken(*checked.Connection) != "check-callback" {
		t.Fatal("check result aliases native callback-retained configuration")
	}
	stopped, err := c.StopConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := c.GetConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	if ownershipEndpoint(stopped) != "original" || ownershipEndpoint(stored) != "original" || ownershipToken(stopped) != "original" || ownershipToken(stored) != "original" || destroyed.Load() != 1 {
		t.Fatal("callbacks changed inventory, stop result, or client teardown")
	}
}

func TestHiddenCredentialUpdateRetiresClient(t *testing.T) {
	cfg := controllerConfig()
	var destroyed atomic.Int32
	cfg.Lifecycle = controllerLifecycle(&destroyed)
	cfg.Lifecycle.CreateClient = func(_ context.Context, connection types.Connection) (*string, error) {
		token := ownershipToken(connection)
		return &token, nil
	}
	c := newTestController(t, cfg)
	if err := c.ApplyConnections(context.Background(), []types.Connection{credentialOwnershipConnection("original")}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartConnection(context.Background(), "route"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	canceled := make(chan error, 1)
	go func() {
		canceled <- c.WithClient(context.Background(), AccessRoute{Connection: "route"}, func(ctx context.Context, _ *string) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("client callback did not start")
	}
	update, err := c.GetConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	update.GetSensitiveData()["credentials"].(ownershipCredentials).Tokens[0] = "replacement"
	stored, err := c.GetConnection(context.Background(), "route")
	if err != nil {
		t.Fatal(err)
	}
	if ownershipToken(stored) != "original" || destroyed.Load() != 0 {
		t.Fatal("editing hidden credential snapshot changed active configuration")
	}
	if SameConnectionClientConfig(stored, update) {
		t.Fatal("hidden credential update was not meaningful")
	}
	if _, err := c.UpdateConnection(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("retired callback error = %v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("credential update did not cancel old client")
	}
	if destroyed.Load() != 1 {
		t.Fatalf("destroy count = %d, want 1", destroyed.Load())
	}
	if _, err := c.StartConnection(context.Background(), "route"); err != nil {
		t.Fatal(err)
	}
	if err := c.WithClient(context.Background(), AccessRoute{Connection: "route"}, func(_ context.Context, client *string) error {
		if *client != "replacement" {
			return fmt.Errorf("client token = %q, want replacement", *client)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCloneConnectionPreservesOpaqueState(t *testing.T) {
	type privateState struct {
		Tokens []string `json:"-"`
		opaque []string
	}
	state := privateState{Tokens: []string{"original"}, opaque: []string{"native"}}
	handle := &ownershipEndpointClient{endpoint: "native"}
	connection := types.Connection{Data: map[string]any{"state": state}, Client: handle}
	cloned := CloneConnection(connection)
	copy := cloned.Data["state"].(privateState)
	copy.Tokens[0] = "edited"
	if state.Tokens[0] != "original" {
		t.Fatal("exported hidden credential shares storage")
	}
	if &copy.opaque[0] != &state.opaque[0] || cloned.Client != handle {
		t.Fatal("opaque implementation state or native handle was copied")
	}
}
