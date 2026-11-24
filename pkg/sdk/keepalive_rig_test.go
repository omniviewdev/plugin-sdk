package sdk_test

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
)

// The keepalive tests run a real gRPC server and a real gRPC client over an exectestkit.Path on the clock of a synctest bubble.
// grpc-go never lets a client ping more often than every 10 s, so a test that waited for the real parameters would wait for
// them (and one that shortened them could not shorten the client's); on a bubble's clock the parameters that production uses
// elapse in no time, a vanished peer is a Path that was cut, and the instant of every event is exact.

// What a host and a plugin are set to, as the tests pin it: the host's are the plan's (30 s ping, 10 s timeout), the plugin's
// are the SDK's choice for a peer that vanished.
const (
	kaHostTime     = 30 * time.Second
	kaHostTimeout  = 10 * time.Second
	kaPluginTime   = 10 * time.Second
	kaPluginWait   = 5 * time.Second // the plugin's Timeout
	kaHalf         = 500 * time.Millisecond
	kaWatchMethod  = "/grpc.health.v1.Health/Watch"
	kaPluginPrefix = "keepalive-test"
)

// sleepUntil runs the bubble's clock to t and lets everything that is due by then settle. A test asks its questions at instants that
// are not the instants of the system's own events (see the note at the head of keepalive_test.go).
func sleepUntil(t time.Time) { synctest.Sleep(time.Until(t)) }

// kaStreams records the streams that the plugin's server serves: when the context of each ended, which is when the transport it
// ran on closed under it, or when the stream finished.
type kaStreams struct {
	mu    sync.Mutex
	items []*kaStream
}

type kaStream struct {
	method string
	ended  time.Time
	done   bool
}

// intercept is a stream interceptor of the plugin's server. The end of a stream is recorded by a goroutine that waits for its
// context: the context is cancelled when the transport closes under the stream, or when the stream finishes, and the goroutine
// is never stopped (a context.AfterFunc that the handler's return stops can lose the race with the cancellation that woke the
// handler, and the end would not be recorded).
func (k *kaStreams) intercept(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	s := &kaStream{method: info.FullMethod}
	k.mu.Lock()
	k.items = append(k.items, s)
	k.mu.Unlock()
	go func() {
		<-ss.Context().Done()
		k.mu.Lock()
		s.ended, s.done = time.Now(), true
		k.mu.Unlock()
	}()
	return handler(srv, ss)
}

// open is how many streams of the method the server has seen begin.
func (k *kaStreams) open(method string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, s := range k.items {
		if s.method == method {
			n++
		}
	}
	return n
}

// ends returns when each stream of the method that has ended ended.
func (k *kaStreams) ends(method string) []time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []time.Time
	for _, s := range k.items {
		if s.method == method && s.done {
			out = append(out, s.ended)
		}
	}
	return out
}

// kaConfig is what a rig is made of.
type kaConfig struct {
	// server is what the caller gives the SDK's factory.
	server []grpc.ServerOption
	// build makes the server; nil is the SDK's factory.
	build func([]grpc.ServerOption) *grpc.Server
	// host makes the client set the keepalive a host sets.
	host bool
	// options builds the dial options the SDK gives a client; nil is sdk.GRPCDialOptionsWithLogger.
	options func() []grpc.DialOption
	// dial is added to the client's options.
	dial []grpc.DialOption
}

// kaRig is a plugin's server with the health service, a client, and the path between them.
type kaRig struct {
	path    *exectestkit.Path
	streams *kaStreams
	conn    *grpc.ClientConn
	health  grpc_health_v1.HealthClient
}

// newKaRig serves the health service (one unary and one streaming method, which is all a keepalive test needs of a service) on a
// server that the SDK builds, and dials it through a path. It must be called inside a bubble.
func newKaRig(t *testing.T, cfg kaConfig) *kaRig {
	t.Helper()
	log := logging.NewNop()
	path := exectestkit.NewPath(t)
	streams := &kaStreams{}
	build := cfg.build
	if build == nil {
		build = func(opts []grpc.ServerOption) *grpc.Server { return sdk.GRPCServerFactoryWithLogger(log, opts) }
	}
	server := build(append([]grpc.ServerOption{grpc.ChainStreamInterceptor(streams.intercept)}, cfg.server...))
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	served := make(chan error, 1)
	go func() { served <- server.Serve(path.Listener()) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})

	sdkDial := cfg.options
	if sdkDial == nil {
		sdkDial = func() []grpc.DialOption { return sdk.GRPCDialOptionsWithLogger(log) }
	}
	options := append(sdkDial(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	options = append(options, path.DialOptions()...)
	if cfg.host {
		options = append(options, sdk.HostKeepaliveDialOption())
	}
	options = append(options, cfg.dial...)
	conn, err := grpc.NewClient(path.Target(), options...)
	if err != nil {
		t.Fatalf("dial the plugin: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &kaRig{path: path, streams: streams, conn: conn, health: grpc_health_v1.NewHealthClient(conn)}
}

// kaCapability is a capability that serves nothing: Serve needs one to have registered.
type kaCapability struct{ plugin.Plugin }

func (kaCapability) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error { return nil }

func (kaCapability) GRPCClient(context.Context, *plugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, nil
}

// serveLike returns the factory that Serve gives go-plugin for a plugin: the server that a real plugin is served by.
func serveLike(t *testing.T) func([]grpc.ServerOption) *grpc.Server {
	t.Helper()
	var factory func([]grpc.ServerOption) *grpc.Server
	p := sdk.NewPluginForTest(kaPluginPrefix, logging.NewNop())
	p.RegisterCapability("keepalive-test", kaCapability{})
	p.ServeWith(func(cfg *plugin.ServeConfig) { factory = cfg.GRPCServer })
	if factory == nil {
		t.Fatalf("Serve gave go-plugin no server factory")
	}
	return factory
}

// check makes a call, which is activity on the connection (and makes it, if it was not).
func (r *kaRig) check(t *testing.T) {
	t.Helper()
	if _, err := r.health.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("a call on the connection: %v", err)
	}
}

// kaWatch is a stream the host holds open, and how it ended.
type kaWatch struct {
	mu  sync.Mutex
	err error
	at  time.Time
	end chan struct{}
}

// watch opens a stream that the plugin never ends, and returns once it is established.
func (r *kaRig) watch(t *testing.T) *kaWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := r.health.Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("open a stream: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("the first status of the stream: %v", err)
	}
	w := &kaWatch{end: make(chan struct{})}
	go func() {
		_, err := stream.Recv()
		w.mu.Lock()
		w.err, w.at = err, time.Now()
		w.mu.Unlock()
		close(w.end)
	}()
	return w
}

// ended returns when the stream ended, whether it has, and what ended it.
func (w *kaWatch) ended() (at time.Time, ended bool, err error) {
	select {
	case <-w.end:
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.at, true, w.err
	default:
		return time.Time{}, false, nil
	}
}

// pings returns the keepalive PINGs (not their acknowledgements) that the end sent after t0.
func (r *kaRig) pings(from exectestkit.Side, after time.Time) []exectestkit.Frame {
	var out []exectestkit.Frame
	for _, f := range r.path.Frames() {
		if f.From == from && f.Type == exectestkit.FramePing && !f.Ack && f.At.After(after) {
			out = append(out, f)
		}
	}
	return out
}

// acks returns how many PING acknowledgements the end sent.
func (r *kaRig) acks(from exectestkit.Side) int {
	n := 0
	for _, f := range r.path.Frames() {
		if f.From == from && f.Type == exectestkit.FramePing && f.Ack {
			n++
		}
	}
	return n
}

// goAways returns the GOAWAYs that crossed the path.
func goAways(path *exectestkit.Path) []exectestkit.Frame {
	var out []exectestkit.Frame
	for _, f := range path.Frames() {
		if f.Type == exectestkit.FrameGoAway {
			out = append(out, f)
		}
	}
	return out
}

// kaFrame is one HTTP/2 frame.
func kaFrame(typ, flags byte, payload []byte) []byte {
	n := len(payload)
	head := []byte{byte(n >> 16), byte(n >> 8), byte(n), typ, flags, 0, 0, 0, 0}
	return append(head, payload...)
}

// kaRawClient is a client that speaks HTTP/2 itself, so that it can ping at any interval: the keepalive of a gRPC client
// never pings more often than every 10 s, and a peer that is not one can.
type kaRawClient struct {
	conn net.Conn
	// closed is closed when the plugin has closed its end.
	closed chan struct{}
}

// dialRaw connects a raw client: the connection preface and an empty SETTINGS, which is all a server needs to take it for a
// client, and a reader that drops what the plugin says (the path records it).
func dialRaw(t *testing.T, path *exectestkit.Path) *kaRawClient {
	t.Helper()
	conn, err := path.DialContext(context.Background(), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write(append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), kaFrame(0x4, 0, nil)...)); err != nil {
		t.Fatalf("write the preface: %v", err)
	}
	c := &kaRawClient{conn: conn, closed: make(chan struct{})}
	go func() {
		defer close(c.closed)
		_, _ = io.Copy(io.Discard, conn)
	}()
	return c
}

// ping sends a PING.
func (c *kaRawClient) ping(t *testing.T) {
	t.Helper()
	select {
	case <-c.closed:
		return // the plugin has closed the connection: a ping would be written to nobody
	default:
	}
	if _, err := c.conn.Write(kaFrame(0x6, 0, []byte{1, 2, 3, 4, 5, 6, 7, 8})); err != nil {
		t.Fatalf("write a PING: %v", err)
	}
}

// isClosed says whether the plugin has closed its end of the connection.
func (c *kaRawClient) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}
