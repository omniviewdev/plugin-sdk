package plugin

import (
	"context"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const (
	updateCallMethod      = "Call"
	updateStartConnection = "startConnection"
	updateEnsure          = "ensure"
	updateRestart         = "restart"
	updateStopConnection  = "stopConnection"
	updateHas             = "has"
	updateState           = "state"
	updateStopResource    = "stopResource"
	updateRunning         = "running"
	updateListen          = "listen"
)

type updatesServer struct {
	resourcepb.UnimplementedResourceUpdatesServer

	owner *server
}

func registerUpdates(s *grpc.Server, owner *server) {
	d := resourcepb.ResourceUpdates_ServiceDesc
	d.Methods = []grpc.MethodDesc{{
		MethodName: updateCallMethod,
		Handler: func(srv any, ctx context.Context, dec func(any) error, i grpc.UnaryServerInterceptor) (any, error) {
			return updateCallHandler(ctx, srv, dec, i)
		},
	}}
	d.Streams = []grpc.StreamDesc{{StreamName: "Listen", Handler: listenUpdatesHandler, ServerStreams: true}}
	s.RegisterService(&d, &updatesServer{owner: owner})
}

func updateCallHandler(
	ctx context.Context,
	srv any,
	dec func(any) error,
	i grpc.UnaryServerInterceptor,
) (any, error) {
	r := &resourcepb.UpdateRequest{}
	e := &envelope{message: r}
	if err := dec(e); err != nil {
		return nil, rpcError(invalid("malformed update request"))
	}
	if e.err != nil {
		return nil, rpcError(e.err)
	}
	f := func(ctx context.Context, v any) (any, error) {
		s, ok := srv.(*updatesServer)
		if !ok {
			return nil, rpcError(invalid("invalid update server"))
		}
		request, ok := v.(*resourcepb.UpdateRequest)
		if !ok {
			return nil, rpcError(invalid("invalid update request"))
		}
		ctx, err := incomingSession(ctx, s.owner.settings)
		if err != nil {
			return nil, rpcError(err)
		}
		routes := updateRoutes(request)
		if err = s.owner.checkCatalogRoutes(ctx, routes); err != nil {
			return nil, rpcError(s.owner.classify(err))
		}
		out, err := s.call(ctx, request)
		if fenceErr := s.owner.checkCatalogRoutes(ctx, routes); fenceErr != nil {
			return nil, rpcError(s.owner.classify(fenceErr))
		}
		err = responseError(out, err)
		return out, rpcError(s.owner.classify(err))
	}
	if i == nil {
		return f(ctx, r)
	}
	return i(ctx, r, &grpc.UnaryServerInfo{Server: srv, FullMethod: resourcepb.ResourceUpdates_Call_FullMethodName}, f)
}

func (s *updatesServer) call(ctx context.Context, r *resourcepb.UpdateRequest) (*resourcepb.UpdateResponse, error) {
	if r.Version != resourcev2.WireVersion {
		return nil, unsupported("unsupported update wire version")
	}
	p, ok := s.owner.impl.(resourcev2.BestEffortUpdateProvider)
	if !ok {
		return nil, unsupported("best-effort updates unsupported")
	}
	out := &resourcepb.UpdateResponse{
		Version: resourcev2.WireVersion, Result: &resourcepb.UpdateResponse_Success{Success: true},
	}
	switch r.Operation {
	case updateStartConnection:
		targets, err := s.admitUpdateTargets(ctx, r.Targets)
		if err != nil {
			return nil, err
		}
		return out, p.StartConnectionWatch(ctx, targets)
	case updateEnsure, updateRestart:
		target, err := s.admitUpdateTarget(ctx, r.Target)
		if err != nil {
			return nil, err
		}
		if r.Operation == updateEnsure {
			return out, p.EnsureResourceWatch(ctx, target)
		}
		return out, p.RestartResourceWatch(ctx, target)
	case updateStopConnection, updateHas, updateState, updateStopResource, updateRunning:
		return routeUpdate(ctx, p, r, out)
	default:
		return nil, unsupported("unknown update operation")
	}
}

func (s *updatesServer) admitUpdateTarget(
	ctx context.Context,
	target *resourcepb.ReadTarget,
) (resourcev2.ReadTarget, error) {
	value, err := resourcev2.FromProtoReadTarget(target)
	if err != nil {
		return value, err
	}
	_, err = resourcev2.AdmitBestEffortTarget(ctx, s.owner.impl, value)
	return value, err
}

func (s *updatesServer) admitUpdateTargets(
	ctx context.Context,
	targets []*resourcepb.ReadTarget,
) ([]resourcev2.ReadTarget, error) {
	if len(targets) == 0 || len(targets) > MaxPageSize {
		return nil, invalid("explicit bounded watch targets required")
	}
	values := make([]resourcev2.ReadTarget, 0, len(targets))
	for _, target := range targets {
		value, err := s.admitUpdateTarget(ctx, target)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func routeUpdate(
	ctx context.Context,
	p resourcev2.BestEffortUpdateProvider,
	r *resourcepb.UpdateRequest,
	out *resourcepb.UpdateResponse,
) (*resourcepb.UpdateResponse, error) {
	route, err := resourcev2.FromProtoAccessRoute(r.Route)
	if err != nil {
		return nil, err
	}
	switch r.Operation {
	case updateStopConnection:
		return out, p.StopConnectionWatch(ctx, route)
	case updateHas:
		out.Result = &resourcepb.UpdateResponse_Running{Running: p.HasWatch(ctx, route)}
	case updateState:
		summary, summaryErr := updateSummary(ctx, p, route)
		if summaryErr != nil {
			return nil, summaryErr
		}
		out.Result = &resourcepb.UpdateResponse_Summary{Summary: summary}
	case updateStopResource, updateRunning:
		if r.Type == "" {
			return nil, invalid("missing resource type")
		}
		if r.Operation == updateStopResource {
			return out, p.StopResourceWatch(ctx, route, r.Type)
		}
		running, runningErr := p.IsResourceWatchRunning(ctx, route, r.Type)
		if runningErr != nil {
			return nil, runningErr
		}
		out.Result = &resourcepb.UpdateResponse_Running{Running: running}
	}
	return out, nil
}

func updateSummary(
	ctx context.Context,
	p resourcev2.BestEffortUpdateProvider,
	route resourcev2.AccessRoute,
) (*resourcepb.OptionalWatchSummary, error) {
	value, err := p.GetWatchState(ctx, route)
	if err != nil {
		return nil, err
	}
	result := &resourcepb.OptionalWatchSummary{}
	if value != nil {
		result.Summary, err = resourcev2.ToProtoWatchConnectionSummary(*value)
	}
	return result, err
}

func (c *Client) update(ctx context.Context, r *resourcepb.UpdateRequest) (*resourcepb.UpdateResponse, error) {
	ctx, err := outgoingSession(ctx)
	if err != nil {
		return nil, err
	}
	r.Version = resourcev2.WireVersion
	out := &resourcepb.UpdateResponse{}
	e := &envelope{message: out}
	if err := c.conn.Invoke(
		ctx,
		resourcepb.ResourceUpdates_Call_FullMethodName,
		r,
		e,
		grpc.MaxCallRecvMsgSize(MaxWireBytes),
		grpc.MaxCallSendMsgSize(MaxWireBytes),
	); err != nil {
		return nil, domainError(err)
	}
	if e.err != nil {
		return nil, e.err
	}
	if out.Version != resourcev2.WireVersion {
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

// StartConnectionWatch starts advisory watches for the explicit bounded target set.
func (c *Client) StartConnectionWatch(ctx context.Context, targets []resourcev2.ReadTarget) error {
	if len(targets) == 0 || len(targets) > MaxPageSize {
		return invalid("explicit bounded watch targets required")
	}
	r := &resourcepb.UpdateRequest{Operation: updateStartConnection}
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

// StopConnectionWatch stops advisory watches on the selected route.
func (c *Client) StopConnectionWatch(ctx context.Context, route resourcev2.AccessRoute) error {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateStopConnection, Route: p})
	return updateSuccess(out, err)
}

// HasWatch has no error result in the domain contract; failures do not claim support or readiness.
func (c *Client) HasWatch(ctx context.Context, route resourcev2.AccessRoute) bool {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return false
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateHas, Route: p})
	if err != nil {
		return false
	}
	v, ok := out.Result.(*resourcepb.UpdateResponse_Running)
	return ok && v.Running
}

// GetWatchState returns the route's current watch summary, or nil when no watch exists.
//
//nolint:nilnil // A successful response without a summary means no watch exists.
func (c *Client) GetWatchState(
	ctx context.Context,
	route resourcev2.AccessRoute,
) (*resourcev2.WatchConnectionSummary, error) {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return nil, err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateState, Route: p})
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

// EnsureResourceWatch starts the target's advisory watch unless it already exists.
func (c *Client) EnsureResourceWatch(ctx context.Context, target resourcev2.ReadTarget) error {
	p, err := resourcev2.ToProtoReadTarget(target)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateEnsure, Target: p})
	return updateSuccess(out, err)
}

// RestartResourceWatch replaces the target's advisory watch with a fresh source.
func (c *Client) RestartResourceWatch(ctx context.Context, target resourcev2.ReadTarget) error {
	p, err := resourcev2.ToProtoReadTarget(target)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateRestart, Target: p})
	return updateSuccess(out, err)
}

// StopResourceWatch stops the resource type's watch on the selected route.
func (c *Client) StopResourceWatch(ctx context.Context, route resourcev2.AccessRoute, key string) error {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateStopResource, Route: p, Type: key})
	return updateSuccess(out, err)
}

// IsResourceWatchRunning reports whether the route and resource type have an active watch.
func (c *Client) IsResourceWatchRunning(ctx context.Context, route resourcev2.AccessRoute, key string) (bool, error) {
	p, err := resourcev2.ToProtoAccessRoute(route)
	if err != nil {
		return false, err
	}
	out, err := c.update(ctx, &resourcepb.UpdateRequest{Operation: updateRunning, Route: p, Type: key})
	if err != nil {
		return false, err
	}
	v, ok := out.Result.(*resourcepb.UpdateResponse_Running)
	if !ok {
		return false, invalid("unexpected running result")
	}
	return v.Running, nil
}
