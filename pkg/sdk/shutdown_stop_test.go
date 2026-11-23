package sdk_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/stats"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// What a host's Client.Kill does to a plugin: it calls go-plugin's controller Shutdown, which stops the plugin's gRPC server at
// once, and then waits for the process to exit. A stream of the exec lane can receive the typed end of its session only if the
// sessions are ended before the server stops, so the plugin holds that call until its capabilities have ended what they hold.
//
// Two rigs serve a plugin the way Serve serves it (the server is the one that Serve hands go-plugin, built by the factory in its
// ServeConfig). One serves it through go-plugin itself, in its test mode, with go-plugin's own controller: it proves that the
// method the plugin holds is the one go-plugin's controller answers. The other serves it on a server of its own with a stand-in
// for the controller whose Shutdown stops the server only when the test lets it, so that the test can read what the plugin sent
// to every stream while the server still serves, and decides the order of events instead of racing the stop.

// controllerShutdown is the method of go-plugin's controller that stops the server, as go-plugin names it.
const controllerShutdown = "/plugin.GRPCController/Shutdown"

// stopWait bounds a wait for something that has to happen: it turns a hang into a failure and decides no outcome.
const stopWait = 10 * time.Second

// stopSlack is what a bound that the plugin keeps is allowed to overrun by: the time to answer a call. It is an upper bound that no
// machine in use comes near, and it is below the difference between the plugin's two bounds (1.5 s and 10 s), so that a bound that
// is not kept is seen.
const stopSlack = 4 * time.Second

// stopProbe is a capability that holds nothing to end. It records how the plugin tells it that the plugin stops: how often, with
// what bound, and whether the plugin's server still answered while it was told.
type stopProbe struct {
	plugin.Plugin

	// hold, when set, is what the first Shutdown waits for besides its context: a capability that does not return.
	hold <-chan struct{}

	mu      sync.Mutex
	serving func() bool
	calls   []stopCall
}

// stopCall is one Shutdown of the probe.
type stopCall struct {
	serving     bool          // the plugin's server answered a request while the probe was told
	hasDeadline bool          // the probe was given a bound
	remaining   time.Duration // the bound the probe was given, as it was when the call arrived
}

func (s *stopProbe) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error { return nil }

func (s *stopProbe) GRPCClient(context.Context, *plugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, nil
}

func (s *stopProbe) Shutdown(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	call := stopCall{hasDeadline: ok, remaining: time.Until(deadline)}
	s.mu.Lock()
	serving := s.serving
	s.mu.Unlock()
	if serving != nil {
		call.serving = serving()
	}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	hold := s.hold
	if len(s.calls) > 1 {
		hold = nil // only the first call does not return: the one after the server has stopped must not take its bound again
	}
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *stopProbe) setServing(fn func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serving = fn
}

func (s *stopProbe) told() []stopCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stopCall(nil), s.calls...)
}

// stopRecord is what the functions of a rig's target tell the test: when each ran and when each returned. The function a
// session runs is the one its "role" label names: an echo, a function that waits for its context, a flood that writes until it is
// stopped, and one that ignores its context until the test lets it go.
type stopRecord struct {
	entered  chan string
	returned chan string
	stuck    chan struct{}
}

func newStopRecord() *stopRecord {
	return &stopRecord{entered: make(chan string, 16), returned: make(chan string, 16), stuck: make(chan struct{})}
}

func (r *stopRecord) target() exec.Target {
	target := exectest.NewFakeTarget()
	target.Exec = func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
		role := p.Request.Labels["role"]
		r.entered <- role
		defer func() { r.returned <- role }()
		switch role {
		case "echo":
			_, _ = io.Copy(p.Stdout, p.Stdin)
		case "flood":
			chunk := make([]byte, 4<<10)
			for ctx.Err() == nil {
				if _, err := p.Stdout.Write(chunk); err != nil {
					break
				}
			}
		case "stuck":
			<-r.stuck
		default:
			<-ctx.Done()
		}
		return exec.Exit{}, ctx.Err()
	}
	return target
}

// endedSent is a stats handler of the plugin's server that counts the Ended frames as the server's transport took them: the
// evidence, on the plugin's side, that the end of a session was sent, and when.
type endedSent struct{ n atomic.Int64 }

func (e *endedSent) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (e *endedSent) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (e *endedSent) HandleConn(context.Context, stats.ConnStats) {}
func (e *endedSent) HandleRPC(_ context.Context, s stats.RPCStats) {
	if out, ok := s.(*stats.OutPayload); ok {
		if frame, ok := out.Payload.(*execv2.ServerFrame); ok && frame.GetEnded() != nil {
			e.n.Add(1)
		}
	}
}

// controllerServer is the service of go-plugin's controller: the method that a host's Kill calls.
type controllerServer interface {
	Shutdown(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

// gatedController stands in for go-plugin's controller on a server of the test's own: the same service, the same method, and a
// Shutdown that stops the server only when the test releases it. It is registered on the server that Serve's factory built, so
// the plugin's interceptor sees the call as it sees go-plugin's.
type gatedController struct {
	server      *grpc.Server
	reachedOnce sync.Once
	releaseOnce sync.Once
	reached     chan struct{} // closed when the call reaches the handler: the interceptor has let it through
	release     chan struct{} // closed by letStop: the handler then stops the server
}

// letStop lets the controller stop the server. It may be called more than once.
func (c *gatedController) letStop() { c.releaseOnce.Do(func() { close(c.release) }) }

func (c *gatedController) Shutdown(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	c.reachedOnce.Do(func() { close(c.reached) })
	select {
	case <-c.release:
	case <-ctx.Done():
	case <-time.After(stopWait):
	}
	c.server.Stop()
	return &emptypb.Empty{}, nil
}

var gatedControllerDesc = grpc.ServiceDesc{
	ServiceName: "plugin.GRPCController",
	HandlerType: (*controllerServer)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Shutdown",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			in := new(emptypb.Empty)
			if err := dec(in); err != nil {
				return nil, err
			}
			if interceptor == nil {
				return srv.(controllerServer).Shutdown(ctx, in)
			}
			info := &grpc.UnaryServerInfo{Server: srv, FullMethod: controllerShutdown}
			return interceptor(ctx, in, info, func(ctx context.Context, req any) (any, error) {
				return srv.(controllerServer).Shutdown(ctx, req.(*emptypb.Empty))
			})
		},
	}},
}

// gatedRig is a plugin with the exec capability and the probe, served on a server of the test's own.
type gatedRig struct {
	t          *testing.T
	conns      *exectestkit.Connections
	capability *exec.Plugin
	probe      *stopProbe
	conn       *grpc.ClientConn
	v2         exec.Client
	gate       *gatedController
	sent       *endedSent
	// served is closed when Serve has returned: the server has stopped and the capabilities were told once more.
	served chan struct{}
}

func newGatedRig(t *testing.T, probe *stopProbe, target exec.Target) *gatedRig {
	t.Helper()
	conns := exectestkit.NewConnectionsT(t, "fake")
	capability, err := exec.NewTargetPlugin([]exec.Target{target})
	if err != nil {
		t.Fatalf("build the exec capability: %v", err)
	}
	capability.BindConnectionContextProvider(conns)
	p := sdk.NewPluginForTest("stop-test", logging.NewNop())
	p.RegisterCapability("exec", capability)
	p.RegisterCapability("probe", probe)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	r := &gatedRig{
		t: t, conns: conns, capability: capability, probe: probe, sent: &endedSent{}, served: make(chan struct{}),
		gate: &gatedController{reached: make(chan struct{}), release: make(chan struct{})},
	}
	built := make(chan struct{})
	go func() {
		defer close(r.served)
		p.ServeWith(func(cfg *plugin.ServeConfig) {
			// The server is built by the factory that Serve gives go-plugin; the test adds only what it observes with.
			server := cfg.GRPCServer([]grpc.ServerOption{grpc.StatsHandler(r.sent)})
			r.gate.server = server
			for _, set := range cfg.VersionedPlugins {
				for _, name := range []string{"exec", "probe"} {
					if impl, ok := set[name].(plugin.GRPCPlugin); ok {
						_ = impl.GRPCServer(nil, server)
					}
				}
			}
			server.RegisterService(&gatedControllerDesc, r.gate)
			close(built)
			_ = server.Serve(listener)
		})
	}()
	select {
	case <-built:
	case <-time.After(stopWait):
		t.Fatalf("the plugin's server was not built within %v", stopWait)
	}
	t.Cleanup(func() {
		r.gate.letStop()
		r.gate.server.Stop()
		select {
		case <-r.served:
		case <-time.After(stopWait):
			t.Errorf("Serve did not return within %v", stopWait)
		}
	})

	options := append(sdk.GRPCDialOptionsWithLogger(logging.NewNop()), grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(listener.Addr().String(), options...)
	if err != nil {
		t.Fatalf("dial the plugin: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	raw, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatalf("build the exec client: %v", err)
	}
	r.conn, r.v2 = conn, raw.(*exec.PluginClient).V2()
	probe.setServing(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), stopWait)
		defer cancel()
		_, err := r.v2.Describe(ctx)
		return err == nil
	})
	return r
}

// open opens a session whose function is the one the role names, and returns the host's side of it.
func (r *gatedRig) open(id, role string) exec.Conn {
	r.t.Helper()
	req := &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{
		Target: r.conns.WireTarget(r.t, "thing", "thing-1"),
		Labels: []*execv2.NameValue{{Name: "role", Value: role}},
	}}}
	conn, err := r.v2.Open(r.conns.Context(context.Background()), req)
	if err != nil {
		r.t.Fatalf("open %q: %v", id, err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// kill makes the call that a host's Client.Kill makes first. The call ends with the server stopping, so its answer is an error as
// often as not: Kill does not read it either.
func (r *gatedRig) kill() <-chan error {
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*stopWait)
		defer cancel()
		done <- r.conn.Invoke(ctx, controllerShutdown, &emptypb.Empty{}, &emptypb.Empty{})
	}()
	return done
}

// reached waits until the call has passed the plugin's interceptor and reached the controller, and returns how long that took.
func (r *gatedRig) reached(start time.Time) time.Duration {
	r.t.Helper()
	select {
	case <-r.gate.reached:
		return time.Since(start)
	case <-time.After(3 * stopWait):
		r.t.Fatalf("the controller's Shutdown did not reach the controller within %v", 3*stopWait)
		return 0
	}
}

// await waits for the signal of a function.
func await(t *testing.T, c <-chan string, what string) string {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(stopWait):
		t.Fatalf("no %s within %v", what, stopWait)
		return ""
	}
}

// streamEnd is what a host read from a stream: the Ended it received, what followed it, or what ended the stream before one.
type streamEnd struct {
	ended *execv2.Ended
	after error // what the stream said after Ended: io.EOF for a stream that ended cleanly
	err   error // what ended the stream when no Ended arrived
}

// collect reads a stream until it ends, in the background, as a host's reader does.
func collect(conn exec.Conn) <-chan streamEnd {
	out := make(chan streamEnd, 1)
	go func() {
		var end streamEnd
		for {
			frame, err := conn.Recv()
			if err != nil {
				end.err = err
				break
			}
			if ended := frame.GetEnded(); ended != nil {
				end.ended = ended
				_, end.after = conn.Recv()
				break
			}
		}
		out <- end
	}()
	return out
}

// wantShutdownEnd holds what a stream read to the typed end of a plugin that stops, and then to its clean end.
func wantShutdownEnd(t *testing.T, name string, ch <-chan streamEnd) {
	t.Helper()
	select {
	case end := <-ch:
		switch {
		case end.ended == nil:
			t.Errorf("%s: the stream ended with %v and no Ended", name, end.err)
		case end.ended.GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN:
			t.Errorf("%s: the session ended %v, want closed by the plugin's shutdown", name, end.ended.GetEnd())
		case end.after != io.EOF:
			t.Errorf("%s: after Ended the stream said %v, want io.EOF", name, end.after)
		}
	case <-time.After(stopWait):
		t.Errorf("%s: the stream did not end within %v: its session was not ended before the server was to stop", name, stopWait)
	}
}

// A host that kills a plugin first calls the controller's Shutdown, which stops the server. The plugin holds that call until the
// exec lane has ended its sessions, so that each stream receives its Ended, typed, and then its clean end, instead of a
// connection that broke (the independent review of the exec lane's wire: 0 of 5 streams received it).
func TestTheControllersShutdownEndsEverySessionBeforeTheServerStops(t *testing.T) {
	rec, probe := newStopRecord(), &stopProbe{}
	rig := newGatedRig(t, probe, rec.target())
	roles := []string{"echo", "idle", "flood", "idle"}
	ends := make([]<-chan streamEnd, len(roles))
	for i, role := range roles {
		ends[i] = collect(rig.open(fmt.Sprintf("s%d", i), role))
	}
	for range roles {
		await(t, rec.entered, "function to run")
	}

	call := rig.kill()
	rig.reached(time.Now())

	// The call is in the controller and the server has not stopped: what the plugin sent can be read.
	if got := rig.sent.n.Load(); got != int64(len(roles)) {
		t.Errorf("the plugin had sent %d Ended frames when the controller was reached, want %d: a session was ended after the server was to stop", got, len(roles))
	}
	for i, role := range roles {
		wantShutdownEnd(t, fmt.Sprintf("session %d (%s)", i, role), ends[i])
	}
	rig.gate.letStop()
	<-call
	select {
	case <-rig.served:
	case <-time.After(stopWait):
		t.Fatalf("Serve did not return within %v of the controller's Shutdown", stopWait)
	}

	calls := probe.told()
	if len(calls) != 2 {
		t.Fatalf("the capabilities were told %d times, want twice: before the server stopped, and once more when Serve returned", len(calls))
	}
	if !calls[0].serving {
		t.Error("the capabilities were told after the server had stopped: the controller's Shutdown ran before them")
	}
	if !calls[0].hasDeadline || calls[0].remaining > sdk.CapabilityPreStopBound {
		t.Errorf("the capabilities were given %v (a bound: %v) before the server stopped, want at most %v", calls[0].remaining, calls[0].hasDeadline, sdk.CapabilityPreStopBound)
	}
	if calls[1].serving {
		t.Error("the server still answered when Serve had returned")
	}
	if !calls[1].hasDeadline || calls[1].remaining > sdk.CapabilityShutdownBound {
		t.Errorf("the capabilities were given %v (a bound: %v) after the server stopped, want at most %v", calls[1].remaining, calls[1].hasDeadline, sdk.CapabilityShutdownBound)
	}
	// The second telling found nothing left to end: it ended nothing again and returns at once.
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()
	if err := rig.capability.Shutdown(ctx); err != nil {
		t.Errorf("a third Shutdown of the exec capability: %v, want nil: a second telling is a no-op", err)
	}
	if got := rig.sent.n.Load(); got != int64(len(roles)) {
		t.Errorf("the plugin sent %d Ended frames in all, want %d: a second telling ended sessions again", got, len(roles))
	}
}

// A function that ignores its context cannot keep the controller's Shutdown for longer than the bound, and the sessions of the
// functions that do honour theirs, and the stuck one's too (the engine gives a stopping plugin's functions 1 s, which is less
// than the bound), still receive their Ended before the server stops.
func TestAStuckAuthorsSessionStillReceivesItsEndBeforeTheServerStops(t *testing.T) {
	rec, probe := newStopRecord(), &stopProbe{}
	t.Cleanup(func() { close(rec.stuck) })
	rig := newGatedRig(t, probe, rec.target())
	roles := []string{"echo", "stuck", "idle"}
	ends := make([]<-chan streamEnd, len(roles))
	for i, role := range roles {
		ends[i] = collect(rig.open(fmt.Sprintf("s%d", i), role))
	}
	for range roles {
		await(t, rec.entered, "function to run")
	}

	start := time.Now()
	call := rig.kill()
	took := rig.reached(start)

	if limit := sdk.CapabilityPreStopBound + stopSlack; took > limit {
		t.Errorf("the controller's Shutdown was held %v with a function that ignores its context, want at most %v", took, limit)
	}
	if got := rig.sent.n.Load(); got != int64(len(roles)) {
		t.Errorf("the plugin had sent %d Ended frames when the controller was reached, want %d: the stuck function's session was not ended in time", got, len(roles))
	}
	for i, role := range roles {
		wantShutdownEnd(t, fmt.Sprintf("session %d (%s)", i, role), ends[i])
	}
	rig.gate.letStop()
	<-call
}

// A capability's shutdown that does not return keeps the controller's Shutdown for its bound and no longer: the call then goes on
// to the controller, whatever the capability did.
func TestACapabilityThatDoesNotReturnKeepsTheControllersShutdownForItsBoundOnly(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	rec, probe := newStopRecord(), &stopProbe{hold: never}
	rig := newGatedRig(t, probe, rec.target())
	end := collect(rig.open("s1", "idle"))
	await(t, rec.entered, "function to run")

	start := time.Now()
	call := rig.kill()
	took := rig.reached(start)

	if limit := sdk.CapabilityPreStopBound + stopSlack; took > limit {
		t.Errorf("the controller's Shutdown was held %v by a capability that does not return, want at most %v: the plugin holds it for %v", took, limit, sdk.CapabilityPreStopBound)
	}
	// The exec capability, which comes before the probe, did its work meanwhile.
	wantShutdownEnd(t, "the session of the capability that returned", end)
	rig.gate.letStop()
	<-call
}

// Only the controller's Shutdown is held: every other call is answered as before, and the sessions go on.
func TestOtherCallsDoNotShutTheCapabilitiesDown(t *testing.T) {
	rec, probe := newStopRecord(), &stopProbe{}
	rig := newGatedRig(t, probe, rec.target())
	conn := rig.open("s1", "echo")
	await(t, rec.entered, "function to run")

	for range 3 {
		if _, err := rig.v2.Describe(context.Background()); err != nil {
			t.Fatalf("describe: %v", err)
		}
	}
	if err := conn.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte("still here")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	for len(got) < len("still here") {
		frame, err := conn.Recv()
		if err != nil {
			t.Fatalf("recv after %q: %v: a call that is not the controller's Shutdown ended the session", got, err)
		}
		if frame.GetEnded() != nil {
			t.Fatalf("the session ended after %q: a call that is not the controller's Shutdown ended it", got)
		}
		got = append(got, frame.GetOutput().GetData()...)
	}
	if calls := probe.told(); len(calls) != 0 {
		t.Errorf("the capabilities were told that the plugin stops %d times, by calls that are not the controller's Shutdown", len(calls))
	}
}

// A plugin that stops itself, with no host call, still ends what its capabilities hold once the server has stopped: its
// sessions' functions are cancelled and given their grace. The controller's way of stopping is not the only one, and the
// capabilities are told once, after the server stopped, when no controller call came before.
func TestAPluginThatStopsItselfEndsItsSessionsOnceItsServerHasStopped(t *testing.T) {
	rec, probe := newStopRecord(), &stopProbe{}
	rig := newGatedRig(t, probe, rec.target())
	rig.open("s1", "idle")
	await(t, rec.entered, "function to run")

	rig.gate.server.Stop()

	select {
	case <-rig.served:
	case <-time.After(stopWait):
		t.Fatalf("Serve did not return within %v of its server stopping", stopWait)
	}
	await(t, rec.returned, "function to be cancelled and return once the server stopped")
	calls := probe.told()
	if len(calls) != 1 || calls[0].serving || !calls[0].hasDeadline || calls[0].remaining > sdk.CapabilityShutdownBound {
		t.Errorf("the capabilities were told %+v, want once, after the server stopped, with a bound of at most %v", calls, sdk.CapabilityShutdownBound)
	}
}

// go-plugin's own controller answers the method that the plugin holds: served through go-plugin, with its real controller, a call
// to the controller's Shutdown tells the capabilities while the server still answers, and the server then stops.
func TestGoPluginsOwnControllerShutdownIsHeldUntilTheCapabilitiesAreTold(t *testing.T) {
	probe := &stopProbe{}
	p := sdk.NewPluginForTest("stop-test", logging.NewNop())
	p.RegisterCapability("probe", probe)
	ctx, cancel := context.WithCancel(context.Background())
	reattach := make(chan *plugin.ReattachConfig, 1)
	stopped, served := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(served)
		p.ServeWith(func(cfg *plugin.ServeConfig) {
			cfg.Test = &plugin.ServeTestConfig{Context: ctx, ReattachConfigCh: reattach, CloseCh: stopped}
			cfg.Logger = hclog.NewNullLogger()
			plugin.Serve(cfg)
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(stopWait):
			t.Errorf("Serve did not return within %v", stopWait)
		}
	})
	var config *plugin.ReattachConfig
	select {
	case config = <-reattach:
	case <-time.After(stopWait):
		t.Fatalf("go-plugin did not serve within %v", stopWait)
	}
	address := config.Addr.String()
	if config.Addr.Network() == "unix" {
		address = "unix:" + address
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial go-plugin's server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	probe.setServing(func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), stopWait)
		defer cancel()
		_, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName})
		return err == nil
	})

	callCtx, cancelCall := context.WithTimeout(context.Background(), 3*stopWait)
	defer cancelCall()
	// The call ends with go-plugin's controller stopping the server, so its answer is an error as often as not.
	_ = conn.Invoke(callCtx, controllerShutdown, &emptypb.Empty{}, &emptypb.Empty{})

	select {
	case <-stopped:
	case <-time.After(stopWait):
		t.Fatalf("go-plugin's server did not stop within %v of its controller's Shutdown", stopWait)
	}
	select {
	case <-served:
	case <-time.After(stopWait):
		t.Fatalf("Serve did not return within %v of its server stopping", stopWait)
	}
	calls := probe.told()
	if len(calls) != 2 || !calls[0].serving || calls[1].serving {
		t.Fatalf("the capabilities were told %+v, want twice: while go-plugin's server answered, and once more after it stopped", calls)
	}
	if !calls[0].hasDeadline || calls[0].remaining > sdk.CapabilityPreStopBound {
		t.Errorf("the capabilities were given %v (a bound: %v) before the server stopped, want at most %v", calls[0].remaining, calls[0].hasDeadline, sdk.CapabilityPreStopBound)
	}
}
