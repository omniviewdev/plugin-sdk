package exec_test

import (
	"context"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func TestChannelSink_BlocksWhenFull(t *testing.T) {
	out := make(chan exec.StreamOutput, 1)
	sink := exec.NewChannelSink(context.Background(), out)

	msg1 := exec.StreamOutput{SessionID: "s1", Data: []byte("first")}
	msg2 := exec.StreamOutput{SessionID: "s1", Data: []byte("second")}

	sink.OnOutput(msg1)

	done := make(chan struct{})
	go func() {
		sink.OnOutput(msg2)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("OnOutput returned immediately — expected it to block when channel is full")
	case <-time.After(50 * time.Millisecond):
	}

	<-out

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnOutput still blocked after draining channel")
	}

	got := <-out
	if string(got.Data) != "second" {
		t.Fatalf("expected 'second', got %q", string(got.Data))
	}
}

func TestChannelSink_UnblocksOnContextCancel(t *testing.T) {
	out := make(chan exec.StreamOutput, 1)
	ctx, cancel := context.WithCancel(context.Background())
	sink := exec.NewChannelSink(ctx, out)

	sink.OnOutput(exec.StreamOutput{Data: []byte("fill")})

	done := make(chan struct{})
	go func() {
		sink.OnOutput(exec.StreamOutput{Data: []byte("blocked")})
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("OnOutput still blocked after context cancellation")
	}
}
