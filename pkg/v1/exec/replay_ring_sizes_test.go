package exec_test

import (
	"context"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The replay ring is the size the host asked for (Start.replay_bytes: zero is the target's default, 1 MiB; at most the target's
// maximum, 4 MiB), and what a flood does to a host that cannot keep up with it is counted to the byte, whatever the ring's size: the
// Kubernetes proof lost 1.4 to 1.9 MB of a 64 MiB flood to a lossy consumer and wanted every byte of it accounted for.

// ringSizeCase is a size of ring: what the host asks for, what the ring holds, and the chunk it is cut in.
type ringSizeCase struct {
	name  string
	ask   uint32 // Start.replay_bytes; zero is the target's default
	size  uint64 // what the ring holds at most
	chunk uint64
}

var ringSizeCases = []ringSizeCase{
	{"1 KiB (the smallest)", 1, 1 << 10, 256},
	{"64 KiB", 64 << 10, 64 << 10, 16 << 10},
	{"256 KiB", 256 << 10, 256 << 10, 64 << 10},
	{"1 MiB (the default, asked for as zero)", 0, 1 << 20, 64 << 10},
	{"1 MiB", 1 << 20, 1 << 20, 64 << 10},
	{"4 MiB (the maximum)", 4 << 20, 4 << 20, 64 << 10},
}

// A lossy target never holds its author, and what a host that cannot keep up does not read is dropped, oldest first, and counted exactly:
// for every size of ring, the Dropped frames, Ended.dropped_bytes, the output's completeness, the bytes read and the bytes the author
// wrote say the same, and so does the engine's own count of its ring. A host that cannot keep up is a host that is held, and how much the
// author writes meanwhile is the test's to say (ringRun.burst), so the loss is a function of the schedule and of the size of the ring,
// and the figures of the documentation (a consumer that keeps pace loses nothing, one that stops while a flood goes by loses nearly all
// of it) hold on every machine: no author or reader here is paced by a timer, which on a Windows runner ticks every 1 to 15.6 ms.
func TestALossyTargetsAccountingIsExactForEveryRingSizeAndAReaderThatCannotKeepUp(t *testing.T) {
	flood := uint64(proofScaled(64<<20, 16<<20))
	for _, size := range ringSizeCases {
		t.Run(size.name, func(t *testing.T) {
			// A host that keeps pace reads every lap the author writes before the author writes the next, until the ring has been written round
			// many times: a lap is the most that cannot reach a byte the host has not read, so such a host loses nothing, exactly.
			laps := int(min(64, max(3, uint64(proofScaled(8<<20, 2<<20))/size.lap())))
			keepPace := slices.Repeat([]uint64{size.lap()}, laps)
			for _, tc := range []struct {
				name   string
				bursts []uint64
			}{
				{"a host released at once loses nothing", keepPace},
				{"a host held while the author writes far more than the ring loses almost everything", []uint64{flood}},
				{"a host that keeps pace, stops in the middle of a chunk while the author floods and catches up again loses only what the flood took", slices.Concat(keepPace, []uint64{size.chunk/2 + 1, flood}, keepPace)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rig := newRingRig(t, exec.StallLossy)
					run := rig.openRun(t, size, true)
					for _, n := range tc.bursts {
						run.burst(n)
					}
					run.finish()
				})
			}
		})
	}
}

// A target that propagates a stalled reader loses nothing to a host that is slow, whatever its ring: a host that reads nothing holds the
// author when it has left the unread limit unread (256 KiB, or the ring less a chunk for a ring that cannot hold that), however far the
// flood still has to go, and when it reads again every byte arrives. The ring only decides how much a host that leaves can ask for again.
func TestABlockingTargetLosesNothingToASlowReaderWhateverItsRing(t *testing.T) {
	for _, size := range ringSizeCases {
		t.Run(size.name, func(t *testing.T) {
			total := min(uint64(proofScaled(16<<20, 4<<20)), 1024*size.size)
			rig := newRingRig(t, exec.StallPropagates)
			run := rig.openRun(t, size, false)
			run.holdBlocked(total)
			run.finish()
		})
	}
}

// The ring holds what the host asked for, and no more: a detached session whose author writes three rings' worth keeps the newest
// whole chunks, between a ring less one chunk and a ring, with a front that is a chunk boundary; an attach replays exactly that.
func TestTheReplayRingHoldsExactlyTheSizeThatTheHostAskedFor(t *testing.T) {
	for _, size := range ringSizeCases {
		t.Run(size.name, func(t *testing.T) {
			rig := newGatedRig(t, exechook.EngineConfig{})
			first := rig.mustOpen("s1", withoutTTY, func(s *execv2.Start) { s.ReplayBytes = size.ask })
			sendDetach(t, first, 0)
			endsWith(t, drainToEnd(t, first))
			written := 3*size.size + 12_345
			rig.grant(t, int(written))

			info := proofListed(rig.client, "s1")
			if info == nil || info.GetNextOffset() != written {
				t.Fatalf("ListSessions says %v of a detached session that wrote %d bytes", info, written)
			}
			held := info.GetNextOffset() - info.GetReplayStart()
			if held > size.size || held <= size.size-size.chunk || info.GetReplayStart()%size.chunk != 0 {
				t.Fatalf("the ring holds [%d, %d): %d bytes, want more than %d and at most %d, from a chunk boundary of %d",
					info.GetReplayStart(), info.GetNextOffset(), held, size.size-size.chunk, size.size, size.chunk)
			}
			second := rig.mustAttach("s1", 0)
			r := second.Opened().GetReplay()
			if r.GetStart() != info.GetReplayStart() || r.GetEnd() != written || r.GetBytes() != held {
				t.Fatalf("an attach from 0 replays %v, want [%d, %d)", r, info.GetReplayStart(), written)
			}
			readPatternBytes(t, second, r.GetStart(), int(r.GetBytes()))
			// ReadOutput reads the same ring, a megabyte at a time.
			var got uint64
			for from := uint64(0); from < written; {
				resp, err := rig.client.ReadOutput(context.Background(), &execv2.ReadOutputRequest{SessionId: "s1", FromOffset: from, MaxBytes: 1 << 20})
				if err != nil {
					t.Fatalf("ReadOutput(%d): %v", from, err)
				}
				if resp.GetReplayStart() != info.GetReplayStart() || resp.GetNextOffset() <= max(from, resp.GetReplayStart()) {
					t.Fatalf("ReadOutput from %d: replay_start %d, next_offset %d, want the ring's front at %d and progress", from, resp.GetReplayStart(), resp.GetNextOffset(), info.GetReplayStart())
				}
				for _, c := range resp.GetChunks() {
					got += uint64(len(c.GetData()))
				}
				from = resp.GetNextOffset()
			}
			if got != held {
				t.Fatalf("ReadOutput read %d bytes, the ring holds %d", got, held)
			}
		})
	}
}

// A ring above the target's maximum is INVALID_REQUEST, and nothing ran; at the maximum it is accepted, and zero is the default.
func TestAReplayRingAboveTheTargetsMaximumIsRefusedAndNothingRan(t *testing.T) {
	var ran atomic.Int32
	rig := newWireRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
		ran.Add(1)
		<-ctx.Done()
		return exec.Exit{}, ctx.Err()
	}))
	for _, tc := range []struct {
		name    string
		ask     uint32
		refused bool
	}{
		{"the maximum and a byte", 4<<20 + 1, true},
		{"the largest the wire carries", 1<<32 - 1, true},
		{"zero is the default", 0, false},
		{"the smallest", 1, false},
		{"the maximum", 4 << 20, false},
	} {
		conn, err := rig.open(context.Background(), "ring-"+strconv.FormatUint(uint64(tc.ask), 10), func(s *execv2.Start) { s.ReplayBytes = tc.ask })
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
		}
		switch {
		case tc.refused && !isRefusalOf(err, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified):
			t.Errorf("%s: a replay ring of %d bytes: %v, want INVALID_REQUEST", tc.name, tc.ask, err)
		case !tc.refused && err != nil:
			t.Errorf("%s: a replay ring of %d bytes: %v, want it accepted", tc.name, tc.ask, err)
		}
	}
	eventually(t, "the three accepted sessions to run", func() bool { return ran.Load() >= 3 })
	if got := ran.Load(); got != 3 {
		t.Errorf("the function ran %d times for 3 accepted opens and 2 refused ones: a refusal runs nothing", got)
	}
}
