package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// kaOpenMethod is the method of an exec session's stream.
const kaOpenMethod = execv2.Exec_Open_FullMethodName

// kaSession is the host's end of a session, read until its stream fails.
type kaSession struct {
	mu  sync.Mutex
	err error
	at  time.Time
	end chan struct{}
}

// ended returns when the stream ended, whether it has, and what ended it.
func (s *kaSession) ended() (at time.Time, ended bool, err error) {
	select {
	case <-s.end:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.at, true, s.err
	default:
		return time.Time{}, false, nil
	}
}

// readInBackground reads a session's frames, as a host's reader does, until the stream ends.
func readInBackground(conn exec.Conn) *kaSession {
	s := &kaSession{end: make(chan struct{})}
	go func() {
		var err error
		for err == nil {
			_, err = conn.Recv()
		}
		s.mu.Lock()
		s.err, s.at = err, time.Now()
		s.mu.Unlock()
		close(s.end)
	}()
	return s
}

// kaRuns records when the function of each session was told to stop (its context ended), by the id the session was opened with as
// the label "id".
type kaRuns struct {
	mu    sync.Mutex
	stops map[string]time.Time
}

func (k *kaRuns) exec(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	<-ctx.Done()
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stops == nil {
		k.stops = map[string]time.Time{}
	}
	k.stops[p.Request.Labels["id"]] = time.Now()
	return exec.Exit{}, ctx.Err()
}

// stopped returns the sessions whose function was told to stop, and when.
func (k *kaRuns) stopped() map[string]time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]time.Time, len(k.stops))
	for id, at := range k.stops {
		out[id] = at
	}
	return out
}

// kaSecondHost is a second connection to the plugin of a test, over a path of its own that nothing cuts: the host that comes back to
// a plugin whose connection to it was dropped. The plugin serves the same exec capability on a second server, so that what the second
// host sees is the plugin's own state.
func kaSecondHost(t *testing.T, plugin *exec.Plugin) exec.Client {
	t.Helper()
	log := logging.NewNop()
	path := exectestkit.NewPath(t)
	server := sdk.GRPCServerFactoryWithLogger(log, nil)
	if err := plugin.GRPCServer(nil, server); err != nil {
		t.Fatalf("serve the exec capability on a second server: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(path.Listener()) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	options := append(sdk.GRPCDialOptionsWithLogger(log), sdk.HostKeepaliveDialOption(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	options = append(options, path.DialOptions()...)
	conn, err := grpc.NewClient(path.Target(), options...)
	if err != nil {
		t.Fatalf("dial the second server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatalf("build the exec client: %v", err)
	}
	return client.(*exec.PluginClient).V2()
}

// The exec lane's streams are among the streams a vanished peer ends: with three sessions open, the plugin's server drops the
// connection of a host that stopped answering and the stream of every session ends at that instant, and the host, through its own
// keepalive, finds the plugin that stopped answering and fails every session's stream with a typed error (a connection error: the
// session may still run) and not with an end it never received.
//
// A stream that ends is an implicit detach, not the end of its session: at that same instant the three sessions have no stream and a
// lease, their functions are not told to stop, and each still holds its place (the target serves three, so a fourth is refused). A
// host that comes back, over a connection of its own, finds them listed, ends one of them with EndSession, which frees its place, and
// the others end when their lease does (5 minutes after the instant their stream ended), closed by DETACH_TIMEOUT.
func TestKeepaliveAVanishedPeerEndsTheStreamOfEverySession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := exectestkit.NewPath(t)
		streams := &kaStreams{}
		conns := exectestkit.NewConnectionsT(t, "fake")
		const sessions = 3
		runs := &kaRuns{}
		target := exectest.NewFakeTarget()
		target.Info.Session.MaxSessions = sessions
		target.Exec = runs.exec
		lease := target.Info.Session.DetachTimeout
		server := exectestkit.Serve(t, exectestkit.ServeConfig{
			Targets:       []exec.Target{target},
			Connections:   conns,
			Path:          path,
			ServerOptions: []grpc.ServerOption{grpc.ChainStreamInterceptor(streams.intercept)},
		})
		// Whatever a session whose stream broke is left as, the plugin's shutdown ends it, so that nothing of the plugin outlives the
		// bubble.
		t.Cleanup(func() { _ = server.Plugin.Shutdown(context.Background()) })
		client := server.V2(t)
		openRequest := func(id string) *execv2.OpenRequest {
			return &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: &execv2.Start{
				Target: conns.WireTarget(t, "thing", "thing-1"),
				Labels: []*execv2.NameValue{{Name: "id", Value: id}},
			}}}
		}

		synctest.Sleep(3 * time.Second)
		var open []*kaSession
		for i := range sessions {
			conn, err := client.Open(conns.Context(context.Background()), openRequest(fmt.Sprintf("s%d", i)))
			if err != nil {
				t.Fatalf("open session %d: %v", i, err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			open = append(open, readInBackground(conn))
		}
		if got := streams.open(kaOpenMethod); got != sessions {
			t.Fatalf("the server has %d session streams, want %d", got, sessions)
		}
		host := kaSecondHost(t, server.Plugin)
		if list, err := host.ListSessions(context.Background()); err != nil || len(list.GetSessions()) != sessions {
			t.Fatalf("the second host lists %v, %v before the first host vanished, want the %d sessions", list.GetSessions(), err, sessions)
		}
		t0 := time.Now()
		path.Blackhole()

		// The plugin finds the host first: 10 s of silence and the 5 s of its ping that nobody answers.
		dropped := t0.Add(kaPluginTime + kaPluginWait)
		sleepUntil(dropped.Add(kaHalf))
		ends := streams.ends(kaOpenMethod)
		if len(ends) != sessions {
			t.Fatalf("%d of %d session streams had ended %v after the host vanished", len(ends), sessions, kaPluginTime+kaPluginWait+kaHalf)
		}
		for i, at := range ends {
			if got := at.Sub(t0); got != kaPluginTime+kaPluginWait {
				t.Errorf("session stream %d ended at +%v, want all of them at +%v: one connection, one instant", i, got, kaPluginTime+kaPluginWait)
			}
		}
		for i, s := range open {
			if _, ended, _ := s.ended(); ended {
				t.Errorf("the host's end of session %d failed before the host's own keepalive could have found anything", i)
			}
		}
		// The streams ended and the sessions did not: each has no stream and a lease that began when its stream ended, and none of
		// their functions was told to stop.
		wantIDs := []string{"s0", "s1", "s2"}
		list, err := host.ListSessions(context.Background())
		if err != nil {
			t.Fatalf("ListSessions after the connection of the first host was dropped: %v", err)
		}
		if ids := kaIDs(list.GetSessions()); !slices.Equal(ids, wantIDs) {
			t.Fatalf("the plugin lists %v after the connection of the host was dropped, want %v: a stream that ends is an implicit detach", ids, wantIDs)
		}
		for _, info := range list.GetSessions() {
			id := info.GetFacts().GetSessionId()
			if info.GetEndedWith() != nil || info.GetAttached() {
				t.Errorf("session %s is %v after its stream ended, want it live with no stream attached", id, info)
			}
			if got, want := info.GetLeaseExpiresUnixMs(), dropped.Add(lease).UnixMilli(); got != want {
				t.Errorf("session %s has a lease that ends at %d, want %d: the lease of a session begins when its stream ends, and lasts %v", id, got, want, lease)
			}
		}
		if stopped := runs.stopped(); len(stopped) != 0 {
			t.Errorf("the functions of %v were told to stop when their streams ended: the sessions detach, they do not end", stopped)
		}
		// A detached session holds its place: the target serves three sessions, and a fourth is refused until one of them is gone.
		if _, err := host.Open(conns.Context(context.Background()), openRequest("s3")); !kaRefusedAsFull(err) {
			t.Fatalf("a fourth session was opened beside three that are detached (%v), want TOO_MANY_SESSIONS", err)
		}
		if err := host.EndSession(context.Background(), &execv2.EndSessionRequest{SessionId: "s0", Mode: execv2.CloseMode_CLOSE_MODE_KILL}); err != nil {
			t.Fatalf("EndSession of a detached session: %v", err)
		}
		if stopped := runs.stopped(); len(stopped) != 1 || stopped["s0"].IsZero() {
			t.Errorf("the functions of %v were told to stop after EndSession of s0, want that of s0 only", stopped)
		}
		fourth, err := host.Open(conns.Context(context.Background()), openRequest("s3"))
		if err != nil {
			t.Fatalf("a session was refused after EndSession freed a place: %v", err)
		}
		t.Cleanup(func() { _ = fourth.Close() })

		// The host finds the plugin 30 s after its last word and 10 s after its ping.
		sleepUntil(t0.Add(kaHostTime + kaHostTimeout + kaHalf))
		for i, s := range open {
			at, ended, err := s.ended()
			if !ended {
				t.Errorf("the host's end of session %d still stands %v after the plugin vanished", i, kaHostTime+kaHostTimeout)
				continue
			}
			var refusal *exec.Error
			if !errors.As(err, &refusal) || refusal.Class != exec.ErrorClassConnection {
				t.Errorf("session %d failed with %v, want an exec error of class connection", i, err)
			}
			if got := at.Sub(t0); got < kaHostTime+kaHostTimeout || got > kaHostTime+kaHostTimeout+kaHalf {
				t.Errorf("session %d failed at +%v, want +%v", i, got, kaHostTime+kaHostTimeout)
			}
		}

		// The sessions nobody came back for end when their lease does, and not before: the lease was five minutes from the instant their
		// streams ended.
		sleepUntil(dropped.Add(lease - kaHalf))
		if stopped := runs.stopped(); len(stopped) != 1 {
			t.Errorf("the functions of %v were told to stop before their lease ended", stopped)
		}
		sleepUntil(dropped.Add(lease + kaHalf))
		stopped := runs.stopped()
		for _, id := range []string{"s1", "s2"} {
			if got := stopped[id]; got != dropped.Add(lease) {
				t.Errorf("the function of %s was told to stop at %v, want the end of its lease, %v", id, got.Sub(t0), dropped.Add(lease).Sub(t0))
			}
		}
		list, err = host.ListSessions(context.Background())
		if err != nil {
			t.Fatalf("ListSessions after the leases ended: %v", err)
		}
		want := map[string]execv2.ClosedBy{"s0": execv2.ClosedBy_CLOSED_BY_REQUEST, "s1": execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT, "s2": execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT}
		for _, info := range list.GetSessions() {
			id := info.GetFacts().GetSessionId()
			if id == "s3" {
				if info.GetEndedWith() != nil || !info.GetAttached() {
					t.Errorf("the session of the host that came back is %v, want it live and attached", info)
				}
				continue
			}
			if got := info.GetEndedWith().GetEnd().GetClosed().GetBy(); got != want[id] {
				t.Errorf("session %s ended with %v, want closed by %v", id, info.GetEndedWith(), want[id])
			}
		}
	})
}

// kaIDs is the ids of a listing, in the order it gives them.
func kaIDs(infos []*execv2.SessionInfo) []string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.GetFacts().GetSessionId()
	}
	return ids
}

// kaRefusedAsFull says whether an Open was refused because the target serves no more sessions.
func kaRefusedAsFull(err error) bool {
	var refusal *exec.Error
	return errors.As(err, &refusal) && refusal.Class == exec.ErrorClassLimit && refusal.Reason == exec.ErrorReasonTooManySessions
}
