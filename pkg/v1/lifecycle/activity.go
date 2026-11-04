package lifecycle

import (
	"context"
	"errors"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/activity"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/activity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type activityBindingServer struct {
	pb.UnimplementedActivityBindingServer
	owner *Server
}

func (s *activityBindingServer) Bind(_ context.Context, req *pb.BindRequest) (*pb.BindResponse, error) {
	owner := s.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if req.Version != activity.Version || req.BrokerId == 0 {
		return nil, status.Error(codes.FailedPrecondition, "Unsupported activity binding.")
	}
	if owner.broker == nil || owner.Activities == nil {
		return nil, status.Error(codes.FailedPrecondition, "Activity reporter is unavailable.")
	}
	conn, err := owner.broker.Dial(req.BrokerId)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Activity reporter is unavailable.")
	}
	if err := owner.Activities.Bind(conn); err != nil {
		conn.Close()
		return nil, status.Error(codes.FailedPrecondition, "Activity reporter is already bound.")
	}
	return &pb.BindResponse{Version: activity.Version}, nil
}

// ServeActivities negotiates a process-scoped reporting backchannel independent of
// delegated host connections. Context bounds negotiation only. Call stop when the
// process retires. An uncertain binding failure requires retiring the process.
func (c *Client) ServeActivities(ctx context.Context, reporter activity.Reporter) (stop func(), err error) {
	if c == nil || c.activityClient == nil || c.broker == nil || reporter == nil {
		return nil, errors.New("activity broker is unavailable")
	}
	var mu sync.Mutex
	var once sync.Once
	var server *grpc.Server
	stopped := false
	stop = func() {
		once.Do(func() {
			mu.Lock()
			stopped = true
			current := server
			mu.Unlock()
			if current != nil {
				current.Stop()
			}
		})
	}
	id := c.broker.NextId()
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.broker.AcceptAndServe(id, func(options []grpc.ServerOption) *grpc.Server {
			current := grpc.NewServer(options...)
			activity.RegisterServer(current, reporter)
			mu.Lock()
			server = current
			alreadyStopped := stopped
			mu.Unlock()
			if alreadyStopped {
				current.Stop()
			}
			close(ready)
			return current
		})
	}()
	select {
	case <-ready:
	case <-done:
		stop()
		return nil, errors.New("activity callback server could not start")
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
	response, err := c.activityClient.Bind(ctx, &pb.BindRequest{
		Version:  activity.Version,
		BrokerId: id,
	})
	if err != nil {
		stop()
		return nil, err
	}
	if response.Version != activity.Version {
		stop()
		return nil, errors.New("unsupported activity version")
	}
	return stop, nil
}
