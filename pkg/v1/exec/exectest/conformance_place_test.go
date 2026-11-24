package exectest

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The plugin answers a stream that broke the protocol with its status, and it gives the place of that session back when the function of the
// session has returned and the session has been removed, which is a moment after the host has read the status. A target that serves one session at a
// time refuses, for that moment, the open of the next session with LIMIT / TOO_MANY_SESSIONS, and how long the moment is a machine decides: on a busy
// one a host that opens the next session as soon as it has read the status meets the refusal. A body that does so fails for the suite's own sake, on
// the kind of an author whose target serves one session at a time, and only now and then.

// latePlaceClient is the host's client of a plugin that gives the place of a session back late: after the host has read the status that ended a
// session, the next opens (late of them) are refused with LIMIT / TOO_MANY_SESSIONS, and the open after them is the plugin's own.
type latePlaceClient struct {
	exec.Client
	late    int64
	owed    atomic.Int64  // the opens that are still to be refused
	refused *atomic.Int64 // counts every refusal, of every client that is served
}

func (c *latePlaceClient) Open(ctx context.Context, req *execv2.OpenRequest) (exec.Conn, error) {
	for owed := c.owed.Load(); owed > 0; owed = c.owed.Load() {
		if c.owed.CompareAndSwap(owed, owed-1) {
			c.refused.Add(1)
			return nil, &exec.Error{Class: exec.ErrorClassLimit, Reason: exec.ErrorReasonTooManySessions}
		}
	}
	conn, err := c.Client.Open(ctx, req)
	if err != nil {
		return nil, err
	}
	return &latePlaceConn{Conn: conn, client: c}, nil
}

// latePlaceConn is a stream of that client: a status that ends it is what the place of its session waits for.
type latePlaceConn struct {
	exec.Conn
	client *latePlaceClient
}

func (c *latePlaceConn) Recv() (*execv2.ServerFrame, error) {
	frame, err := c.Conn.Recv()
	var status *exec.Error
	if errors.As(err, &status) && status.Class != exec.ErrorClassCancelled {
		c.client.owed.Store(c.client.late)
	}
	return frame, err
}

// serveLatePlace serves the real plugin and has the host read it through a client whose places come back late.
func serveLatePlace(late int64, refused *atomic.Int64) serveFunc {
	return func(t checkT, targets []exec.Target, conns *exectestkit.Connections) *wireServer {
		server := serveReal(t, targets, conns)
		server.client = &latePlaceClient{Client: server.client, late: late, refused: refused}
		return server
	}
}

// Every body that drives a plugin over a transport passes for a target that serves one session at a time whose places come back late: a body that
// has read the status that ended a session opens the next one once its place is free, and does not take the first refusal for a failure. The test
// also holds that the refusal was met: a body list that no longer ends a session with a status before it opens another would pass this for nothing.
func TestEveryWireBodyOpensTheNextSessionOnceThePlaceOfAnEndedOneIsFree(t *testing.T) {
	var one *shape
	for _, s := range openShapes() {
		if s.info().Session.MaxSessions == 1 {
			one = &s
			break
		}
	}
	if one == nil {
		t.Fatal("no shape of the table declares MaxSessions 1")
	}
	var refused, calls atomic.Int64
	for _, c := range engineCases() {
		if c.wire == nil {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			rec := &recordT{}
			rec.run(func(r checkT) {
				c.wire(r, declared(t, *one, &calls), &wireEnv{resource: "thing-1", serve: serveLatePlace(3, &refused)})
			})
			if failures := rec.failed(); len(failures) != 0 {
				t.Errorf("a target that serves one session at a time and gives its places back late fails the body: %q", failures)
			}
		})
	}
	if got := refused.Load(); got == 0 {
		t.Errorf("no body met a place that was not free yet: the test holds nothing")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the author's Exec was called %d times: the cases run a fake built from what the author declared", got)
	}
}
