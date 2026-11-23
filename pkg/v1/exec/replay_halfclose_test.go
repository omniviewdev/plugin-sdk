package exec_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A host that closes its side of the stream (the transport's half-close: io.EOF at the plugin's Recv, which is not a stdin EOF, that is
// CloseInput) leaves its session as a stream that breaks does, and it can still receive: its stream ends with Detached, with no reason
// (the host did not say Detach) and the session's own lease, and what the plugin had sent before is in order and whole. Over a real
// transport, with the generated client, which can half-close; the SDK's own client has no call for it.
func TestAHostThatHalfClosesTheStreamOnTheWireIsToldDetachedWithNoReasonAfterEveryFrameThatWasSentAndItsSessionLives(t *testing.T) {
	rig := newGatedRig(t, exechook.EngineConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), engineWait)
	defer cancel()
	stream, err := rig.server.Raw().Open(rig.rawCtx(ctx))
	if err != nil {
		t.Fatalf("open the raw stream: %v", err)
	}
	if err := stream.Send(&execv2.ClientFrame{Frame: &execv2.ClientFrame_Open{Open: rig.openRequest("s1", withoutTTY)}}); err != nil {
		t.Fatalf("send the open frame: %v", err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetOpened() == nil {
		t.Fatalf("the first frame: %v (%v), want Opened", first, err)
	}
	rig.grant(t, 200_000) // the host is attached and has read nothing: what fits in the output queue (256 KiB) is written
	begin := rig.clock.Now()
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	// Every frame that was sent comes first and is the output in order; the last is Detached, and then the stream ends with status OK.
	tr := newTranscript(t)
	tr.attached(first.GetOpened(), 0)
	var last *execv2.ServerFrame
	for last == nil {
		frame, err := stream.Recv()
		if err != nil {
			t.Fatalf("after %d bytes the stream broke with %v: the host closed its side and can still read", tr.read, err)
		}
		if isLast(frame) {
			last = frame
			break
		}
		tr.take(frame)
	}
	tr.check()
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("after Detached the stream ended with %v, want io.EOF", err)
	}
	d := last.GetDetached()
	if d == nil || d.GetReason() != execv2.DetachReason_DETACH_REASON_UNSPECIFIED || !leaseExpiry(d).Equal(begin.Add(5*time.Minute)) {
		t.Fatalf("the last frame is %v, want Detached with no reason and the session's own lease of 5 minutes from %v", last, begin)
	}
	if tr.have > 200_000 {
		t.Fatalf("the stream delivered %d bytes of 200000", tr.have)
	}

	// The session lives, detached: what it goes on writing is in its ring, and the host finds it again by the offset it has.
	if !proofDetached(rig.client, "s1") {
		t.Fatalf("the session is not live with no stream attached after its host closed its side")
	}
	rig.grant(t, 100_000)
	second := rig.mustAttach("s1", tr.have)
	if r := second.Opened().GetReplay(); r.GetStart() != tr.have || r.GetEnd() != 300_000 {
		t.Fatalf("an attach from %d: replay %v, want [%d, 300000)", tr.have, r, tr.have)
	}
	readPatternBytes(t, second, tr.have, int(300_000-tr.have))
}
