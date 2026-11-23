package exec_test

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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

// pacedFlood writes the number of bytes its label "bytes" says, in writes of 32 KiB of the pattern, and spends the CPU time its label
// "pace_us" says after each: a target that produces at a rate (zero is as fast as the session takes it). It ends with exit 0.
func pacedFlood(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	total, err := strconv.ParseUint(p.Request.Labels["bytes"], 10, 64)
	if err != nil {
		return exec.Exit{}, err
	}
	pace, _ := strconv.Atoi(p.Request.Labels["pace_us"])
	for offset := uint64(0); offset < total; {
		if err := ctx.Err(); err != nil {
			return exec.Exit{}, err
		}
		n := int(min(uint64(floodChunk), total-offset))
		if _, err := p.Stdout.Write(proofChunk(offset, n)); err != nil {
			return exec.Exit{}, err
		}
		offset += uint64(n)
		if pace > 0 {
			proofSpin(time.Duration(pace) * time.Microsecond)
		}
	}
	return exec.ExitCode(0), nil
}

// spinConn is a host whose reading costs CPU: each Output frame it takes is followed by a spin, as a consumer that does work with it
// does (a terminal that parses it, a log that writes it). It keeps the Dropped frames it saw.
type spinConn struct {
	exec.Conn
	spin    time.Duration
	dropped []*execv2.Dropped
}

func (c *spinConn) Recv() (*execv2.ServerFrame, error) {
	frame, err := c.Conn.Recv()
	if err != nil {
		return frame, err
	}
	if frame.GetOutput() != nil {
		proofSpin(c.spin)
	}
	if d := frame.GetDropped(); d != nil {
		c.dropped = append(c.dropped, d)
	}
	return frame, nil
}

// lossyAccount is what a flood through a ring of one size cost the host.
type lossyAccount struct {
	total                 uint64
	read, dropped         uint64
	dropFrames, outFrames int
	wall                  time.Duration
	smallDrop, bigDrop    uint64
}

func (a lossyAccount) String() string {
	return strconv.FormatUint(a.read>>10, 10) + " KiB read, " + strconv.FormatUint(a.dropped>>10, 10) + " KiB dropped (" +
		strconv.FormatFloat(100*float64(a.dropped)/float64(a.total), 'f', 2, 64) + "%) in " + strconv.Itoa(a.dropFrames) +
		" Dropped frames (" + strconv.FormatUint(a.smallDrop, 10) + " to " + strconv.FormatUint(a.bigDrop, 10) + " bytes each), " +
		strconv.Itoa(a.outFrames) + " Output frames, in " + a.wall.Round(time.Millisecond).String()
}

// accountedFlood floods total bytes through a session whose ring is the size, written at the pace, to a host that spends spin of CPU on
// each frame, and holds every account to the others.
func accountedFlood(t *testing.T, rig *proofRig, kind, id string, size ringSizeCase, pace, spin time.Duration, total uint64, lossy bool) lossyAccount {
	t.Helper()
	start := time.Now()
	conn, err := rig.openKind(context.Background(), kind, id,
		proofLabels("bytes", strconv.FormatUint(total, 10), "pace_us", strconv.FormatInt(pace.Microseconds(), 10)),
		func(s *execv2.Start) { s.ReplayBytes = size.ask })
	if err != nil {
		t.Fatalf("%s: open: %v", id, err)
	}
	defer func() { _ = conn.Close() }()
	spun := &spinConn{Conn: conn, spin: spin}
	audit := &proofAudit{verify: true, lossy: lossy}
	if err := proofDrain(spun, audit, nil); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: the stream ended with %v, want Ended and then io.EOF", id, err)
	}
	for _, problem := range audit.problems {
		t.Errorf("%s: the stream: %s", id, problem)
	}
	ended := audit.ended
	if ended == nil {
		t.Fatalf("%s: the stream ended without an Ended", id)
	}
	acc := lossyAccount{total: total, read: audit.read, dropped: audit.dropped, dropFrames: audit.droppedFrames, outFrames: audit.frames, wall: audit.endedAt.Sub(start), smallDrop: ^uint64(0)}
	for _, d := range spun.dropped {
		acc.bigDrop, acc.smallDrop = max(acc.bigDrop, d.GetBytes()), min(acc.smallDrop, d.GetBytes())
		// The ring loses whole chunks, so a lost range ends where a chunk does, whatever the ring's size.
		if end := d.GetFromOffset() + d.GetBytes(); end%size.chunk != 0 {
			t.Errorf("%s: the range [%d, %d) ends in the middle of a chunk of %d bytes: the ring of %s loses whole chunks", id, d.GetFromOffset(), end, size.chunk, size.name)
		}
	}
	if acc.dropFrames == 0 {
		acc.smallDrop = 0
	}
	// Every byte the author wrote was read or lies in a range that the host was told of: nothing is counted twice, nothing is missing,
	// and Ended says the same.
	if acc.read+acc.dropped != total || ended.GetBytesOut() != total || ended.GetLastOffset() != total {
		t.Errorf("%s: read %d + dropped %d = %d, Ended.bytes_out %d, Ended.last_offset %d, the author wrote %d",
			id, acc.read, acc.dropped, acc.read+acc.dropped, ended.GetBytesOut(), ended.GetLastOffset(), total)
	}
	if ended.GetDroppedBytes() != acc.dropped {
		t.Errorf("%s: Ended.dropped_bytes is %d, the Dropped frames said %d", id, ended.GetDroppedBytes(), acc.dropped)
	}
	wantOutput := execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE
	if acc.dropped > 0 {
		wantOutput = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED
	}
	if ended.GetOutput() != wantOutput {
		t.Errorf("%s: Ended.output is %v after %d bytes were dropped, want %v", id, ended.GetOutput(), acc.dropped, wantOutput)
	}
	if exited := ended.GetEnd().GetExited(); exited == nil || exited.GetExitCode() != 0 {
		t.Errorf("%s: the flood ended %v, want exit 0", id, ended.GetEnd())
	}
	if acc.dropped > 0 && acc.dropFrames == 0 {
		t.Errorf("%s: %d bytes were dropped and no Dropped frame said so", id, acc.dropped)
	}
	return acc
}

// A lossy target never holds its author, and what the host cannot take in time is dropped, oldest first, and counted exactly: for
// every size of ring, and for a reader that is faster than its author and one that is slower, the Dropped frames, Ended.dropped_bytes,
// the output's completeness, the bytes read and the bytes the author wrote say the same. An author that writes as fast as the session
// takes it drops nearly everything (the ring is overwritten many times for each frame the host takes); one that produces at a rate
// of its own loses what the reader cannot keep up with.
func TestALossyTargetsAccountingIsExactForEveryRingSizeAndAReaderThatCannotKeepUp(t *testing.T) {
	total := uint64(proofScaled(64<<20, 16<<20))
	rig := newProofRig(t, proofTarget("lossy", pacedFlood, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallLossy }))
	cases := []struct {
		name       string
		pace, spin time.Duration
		mustDrop   bool
	}{
		{"an author at memory speed and a reader of 200us a frame", 0, 200 * time.Microsecond, true},
		{"an author of 60us a write and a reader of 10us a frame", 60 * time.Microsecond, 10 * time.Microsecond, false},
		{"an author of 60us a write and a reader of 100us a frame", 60 * time.Microsecond, 100 * time.Microsecond, false},
		{"an author of 60us a write and a reader of 400us a frame", 60 * time.Microsecond, 400 * time.Microsecond, true},
	}
	n := 0
	for _, size := range ringSizeCases {
		for _, tc := range cases {
			t.Run(size.name+"/"+tc.name, func(t *testing.T) {
				n++
				acc := accountedFlood(t, rig, "lossy", "lossy-"+strconv.Itoa(n), size, tc.pace, tc.spin, total, true)
				t.Logf("ring %s, %s, %d MiB written: %v", size.name, tc.name, total>>20, acc)
				if tc.mustDrop && acc.dropped == 0 {
					t.Errorf("a reader that cannot keep up lost nothing of %d MiB through a ring of %s: the author was held, and a lossy target never is", total>>20, size.name)
				}
			})
		}
	}
}

// A target that propagates a stalled reader loses nothing to a reader that is slow, whatever its ring: the ring only decides how much a
// host that leaves can ask for again.
func TestABlockingTargetLosesNothingToASlowReaderWhateverItsRing(t *testing.T) {
	total := uint64(proofScaled(16<<20, 4<<20))
	rig := newProofRig(t, proofTarget("blocking", pacedFlood, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
	for i, size := range ringSizeCases {
		t.Run(size.name, func(t *testing.T) {
			acc := accountedFlood(t, rig, "blocking", "blocking-"+strconv.Itoa(i), size, 0, 100*time.Microsecond, total, false)
			t.Logf("ring %s, %d MiB written: %v", size.name, total>>20, acc)
			if acc.dropped != 0 || acc.read != total {
				t.Errorf("a target that propagates a stalled reader lost %d of %d bytes to a reader that read them all", acc.dropped, total)
			}
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
