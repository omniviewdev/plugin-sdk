package lifecycle

import (
	"context"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/activity"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/activity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestActivityNegotiationRejectsUnsupportedAndUnavailableBindings(t *testing.T) {
	server := &activityBindingServer{owner: &Server{Activities: &activity.Binding{}}}
	for _, request := range []*pb.BindRequest{
		{Version: activity.Version + 1, BrokerId: 1},
		{Version: activity.Version, BrokerId: 0},
		{Version: activity.Version, BrokerId: 1},
	} {
		if _, err := server.Bind(context.Background(), request); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("unexpected binding result: %v", err)
		}
	}
}

func TestActivityCallbacksRequireProcessBroker(t *testing.T) {
	var client *Client
	if stop, err := client.ServeActivities(context.Background(), &activity.Binding{}); err == nil || stop != nil {
		t.Fatal("uninitialized lifecycle admitted callback server")
	}
}
