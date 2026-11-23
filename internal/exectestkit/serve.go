package exectestkit

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// TB is the part of testing.TB that Serve, the connection registry and the clients of a Server use. A *testing.T has it all;
// a recorder that stands in for one lets a suite be run against a server that is wrong and be seen to fail.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
	Cleanup(func())
}

// ServeConfig configures Serve.
type ServeConfig struct {
	// Legacy serves the legacy lane (omniview.sdk.exec.v1) beside the v2 lane; nil serves none. A config with
	// neither Targets nor Legacy serves the legacy service with no provider, as a zero exec.Plugin does.
	Legacy exec.Provider
	// Targets are the exec v2 targets the plugin serves.
	Targets []exec.Target
	// Connections binds a fake connection registry, as a plugin's resource provider would. Nil leaves none
	// bound: a call that addresses a resource is then refused by the resource layer, as in a plugin that has no
	// resource provider.
	Connections *Connections
	// Logger receives the plugin's logs. Nil discards them.
	Logger logging.Logger
	// ServerOptions are given to the SDK's server factory. The factory puts its keepalive before them, so a keepalive option given
	// here replaces the SDK's, and an interceptor given here runs before the SDK's.
	ServerOptions []grpc.ServerOption
	// DialOptions are added to the client's, after the SDK's and the host's own: one that sets the same thing replaces it.
	DialOptions []grpc.DialOption
	// Path carries the plugin and its client in memory, in place of a socket. A test that cuts the connection, or looks at what
	// crosses it, makes the path inside its synctest bubble and passes it here.
	Path *Path
}

// Server is the exec capability of a plugin served over a real transport, with a client that dials it the way the
// host does. The transport is a unix socket (a TCP loopback socket on Windows, as go-plugin does) or the ServeConfig's Path,
// the server is built with the SDK's own middleware and wire limits, and the client with the SDK's dial options and the
// keepalive a host sets (sdk.HostKeepaliveDialOption): what a host and a plugin exchange, without the child process.
type Server struct {
	// Plugin is the served exec capability.
	Plugin *exec.Plugin

	conn *grpc.ClientConn
}

// Serve serves cfg.Targets as an exec plugin and returns the server with a connected client. Everything is torn
// down at the end of the test: the client first, then the server, waiting for its serve loop to return.
func Serve(t TB, cfg ServeConfig) *Server {
	t.Helper()
	log := cfg.Logger
	if log == nil {
		log = logging.NewNop()
	}
	capability, err := exec.NewTargetPlugin(cfg.Targets)
	if err != nil {
		t.Fatalf("build the exec capability: %v", err)
	}
	capability.Impl = cfg.Legacy
	if cfg.Connections != nil {
		capability.BindConnectionContextProvider(cfg.Connections)
	}
	server := sdk.GRPCServerFactoryWithLogger(log, slices.Clone(cfg.ServerOptions))
	if err := capability.GRPCServer(nil, server); err != nil {
		t.Fatalf("register the exec capability: %v", err)
	}
	var (
		listener net.Listener
		target   string
	)
	options := append(sdk.GRPCDialOptionsWithLogger(log), sdk.HostKeepaliveDialOption(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if cfg.Path != nil {
		listener, target = cfg.Path.Listener(), cfg.Path.Target()
		options = append(options, cfg.Path.DialOptions()...)
	} else {
		listener, target = listen(t)
	}
	options = append(options, cfg.DialOptions...)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
		// As the SDK does once its server has stopped: the sessions that outlived their streams (a stream that is over is an
		// implicit detach, and the session lives for its lease) end with the plugin, and no function outlives the test.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = capability.Shutdown(ctx)
	})

	conn, err := grpc.NewClient(target, options...)
	if err != nil {
		t.Fatalf("dial the exec capability: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Server{Plugin: capability, conn: conn}
}

// listen opens the listener of a Server and returns the target a client dials.
func listen(t TB) (net.Listener, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		return listener, listener.Addr().String()
	}
	// A short directory: a unix socket path is limited to about a hundred bytes, and t.TempDir can exceed it.
	dir, err := os.MkdirTemp("", "ex")
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener, "unix:" + path
}

// Client returns the host-side client of the served plugin, as the host dispenses it. Its V2 method is the exec v2
// client; its legacy methods address the legacy lane.
func (s *Server) Client(t TB) *exec.PluginClient {
	t.Helper()
	client, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, s.conn)
	if err != nil {
		t.Fatalf("build the exec client: %v", err)
	}
	typed, ok := client.(*exec.PluginClient)
	if !ok {
		t.Fatalf("unexpected exec client type %T", client)
	}
	return typed
}

// V2 returns the exec v2 client of the served plugin.
func (s *Server) V2(t TB) exec.Client {
	t.Helper()
	return s.Client(t).V2()
}

// Raw returns a raw generated gRPC client of the exec v2 service, for tests that must send what the SDK's own
// client refuses to (an unknown field, a selection) or look at the wire status.
func (s *Server) Raw() execv2.ExecClient { return execv2.NewExecClient(s.conn) }

// Conn returns the client connection, for tests that dial another service of the same plugin.
func (s *Server) Conn() *grpc.ClientConn { return s.conn }
