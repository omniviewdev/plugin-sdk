package exectest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A session that nobody holds ends when its lease runs out, and a host that lists it meanwhile reads it in one of three states: alive,
// with its lease running; ended, with a tombstone; and, for the moment between the plugin deciding that the lease is over and its
// tombstone being readable, ending, which a plugin lists as a session with no stream, no lease and no end. How long each of the three
// lasts is the machine's: a lease of one millisecond is over before a host on a loaded runner has asked, and the moment between is
// as long as the scheduler makes it. A body that ships to every author must therefore read nothing of a session whose lease is about
// to run out except its end, which comes whatever the pace.

// lapsedClient is the client of a plugin that the host reads after the lease of every session that had one about to run out has run out,
// which is how a host that is slow, or a machine that is busy, reads a lease of a millisecond. The plugin has ended the session when the
// host asks, so the wait for it is the event that the lease ran out and not a delay, and what ListSessions then says first of it is what
// a plugin says of a session between the moment its end is decided and the moment its tombstone is readable: no stream, no lease, no
// end. Every later listing says the tombstone.
type lapsedClient struct {
	exec.Client
	// windows counts the sessions that were listed so once, over every plugin that the case serves.
	windows *atomic.Int64
	mu      sync.Mutex
	listed  map[string]bool // the sessions whose lease ran out and whose end has been listed in the moment before the tombstone
}

// lapseHorizon is how soon a lease must end to be one that is about to run out: the leases that a body leaves a session with for its
// scenario to read are a minute or more.
const lapseHorizon = 10 * time.Second

// lapsing says whether the list has a session with no stream whose lease ends within the horizon.
func lapsing(list *execv2.ListSessionsResponse, now time.Time) bool {
	for _, info := range list.GetSessions() {
		if info.GetEndedWith() == nil && !info.GetAttached() && info.GetLeaseExpiresUnixMs() != 0 &&
			info.GetLeaseExpiresUnixMs() < now.Add(lapseHorizon).UnixMilli() {
			return true
		}
	}
	return false
}

func (c *lapsedClient) ListSessions(ctx context.Context) (*execv2.ListSessionsResponse, error) {
	var list *execv2.ListSessionsResponse
	var err error
	// The plugin is asked until no lease that was about to run out is left running: each of them ends in a millisecond, within the bound
	// of a case, whatever the machine.
	if !poll(caseBound, func() bool {
		list, err = c.Client.ListSessions(ctx)
		return err != nil || !lapsing(list, time.Now())
	}) {
		return list, err
	}
	if err != nil {
		return nil, err
	}
	list = proto.Clone(list).(*execv2.ListSessionsResponse)
	for _, info := range list.GetSessions() {
		if info.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
			continue
		}
		id := info.GetFacts().GetSessionId()
		c.mu.Lock()
		first := !c.listed[id]
		c.listed[id] = true
		c.mu.Unlock()
		if first {
			info.EndedWith, info.LeaseExpiresUnixMs, info.Attached = nil, 0, false
			c.windows.Add(1)
		}
	}
	return list, nil
}

// lapsedPlugins serves plugins that the host reads late. A case serves more than one (the ones that end the connection or the plugin
// run on a plugin of their own), and each is read through a client of its own.
type lapsedPlugins struct {
	windows atomic.Int64
}

// serve serves the real plugin and has the host read it through the client of a plugin that is read late.
func (l *lapsedPlugins) serve(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
	t.Helper()
	server := serveReal(t, targets, conns)
	server.client = &lapsedClient{Client: server.client, windows: &l.windows, listed: map[string]bool{}}
	return server
}

// leasesThatRunOut is how many sessions of a kind of target the scenarios of case 9 leave with a lease that runs out: one for each way the
// host leaves it.
func leasesThatRunOut(caps exec.Capabilities) int {
	n := 0
	for _, sc := range endScenarios(caps) {
		if sc.end.GetClosed().GetBy() == execv2.ClosedBy_CLOSED_BY_DETACH_TIMEOUT {
			n += 2
		}
	}
	return n
}

// Case 9 reads only the end of a session whose lease runs out. The host that lists the session after its lease ran out, as one on a loaded
// runner does, reads it ending (no stream, no lease, no end) and then ended; the body is the same for every pace, and holds the plugin to
// the end alone: closed by DETACH_TIMEOUT, with the survival that the Kill of the session says. A body that read what a session with a
// lease of a millisecond says of the lease it lives by would fail for the plugin that is right: the one that is listed in the moment before
// its tombstone.
func TestCaseNineReadsOnlyTheEndOfASessionWhoseLeaseRanOutBeforeTheHostListedIt(t *testing.T) {
	for _, s := range openShapes() {
		info := s.info()
		if info.Session.IsZero() {
			continue
		}
		t.Run(s.name, func(t *testing.T) {
			var calls atomic.Int64
			late := &lapsedPlugins{}
			rec := &recordT{}
			rec.run(func(r checkT) {
				caseWireEnds(r, declared(t, s, &calls), &wireEnv{resource: "thing-1", serve: late.serve})
			})
			if failures := rec.failed(); len(failures) != 0 {
				t.Errorf("the real plugin fails case 9 when it is read after its leases ran out, for a target declared as %s: %q", s.name, failures)
			}
			if got, want := late.windows.Load(), int64(leasesThatRunOut(info.Capabilities)); got != want {
				t.Errorf("%d sessions were listed in the moment before their tombstone, want %d: one for each lease that runs out and each way the host leaves", got, want)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("the author's Exec was called %d times", got)
			}
		})
	}
}
