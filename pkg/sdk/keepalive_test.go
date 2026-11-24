package sdk_test

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
)

// Stream health: a plugin finds a host that vanished, a host finds a plugin that vanished, and neither finds the other dead when it
// is only idle. See docs/exec.md, "Stream health". Every test runs on a bubble's clock over a Path, and the instants it asserts
// are the instants of the production parameters: the host pings after 30 s of silence and gives up 10 s later, the plugin after 10 s
// and 5 s. A test looks at the system between its events, half a second before one and half a second after it, and never at the
// instant of one: when the test's own timer and a timer of the system fall due together, which of them runs first is not decided on
// more than one CPU, and a test that looks at that instant sees the system before or after the event as the scheduler pleases.

// pluginPingsOff is the caller's option that takes the plugin's own pings out of a test of the other side: a plugin that pings
// a connection that is answering keeps the host's timer from ever firing, and a host that pings keeps the plugin's.
var pluginPingsOff = []grpc.ServerOption{grpc.KeepaliveParams(keepalive.ServerParameters{Time: 24 * time.Hour})}

// wantVanishedHostEnds runs the scene of a host that vanished against the server that build makes: a stream stands, the path is
// cut, and the plugin has to ask after the host once it has heard nothing for its Time, and end every stream when the Timeout
// after that has passed without an answer. Time and timeout are what the server is expected to be set to.
func wantVanishedHostEnds(t *testing.T, build func([]grpc.ServerOption) *grpc.Server, extra []grpc.ServerOption, ping, timeout time.Duration) {
	t.Helper()
	// A host that sets no keepalive: nothing but the plugin's own can notice that it is gone.
	rig := newKaRig(t, kaConfig{build: build, server: extra})
	synctest.Sleep(3 * time.Second) // so that the connection is not at the start of its own clock
	rig.watch(t)
	rig.check(t)
	t0 := time.Now()
	rig.path.Blackhole()

	sleepUntil(t0.Add(ping - kaHalf))
	if pings := rig.pings(exectestkit.FromPlugin, t0); len(pings) != 0 {
		t.Errorf("the plugin pinged before its Time (%v) of silence had passed: %v", ping, pings)
	}
	if ends := rig.streams.ends(kaWatchMethod); len(ends) != 0 {
		t.Errorf("a stream ended before the plugin had so much as asked")
	}
	sleepUntil(t0.Add(ping + kaHalf))
	pings := rig.pings(exectestkit.FromPlugin, t0)
	if len(pings) != 1 || pings[0].At.Sub(t0) != ping {
		t.Errorf("after %v of silence the plugin sent %v, want one PING at +%v", ping, pings, ping)
	}
	sleepUntil(t0.Add(ping + timeout - kaHalf))
	if ends := rig.streams.ends(kaWatchMethod); len(ends) != 0 {
		t.Errorf("a stream ended before the plugin's timeout (%v) had passed", timeout)
	}
	sleepUntil(t0.Add(ping + timeout + kaHalf))
	ends := rig.streams.ends(kaWatchMethod)
	if len(ends) != 1 {
		t.Fatalf("%d streams had ended %v after the host vanished, want the one that stood", len(ends), ping+timeout)
	}
	t.Logf("the plugin pinged at +%v and ended the stream at +%v after the host's last word", ping, ends[0].Sub(t0))
	if got := ends[0].Sub(t0); got < ping+timeout || got > ping+timeout+kaHalf {
		t.Errorf("the stream ended at +%v, want +%v (the plugin's time and timeout)", got, ping+timeout)
	}
	if pings := rig.pings(exectestkit.FromPlugin, t0); len(pings) != 1 {
		t.Errorf("the plugin pinged %d times, want once: it gives up when its ping is not answered, it does not ask again", len(pings))
	}
}

// A host that vanished without a FIN (a frozen process, a path that dropped its packets) leaves the plugin's connection open and
// silent for ever. The plugin's server asks after it once the connection has been silent for 10 s, and when nothing answers
// within 5 s it closes the connection, which ends every stream on it at once: the sessions of the host are found in 15 s.
func TestKeepalivePluginEndsTheStreamsOfAHostThatVanished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wantVanishedHostEnds(t, nil, nil, kaPluginTime, kaPluginWait)
	})
}

// The server that Serve hands go-plugin is the one that has the keepalive: the plugins a host runs are served through it.
func TestKeepaliveServeHandsGoPluginAServerThatEndsTheStreamsOfAHostThatVanished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wantVanishedHostEnds(t, serveLike(t), nil, kaPluginTime, kaPluginWait)
	})
}

// The keepalive of the SDK is a default: what the caller gives the factory for it replaces it, as everything the caller gives does,
// so a plugin that needs other times can have them.
func TestKeepaliveCallerOptionsReplaceTheSDKs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		custom := []grpc.ServerOption{grpc.KeepaliveParams(keepalive.ServerParameters{Time: 3 * time.Second, Timeout: 2 * time.Second})}
		wantVanishedHostEnds(t, nil, custom, 3*time.Second, 2*time.Second)
	})
}

// A plugin that vanished (a frozen process, a path that dropped its packets) is found by the host: it pings after 30 s of silence,
// gives up 10 s after, and every stream of the connection fails at once with a transport error, typed Unavailable, and the
// connection is no longer a connection that can be used. It is not left to wait for ever.
func TestKeepaliveHostFindsAPluginThatVanishedAndFailsItsStreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{host: true})
		synctest.Sleep(3 * time.Second)
		w := rig.watch(t)
		rig.check(t)
		t0 := time.Now()
		rig.path.Blackhole()

		sleepUntil(t0.Add(kaHostTime - kaHalf))
		if pings := rig.pings(exectestkit.FromHost, t0); len(pings) != 0 {
			t.Errorf("the host pinged into the silence before its Time (%v): %v", kaHostTime, pings)
		}
		if _, ended, _ := w.ended(); ended {
			t.Errorf("the stream failed before the host had asked after the plugin")
		}
		sleepUntil(t0.Add(kaHostTime + kaHalf))
		pings := rig.pings(exectestkit.FromHost, t0)
		if len(pings) != 1 || pings[0].At.Sub(t0) != kaHostTime {
			t.Errorf("after %v of silence the host sent %v, want one PING at +%v", kaHostTime, pings, kaHostTime)
		}
		sleepUntil(t0.Add(kaHostTime + kaHostTimeout - kaHalf))
		if _, ended, _ := w.ended(); ended {
			t.Errorf("the stream failed before the host's timeout (%v) had passed", kaHostTimeout)
		}
		sleepUntil(t0.Add(kaHostTime + kaHostTimeout + kaHalf))
		at, ended, err := w.ended()
		if !ended {
			t.Fatalf("the stream still stands %v after the plugin vanished", kaHostTime+kaHostTimeout)
		}
		t.Logf("the host pinged at +%v and failed the stream at +%v after the plugin's last word", kaHostTime, at.Sub(t0))
		if got := at.Sub(t0); got < kaHostTime+kaHostTimeout || got > kaHostTime+kaHostTimeout+kaHalf {
			t.Errorf("the stream failed at +%v, want +%v", got, kaHostTime+kaHostTimeout)
		}
		if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "keepalive") {
			t.Errorf("the stream failed with %v, want Unavailable, and the keepalive as the reason", err)
		}
		if state := rig.conn.GetState(); state == connectivity.Ready {
			t.Errorf("the connection to a plugin that was found dead is still %v", state)
		}
	})
}

// The host's pings are allowed without a stream: a plugin that vanished while the host held no stream is found as well, and the
// connection does not stay Ready for the call that comes later.
func TestKeepaliveHostFindsAPluginThatVanishedWithNoStreamOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{host: true})
		rig.check(t) // the connection is made and no stream is open
		t0 := time.Now()
		rig.path.Blackhole()

		sleepUntil(t0.Add(kaHostTime + kaHostTimeout + kaHalf))
		pings := rig.pings(exectestkit.FromHost, t0)
		if len(pings) != 1 || pings[0].At.Sub(t0) != kaHostTime {
			t.Errorf("the host sent %v with no stream open, want one PING at +%v", pings, kaHostTime)
		}
		if state := rig.conn.GetState(); state == connectivity.Ready {
			t.Errorf("a connection with no stream to a plugin that vanished is still %v after %v", state, kaHostTime+kaHostTimeout+kaHalf)
		}
	})
}

// A peer that answers late is not a peer that vanished. The path stalls for 12 s, which holds the plugin's ping (sent at +10 s) and the
// answer behind it, and then resumes: the answer arrives 3 s before the plugin's 15 s, and the connection and its stream stand for as
// long as the test looks. A host that stays silent for longer is dropped, as the test that cuts the path shows.
func TestKeepalivePluginDropsNoHostThatAnswersWithinItsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{})
		synctest.Sleep(3 * time.Second)
		rig.watch(t)
		rig.check(t)

		t0 := time.Now()
		rig.path.Stall()
		sleepUntil(t0.Add(kaPluginTime + 2*time.Second))
		rig.path.Resume()
		sleepUntil(t0.Add(kaPluginTime + 2*time.Second + time.Minute + kaHalf))
		if ends := rig.streams.ends(kaWatchMethod); len(ends) != 0 {
			t.Errorf("the plugin dropped a host that answered its ping 2 s after the ping was sent, with 3 s to spare")
		}
		if goaways := goAways(rig.path); len(goaways) != 0 {
			t.Errorf("a GOAWAY crossed the path: %v", goaways)
		}
		if dials := rig.path.Dials(); dials != 1 {
			t.Errorf("the host connected %d times, want once", dials)
		}
		rig.check(t)

		// The path stalls again, and this time for good: the plugin drops the connection when its timeout has passed.
		t1 := time.Now()
		rig.path.Stall()
		sleepUntil(t1.Add(kaPluginTime + kaPluginWait + kaHalf))
		if ends := rig.streams.ends(kaWatchMethod); len(ends) != 1 {
			t.Errorf("%d streams had ended after the path stalled for good, want the one that stood", len(ends))
		}
	})
}

// The same for the host: a plugin whose answer arrives 5 s before the host's 40 s is not dropped, and one that is stalled for longer is.
func TestKeepaliveHostDropsNoPluginThatAnswersWithinItsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{host: true, server: pluginPingsOff})
		synctest.Sleep(3 * time.Second)
		w := rig.watch(t)
		rig.check(t)

		t0 := time.Now()
		rig.path.Stall()
		sleepUntil(t0.Add(kaHostTime + 5*time.Second))
		rig.path.Resume()
		sleepUntil(t0.Add(kaHostTime + 5*time.Second + time.Minute + kaHalf))
		if _, ended, err := w.ended(); ended {
			t.Errorf("the host dropped a plugin that answered its ping 5 s after the ping was sent, with 5 s to spare: %v", err)
		}
		if dials := rig.path.Dials(); dials != 1 {
			t.Errorf("the host connected %d times, want once", dials)
		}
		if state := rig.conn.GetState(); state != connectivity.Ready {
			t.Errorf("the connection is %v, want READY", state)
		}

		t1 := time.Now()
		rig.path.Stall()
		sleepUntil(t1.Add(kaHostTime + kaHostTimeout + kaHalf))
		if _, ended, err := w.ended(); !ended || status.Code(err) != codes.Unavailable {
			t.Errorf("after the path stalled for good the stream's end is %v (ended %v), want Unavailable", err, ended)
		}
	})
}

// The host's pings are never refused. With nothing else on the connection (the plugin's own pings would keep the host's timer from
// ever firing) the host pings every 30 s, with no stream open, for as long as it is idle, and the plugin neither answers with a
// GOAWAY (a grpc server's policy, unset, closes the connection of a host that pings every 30 s after the fourth ping) nor
// closes the connection.
func TestKeepalivePluginNeverRefusesAHostsPings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{host: true, server: pluginPingsOff})
		rig.check(t)
		const idle = 20 * time.Minute // below the 30 min after which gRPC makes a client of NewClient idle
		t0 := time.Now()
		sleepUntil(t0.Add(idle + kaHalf))

		pings := rig.pings(exectestkit.FromHost, time.Time{})
		if want := int(idle/kaHostTime) - 2; len(pings) < want {
			t.Errorf("the host pinged %d times in %v, want at least %d: the test would prove nothing", len(pings), idle, want)
		}
		for i := 1; i < len(pings); i++ {
			if gap := pings[i].At.Sub(pings[i-1].At); gap < kaHostTime || gap > kaHostTime+kaHalf {
				t.Errorf("pings %d and %d were %v apart, want %v", i-1, i, gap, kaHostTime)
			}
		}
		if goaways := goAways(rig.path); len(goaways) != 0 {
			t.Errorf("the plugin answered the host's pings with %v", goaways)
		}
		if dials := rig.path.Dials(); dials != 1 {
			t.Errorf("the host connected %d times in %v, want once: the connection was closed", dials, idle)
		}
		if state := rig.conn.GetState(); state != connectivity.Ready {
			t.Errorf("the connection is %v after %v idle, want READY", state, idle)
		}
		rig.check(t)
	})
}

// With both sides set as in production, a connection that is only idle is never closed: the plugin asks after the host every 10 s, the
// host answers each time, and a stream that stands through it is still there.
func TestKeepaliveAHealthyIdleConnectionIsNotClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{host: true})
		w := rig.watch(t)
		rig.check(t)
		const idle = 20 * time.Minute
		t0 := time.Now()
		sleepUntil(t0.Add(idle + kaHalf))

		pings := rig.pings(exectestkit.FromPlugin, time.Time{})
		if want := int(idle/kaPluginTime) - 2; len(pings) < want {
			t.Errorf("the plugin pinged %d times in %v, want at least %d: the test would prove nothing", len(pings), idle, want)
		}
		if acks := rig.acks(exectestkit.FromHost); acks < len(pings)-1 {
			t.Errorf("the host answered %d of the plugin's %d pings", acks, len(pings))
		}
		if goaways := goAways(rig.path); len(goaways) != 0 {
			t.Errorf("a GOAWAY crossed a healthy connection: %v", goaways)
		}
		if _, ended, err := w.ended(); ended {
			t.Errorf("the stream ended after %v idle: %v", idle, err)
		}
		if dials := rig.path.Dials(); dials != 1 {
			t.Errorf("the host connected %d times, want once", dials)
		}
		if state := rig.conn.GetState(); state != connectivity.Ready {
			t.Errorf("the connection is %v, want READY", state)
		}
		rig.check(t)
	})
}

// The plugin's policy is a limit as well as a permission: a peer that pings more often than the policy allows is told to go away
// (ENHANCE_YOUR_CALM, "too_many_pings") and the connection is closed. The policy that a gRPC client is subject to is the one a raw
// peer is held to, so a raw peer pings every second.
func TestKeepalivePolicyRefusesAPeerThatPingsTooOften(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newKaRig(t, kaConfig{server: pluginPingsOff})
		raw := dialRaw(t, rig.path)
		for range 8 {
			raw.ping(t)
			synctest.Sleep(time.Second)
		}
		goaways := goAways(rig.path)
		if len(goaways) != 1 || goaways[0].From != exectestkit.FromPlugin || goaways[0].ErrCode != exectestkit.ErrCodeEnhanceYourCalm ||
			goaways[0].Debug != "too_many_pings" {
			t.Errorf("a peer that pinged every second was answered with %v, want one GOAWAY ENHANCE_YOUR_CALM \"too_many_pings\"", goaways)
		}
		if !raw.isClosed() {
			t.Errorf("the connection of a peer that pinged every second is still open")
		}
	})
}

// A peer that pings every 10 s, the most often that a gRPC client does, and one that pings every 30 s, as the host does, are
// within the policy, with no stream open, for as long as they like.
func TestKeepalivePolicyAllowsPingsFromTenSecondsOnWithNoStreamOpen(t *testing.T) {
	for _, every := range []time.Duration{10 * time.Second, kaHostTime} {
		t.Run(every.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rig := newKaRig(t, kaConfig{server: pluginPingsOff})
				raw := dialRaw(t, rig.path)
				for range int(10 * time.Minute / every) {
					raw.ping(t)
					synctest.Sleep(every)
				}
				if goaways := goAways(rig.path); len(goaways) != 0 {
					t.Errorf("a peer that pinged every %v with no stream open was answered with %v", every, goaways)
				}
				if raw.isClosed() {
					t.Errorf("the connection of a peer that pinged every %v was closed", every)
				}
			})
		})
	}
}

// The options that a host builds with the SDK's helpers do not ping: the keepalive is opted into with HostKeepaliveDialOption,
// because a plugin that was built before the SDK carried the policy answers a host that pings with a GOAWAY, and every host that
// already dials with the helpers would start to lose its connections to such plugins.
func TestKeepaliveGRPCDialOptionsSetNone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for name, options := range map[string]func() []grpc.DialOption{
			"GRPCDialOptions":           sdk.GRPCDialOptions,
			"GRPCDialOptionsWithLogger": nil,
		} {
			rig := newKaRig(t, kaConfig{server: pluginPingsOff, options: options})
			rig.check(t)
			t0 := time.Now()
			sleepUntil(t0.Add(10*time.Minute + kaHalf))
			if pings := rig.pings(exectestkit.FromHost, t0); len(pings) != 0 {
				t.Errorf("%s: a host that dials with the SDK's helper pinged %d times: %v", name, len(pings), pings)
			}
		}
	})
}
