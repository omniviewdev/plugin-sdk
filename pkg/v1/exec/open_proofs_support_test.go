package exec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The proofs of step X3a (the plan's section 3.6) run the stack a host and a plugin really use: a real gRPC server over a
// socket behind the SDK's middleware, a real client, and the author's function. This file holds what more than one of them
// needs: a pattern whose every byte can be checked from its offset, scripted targets, the host's side of an Open, and the
// audit of the frames of one stream.

// ---------------------------------------------------------------------------------------------------------------
// the pattern
// ---------------------------------------------------------------------------------------------------------------

const (
	// proofPeriod is the length after which the pattern repeats. It is a prime, so that no write, frame or window size lines
	// the repetition up with itself, and a byte that moved by one, or a frame that was repeated, shows.
	proofPeriod = 65521
	// proofMaxFrame is the largest Output frame of the contract, and so the longest view of the pattern a reader compares.
	proofMaxFrame = 64 << 10
)

// proofRing is the pattern twice over, so that a view of up to proofMaxFrame bytes from any offset is contiguous.
var proofRing = func() []byte {
	ring := make([]byte, proofPeriod+proofMaxFrame)
	for i := range ring {
		ring[i] = proofByte(uint64(i % proofPeriod))
	}
	return ring
}()

// proofByte is the byte of the pattern at an offset of the period.
func proofByte(i uint64) byte { return byte((i * 2654435761) >> 13) }

// proofChunk returns the n bytes of the pattern that start at offset, as a view that nothing may change, with no copy and no
// allocation. An author that writes it and a reader that checks it cost nothing but the copy the transport makes.
func proofChunk(offset uint64, n int) []byte {
	start := int(offset % proofPeriod)
	return proofRing[start : start+n : start+n]
}

// ---------------------------------------------------------------------------------------------------------------
// targets and the host's side of an Open
// ---------------------------------------------------------------------------------------------------------------

// proofTarget is a target of the fake's routing and defaults whose type is kind, so that one plugin serves many scripted
// behaviours, each with the limits of its own. Its function is fn; the info mods may change what it declares.
func proofTarget(kind string, fn exec.ExecFunc, mods ...func(*exec.TargetInfo)) exec.Target {
	target := exectest.NewFakeTarget(exectest.FakeRouting("fake", kind))
	target.Exec = fn
	for _, mod := range mods {
		mod(&target.Info)
	}
	return target
}

// proofRig is a wire rig whose host opens sessions of a named kind of target.
type proofRig struct {
	*wireRig
	refs map[string]*execv2.TargetRef
}

// newProofRig serves targets over the wire and prepares the reference of each, so that a goroutine that is not the test's can
// open a session without touching the test.
func newProofRig(t testing.TB, targets ...exec.Target) *proofRig {
	t.Helper()
	rig := newWireRig(t, targets...)
	refs := make(map[string]*execv2.TargetRef, len(targets))
	for _, target := range targets {
		refs[target.Info.Type] = rig.conns.WireTarget(t, target.Info.Type, "thing-1")
	}
	return &proofRig{wireRig: rig, refs: refs}
}

// openOn opens a session of a kind through a client, which is the rig's or another connection's. It is safe to call from any
// goroutine.
func (r *proofRig) openOn(ctx context.Context, client exec.Client, kind, id string, mods ...func(*execv2.Start)) (exec.Conn, error) {
	ref := r.refs[kind]
	if ref == nil {
		return nil, fmt.Errorf("the rig serves no kind %q", kind)
	}
	start := &execv2.Start{Target: proto.Clone(ref).(*execv2.TargetRef)}
	for _, mod := range mods {
		mod(start)
	}
	return client.Open(r.request(ctx), &execv2.OpenRequest{SessionId: id, Kind: &execv2.OpenRequest_Start{Start: start}})
}

// openKind opens a session of a kind through the rig's own client.
func (r *proofRig) openKind(ctx context.Context, kind, id string, mods ...func(*execv2.Start)) (exec.Conn, error) {
	return r.openOn(ctx, r.client, kind, id, mods...)
}

// mustOpenKind opens a session that has to be accepted and closes its stream when the test ends.
func (r *proofRig) mustOpenKind(kind, id string, mods ...func(*execv2.Start)) exec.Conn {
	r.t.Helper()
	conn, err := r.openKind(context.Background(), kind, id, mods...)
	if err != nil {
		r.t.Fatalf("open %q of kind %q: %v", id, kind, err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// proofTTY asks for a session with a terminal; the default is without.
func proofTTY(s *execv2.Start) { s.Tty = true }

// proofLabels gives the session labels, which its function reads as Proc.Request.Labels. The arguments are name, value pairs.
func proofLabels(pairs ...string) func(*execv2.Start) {
	return func(s *execv2.Start) {
		for i := 0; i+1 < len(pairs); i += 2 {
			s.Labels = append(s.Labels, &execv2.NameValue{Name: pairs[i], Value: pairs[i+1]})
		}
	}
}

// proofRefusal names the refusal of an Open as "CLASS/REASON", or "CLASS" for a class with no reason, and "not an exec error"
// with the type for anything else.
func proofRefusal(err error) string {
	var refusal *exec.Error
	if !errors.As(err, &refusal) {
		return fmt.Sprintf("not an exec error: %T %v", err, err)
	}
	if refusal.Reason == exec.ErrorReasonUnspecified {
		return refusal.Class.String()
	}
	return refusal.Class.String() + "/" + refusal.Reason.String()
}

// ---------------------------------------------------------------------------------------------------------------
// the audit of one stream
// ---------------------------------------------------------------------------------------------------------------

// maxProofProblems bounds what an audit keeps: a stream that breaks a rule breaks it for every frame, and the first ones
// say what is wrong.
const maxProofProblems = 6

// proofAudit follows the frames of one session, from the first after Opened, and keeps what breaks the contract of the
// stream: the order of the frames, the offsets, the bytes, the dropped ranges and the accounts of Ended. It is not safe for
// concurrent use: the goroutine that reads the stream is the one that feeds it.
type proofAudit struct {
	// tty says the session runs with a terminal: its output is the one terminal stream.
	tty bool
	// verify compares the bytes of every Output frame with the pattern at its offset. It costs one comparison of the frame.
	verify bool
	// lossy says the target declared STALL_LOSSY: its dropped ranges are EAGER_DRAIN_OVERFLOW, and any other target's are
	// REPLAY_FULL, which a blocking target loses only to a session that is ending.
	lossy bool

	next          uint64 // the offset the next Output or Dropped frame has to start at
	read          uint64 // bytes of Output frames
	dropped       uint64 // bytes of Dropped frames
	frames        int    // Output frames
	droppedFrames int
	started       bool
	sawOutput     bool
	ended         *execv2.Ended
	endedAt       time.Time
	problems      []string
}

func (a *proofAudit) problem(format string, args ...any) {
	if len(a.problems) < maxProofProblems {
		a.problems = append(a.problems, fmt.Sprintf(format, args...))
	}
}

// take applies the next frame of the stream. It reports true once the stream has its Ended, which is also the answer for a
// frame that comes after it, a violation that is kept.
func (a *proofAudit) take(frame *execv2.ServerFrame) bool {
	if a.ended != nil {
		a.problem("a %s frame after Ended", frameKind(frame))
		return true
	}
	switch f := frame.GetFrame().(type) {
	case *execv2.ServerFrame_Started:
		if a.started {
			a.problem("a second Started")
		}
		if a.sawOutput {
			a.problem("Started after the first Output: it comes before it")
		}
		a.started = true
	case *execv2.ServerFrame_Notice, *execv2.ServerFrame_Link, *execv2.ServerFrame_Resized, *execv2.ServerFrame_Fact:
	case *execv2.ServerFrame_Output:
		a.output(f.Output)
	case *execv2.ServerFrame_Dropped:
		a.drop(f.Dropped)
	case *execv2.ServerFrame_Ended:
		a.ended, a.endedAt = f.Ended, time.Now()
		a.checkEnded()
		return true
	default:
		a.problem("an unexpected %s frame", frameKind(frame))
	}
	return false
}

func (a *proofAudit) output(o *execv2.Output) {
	data := o.GetData()
	a.sawOutput = true
	switch {
	case len(data) == 0:
		a.problem("an Output frame with no data at offset %d", o.GetOffset())
	case len(data) > proofMaxFrame:
		a.problem("an Output frame of %d bytes: at most %d", len(data), proofMaxFrame)
	}
	want := execv2.Stream_STREAM_STDOUT
	if a.tty {
		want = execv2.Stream_STREAM_TTY
	}
	if o.GetStream() != want {
		a.problem("an Output frame on stream %v, want %v", o.GetStream(), want)
	}
	if o.GetOffset() != a.next {
		a.problem("an Output frame at offset %d, want %d: offsets are contiguous except across a Dropped range", o.GetOffset(), a.next)
	}
	if a.verify && len(data) > 0 && len(data) <= proofMaxFrame && !bytes.Equal(data, proofChunk(o.GetOffset(), len(data))) {
		a.problem("the bytes of the Output frame at offset %d (%d bytes) are not the pattern", o.GetOffset(), len(data))
	}
	a.next = o.GetOffset() + uint64(len(data))
	a.read += uint64(len(data))
	a.frames++
}

func (a *proofAudit) drop(d *execv2.Dropped) {
	if d.GetBytes() == 0 {
		a.problem("a Dropped frame of no bytes at offset %d", d.GetFromOffset())
	}
	if d.GetFromOffset() != a.next {
		a.problem("a Dropped range from offset %d, want %d: what is lost follows what was delivered", d.GetFromOffset(), a.next)
	}
	want := execv2.DropReason_DROP_REASON_REPLAY_FULL
	if a.lossy {
		want = execv2.DropReason_DROP_REASON_EAGER_DRAIN_OVERFLOW
	}
	if d.GetReason() != want {
		a.problem("a Dropped range with reason %v, want %v", d.GetReason(), want)
	}
	a.next = d.GetFromOffset() + d.GetBytes()
	a.dropped += d.GetBytes()
	a.droppedFrames++
}

// checkEnded holds the accounts of Ended to what was delivered: every byte the function wrote was read or reported dropped.
func (a *proofAudit) checkEnded() {
	e := a.ended
	if e.GetEnd() == nil {
		a.problem("Ended has no end")
	}
	if got, want := e.GetBytesOut(), a.read+a.dropped; got != want {
		a.problem("Ended.bytes_out is %d, the stream delivered %d and reported %d dropped: %d", got, a.read, a.dropped, want)
	}
	if e.GetLastOffset() != e.GetBytesOut() {
		a.problem("Ended.last_offset is %d, bytes_out %d", e.GetLastOffset(), e.GetBytesOut())
	}
	if e.GetDroppedBytes() != a.dropped {
		a.problem("Ended.dropped_bytes is %d, the Dropped frames reported %d", e.GetDroppedBytes(), a.dropped)
	}
	if a.dropped > 0 && e.GetOutput() == execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE {
		a.problem("Ended.output is COMPLETE after %d bytes were dropped", a.dropped)
	}
}

// errProofAfterEnded is what proofDrain returns when a frame followed Ended.
var errProofAfterEnded = errors.New("a frame followed Ended")

// proofDrain reads the stream into the audit until it ends. It returns io.EOF when the stream ended as the contract says
// (Ended, then io.EOF), errProofAfterEnded when a frame followed Ended, and otherwise the error that ended the read, which is
// the cancel of the host's own context, or a status the plugin ended the stream with. leaving says the host cancelled, which
// is allowed to turn the io.EOF that follows Ended into the cancel.
func proofDrain(conn exec.Conn, a *proofAudit, leaving func() bool) error {
	for {
		frame, err := conn.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) && a.ended == nil {
				a.problem("the stream ended with io.EOF and no Ended")
			}
			return err
		}
		if !a.take(frame) {
			continue
		}
		next, err := conn.Recv()
		switch {
		case err == nil:
			a.problem("after Ended: a %s frame, want io.EOF", frameKind(next))
			return errProofAfterEnded
		case errors.Is(err, io.EOF):
		case leaving != nil && leaving():
		default:
			a.problem("after Ended: %v, want io.EOF", err)
		}
		return err
	}
}

// ---------------------------------------------------------------------------------------------------------------
// time, scale and polling
// ---------------------------------------------------------------------------------------------------------------

// proofSpin busy-waits for d: a consumer that costs d of CPU, as a slow one that does work does, and not one that sleeps.
func proofSpin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

// proofScaled is full, or reduced when a run is slow by design: under the race detector, or with -short.
func proofScaled(full, reduced int) int {
	if proofRace || testing.Short() {
		return reduced
	}
	return full
}

// proofPoll polls cond until it holds or bound has passed, and reports whether it held. The condition is the oracle; the bound
// only keeps a bug from hanging the suite. It is safe to call from any goroutine.
func proofPoll(bound time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(bound)
	for spins := 0; ; spins++ {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		if spins < 100 {
			runtime.Gosched()
		} else {
			time.Sleep(time.Millisecond)
		}
	}
}

// proofLatency is the distribution of a set of timings.
type proofLatency struct {
	N                        int
	P50, P95, P99, Max, Mean time.Duration
}

// proofSummarize reads the percentiles (nearest rank) of samples, which it does not change.
func proofSummarize(samples []time.Duration) proofLatency {
	if len(samples) == 0 {
		return proofLatency{}
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	at := func(q float64) time.Duration {
		rank := int(math.Ceil(q*float64(len(sorted)))) - 1
		return sorted[max(0, min(rank, len(sorted)-1))]
	}
	var sum time.Duration
	for _, s := range sorted {
		sum += s
	}
	return proofLatency{
		N: len(sorted), P50: at(0.50), P95: at(0.95), P99: at(0.99), Max: sorted[len(sorted)-1], Mean: sum / time.Duration(len(sorted)),
	}
}

func (l proofLatency) String() string {
	round := func(d time.Duration) time.Duration { return d.Round(time.Microsecond) }
	return fmt.Sprintf("n=%d p50=%v p95=%v p99=%v max=%v mean=%v", l.N, round(l.P50), round(l.P95), round(l.P99), round(l.Max), round(l.Mean))
}

// ---------------------------------------------------------------------------------------------------------------
// the echo, one byte at a time
// ---------------------------------------------------------------------------------------------------------------

// proofEchoFunction copies the host's input to the output until the session ends.
func proofEchoFunction(ctx context.Context, p *exec.Proc) (exec.Exit, error) {
	_, _ = io.Copy(p.Stdout, p.Stdin)
	if err := ctx.Err(); err != nil {
		return exec.Exit{}, err
	}
	return exec.ExitCode(0), nil
}

// proofEchoer sends one byte to an echo session and waits for it to come back: a keystroke and its echo. It is used from one
// goroutine at a time.
type proofEchoer struct {
	conn   exec.Conn
	frames [256]*execv2.ClientFrame // one input frame for each byte value, built once: the loop allocates none
	sent   uint64
}

func newProofEchoer(conn exec.Conn) *proofEchoer {
	e := &proofEchoer{conn: conn}
	for b := range e.frames {
		e.frames[b] = &execv2.ClientFrame{Frame: &execv2.ClientFrame_Input{Input: []byte{byte(b)}}}
	}
	return e
}

// roundTrip sends one byte and returns how long it took to come back as an Output frame. The frames that come before the
// first output (a Notice, the Started) are read and not timed apart: they are part of the first round trip, and a caller that
// measures warms the session up first. The byte that comes back has to be the one sent, at the offset it was sent.
func (e *proofEchoer) roundTrip() (time.Duration, error) {
	b := byte(e.sent)
	start := time.Now()
	if err := e.conn.Send(e.frames[b]); err != nil {
		return 0, fmt.Errorf("send: %w", err)
	}
	for {
		frame, err := e.conn.Recv()
		if err != nil {
			return 0, fmt.Errorf("recv after %d echoes: %w", e.sent, err)
		}
		switch f := frame.GetFrame().(type) {
		case *execv2.ServerFrame_Output:
			elapsed := time.Since(start)
			data := f.Output.GetData()
			if len(data) != 1 || data[0] != b || f.Output.GetOffset() != e.sent {
				return 0, fmt.Errorf("echo %d: got %d bytes %v at offset %d, want the byte %#x at offset %d", e.sent, len(data), data, f.Output.GetOffset(), b, e.sent)
			}
			e.sent++
			return elapsed, nil
		case *execv2.ServerFrame_Started, *execv2.ServerFrame_Notice, *execv2.ServerFrame_Link:
		default:
			return 0, fmt.Errorf("echo %d: an unexpected %s frame", e.sent, frameKind(frame))
		}
	}
}

// measure runs n round trips and returns their timings.
func (e *proofEchoer) measure(n int) ([]time.Duration, error) {
	samples := make([]time.Duration, 0, n)
	for range n {
		d, err := e.roundTrip()
		if err != nil {
			return samples, err
		}
		samples = append(samples, d)
	}
	return samples, nil
}
