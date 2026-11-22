package exec_test

// What the host sees when a manager refuses a start because it has been closed. The host
// runs the plugin out of process and reaches its exec Manager through PluginClient and
// PluginServer, over gRPC: the refusal has to keep its classification across it, on both
// RPCs that start a session.

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// grpcCode is the status code a call returned, however the client wrapped it.
func grpcCode(err error) codes.Code {
	var withStatus interface{ GRPCStatus() *status.Status }
	if errors.As(err, &withStatus) {
		return withStatus.GRPCStatus().Code()
	}
	return codes.OK
}

func TestAClosedManagerKeepsItsRefusalOverGRPC(t *testing.T) {
	for _, kind := range bothKinds {
		t.Run(kind.String(), func(t *testing.T) {
			r := newRig(t)
			r.closeManager()
			client := nativeExecClient(t, r.m, nil)

			var (
				session *exec.Session
				err     error
			)
			if kind == startResource {
				session, err = client.CreateResourceSession(r.pluginContext(),
					exec.SessionOptions{ID: "late", ResourcePlugin: "test", ResourceKey: "pod"})
			} else {
				session, err = client.CreateSession(r.pluginContext(), exec.SessionOptions{ID: "late", Command: []string{"sh"}})
			}
			if session != nil || !isRefusal(err) {
				t.Fatalf("a start over gRPC after Close returned (%+v, %v); want the refusal CONNECTION_ERROR, classified: got code %q",
					session, err, resourcev2.ErrorCode(err))
			}
			if code := grpcCode(err); code != codes.Unavailable {
				t.Errorf("the refusal reached the client with gRPC code %v, want Unavailable", code)
			}
			r.assertDrained(t)
		})
	}
}

// What the refusal changed for the legacy RPC is the classification of a failure the manager
// classified. A failure it did not classify reaches the client as it always did.
func TestAnUnclassifiedLegacyFailureKeepsItsStatusOverGRPC(t *testing.T) {
	r := newRig(t)
	client := nativeExecClient(t, r.m, nil)

	_, err := client.CreateSession(r.pluginContext(), exec.SessionOptions{ID: "x", Params: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("a legacy start of an open manager: %v", err)
	}
	// An ID that is taken is the manager's own classification (SESSION_EXISTS): AlreadyExists.
	_, err = client.CreateSession(r.pluginContext(), exec.SessionOptions{ID: "x"})
	if code := grpcCode(err); code != codes.AlreadyExists || isRefusal(err) {
		t.Fatalf("a start of a taken ID over gRPC: code %v, err %v; want AlreadyExists, and not the refusal", code, err)
	}
}
