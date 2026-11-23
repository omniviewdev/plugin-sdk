package utils

import (
	"testing"
	"time"

	"google.golang.org/grpc/keepalive"
)

// The host's parameters are the plan's: a ping after 30 s without anything received, a drop 10 s after an unanswered ping, and pings
// with no stream open. A host reads them from here, so they are pinned.
func TestHostKeepaliveIsThePlans(t *testing.T) {
	want := keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}
	if got := HostKeepalive(); got != want {
		t.Errorf("HostKeepalive() = %+v, want %+v", got, want)
	}
}

// The plugin's policy has to allow what the host does, or the host's connection is closed with a GOAWAY: the policy's minimum is below
// the host's interval, and below the 10 s under which grpc-go does not let any client ping, so that no host set within what grpc-go
// allows is refused. The behaviour is proved over a transport in pkg/sdk; this holds the relation between the two sets of constants
// for whoever changes one.
func TestPluginPingPolicyAllowsTheHostsPings(t *testing.T) {
	const grpcClientFloor = 10 * time.Second
	if pluginPingMinTime >= hostKeepaliveTime {
		t.Errorf("the plugin refuses pings more often than every %v and the host pings every %v", pluginPingMinTime, hostKeepaliveTime)
	}
	if pluginPingMinTime > grpcClientFloor {
		t.Errorf("the plugin refuses pings more often than every %v, but a grpc-go client may ping every %v", pluginPingMinTime, grpcClientFloor)
	}
	if pluginPingMinTime <= 0 {
		t.Errorf("the plugin's minimum ping interval is %v: a policy of nothing would leave the default of 5 minutes", pluginPingMinTime)
	}
}
