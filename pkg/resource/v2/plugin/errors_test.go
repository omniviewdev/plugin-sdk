package plugin

import (
	"context"
	"errors"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

// Cancellation and frame exhaustion can originate in gRPC before typed details exist.
func testErrorClass(err error) string {
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) {
		return resourcev2.ErrorCode(err)
	}
	switch status.Code(err) {
	case codes.Canceled:
		return "CANCELLED"
	case codes.DeadlineExceeded:
		return "TIMEOUT"
	case codes.ResourceExhausted:
		return "RESOURCE_LIMIT"
	}
	return resourcev2.ErrorCode(err)
}

type outageProvider struct {
	countProvider
	declared bool
	ordinary bool
}

func (p *outageProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	if p.ordinary {
		return resourcev2.Catalog{}, errors.New("native secret diagnostic")
	}
	if p.declared {
		return resourcev2.Catalog{}, resourcev2.NewError("UNAVAILABLE", "declared provider outage")
	}
	return resourcev2.Catalog{}, status.Error(codes.Unavailable, "process transport lost")
}
func TestActualGRPCTransportLossDiffersFromDeclaredOutage(t *testing.T) {
	for _, declared := range []bool{false, true} {
		s := grpc.NewServer()
		RegisterServer(s, &outageProvider{declared: declared})
		_, err := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("grpc status changed: %v", err)
		}
		var typed *resourcev2.ResourceOperationError
		if errors.As(err, &typed) != declared {
			t.Fatalf("typed provider classification = %v, want %v", typed, declared)
		}
		if declared && (typed.Code != "UNAVAILABLE" || typed.Err == nil) {
			t.Fatal("declared provider class or transport cause lost")
		}
	}
}
func TestOrdinaryProviderErrorClassifiesProviderError(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &outageProvider{ordinary: true})
	_, err := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
	if resourcev2.ErrorCode(err) != "PROVIDER_ERROR" {
		t.Fatalf("ordinary provider error class: %v", err)
	}
}

type unknownReasonServer struct {
	resourcepb.UnimplementedResourceProviderServer
}

func (*unknownReasonServer) Call(context.Context, *resourcepb.Request) (*resourcepb.Response, error) {
	s, err := status.New(codes.Unavailable, "remote diagnostic").WithDetails(&errdetails.ErrorInfo{Reason: "HOST_ROOT_GRANT", Domain: "omniview.resource.v2"})
	if err != nil {
		return nil, err
	}
	return nil, s.Err()
}
func TestUnknownRemoteErrorReasonCannotCreateProviderClass(t *testing.T) {
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, &unknownReasonServer{})
	_, err := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
	if status.Code(err) != codes.Internal {
		t.Fatalf("unknown reason status: %v", err)
	}
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) {
		t.Fatal("unknown reason became a typed provider class")
	}
}

type actionCatalogServer struct {
	resourcepb.UnimplementedResourceProviderServer
}

func (*actionCatalogServer) Call(ctx context.Context, _ *resourcepb.Request) (*resourcepb.Response, error) {
	p := &streamProvider{}
	v, err := p.Catalog(ctx, resourcev2.CatalogRequest{})
	if err != nil {
		return nil, err
	}
	catalog, err := resourcev2.ToProtoCatalog(v)
	return &resourcepb.Response{Version: 2, Result: &resourcepb.Response_Catalog{Catalog: catalog}}, err
}

type unknownActionCodeServer struct {
	resourcepb.UnimplementedResourceAncillaryServer
}

func (*unknownActionCodeServer) StreamAction(_ *resourcepb.ActionStreamRequest, s resourcepb.ResourceAncillary_StreamActionServer) error {
	if err := s.Send(&resourcepb.ActionStreamEvent{Version: 2, Type: "ready"}); err != nil {
		return err
	}
	return s.Send(&resourcepb.ActionStreamEvent{Version: 2, Type: "error", ErrorCode: "HOST_ROOT_GRANT"})
}
func TestUnknownActionErrorCodeRejectedBeforeDelivery(t *testing.T) {
	s := grpc.NewServer()
	resourcepb.RegisterResourceProviderServer(s, &actionCatalogServer{})
	resourcepb.RegisterResourceAncillaryServer(s, &unknownActionCodeServer{})
	out := make(chan resourcev2.ActionEvent, 2)
	err := NewClient(pipe(t, s)).StreamAction(context.Background(), actionInput(), out)
	if resourcev2.ErrorCode(err) != "INTERNAL" {
		t.Fatalf("unknown action code: %v", err)
	}
	if len(out) != 1 || (<-out).Type != "ready" {
		t.Fatal("untrusted action error was delivered")
	}
}
func TestActualStoppedGRPCTransportRemainsUntyped(t *testing.T) {
	s := grpc.NewServer()
	RegisterServer(s, &countProvider{})
	c := NewClient(pipe(t, s))
	s.Stop()
	_, err := c.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("transport loss status: %v", err)
	}
	var typed *resourcev2.ResourceOperationError
	if errors.As(err, &typed) {
		t.Fatal("process transport loss became a provider outage")
	}
}
func TestC02StatusMappingsPreserveExactTypedClass(t *testing.T) {
	for _, tc := range []struct {
		code string
		grpc codes.Code
	}{{"PERMISSION_DENIED", codes.PermissionDenied}, {"FORBIDDEN", codes.PermissionDenied}, {"INVALID_REQUEST", codes.InvalidArgument}, {"INVALID_INPUT", codes.InvalidArgument}, {"FILTER_UNKNOWN_FIELD", codes.InvalidArgument}, {"FILTER_INVALID_OPERATOR", codes.InvalidArgument}, {"ALREADY_EXISTS", codes.AlreadyExists}, {"UNAVAILABLE", codes.Unavailable}, {"CONNECTION_ERROR", codes.Unavailable}, {"CERTIFICATE_ERROR", codes.Unavailable}, {"CONTINUATION_EXPIRED", codes.FailedPrecondition}, {"RESET_REQUIRED", codes.FailedPrecondition}, {"PARTIAL_RESULT", codes.FailedPrecondition}} {
		wire := rpcError(resourcev2.NewError(tc.code, "unsafe provider diagnostic"))
		if status.Code(wire) != tc.grpc {
			t.Fatalf("%s status %s want %s", tc.code, status.Code(wire), tc.grpc)
		}
		decoded := domainError(wire)
		var typed *resourcev2.ResourceOperationError
		if !errors.As(decoded, &typed) || typed.Code != tc.code || typed.Err == nil {
			t.Fatalf("class or cause lost for %s: %v", tc.code, decoded)
		}
	}
}
