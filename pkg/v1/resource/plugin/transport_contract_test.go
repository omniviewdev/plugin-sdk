package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
	"google.golang.org/grpc/metadata"
)

func TestTypedStatusPreservesAllResourceErrorCodes(t *testing.T) {
	for _, code := range []string{"FORBIDDEN", "UNAUTHORIZED", "INVALID_REQUEST", "UNSUPPORTED", "PARTIAL_RESULT", "RESOURCE_LIMIT", "CONTINUATION_EXPIRED", "RESET_REQUIRED", "CANCELLED", "TIMEOUT", "PROVIDER_ERROR"} {
		t.Run(code, func(t *testing.T) {
			got := decodeRPCError(encodeRPCError(&resource.ResourceOperationError{Code: code, Title: "Public", Message: "Safe"}))
			var roe *resource.ResourceOperationError
			if !errors.As(got, &roe) || roe.Code != code {
				t.Fatalf("classification lost: %T %v", got, got)
			}
		})
	}
}

func TestDiagnosticMetadataRestoresSessionButNotAuthority(t *testing.T) {
	ctx := resource.WithSession(context.Background(), &resource.Session{RequestID: "host-request", RequesterID: "trusted-ui"})
	out := outgoingSession(ctx)
	md, _ := metadata.FromOutgoingContext(out)
	session, err := incomingSession(metadata.NewIncomingContext(context.Background(), md))
	if err != nil || session.RequestID != "host-request" || session.RequesterID != "trusted-ui" || session.Connection != nil {
		t.Fatalf("invalid diagnostic session: %+v %v", session, err)
	}
	for _, values := range [][]string{{"one", "two"}, {string(make([]byte, 257))}} {
		_, err = incomingSession(metadata.NewIncomingContext(context.Background(), metadata.MD{requestIDKey: values}))
		var roe *resource.ResourceOperationError
		if !errors.As(err, &roe) || roe.Code != "INVALID_REQUEST" {
			t.Fatalf("malformed correlation accepted: %v", err)
		}
	}
}

func TestCancellationTransportIsNotInternal(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{context.Canceled, "CANCELLED"}, {context.DeadlineExceeded, "TIMEOUT"}} {
		var roe *resource.ResourceOperationError
		if err := decodeRPCError(encodeRPCError(tc.err)); !errors.As(err, &roe) || roe.Code != tc.code {
			t.Fatalf("cancellation meaning lost: %v", err)
		}
	}
}

func TestTypedActionErrorCodecDoesNotDiscardDenial(t *testing.T) {
	wire, err := actionEventToProto(resource.ActionEvent{Type: "error", Error: &resource.ResourceOperationError{Code: "FORBIDDEN", Title: "Denied", Message: "safe"}})
	if err != nil {
		t.Fatal(err)
	}
	got := actionEventFromProto(wire)
	if got.Error == nil || got.Error.Code != "FORBIDDEN" {
		t.Fatalf("typed event error lost: %+v", got)
	}
}

func TestDiagnosticMetadataThroughActualGRPC(t *testing.T) {
	for _, tc := range []struct { name, requestID, requesterID string; invalid bool }{
		{name: "unicode", requestID: "request-é\n", requesterID: "调用者"},
		{name: "maximum bytes", requestID: strings.Repeat("é", 128), requesterID: strings.Repeat("x", 256)},
		{name: "invalid request UTF8", requestID: string([]byte{255}), invalid: true},
		{name: "invalid requester UTF8", requesterID: string([]byte{255}), invalid: true},
		{name: "request too long", requestID: strings.Repeat("x", 257), invalid: true},
		{name: "requester too long", requesterID: strings.Repeat("é", 129), invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan resource.Session, 1)
			provider := resourcetest.NewTestProvider(t, resourcetest.WithLoadConnectionsFunc(func(ctx context.Context) ([]types.Connection, error) {
				seen <- *resource.SessionFromContext(ctx)
				return nil, nil
			}))
			client := setupBufconn(t, provider)
			ctx := resource.WithSession(context.Background(), &resource.Session{RequestID: tc.requestID, RequesterID: tc.requesterID})
			_, err := client.LoadConnections(ctx)
			if tc.invalid {
				var typed *resource.ResourceOperationError
				if !errors.As(err, &typed) || typed.Code != "INVALID_REQUEST" { t.Fatalf("invalid label was not classified: %v", err) }
				select { case <-seen: t.Fatal("invalid metadata reached provider"); default: }
			} else {
				if err != nil { t.Fatal(err) }
				session := <-seen
				if session.RequestID != tc.requestID || session.RequesterID != tc.requesterID { t.Fatal("UTF8 labels changed across actual transport") }
			}
		})
	}
	for _, key := range []string{requestIDKey, requesterIDKey} {
		t.Run("duplicate "+key, func(t *testing.T) {
			seen := make(chan struct{}, 1)
			provider := resourcetest.NewTestProvider(t, resourcetest.WithLoadConnectionsFunc(func(context.Context) ([]types.Connection, error) { seen <- struct{}{}; return nil, nil }))
			client := setupBufconn(t, provider)
			ctx := metadata.AppendToOutgoingContext(context.Background(), key, "one", key, "two")
			_, err := client.LoadConnections(ctx)
			var typed *resource.ResourceOperationError
			if !errors.As(err, &typed) || typed.Code != "INVALID_REQUEST" { t.Fatalf("duplicate labels accepted: %v", err) }
			select { case <-seen: t.Fatal("duplicate metadata reached provider"); default: }
		})
	}
}

func TestRegisteredLifecycleConsumesCorrelationThroughActualGRPC(t *testing.T) {
	type observed struct { operation string; session resource.Session }
	seen := make(chan observed, 16)
	connections := &resourcetest.StubConnectionProvider[string]{
		CreateClientFunc: func(ctx context.Context) (*string, error) { seen <- observed{"create", *resource.SessionFromContext(ctx)}; return new(string), nil },
		DestroyClientFunc: func(ctx context.Context, _ *string) error { seen <- observed{"destroy", *resource.SessionFromContext(ctx)}; return nil },
	}
	provider, err := resource.BuildResourceController(context.Background(), resource.ResourcePluginConfig[string]{Connections: connections, Resources: []resource.ResourceRegistration[string]{{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.StubResourcer[string]{}}}})
	if err != nil { t.Fatal(err) }
	defer provider.Close()
	client := setupBufconn(t, provider)
	if _, err = client.LoadConnections(context.Background()); err != nil { t.Fatal(err) }
	call := func(id string, operations []string, invoke func(context.Context) error) {
		t.Helper()
		original := &resource.Session{RequestID: id, RequesterID: "调用者", Connection: &types.Connection{ID: "caller-owned"}}
		if err := invoke(resource.WithSession(context.Background(), original)); err != nil { t.Fatal(err) }
		for _, operation := range operations {
			select {
			case actual := <-seen:
				if actual.operation != operation || actual.session.RequestID != id || actual.session.RequesterID != original.RequesterID || actual.session.Connection == nil || actual.session.Connection.ID != "conn-1" { t.Fatalf("lifecycle consumer lost correlation: %+v", actual) }
			case <-time.After(time.Second): t.Fatal("lifecycle consumer was not invoked")
			}
		}
		if original.Connection.ID != "caller-owned" || original.RequestID != id { t.Fatal("caller session was mutated") }
	}
	call("start", []string{"create"}, func(ctx context.Context) error { _, err := client.StartConnection(ctx, "conn-1"); return err })
	call("update", []string{"create", "destroy"}, func(ctx context.Context) error { _, err := client.UpdateConnection(ctx, types.Connection{ID: "conn-1", Name: "Changed"}); return err })
	call("stop", []string{"destroy"}, func(ctx context.Context) error { _, err := client.StopConnection(ctx, "conn-1"); return err })
	call("restart", []string{"create"}, func(ctx context.Context) error { _, err := client.StartConnection(ctx, "conn-1"); return err })
	call("delete", []string{"destroy"}, func(ctx context.Context) error { return client.DeleteConnection(ctx, "conn-1") })
}
