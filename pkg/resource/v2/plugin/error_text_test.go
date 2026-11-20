package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

// kubernetesError is one classification of the Kubernetes plugin
// (omniview-plugin-kubernetes, pkg/plugin/resource/errors.go): fixed public text
// per class; the native diagnostic travels only in Err.
type kubernetesError struct {
	code, title, message string
	status               codes.Code
}

// The thirteen classes that plugin emits. The first four are the ones the UI
// work reported; the rest complete the set the diagnosis measured.
var kubernetesErrors = []kubernetesError{
	{"NOT_FOUND", "Resource not found", "The requested resource or API group was not found.", codes.NotFound},
	{"FORBIDDEN", "Access denied", "You do not have permission to perform this resource operation.", codes.PermissionDenied},
	{"CONFLICT", "Resource conflict", "The resource operation conflicts with the current cluster state.", codes.Aborted},
	{"TIMEOUT", "Request timed out", "The cluster did not complete the request in time.", codes.DeadlineExceeded},
	{"UNAUTHORIZED", "Authentication failed", "The cluster could not authenticate the request.", codes.Unauthenticated},
	{"INVALID_REQUEST", "Invalid request", "The cluster rejected the resource request.", codes.InvalidArgument},
	{"UNSUPPORTED", "Unsupported operation", "The cluster does not support this resource operation.", codes.Unimplemented},
	{"CONTINUATION_EXPIRED", "Source continuation expired", "The resource listing continuation has expired. Start a new listing.", codes.FailedPrecondition},
	{"RESOURCE_LIMIT", "Resource limit", "The request exceeded a cluster resource or rate limit.", codes.ResourceExhausted},
	{"UNAVAILABLE", "Source unavailable", "The cluster is temporarily unavailable.", codes.Unavailable},
	{"CONNECTION_ERROR", "Connection error", "Unable to connect to the cluster.", codes.Unavailable},
	{"CERTIFICATE_ERROR", "Certificate error", "The cluster certificate could not be verified.", codes.Unavailable},
	{"PROVIDER_ERROR", "Cluster error", "The cluster could not complete the request.", codes.Internal},
}

const nativeDiagnostic = "Get \"https://10.0.0.1:6443/api/v1/pods?token=NATIVE-SECRET\": x509: certificate signed by unknown authority (/Users/someone/.kube/config)"

func (k kubernetesError) typed() *resourcev2.ResourceOperationError {
	return &resourcev2.ResourceOperationError{
		Err:         errors.New(nativeDiagnostic),
		Code:        k.code,
		Title:       k.title,
		Message:     k.message,
		Suggestions: []string{"Check your network connection"},
	}
}

type failingCatalogProvider struct{ err error }

func (p *failingCatalogProvider) Catalog(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	return resourcev2.Catalog{}, p.err
}

// catalogError sends err through a real gRPC pair (server encoder, trailers,
// client decoder) and returns what the host-side client hands to its caller.
func catalogError(t *testing.T, err error) error {
	t.Helper()
	s := grpc.NewServer()
	RegisterServer(s, &failingCatalogProvider{err: err})
	_, got := NewClient(pipe(t, s)).Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: "c"}})
	if got == nil {
		t.Fatal("provider error was not delivered")
	}
	return got
}

// receivedStatus is the gRPC status the client received, before any decoding.
// The decoded error keeps it in Err.
func receivedStatus(t *testing.T, err error) *status.Status {
	t.Helper()
	var typed *resourcev2.ResourceOperationError
	if !errors.As(err, &typed) {
		t.Fatalf("not a classified resource error: %v", err)
	}
	st, ok := status.FromError(typed.Err)
	if !ok {
		t.Fatalf("no gRPC status behind %v", err)
	}
	return st
}

func TestProviderSentenceCrossesTheResourceTransport(t *testing.T) {
	for _, k := range kubernetesErrors {
		t.Run(k.code, func(t *testing.T) {
			got := catalogError(t, k.typed())
			var typed *resourcev2.ResourceOperationError
			if !errors.As(got, &typed) {
				t.Fatalf("classification lost: %v", got)
			}
			if typed.Code != k.code || status.Code(got) != k.status {
				t.Fatalf("class %s over status %s, want %s over %s", typed.Code, status.Code(got), k.code, k.status)
			}
			if typed.Message != k.message {
				t.Fatalf("the provider's sentence did not arrive: %q, want %q", typed.Message, k.message)
			}
			// The class is the title and the suggestions are the host's to give:
			// neither crosses.
			if typed.Title != "" || len(typed.Suggestions) != 0 {
				t.Fatalf("title or suggestions crossed: %+v", typed)
			}
		})
	}
}

func TestNativeDiagnosticAndStatusTextStayOffTheWire(t *testing.T) {
	for _, k := range kubernetesErrors {
		t.Run(k.code, func(t *testing.T) {
			st := receivedStatus(t, catalogError(t, k.typed()))
			if st.Message() != "resource operation failed" {
				t.Fatalf("status text carries provider text: %q", st.Message())
			}
			raw, err := proto.Marshal(st.Proto())
			if err != nil {
				t.Fatal(err)
			}
			for _, leak := range []string{"NATIVE-SECRET", "/Users/someone", "x509", k.title, "Check your network connection"} {
				if strings.Contains(string(raw), leak) {
					t.Fatalf("%q reached the wire", leak)
				}
			}
		})
	}
}
