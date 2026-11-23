package exec

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/session"
)

// heldCommand is a command whose end has been emitted (Done is closed) and whose id is still held, as it is in the window between
// the two that the engine leaves open on purpose: the command is not removed until a producer of its entry returns. release lets
// the producer go.
func heldCommand(t *testing.T) (c *command, release func()) {
	t.Helper()
	c = &command{doneC: make(chan struct{}), result: commandResult{StartObserved: true, Output: 0}}
	close(c.doneC)
	registry := session.New(session.Config[*command]{})
	entry, err := registry.Admit(context.Background(), session.Spec{ID: "held"}, func(context.Context) (*command, error) { return c, nil })
	if err != nil {
		t.Fatal(err)
	}
	c.entry = entry
	hold := make(chan struct{})
	if !entry.Go(func() { <-hold }) {
		t.Fatal("the entry refused its producer")
	}
	t.Cleanup(func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	})
	return c, func() {
		entry.End(session.CauseFinished)
		close(hold)
	}
}

func newTestRunCall(ctx context.Context, c *command) *runCall {
	return &runCall{
		ctx: ctx, cmd: c, req: runRequest{RequestID: "held"},
		reader: &runReader{
			stdout: newOutputSink(false, 8, truncateHeadTail), stderr: newOutputSink(false, 8, truncateHeadTail), done: make(chan struct{}),
		},
	}
}

// A result is handed over after the id of the run is free, not when the command's end is emitted: a host that reads the result
// and runs the same id again must not be refused for the run that has just ended. While the id is held the call waits, and a call
// that is over while it waits returns its status and no result.
func TestARunsResultIsNotHandedOverWhileItsIDIsStillHeld(t *testing.T) {
	c, release := heldCommand(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the call is over: the wait for the id has nothing else to end it
	res, err := newTestRunCall(ctx, c).result(true, true)
	if res != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("a result of %v, %v while the id was held and the call was over, want no result and the status Canceled", res, err)
	}

	release()
	<-c.Gone()
	res, err = newTestRunCall(context.Background(), c).result(true, true)
	if err != nil || res.GetRequestId() != "held" {
		t.Fatalf("a result of %v, %v once the id was free, want the result", res, err)
	}
}
