package exec

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The call timeout of Open: the SDK bounds the wait for Opened for a caller that set no deadline of its own, and the session
// that Opened announces lives as long as its stream, which is as long as the host wants it. The client runs on the clock of a
// synctest bubble, so that the timeout passes at once and the test asks what it did.

// openOnlyRPC is an ExecClient that serves Open and nothing else: its stream says Opened, unless silent, and then waits for its
// context to end.
type openOnlyRPC struct {
	execv2.ExecClient
	streamCtx context.Context
	silent    bool
}

func (r *openOnlyRPC) Open(ctx context.Context, _ ...grpc.CallOption) (execv2.Exec_OpenClient, error) {
	r.streamCtx = ctx
	return &openOnlyStream{ctx: ctx, silent: r.silent}, nil
}

type openOnlyStream struct {
	grpc.ClientStream
	ctx    context.Context
	silent bool
	opened bool
}

func (s *openOnlyStream) Send(*execv2.ClientFrame) error { return nil }

func (s *openOnlyStream) Recv() (*execv2.ServerFrame, error) {
	if !s.opened && !s.silent {
		s.opened = true
		return openedFrame(&execv2.Opened{Facts: &execv2.SessionFacts{SessionId: "x"}, Replay: &execv2.Replay{}}), nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *openOnlyStream) Context() context.Context { return s.ctx }

// The timer of the call timeout is stopped once Opened has arrived: the stream of an open session is not cancelled when the call
// timeout runs out (M13: a timer that was never stopped cancelled every session the call timeout after it was opened, 30 s, and
// nothing noticed). The clock goes on to ten times the timeout while the test waits for the stream to be cancelled.
func TestOpenClientCallTimerIsStoppedOnceOpened(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rpc := &openOnlyRPC{}
		client := newClientV2(rpc, nil)
		client.callTimeout = 50 * time.Millisecond
		conn, err := client.Open(context.Background(), &execv2.OpenRequest{SessionId: "x"})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer conn.Close()
		select {
		case <-rpc.streamCtx.Done():
			t.Fatalf("the stream of an open session was cancelled by the call timeout after Opened had arrived: %v", rpc.streamCtx.Err())
		case <-time.After(10 * client.callTimeout):
		}
	})
}

// And the other half: an Open that is never answered is given up at the call timeout, for a caller that set no deadline of its
// own. Without the timer the wait would never end, which the bubble reports as a deadlock; a timer set for longer than the call
// timeout gives up late, which the bubble's exact clock shows.
func TestOpenClientGivesUpOnAnOpenThatIsNeverAnsweredAtTheCallTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := newClientV2(&openOnlyRPC{silent: true}, nil)
		client.callTimeout = 100 * time.Millisecond
		start := time.Now()
		if _, err := client.Open(context.Background(), &execv2.OpenRequest{SessionId: "x"}); err == nil {
			t.Fatal("an Open that was never answered returned a session")
		}
		if took := time.Since(start); took > client.callTimeout {
			t.Errorf("Open gave up on an answer that never came after %v, want within the call timeout of %v", took, client.callTimeout)
		}
	})
}
