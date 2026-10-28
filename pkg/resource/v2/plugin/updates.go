package plugin

import (
	"context"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"google.golang.org/grpc"
)

type updatesServer struct {
	resourcepb.UnimplementedResourceUpdatesServer
	owner *server
}

func registerUpdates(s *grpc.Server, owner *server) {
	d := resourcepb.ResourceUpdates_ServiceDesc
	d.Methods = []grpc.MethodDesc{{MethodName: "Call", Handler: updateCallHandler}}
	d.Streams = []grpc.StreamDesc{{StreamName: "Listen", Handler: listenUpdatesHandler, ServerStreams: true}}
	s.RegisterService(&d, &updatesServer{owner: owner})
}
func updateCallHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, i grpc.UnaryServerInterceptor) (interface{}, error) {
	r := &resourcepb.UpdateRequest{}
	e := &envelope{message: r}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed update request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	f := func(ctx context.Context, v interface{}) (interface{}, error) {
		s := srv.(*updatesServer)
		ctx, err := incomingSession(ctx, s.owner.settings)
		if err != nil {
			return nil, rpcError(err)
		}
		out, err := s.call(ctx, v.(*resourcepb.UpdateRequest))
		err = responseError(out, err)
		return out, rpcError(s.owner.classify(err))
	}
	if i == nil {
		return f(ctx, r)
	}
	return i(ctx, r, &grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceUpdates_Call_FullMethodName}, f)
}
func (s *updatesServer) call(ctx context.Context, r *resourcepb.UpdateRequest) (*resourcepb.UpdateResponse, error) {
	if r.Version != 2 {
		return nil, unsupported("unsupported update wire version")
	}
	p, ok := s.owner.impl.(resourcev2.BestEffortUpdateProvider)
	if !ok {
		return nil, unsupported("best-effort updates unsupported")
	}
	out := &resourcepb.UpdateResponse{Version: 2, Result: &resourcepb.UpdateResponse_Success{Success: true}}
	switch r.Operation {
	case "startConnection":
		if len(r.Targets) == 0 || len(r.Targets) > MaxPageSize {
			return nil, invalid("explicit bounded watch targets required")
		}
		targets := make([]resourcev2.ReadTarget, 0, len(r.Targets))
		for _, target := range r.Targets {
			v, err := resourcev2.FromProtoReadTarget(target)
			if err != nil {
				return nil, err
			}
			if _, err = resourcev2.AdmitBestEffortTarget(ctx, s.owner.impl, v); err != nil {
				return nil, err
			}
			targets = append(targets, v)
		}
		return out, p.StartConnectionWatch(ctx, targets)
	case "ensure", "restart":
		target, err := resourcev2.FromProtoReadTarget(r.Target)
		if err != nil {
			return nil, err
		}
		if _, err = resourcev2.AdmitBestEffortTarget(ctx, s.owner.impl, target); err != nil {
			return nil, err
		}
		if r.Operation == "ensure" {
			return out, p.EnsureResourceWatch(ctx, target)
		}
		return out, p.RestartResourceWatch(ctx, target)
	case "stopConnection", "has", "state", "stopResource", "running":
		route, err := resourcev2.FromProtoAccessRoute(r.Route)
		if err != nil {
			return nil, err
		}
		switch r.Operation {
		case "stopConnection":
			return out, p.StopConnectionWatch(ctx, route)
		case "has":
			out.Result = &resourcepb.UpdateResponse_Running{Running: p.HasWatch(ctx, route)}
		case "state":
			v, err := p.GetWatchState(ctx, route)
			if err != nil {
				return nil, err
			}
			result := &resourcepb.OptionalWatchSummary{}
			if v != nil {
				result.Summary, err = resourcev2.ToProtoWatchConnectionSummary(*v)
				if err != nil {
					return nil, err
				}
			}
			out.Result = &resourcepb.UpdateResponse_Summary{Summary: result}
		case "stopResource":
			if r.Type == "" {
				return nil, invalid("missing resource type")
			}
			return out, p.StopResourceWatch(ctx, route, r.Type)
		case "running":
			if r.Type == "" {
				return nil, invalid("missing resource type")
			}
			running, err := p.IsResourceWatchRunning(ctx, route, r.Type)
			if err != nil {
				return nil, err
			}
			out.Result = &resourcepb.UpdateResponse_Running{Running: running}
		}
	default:
		return nil, unsupported("unknown update operation")
	}
	return out, nil
}
func (c *Client) update(ctx context.Context, r *resourcepb.UpdateRequest) (*resourcepb.UpdateResponse, error) {
	ctx, err := outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	r.Version = 2
	out := &resourcepb.UpdateResponse{}
	e := &envelope{message: out}
	if err := c.conn.Invoke(ctx, resourcepb.ResourceUpdates_Call_FullMethodName, r, e, grpc.MaxCallRecvMsgSize(MaxWireBytes), grpc.MaxCallSendMsgSize(MaxWireBytes)); err != nil {
		return nil, domainError(err)
	}
	if e.err != nil {
		return nil, e.err
	}
	if out.Version != 2 {
		return nil, unsupported("unsupported update response version")
	}
	return out, nil
}
func updateSuccess(out *resourcepb.UpdateResponse, err error) error {
	if err != nil {
		return err
	}
	p, ok := out.Result.(*resourcepb.UpdateResponse_Success)
	if !ok || !p.Success {
		return invalid("unexpected update result")
	}
	return nil
}
func (c *Client) StartConnectionWatch(ctx context.Context, targets []resourcev2.ReadTarget) error {
	if len(targets) == 0 || len(targets) > MaxPageSize {
		return invalid("explicit bounded watch targets required")
	}
	r := &resourcepb.UpdateRequest{Operation: "startConnection"}
	for _, target := range targets {
		p, err := resourcev2.ToProtoReadTarget(target)
		if err != nil {
			return err
		}
		r.Targets = append(r.Targets, p)
	}
	out, err := c.update(ctx, r)
	return updateSuccess(out, err)
}
func (c *Client) StopConnectionWatch(ctx context.Context, route resourcev2.AccessRoute) error {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "stopConnection", Route: p})
	return updateSuccess(out, err)
}

// HasWatch has no error result in the domain contract; failures do not claim support or readiness.
func (c *Client) HasWatch(ctx context.Context, route resourcev2.AccessRoute) bool {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return false
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "has", Route: p})
	if err != nil {
		return false
	}
	v, ok := out.Result.(*resourcepb.UpdateResponse_Running)
	return ok && v.Running
}
func (c *Client) GetWatchState(ctx context.Context, route resourcev2.AccessRoute) (*resourcev2.WatchConnectionSummary, error) {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return nil, err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "state", Route: p})
	if err != nil {
		return nil, err
	}
	if out.GetSummary() == nil {
		return nil, invalid("unexpected update summary")
	}
	if out.GetSummary().Summary == nil {
		return nil, nil
	}
	v, err := resourcev2.FromProtoWatchConnectionSummary(out.GetSummary().Summary)
	if err != nil {
		return nil, err
	}
	if v.Route != route {
		return nil, invalid("update summary route mismatch")
	}
	return &v, nil
}
func (c *Client) EnsureResourceWatch(ctx context.Context, target resourcev2.ReadTarget) error {
	p, err := resourcev2.ToProtoReadTarget(target)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "ensure", Target: p})
	return updateSuccess(out, err)
}
func (c *Client) RestartResourceWatch(ctx context.Context, target resourcev2.ReadTarget) error {
	p, err := resourcev2.ToProtoReadTarget(target)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "restart", Target: p})
	return updateSuccess(out, err)
}
func (c *Client) StopResourceWatch(ctx context.Context, route resourcev2.AccessRoute, key string) error {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "stopResource", Route: p, Type: key})
	return updateSuccess(out, err)
}
func (c *Client) IsResourceWatchRunning(ctx context.Context, route resourcev2.AccessRoute, key string) (bool, error) {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return false, err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: "running", Route: p, Type: key})
	if err != nil {
		return false, err
	}
	v, ok := out.Result.(*resourcepb.UpdateResponse_Running)
	if !ok {
		return false, invalid("unexpected running result")
	}
	return v.Running, nil
}
