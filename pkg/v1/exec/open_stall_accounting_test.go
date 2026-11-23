package exec_test

import (
	"context"
	"io"
	"testing"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A consumer that stops reading is what a lossy target is for: the Kubernetes proof's consumer lost 1.4 to 1.9 MB of a 64 MiB flood when
// it did work for each frame, and 57 MB of 64 MiB (85 %) when it stopped for 5 s, and each time the SDK counted every byte it dropped.
// X3a's flood proves the accounting for a reader that keeps up with the transport's own pace; this one holds it for a reader that stops,
// for as long as the author writes, which the test decides by an event and never by a sleep.

// A lossy session whose host stops reading while its author writes: the author is never held (it finishes writing before the host reads
// another frame), and what the queue could not hold is dropped, counted and reported, so that every byte the author wrote is either read,
// or in a range that a Dropped frame names, and Ended says the same: bytes_out, last_offset and dropped_bytes agree with the frames and with
// what was written, and the output is TRUNCATED.
func TestAStalledHostOfALossyTargetLosesBytesAndEveryOneIsCountedOnTheWire(t *testing.T) {
	total := uint64(proofScaled(32<<20, 8<<20))
	written := make(chan struct{})
	rig := newProofRig(t, proofTarget("stall", func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		for offset := uint64(0); offset < total; {
			n := int(min(uint64(floodChunk), total-offset))
			if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
				return exec.Exit{}, err
			}
			offset += uint64(n)
		}
		close(written) // the author is done while the host has read no more than the first frames: nothing held it
		return exec.ExitCode(0), nil
	}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallLossy }))

	conn := rig.mustOpenKind("stall", "s1")
	audit := proofAudit{verify: true, lossy: true}
	for audit.frames == 0 { // the host reads up to the first byte of output and then stops
		frame, err := recvFrame(t, conn)
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if audit.take(frame) {
			t.Fatal("the session ended before it wrote")
		}
	}
	awaitChannel(t, written, "the author to finish writing: a lossy target must not hold its author for a host that stopped reading")

	if err := proofDrain(conn, &audit, nil); err != io.EOF {
		t.Fatalf("the stream ended with %v, want Ended and then io.EOF", err)
	}
	for _, problem := range audit.problems {
		t.Errorf("the stream: %s", problem)
	}
	ended := audit.ended
	if ended == nil {
		t.Fatal("the stream ended without an Ended")
	}
	t.Logf("a host that stopped reading a flood of %d bytes: %d read in %d Output frames, %d dropped in %d ranges", total, audit.read, audit.frames, audit.dropped, audit.droppedFrames)
	if audit.dropped == 0 || audit.droppedFrames == 0 {
		t.Fatalf("the host read %d bytes and %d were dropped in %d ranges: a flood of %d bytes into a queue of 1 MiB must lose some", audit.read, audit.dropped, audit.droppedFrames, total)
	}
	if got := audit.read + audit.dropped; got != total {
		t.Errorf("read %d and dropped %d bytes: %d, the author wrote %d", audit.read, audit.dropped, got, total)
	}
	if ended.GetBytesOut() != total || ended.GetLastOffset() != total || ended.GetBytesOut() != ended.GetLastOffset() {
		t.Errorf("Ended says bytes_out %d and last_offset %d, want both %d, what the author wrote", ended.GetBytesOut(), ended.GetLastOffset(), total)
	}
	if ended.GetDroppedBytes() != audit.dropped {
		t.Errorf("Ended.dropped_bytes is %d, the Dropped frames name %d", ended.GetDroppedBytes(), audit.dropped)
	}
	if got := ended.GetOutput(); got != execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED {
		t.Errorf("Ended.output is %v, want TRUNCATED: bytes were dropped", got)
	}
	if exited := ended.GetEnd().GetExited(); exited == nil || exited.GetExitCode() != 0 {
		t.Errorf("the session ended %v, want exit 0: a lossy session that lost output still ended with its command's status", ended.GetEnd())
	}
}
