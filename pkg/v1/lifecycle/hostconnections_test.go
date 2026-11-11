package lifecycle

import (
	"context"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/hostconnections"
	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHostConnectionsRejectsIncompatibleBindings(t *testing.T) {
	if hostconnections.Version != 2 {
		t.Fatalf("host connections version = %d, want 2", hostconnections.Version)
	}
	server := &Server{HostConnections: &hostconnections.Binding{}}
	for _, version := range []uint32{1, hostconnections.Version + 1} {
		_, err := server.BindHostServices(context.Background(), &lifecyclepb.BindHostServicesRequest{
			Version: version, HostConnectionsBrokerId: 1,
		})
		if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "Unsupported host connections binding." {
			t.Fatalf("version %d reached callback binding: %v", version, err)
		}
	}
}
