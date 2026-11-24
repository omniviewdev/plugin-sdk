package exec_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// A session that ended leaves a tombstone: how it ended and the last of its output, for a minute, readable with ListSessions and
// ReadOutput; and its id is held until the tombstone is gone. The streams are in memory and the clock is the bubble's, so a minute
// is exact and costs nothing.

// readOutput calls ReadOutput as a host does.
func (r *memoryRig) readOutput(from uint64, id string, max uint32) (*execv2.ReadOutputResponse, error) {
	return r.server.ReadOutput(r.callContext(), &execv2.ReadOutputRequest{SessionId: id, FromOffset: from, MaxBytes: max})
}

// mustReadOutput is readOutput for a read that has to be answered.
func (r *memoryRig) mustReadOutput(t testing.TB, id string, from uint64, max uint32) *execv2.ReadOutputResponse {
	t.Helper()
	resp, err := r.readOutput(from, id, max)
	if err != nil {
		t.Fatalf("ReadOutput(%q, from %d, max %d): %v", id, from, max, err)
	}
	return resp
}

// endSession calls EndSession as a host does.
func (r *memoryRig) endSession(id string, mode execv2.CloseMode) error {
	_, err := r.server.EndSession(r.callContext(), &execv2.EndSessionRequest{SessionId: id, Mode: mode})
	return err
}

// chunkBytes concatenates the data of the chunks of a read, and holds them to contiguous offsets from the first.
func chunkBytes(t testing.TB, resp *execv2.ReadOutputResponse) []byte {
	t.Helper()
	var data []byte
	var at uint64
	for i, c := range resp.GetChunks() {
		if i == 0 {
			at = c.GetOffset()
		}
		if c.GetOffset() != at || len(c.GetData()) == 0 {
			t.Fatalf("chunk %d is at offset %d with %d bytes, want contiguous from %d and not empty", i, c.GetOffset(), len(c.GetData()), at)
		}
		data = append(data, c.GetData()...)
		at += uint64(len(c.GetData()))
	}
	return data
}

// writeThenExit is a function that writes its text to the terminal, and ends with a code.
func writeThenExit(text string, code int) exec.ExecFunc {
	return func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = p.Stdout.Write([]byte(text))
		return exec.ExitCode(code), nil
	}
}

func TestATombstoneKeepsTheEndAndTheLastOutputForSixtySecondsAndThenTheSessionIsUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(writeThenExit("the last bytes", 3)))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		host.frames() // Started, the output and Ended: the stream took them
		ended := bubbleNow()

		info := rig.sessionInfo(t, "s1")
		if info.GetEndedWith().GetEnd().GetExited().GetExitCode() != 3 || info.GetAttached() || info.GetNextOffset() != 14 || info.GetReplayStart() != 0 ||
			info.GetEndedWith().GetLastOffset() != 14 || info.GetLastActivityUnixMs() != ended {
			t.Fatalf("the tombstone is %v, want Exited{3}, no stream, the output to offset 14 from 0, the activity of the end", info)
		}
		resp := rig.mustReadOutput(t, "s1", 0, 0)
		if got := chunkBytes(t, resp); string(got) != "the last bytes" || resp.GetNextOffset() != 14 || resp.GetReplayStart() != 0 || !resp.GetEnded() {
			t.Fatalf("ReadOutput of the tombstone: %q next %d start %d ended %v, want the 14 bytes, next 14, start 0, ended", got, resp.GetNextOffset(), resp.GetReplayStart(), resp.GetEnded())
		}
		// Nothing more comes: a read at the end is empty and still ended.
		if resp := rig.mustReadOutput(t, "s1", 14, 0); len(resp.GetChunks()) != 0 || resp.GetNextOffset() != 14 || !resp.GetEnded() {
			t.Fatalf("a read at the end of an ended session: %v", resp)
		}

		time.Sleep(60*time.Second - time.Millisecond)
		synctest.Wait()
		if !rig.hasSession(t, "s1") {
			t.Fatal("the tombstone was gone before its 60 seconds")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if rig.hasSession(t, "s1") {
			t.Fatal("the tombstone was still listed at 60 seconds")
		}
		if _, err := rig.readOutput(0, "s1", 0); !isRefusalOf(exec.StatusError(err), exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("ReadOutput after the tombstone expired: %v, want NOT_FOUND / SESSION_UNKNOWN", err)
		}
		if count, bytes := exec.PluginTombstones(rig.plugin); count != 0 || bytes != 0 {
			t.Fatalf("the store holds %d tombstones and %d bytes after they expired", count, bytes)
		}
		host.hostLeaves()
	})
}

// The id of a session is held by its tombstone: a start of it is a conflict until the tombstone is gone, and then it is admitted. The
// tombstone holds the id and nothing else: the place is free at once, and a run, which is not a session, keeps its own rule.
func TestTheIdOfASessionThatEndedIsHeldByItsTombstoneAndFreeWhenItIsGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(writeThenExit("x", 0), func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 }))
		first := rig.streamOf(rig.startFrame("s1"), nil)
		first.opened(t)
		synctest.Wait()

		again := rig.streamOf(rig.startFrame("s1"), nil)
		synctest.Wait()
		err, returned := again.handlerReturned()
		if !returned || !isRefusalOf(exec.StatusError(err), exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
			t.Fatalf("a start of the id of a session that ended: %v (returned %v), want CONFLICT / SESSION_EXISTS", err, returned)
		}
		// Not a place: another id is admitted at once, though the target admits one session.
		other := rig.streamOf(rig.startFrame("s2"), nil)
		other.opened(t)
		synctest.Wait()

		time.Sleep(61 * time.Second)
		synctest.Wait()
		third := rig.streamOf(rig.startFrame("s1"), nil)
		third.opened(t)
		synctest.Wait()
		for _, h := range []*memoryStream{first, other, third} {
			h.hostLeaves()
		}
	})
}

// A tombstone is no live session: an attach to its id is NOT_FOUND, and ending it is no error.
func TestAnAttachToATombstoneIsNotFoundAndEndingOneIsANoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(writeThenExit("x", 0)))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()

		stream := rig.streamOf(rig.attachFrame("s1", 0), nil)
		synctest.Wait()
		if err, returned := stream.handlerReturned(); !returned || !isRefusalOf(exec.StatusError(err), exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("an attach to the tombstone of s1: %v (returned %v), want NOT_FOUND / SESSION_UNKNOWN", err, returned)
		}
		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
			t.Fatalf("EndSession of a session that has ended: %v, want no error: it is what was asked", err)
		}
		host.hostLeaves()
	})
}

// A host that is attached when the session ends has read Ended, and finds the tombstone at once.
func TestASessionsTombstoneExistsBeforeItsStreamIsToldEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(writeThenExit("bye", 0)))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		frames := host.frames()
		ended := lastOf(frames).GetEnded()
		if ended == nil {
			t.Fatalf("the stream's last frame is %v, want Ended", lastOf(frames))
		}
		info := rig.sessionInfo(t, "s1")
		if !proto.Equal(info.GetEndedWith(), ended) {
			t.Fatalf("the tombstone's end is %v, the stream's Ended is %v: the same message", info.GetEndedWith(), ended)
		}
		if got := chunkBytes(t, rig.mustReadOutput(t, "s1", 0, 0)); string(got) != "bye" {
			t.Fatalf("the tombstone holds %q, want what the stream was sent", got)
		}
		host.hostLeaves()
	})
}

// The store is bounded: at most so many tombstones, the oldest going first with its id, and so many bytes of output, the oldest
// losing its output and keeping its end.
func TestTheTombstoneStoreIsBoundedInTheirCountAndInTheOutputTheyKeep(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			rig := newMemoryRig(t, engTarget(writeThenExit("x", 0)))
			exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{MaxTombstones: 3})
			var hosts []*memoryStream
			for i := 1; i <= 5; i++ {
				h := rig.streamOf(rig.startFrame(fmt.Sprintf("s%d", i)), nil)
				h.opened(t)
				synctest.Wait()
				hosts = append(hosts, h)
			}
			for i := 1; i <= 5; i++ {
				if has, want := rig.hasSession(t, fmt.Sprintf("s%d", i)), i >= 3; has != want {
					t.Errorf("s%d is listed: %v, want %v: the three newest tombstones are kept", i, has, want)
				}
			}
			if count, _ := exec.PluginTombstones(rig.plugin); count != 3 {
				t.Errorf("the store holds %d tombstones, want 3", count)
			}
			// The oldest is gone and so is its id; the others hold theirs (checked first: a new session leaves a tombstone of its own).
			held := rig.streamOf(rig.startFrame("s3"), nil)
			synctest.Wait()
			if err, returned := held.handlerReturned(); !returned || !isRefusalOf(exec.StatusError(err), exec.ErrorClassConflict, exec.ErrorReasonSessionExists) {
				t.Errorf("a start of s3, whose tombstone is kept: %v, want CONFLICT / SESSION_EXISTS", err)
			}
			free := rig.streamOf(rig.startFrame("s1"), nil)
			free.opened(t)
			synctest.Wait()
			for _, h := range append(hosts, free) {
				h.hostLeaves()
			}
		})
	})
	t.Run("output", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			text := string(bytes.Repeat([]byte("o"), 40<<10))
			rig := newMemoryRig(t, engTarget(writeThenExit(text, 0)))
			exec.ConfigurePluginEngine(rig.plugin, exechook.EngineConfig{TombstoneBytes: 100 << 10})
			var hosts []*memoryStream
			for i := 1; i <= 4; i++ {
				h := rig.streamOf(rig.startFrame(fmt.Sprintf("s%d", i)), nil)
				h.opened(t)
				synctest.Wait()
				hosts = append(hosts, h)
			}
			count, kept := exec.PluginTombstones(rig.plugin)
			if count != 4 || kept > 100<<10 || kept != 80<<10 {
				t.Fatalf("the store holds %d tombstones and %d bytes, want all four and the output of the two newest (80 KiB) within the budget of 100 KiB", count, kept)
			}
			for i := 1; i <= 4; i++ {
				id := fmt.Sprintf("s%d", i)
				info := rig.sessionInfo(t, id)
				resp := rig.mustReadOutput(t, id, 0, 1<<20)
				if info.GetEndedWith() == nil || !resp.GetEnded() {
					t.Fatalf("%s: the tombstone has no end: %v", id, info)
				}
				kept := len(chunkBytes(t, resp))
				if want := 0; i <= 2 {
					if kept != want || info.GetReplayStart() != info.GetNextOffset() || resp.GetReplayStart() != 40<<10 {
						t.Errorf("%s: the tombstone kept %d bytes from %d (list says start %d), want none: the budget took the oldest's output and left its end", id, kept, resp.GetReplayStart(), info.GetReplayStart())
					}
				} else if kept != 40<<10 || info.GetReplayStart() != 0 {
					t.Errorf("%s: the tombstone kept %d bytes from %d, want its 40 KiB from 0", id, kept, info.GetReplayStart())
				}
			}
			for _, h := range hosts {
				h.hostLeaves()
			}
		})
	})
}

// ReadOutput is a cursor read of the ring: chunks contiguous from the later of the offset asked for and the front of the ring, the
// default of 64 KiB and no more than the ring holds, with the offset the next read starts at.
func TestReadOutputIsACursorReadOfTheRingAndHoldsToItsBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			for off := 0; off < 200<<10; off += 10 << 10 {
				_, _ = p.Stdout.Write(proofChunk(uint64(off), 10<<10))
			}
			<-gate.Opened()
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.streamOf(rig.startFrame("s1", func(s *execv2.Start) { s.DetachTimeoutMs = 0 }), nil)
		host.opened(t)
		host.sendDetachFrame(0)
		synctest.Wait()
		host.frames()

		// The default is 64 KiB; the next read starts where this one ended.
		first := rig.mustReadOutput(t, "s1", 0, 0)
		if got := chunkBytes(t, first); len(got) != 64<<10 || first.GetNextOffset() != 64<<10 || first.GetReplayStart() != 0 || first.GetEnded() {
			t.Fatalf("the first read: %d bytes, next %d, start %d, ended %v, want 65536, 65536, 0 and a live session", len(got), first.GetNextOffset(), first.GetReplayStart(), first.GetEnded())
		}
		if !bytes.Equal(chunkBytes(t, first), proofChunk(0, 64<<10)) {
			t.Fatal("the first read is not the bytes that were written")
		}
		second := rig.mustReadOutput(t, "s1", first.GetNextOffset(), 100)
		if !bytes.Equal(chunkBytes(t, second), proofChunk(64<<10, 100)) || second.GetNextOffset() != 64<<10+100 {
			t.Fatalf("a read of 100 bytes from 65536: next %d", second.GetNextOffset())
		}
		// Reading to the end: a read of more than is there is what is there.
		rest := rig.mustReadOutput(t, "s1", 190<<10, 1<<20)
		if got := chunkBytes(t, rest); len(got) != 10<<10 || rest.GetNextOffset() != 200<<10 {
			t.Fatalf("a read from 190 KiB: %d bytes, next %d, want the last 10 KiB and next 200 KiB", len(got), rest.GetNextOffset())
		}
		// At the end: empty, and the session is live. Beyond the end is a request that is wrong.
		if end := rig.mustReadOutput(t, "s1", 200<<10, 0); len(end.GetChunks()) != 0 || end.GetNextOffset() != 200<<10 {
			t.Fatalf("a read at the end of the output: %v", end)
		}
		if _, err := rig.readOutput(200<<10+1, "s1", 0); !isRefusalOf(exec.StatusError(err), exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified) {
			t.Fatalf("a read beyond the end of the output: %v, want INVALID_REQUEST", err)
		}
		if _, err := rig.readOutput(0, "nothing", 0); !isRefusalOf(exec.StatusError(err), exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("a read of an unknown session: %v, want NOT_FOUND / SESSION_UNKNOWN", err)
		}
		if _, err := rig.readOutput(0, "has a space", 0); !isRefusalOf(exec.StatusError(err), exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified) {
			t.Fatalf("a read of an id that cannot be one: %v, want INVALID_REQUEST", err)
		}
		gate.Open()
		_ = rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL)
		host.hostLeaves()
	})
}

// A read is clamped to what the ring holds, and one that asks for less than the front gets the front.
func TestReadOutputIsClampedToTheRingAndStartsAtItsFront(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write(proofChunk(0, 10_240))
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		host := rig.streamOf(rig.startFrame("s1", func(s *execv2.Start) { s.ReplayBytes = 1024 }), nil)
		host.opened(t)
		host.sendDetachFrame(0)
		synctest.Wait()
		resp := rig.mustReadOutput(t, "s1", 0, 1<<20)
		if got := chunkBytes(t, resp); resp.GetReplayStart() != 9216 || !bytes.Equal(got, proofChunk(9216, 1024)) || resp.GetNextOffset() != 10_240 {
			t.Fatalf("a read from 0 of a ring of 1 KiB: %d bytes from %d, next %d, want the 1024 bytes of its front, [9216, 10240)", len(got), resp.GetReplayStart(), resp.GetNextOffset())
		}
		if resp := rig.mustReadOutput(t, "s1", 9216, 4096); len(chunkBytes(t, resp)) != 1024 {
			t.Fatalf("a read asking for 4096 bytes of a ring of 1024 got %d", len(chunkBytes(t, resp)))
		}
		_ = rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL)
		host.hostLeaves()
	})
}

// Output of two streams is read back with each run's stream and contiguous offsets.
func TestReadOutputCarriesTheStreamOfEachRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			_, _ = p.Stdout.Write([]byte("out1"))
			_, _ = p.Stderr.Write([]byte("err"))
			_, _ = p.Stdout.Write([]byte("out2"))
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}, func(i *exec.TargetInfo) { i.Capabilities.SeparateStderr = true }))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(0)
		synctest.Wait()
		resp := rig.mustReadOutput(t, "s1", 0, 0)
		type run struct {
			stream execv2.Stream
			offset uint64
			data   string
		}
		var got []run
		for _, c := range resp.GetChunks() {
			got = append(got, run{c.GetStream(), c.GetOffset(), string(c.GetData())})
		}
		want := []run{{execv2.Stream_STREAM_STDOUT, 0, "out1"}, {execv2.Stream_STREAM_STDERR, 4, "err"}, {execv2.Stream_STREAM_STDOUT, 7, "out2"}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("ReadOutput: %v, want %v", got, want)
		}
		_ = rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL)
		host.hostLeaves()
	})
}

// Reading the ring never disturbs a stream that is attached: a blocking author that is held because its reader has left too much
// unread stays held, however much a ReadOutput has read, and the stream is sent what it would have been.
func TestReadOutputDoesNotDisturbAnAttachedStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wrote := make(chan struct{})
		proceed := make(chan struct{})
		rig := newMemoryRig(t, engTarget(func(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
			// The Started goes out before the first output is held in the send: a goroutine that waits for the lock that a held send
			// holds is not durably blocked, and synctest.Wait would never return.
			p.Started(exec.SessionFacts{})
			<-proceed
			for off := 0; off < 600<<10; off += 10 << 10 { // more than a reader that takes nothing may leave unread (256 KiB)
				if _, err := p.Stdout.Write(proofChunk(uint64(off), 10<<10)); err != nil {
					return exec.Exit{}, err
				}
			}
			close(wrote)
			<-ctx.Done()
			return exec.Exit{}, ctx.Err()
		}))
		hold := make(chan struct{})
		host := rig.streamHeld(rig.startFrame("s1"), hold)
		host.opened(t)
		synctest.Wait() // Started is sent
		close(proceed)
		synctest.Wait()
		select {
		case <-wrote:
			t.Fatal("the author finished writing while its reader took nothing: it was not held")
		default:
		}
		// What has been written is readable meanwhile, and reading it releases nobody.
		for from := uint64(0); from < 256<<10; from += 64 << 10 {
			rig.mustReadOutput(t, "s1", from, 0)
		}
		synctest.Wait()
		select {
		case <-wrote:
			t.Fatal("ReadOutput released an author that is held by its attached reader")
		default:
		}
		// The stream takes its output when its host does, and the author goes on to the end: every byte, in order, and none twice.
		close(hold)
		synctest.Wait()
		tr := newTranscript(t)
		tr.attached(&execv2.Opened{Replay: &execv2.Replay{}}, 0)
		for _, f := range host.frames() {
			tr.take(f)
		}
		tr.check()
		if tr.have != 600<<10 {
			t.Fatalf("the stream delivered %d bytes, want 600 KiB", tr.have)
		}
		host.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		host.hostLeaves()
	})
}

// EndSession destroys a session that has no stream and returns once it has ended: its function was told, its place is free and its
// tombstone is there to read.
func TestEndSessionDestroysADetachedSessionAndReturnsOnceItHasEnded(t *testing.T) {
	for _, mode := range []execv2.CloseMode{execv2.CloseMode_CLOSE_MODE_UNSPECIFIED, execv2.CloseMode_CLOSE_MODE_GRACEFUL, execv2.CloseMode_CLOSE_MODE_KILL} {
		t.Run(mode.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cancelled := make(chan struct{})
				rig := newMemoryRig(t, engTarget(func(ctx context.Context, _ *exec.Proc) (exec.Exit, error) {
					<-ctx.Done()
					close(cancelled)
					return exec.Exit{}, ctx.Err()
				}, func(i *exec.TargetInfo) { i.Session.MaxSessions = 1 }))
				host := rig.streamOf(rig.startFrame("s1"), nil)
				host.opened(t)
				host.sendDetachFrame(0)
				synctest.Wait()
				if err := rig.endSession("s1", mode); err != nil {
					t.Fatalf("EndSession: %v", err)
				}
				select {
				case <-cancelled:
				default:
					t.Fatal("EndSession returned before the function was told to stop and returned")
				}
				if rig.live("s1") {
					t.Fatal("EndSession returned before the session was removed: its place is not free")
				}
				info := rig.sessionInfo(t, "s1")
				if info.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
					t.Fatalf("the tombstone says %v, want Closed by REQUEST", info.GetEndedWith().GetEnd())
				}
				// The place is free: the target admits one session, and another id is admitted at once.
				next := rig.streamOf(rig.startFrame("s2"), nil)
				next.opened(t)
				next.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
				synctest.Wait()
				host.hostLeaves()
				next.hostLeaves()
			})
		})
	}
}

// A session with a stream attached is not ended by EndSession: the host closes it on its stream.
func TestEndSessionRefusesASessionWithAStreamAttachedAndKnowsNoUnknownOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); !isRefusalOf(exec.StatusError(err), exec.ErrorClassConflict, exec.ErrorReasonUnspecified) {
			t.Fatalf("EndSession of a session with a stream: %v, want CONFLICT", err)
		}
		if !rig.live("s1") {
			t.Fatal("the refused EndSession ended the session")
		}
		if err := rig.endSession("nothing", execv2.CloseMode_CLOSE_MODE_GRACEFUL); !isRefusalOf(exec.StatusError(err), exec.ErrorClassNotFound, exec.ErrorReasonSessionUnknown) {
			t.Fatalf("EndSession of an unknown session: %v, want NOT_FOUND / SESSION_UNKNOWN", err)
		}
		if err := rig.endSession("has a space", execv2.CloseMode_CLOSE_MODE_GRACEFUL); !isRefusalOf(exec.StatusError(err), exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified) {
			t.Fatalf("EndSession of an id that cannot be one: %v, want INVALID_REQUEST", err)
		}
		host.sendCloseFrame(execv2.CloseMode_CLOSE_MODE_KILL)
		synctest.Wait()
		host.hostLeaves()
	})
}

// The same operation against itself: EndSession called twice at once, and after its own effect, ends the session once and answers
// both without an error.
func TestEndSessionAppliedToItselfEndsTheSessionOnceAndAnswersEveryCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(0)
		synctest.Wait()
		results := make(chan error, 2)
		for range 2 {
			go func() { results <- rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_GRACEFUL) }()
		}
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("a concurrent EndSession: %v", err)
			}
		}
		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
			t.Fatalf("EndSession after its own effect: %v", err)
		}
		if info := rig.sessionInfo(t, "s1"); info.GetEndedWith() == nil {
			t.Fatalf("the session has no end: %v", info)
		}
		host.hostLeaves()
	})
}

// A kill ends a stuck author within the kill wait, and the way out of a close that waits behind unread input is to cancel the stream
// and then EndSession with a kill: the function that ignores its context is given up on after 50 ms, not after the grace.
func TestEndSessionKillEndsAStuckAuthorWithinTheKillWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			<-gate.Opened() // ignores its context
			return exec.Exit{}, nil
		}))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		synctest.Wait()
		host.cancel() // the host gives up on its stream: an implicit detach
		synctest.Wait()
		begin := time.Now()
		if err := rig.endSession("s1", execv2.CloseMode_CLOSE_MODE_KILL); err != nil {
			t.Fatalf("EndSession: %v", err)
		}
		if took := time.Since(begin); took != 50*time.Millisecond {
			t.Fatalf("EndSession with a kill took %v, want the kill wait of 50ms: not the grace", took)
		}
		if info := rig.sessionInfo(t, "s1"); info.GetEndedWith().GetEnd().GetClosed().GetBy() != execv2.ClosedBy_CLOSED_BY_REQUEST {
			t.Fatalf("the tombstone says %v", info.GetEndedWith().GetEnd())
		}
		gate.Open()
		synctest.Wait()
	})
}

// A caller that gives up while EndSession waits for the function is answered with its context's error, and the session still ends.
func TestEndSessionGivesUpWhenItsCallersContextEndsAndTheSessionStillEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := exectest.NewGate()
		t.Cleanup(gate.Open)
		rig := newMemoryRig(t, engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) {
			<-gate.Opened() // ignores its context for as long as the test says
			return exec.Exit{}, nil
		}))
		host := rig.streamOf(rig.startFrame("s1"), nil)
		host.opened(t)
		host.sendDetachFrame(0)
		synctest.Wait()
		ctx, cancel := context.WithTimeout(rig.callContext(), time.Second)
		defer cancel()
		_, err := rig.server.EndSession(ctx, &execv2.EndSessionRequest{SessionId: "s1"})
		if err == nil || !isRefusalOf(exec.StatusError(err), exec.ErrorClassTimeout, exec.ErrorReasonUnspecified) {
			t.Fatalf("EndSession whose caller gave up after 1 s while the function holds on: %v, want TIMEOUT", err)
		}
		gate.Open()
		synctest.Wait()
		if rig.live("s1") {
			t.Fatal("the session did not end after the caller gave up")
		}
		host.hostLeaves()
	})
}

// The calls are admitted like Options: the request is strict, and the plugin's session context of the call is checked.
func TestTheSessionCallsAreStrictAndReadTheSessionContextOfTheCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rig := newMemoryRig(t, engTarget(blockUntilCancelled))
		unknown := []byte{0xd8, 0x3e, 0x01} // an unknown varint field, number 1003
		list := &execv2.ListSessionsRequest{}
		list.ProtoReflect().SetUnknown(unknown)
		read := &execv2.ReadOutputRequest{SessionId: "s1"}
		read.ProtoReflect().SetUnknown(unknown)
		end := &execv2.EndSessionRequest{SessionId: "s1"}
		end.ProtoReflect().SetUnknown(unknown)
		ctx := rig.callContext()
		if _, err := rig.server.ListSessions(ctx, list); !isRefusalOf(exec.StatusError(err), exec.ErrorClassUnsupported, exec.ErrorReasonUnspecified) {
			t.Errorf("ListSessions with a field this build does not know: %v, want UNSUPPORTED", err)
		}
		if _, err := rig.server.ReadOutput(ctx, read); !isRefusalOf(exec.StatusError(err), exec.ErrorClassUnsupported, exec.ErrorReasonUnspecified) {
			t.Errorf("ReadOutput with a field this build does not know: %v, want UNSUPPORTED", err)
		}
		if _, err := rig.server.EndSession(ctx, end); !isRefusalOf(exec.StatusError(err), exec.ErrorClassUnsupported, exec.ErrorReasonUnspecified) {
			t.Errorf("EndSession with a field this build does not know: %v, want UNSUPPORTED", err)
		}
		// A call with no session metadata at all is admitted as one with no pin: the metadata is read, never required.
		if _, err := rig.server.ListSessions(context.Background(), &execv2.ListSessionsRequest{}); err != nil {
			t.Errorf("ListSessions without the session metadata: %v", err)
		}
	})
}
