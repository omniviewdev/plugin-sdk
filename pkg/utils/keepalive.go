package utils

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// A host and a plugin talk over a socket that their two processes share (a unix socket, or a loopback socket on Windows). When
// either process dies the kernel closes the socket and the other end sees it at once. What nothing reports is a peer that is alive
// and does not answer, whose end of the connection stays open and silent for ever: a frozen or a stopped process, or, for a peer
// that the connection reaches through a network, a path that dropped its packets. The keepalive finds such a peer: each side pings
// a connection that has gone quiet and drops it when the ping is not answered, which ends every stream on it at once with a
// transport error, so that the sessions and the calls that were waiting on it are told, and not left to wait.
//
// The two sides are set together. A server counts a strike for each ping of a client that comes sooner after the one before than its
// enforcement policy allows, and at the third strike answers with a GOAWAY (ENHANCE_YOUR_CALM, "too_many_pings") and closes the
// connection. Its policy defaults to a ping every 5 minutes at most and, with no stream open, one every 2 hours: a client that pings
// every 30 s is struck from its second ping on and refused at its fourth. The host's parameters are only safe against a server whose
// policy allows them.
const (
	// hostKeepaliveTime is how long the connection is quiet before the host pings the plugin: the plan's 30 s.
	hostKeepaliveTime = 30 * time.Second
	// hostKeepaliveTimeout is how long the host waits for the answer to its ping before it drops the connection.
	hostKeepaliveTimeout = 10 * time.Second

	// pluginPingMinTime is how often a client may ping the plugin. It is below the host's interval, and below the 10 s that a
	// grpc-go client never goes under (it raises a shorter Time to 10 s), so no host that is set within what grpc-go allows is
	// ever refused, and a peer that pings every second is.
	pluginPingMinTime = 5 * time.Second
	// pluginKeepaliveTime is how long the plugin's connection is quiet before the plugin asks after the host.
	pluginKeepaliveTime = 10 * time.Second
	// pluginKeepaliveTimeout is how long the plugin waits for the answer before it drops the connection: a host that vanished is
	// found 15 s after its last word.
	pluginKeepaliveTimeout = 5 * time.Second
)

// HostKeepalive returns the keepalive parameters a host sets on its connection to a plugin: after 30 s in which nothing has been
// received on the connection it pings the plugin, with a stream open or not, and when the ping is not answered within 10 s it drops
// the connection. A plugin that stopped answering is then found within 40 s, and every stream of the connection fails at once with a
// transport error (a status of code Unavailable) instead of waiting for a close that never comes.
//
// A host puts them in go-plugin's ClientConfig.GRPCDialOptions, as grpc.WithKeepaliveParams(utils.HostKeepalive()) or with
// sdk.HostKeepaliveDialOption, for the plugins that were built with an SDK that carries the matching policy (ServerKeepaliveOptions,
// served by every plugin from the release that added it). It is not part of sdk.GRPCDialOptions: a plugin that was built before
// answers a host that pings every 30 s with a GOAWAY after the fourth ping and closes the connection, and a host that has always dialed
// with those options would begin to lose its connections to every such plugin.
func HostKeepalive() keepalive.ClientParameters {
	return keepalive.ClientParameters{
		Time:                hostKeepaliveTime,
		Timeout:             hostKeepaliveTimeout,
		PermitWithoutStream: true,
	}
}

// ServerKeepaliveOptions returns the keepalive options of a plugin's gRPC server, which RegisterServerOpts sets before the caller's:
//
//   - The enforcement policy lets a client ping every 5 s or less often, with a stream open or not, so the pings of HostKeepalive
//     (every 30 s, with nothing open when the host is idle) are never answered with a GOAWAY. A client that pings more often, every
//     second say, still is.
//   - The server's own parameters ask after a host that has said nothing for 10 s, and drop the connection when the ping is not
//     answered within 5 s: a host that vanished without a FIN is found 15 s after its last word, and every stream on the connection
//     ends at once. A host that is merely idle answers, and a host that is alive but does not read its streams is not gone: its
//     transport answers the ping, and a stream that it does not read blocks its author, which the session's stall policy governs.
//
// A caller that passes grpc.KeepaliveParams or grpc.KeepaliveEnforcementPolicy to the server's factory replaces these: the caller's
// options come after, and grpc keeps the last.
func ServerKeepaliveOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             pluginPingMinTime,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    pluginKeepaliveTime,
			Timeout: pluginKeepaliveTimeout,
		}),
	}
}
