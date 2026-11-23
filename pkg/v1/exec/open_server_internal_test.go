package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// silentStream is an Open stream whose host never sends a frame.
type silentStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *silentStream) Context() context.Context { return s.ctx }

func (s *silentStream) Recv() (*execv2.ClientFrame, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *silentStream) Send(*execv2.ServerFrame) error {
	return errors.New("a stream that sent no open frame is sent nothing")
}

// A stream that connects and sends nothing holds a goroutine of the plugin: it is given the first-frame timeout and then
// refused, typed, so that the host (and a test) can tell it from a stream that broke.
func TestAStreamThatSendsNoOpenFrameIsRefusedAtTheTimeout(t *testing.T) {
	registry, err := newTargetRegistry([]Target{validTarget()})
	if err != nil {
		t.Fatal(err)
	}
	server := newServerV2(nil, registry, nil)
	server.openFirstFrameTimeout = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- server.Open(&silentStream{ctx: ctx}) }()
	select {
	case err := <-returned:
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("Open returned %T %v, want a status", err, err)
		}
		if got := errorFromStatus(context.Background(), err); got == nil {
			t.Fatal("the status does not decode")
		} else if typed, ok := got.(*Error); !ok || typed.Class != ErrorClassTimeout {
			t.Fatalf("status %v decodes to %v, want a typed TIMEOUT", st, got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Open did not return for a stream that never sent its open frame")
	}
}
