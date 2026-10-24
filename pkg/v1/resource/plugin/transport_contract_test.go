package plugin

import (
	"context"
	"errors"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
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
		_, err = incomingSession(metadata.NewIncomingContext(context.Background(), metadata.MD{"omniview-request-id": values}))
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
