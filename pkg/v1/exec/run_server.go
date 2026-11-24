package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omniviewdev/plugin-sdk/internal/session"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The bounded run on the wire: Run is one request and one result, RunStream the same with the output as it happens. The engine
// owns the command; this file owns the call. A run is a command of the engine like a session, started the same way and held in
// the same id space (its id is the request's, or "run-" and an identifier when the host gave none), counted in its own group
// (Run.MaxConcurrent) and ended with the same typed End, which run.go turns into the result.
//
// What a call does, in order:
//
//  1. admits the request (admitRunRequest: strict, every bound mandatory, argv only), takes the plugin context like Open does
//     and hands the command to the engine; a refusal is a status and nothing ran;
//  2. feeds the stdin the request carries and closes the input, on a goroutine that belongs to the command and ends with it;
//  3. reads the output as it is queued, on one goroutine, into a bounded capture for each stream (a Run) or to the host as
//     events (a RunStream), never holding more than the stream's cap;
//  4. waits, with one timer for the run's timeout: at timeout_ms (the target's default when the request gives none) it ends the
//     command with a kill, which does not wait for the function, so that the call returns at its timeout plus the engine's
//     short wait for a function that honors its context, and a function that does not is abandoned and holds its place;
//  5. when the command has ended, gives the output Run.PostExitDrain to be read to its end, waits for the command's id to be
//     free and answers with the result.
//
// A call that ends before the command does (its caller cancelled, its deadline passed, the connection broke) ends the command
// too, gracefully, and returns the transport's own status: it says nothing about whether the command ran.

// defaultRunProgressEvery is the most often a RunStream sends RunProgress: once for each interval in which output advanced.
const defaultRunProgressEvery = time.Second

// runConfig is the timings and the seams of the bounded runs a server serves. The zero value is the production setting.
type runConfig struct {
	// progressTicks makes the ticks that pace RunProgress, and the function that stops them; nil is a ticker of
	// defaultRunProgressEvery. It is a seam so that a test drives the pace by hand.
	progressTicks func() (<-chan time.Time, func())
	// tickHandled runs on the handler's goroutine after each tick of progress was handled, whether the tick queued progress or found nothing
	// new. It is a seam so that a test that drives the ticks by hand knows that a tick is over before it lets the output advance: a tick
	// that is taken is not a tick that is handled, and the output that advances in between is read by the tick that was meant to find none.
	tickHandled func()
	// beforeRead runs on the goroutine that reads a run's output, before each read of the queue. ctx ends when the reader is
	// to stop. It is a seam so that a test can hold the reader.
	beforeRead func(ctx context.Context)
}

// id returns the id the run is held under in the engine: the request's own, or one the SDK makes when the host gave none. The
// result echoes the request's id and never the one that was made.
func (r runRequest) id() string {
	if r.RequestID != "" {
		return r.RequestID
	}
	return "run-" + newRunID()
}

// truncatePolicyOf is the policy a request's output limits name: unset is the default, HEAD_TAIL. Admission has refused a value
// this build does not know.
func truncatePolicyOf(p execv2.TruncatePolicy) truncatePolicy {
	switch p {
	case execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD:
		return truncateHead
	case execv2.TruncatePolicy_TRUNCATE_POLICY_TAIL:
		return truncateTail
	case execv2.TruncatePolicy_TRUNCATE_POLICY_UNSPECIFIED, execv2.TruncatePolicy_TRUNCATE_POLICY_HEAD_TAIL:
	}
	return truncateHeadTail
}

// startRun hands an admitted run request to the engine and feeds its stdin. ctx carries the caller's connection and catalog pin
// and owns the command: when the call's context ends, so does the command. The stdin is sent whole and then closed, so that the
// command reads it and then end of input; the goroutine that does it is a producer of the command, and ends when the command
// does.
func (s *serverV2) startRun(ctx context.Context, run runRequest, target *registeredTarget) (*command, error) {
	cmd, err := s.engine.start(ctx, startParams{
		ID:      run.id(),
		Kind:    kindRun,
		Target:  target,
		Route:   run.Target.Route,
		Request: run.request(target),
		Stdin:   len(run.Stdin) > 0,
		Owner:   ctx,
	})
	if err != nil {
		return nil, err
	}
	if len(run.Stdin) > 0 {
		stdin := run.Stdin
		cmd.entry.Go(func() {
			_, _ = cmd.WriteInput(cmd.entry.Context(), stdin)
			cmd.CloseInput()
		})
	}
	return cmd, nil
}

// Run serves one bounded run: it answers with the RunResult of the run, whatever became of the command once it was handed to the
// target. A request that is refused is a status. The handler is in flight, for Plugin.Shutdown, from before it starts the command
// until it returns: its last act is to hand the result to the transport.
func (s *serverV2) Run(ctx context.Context, req *execv2.RunRequest) (*execv2.RunResult, error) {
	call, err := s.beginRun(ctx, "Run", req, false)
	if err != nil {
		return nil, err
	}
	defer call.end()
	return call.serveResult()
}

// RunStream serves one bounded run with its output as events: RunStarted when the start is observed, Output events, RunProgress
// and then RunEnded, once, and nothing after it. A request that is refused is a status and no event. The handler is in flight,
// for Plugin.Shutdown, from before it starts the command until it returns: its last act is to send RunEnded.
func (s *serverV2) RunStream(req *execv2.RunRequest, stream execv2.Exec_RunStreamServer) error {
	call, err := s.beginRun(stream.Context(), "RunStream", req, true)
	if err != nil {
		return err
	}
	defer call.end()
	return call.serveStream(stream)
}

// runCall is one run being served.
type runCall struct {
	s *serverV2
	// ctx ends with the call.
	ctx      context.Context
	cmd      *command
	req      runRequest
	streamed bool
	// accepted is when the SDK accepted the request, and deadline when its timeout ends: the timeout counts from the request, so
	// that a slow start takes from it.
	accepted, deadline time.Time
	// drain is how long the output has to be read to its end once the command has ended.
	drain time.Duration
	// endedAt is when the call saw the command end: the post-exit drain counts from it. Only the call's goroutine reads or writes it.
	endedAt time.Time
	// opened are the facts of the run that were known before the function ran.
	opened *execv2.SessionFacts
	reader *runReader
	// end releases the call's place among the calls in flight (inflight.go); the handler calls it once, when it returns.
	end func()
}

// beginRun admits a request and starts its command. method names the RPC for the log of a refusal.
func (s *serverV2) beginRun(ctx context.Context, method string, wire *execv2.RunRequest, streamed bool) (*runCall, error) {
	accepted := time.Now()
	run, target, refusal := admitRunRequest(wire, s.reg, newFeatureSet(servedFeatures()))
	if refusal != nil {
		return nil, s.refuse(ctx, method, refusal)
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	end := s.inflight.begin() // before the command starts, so that no command is admitted that Shutdown does not wait for
	cmd, err := s.startRun(ctx, run, target)
	if err != nil {
		end()
		return nil, s.startRefusal(ctx, method, err)
	}
	limits := &target.info.Run
	timeout := run.Timeout
	if timeout == 0 {
		timeout = limits.DefaultTimeout
	}
	policy := truncatePolicyOf(run.Output.Policy)
	c := &runCall{
		s: s, ctx: ctx, cmd: cmd, req: run, streamed: streamed, end: end,
		accepted: accepted, deadline: accepted.Add(timeout), drain: limits.PostExitDrain,
		opened: cmd.openedFacts(),
		reader: &runReader{
			cmd:        cmd,
			stdout:     newOutputSink(streamed, orDefault(run.Output.MaxStdoutBytes, limits.DefaultOutputBytes), policy),
			stderr:     newOutputSink(streamed, orDefault(run.Output.MaxStderrBytes, limits.DefaultOutputBytes), policy),
			beforeRead: s.run.beforeRead,
			done:       make(chan struct{}),
		},
	}
	return c, nil
}

// orDefault is the cap a request asked for, or the target's default when it asked for none.
func orDefault(asked, def int) int {
	if asked > 0 {
		return asked
	}
	return def
}

// serveResult is the call of a Run: it reads the run to its end and answers with its result.
func (c *runCall) serveResult() (*execv2.RunResult, error) {
	readCtx, stopReader := context.WithCancel(c.ctx)
	go c.reader.run(readCtx)
	defer func() {
		stopReader()
		<-c.reader.done // nothing of this call outlives it: the reader does no send, so it stops at once
	}()

	drained, err := c.await(runWatch{})
	if err != nil {
		return nil, err
	}
	return c.result(drained, true)
}

// result is the RunResult of a run that has ended: the call has stopped taking output, and the id of the command is free.
func (c *runCall) result(drained, withPayload bool) (*execv2.RunResult, error) {
	stdout, stderr := c.reader.seal()
	res := c.cmd.Result()
	if err := c.awaitGone(); err != nil {
		return nil, err
	}
	target, program := c.facts()
	return buildRunResult(runResultInput{
		RequestID: c.req.RequestID, Result: res, Target: target, Program: program, Stdout: stdout, Stderr: stderr, Drained: drained,
	}, withPayload), nil
}

// facts are the target and the program that the result reports: what the function reported at Started, when it did, and what
// the SDK knew before it ran otherwise.
func (c *runCall) facts() (*execv2.TargetFacts, *execv2.Program) {
	facts := c.opened
	if refined, ok := c.cmd.startedFacts(c.opened); ok {
		facts = refined
	}
	return facts.GetTarget(), facts.GetProgram()
}

// awaitGone waits until the command has been removed from the engine: its id and its place are free, so that a host that reads
// the result and runs the same id again is not refused for the run that has just ended.
func (c *runCall) awaitGone() error {
	select {
	case <-c.cmd.Gone():
		return nil
	case <-c.ctx.Done():
		return status.FromContextError(c.ctx.Err()).Err()
	}
}

// runWatch is what a stream adds to the wait of a run: events to queue while it goes on, and the failure of the stream. A nil
// channel is never ready. None of the functions blocks: the wait must be able to end the run whatever the host does.
type runWatch struct {
	// started is closed when the command has facts to report at its start, and onStarted queues them.
	started   <-chan struct{}
	onStarted func()
	// ticks pace the progress, and onTick queues it.
	ticks  <-chan time.Time
	onTick func()
	// failed is closed when the stream cannot send, and failure says why.
	failed  <-chan struct{}
	failure func() error
}

// await waits until the command has ended and its output has been read to its end, or has not been read within the drain, and
// reports which. It is the one place the run's timeout is enforced: when the timer fires the command is ended with a kill. It
// returns the status of the call when the call ends first, and the error of a stream that cannot send.
func (c *runCall) await(w runWatch) (drained bool, err error) {
	timeout := time.NewTimer(time.Until(c.deadline))
	defer timeout.Stop()
	done, readerDone := c.cmd.Done(), c.reader.done
	var drainOver <-chan time.Time
	for done != nil || readerDone != nil {
		select {
		case <-timeout.C:
			c.cmd.Close(session.CauseTimeout, true)
		case <-done:
			done = nil
			c.endedAt = time.Now()
			if readerDone != nil {
				drain := time.NewTimer(c.drain)
				defer drain.Stop()
				drainOver = drain.C
			}
		case <-readerDone:
			readerDone = nil
			if readErr := c.reader.err; readErr != nil {
				if !c.streamed {
					return false, nil // not read to its end: the result says less
				}
				if c.ctx.Err() != nil {
					return false, status.FromContextError(c.ctx.Err()).Err()
				}
				return false, readErr // the host cannot take the events: the call is over
			}
		case <-drainOver:
			return false, nil
		case <-w.started:
			w.started = nil
			w.onStarted()
		case <-w.ticks:
			w.onTick()
		case <-w.failed:
			return false, w.failure()
		case <-c.ctx.Done():
			return false, status.FromContextError(c.ctx.Err()).Err()
		}
	}
	return true, nil
}

// ---------------------------------------------------------------------------------------------------------------
// the reader of the output
// ---------------------------------------------------------------------------------------------------------------

// outputSink is where the reader puts the bytes of one stream: a capture keeps them, for a Run, and a tally lets them through to
// be delivered as events, for a RunStream.
type outputSink struct {
	capture *capture
	tally   *streamTally
}

func newOutputSink(streamed bool, limit int, policy truncatePolicy) outputSink {
	if streamed {
		return outputSink{tally: newStreamTally(limit)}
	}
	return outputSink{capture: newCapture(limit, policy)}
}

// write takes p, the next bytes of the stream, and returns the part of it to deliver: none for a capture, which keeps what it
// keeps.
func (k outputSink) write(p []byte) []byte {
	if k.capture != nil {
		k.capture.Write(p)
		return nil
	}
	return k.tally.Take(p)
}

// lose takes n bytes of the stream that the engine's queue overwrote before they were read.
func (k outputSink) lose(n uint64) {
	if k.capture != nil {
		k.capture.Lose(n)
		return
	}
	k.tally.Lose(n)
}

// result is what the sink has kept and its account.
func (k outputSink) result() runOutput {
	if k.capture != nil {
		data, account := k.capture.Result()
		return runOutput{Data: data, Account: account}
	}
	return runOutput{Account: k.tally.Account()}
}

// runReader reads the output of a command to its end, on one goroutine, and accounts for it. Its buffer starts small and grows
// to a frame as the queue fills it: a run that writes a few bytes costs a few KiB, and one that floods reads whole frames.
type runReader struct {
	cmd            *command
	stdout, stderr outputSink
	// beforeRead is the test seam that holds the reader.
	beforeRead func(context.Context)
	// deliver, when it is set, sends what the sinks let through, in order; an error ends the reader. It is set before the reader
	// starts.
	deliver func(stream execv2.Stream, offset uint64, data []byte) error

	// mu guards the sinks and sealed: the call takes the result under it once the reader is to be taken for stopped.
	mu     sync.Mutex
	sealed bool
	// seen is every byte the reader has accounted for, both streams, lost ones included: what RunProgress reports.
	seen atomic.Uint64
	// done is closed when the reader has stopped; err, written before it, says why: nil when the output was read to its end.
	done chan struct{}
	err  error
}

// run reads until the command's output ends, the call is over (ctx), the call has taken the result (sealed) or a send fails.
func (r *runReader) run(ctx context.Context) {
	defer close(r.done)
	buf := make([]byte, minOutputBuffer)
	for {
		if r.beforeRead != nil {
			r.beforeRead(ctx)
		}
		chunk, err := r.cmd.Read(ctx, buf)
		if chunk.N > 0 || len(chunk.Dropped) > 0 {
			out, ok := r.take(chunk, buf[:chunk.N])
			if !ok {
				return
			}
			if len(out) > 0 && r.deliver != nil {
				if sendErr := r.deliver(chunk.Stream, chunk.Offset, out); sendErr != nil {
					r.err = sendErr
					return
				}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.err = err
			}
			return
		}
		if chunk.N == len(buf) && len(buf) < maxOutputFrame {
			buf = make([]byte, min(2*len(buf), maxOutputFrame)) // the queue had more than the buffer held: take more next time
		}
	}
}

// take accounts for one chunk: the ranges the queue lost before it, and its bytes. It returns the part of the bytes to deliver,
// and false when the call has already taken the result.
func (r *runReader) take(chunk outputChunk, data []byte) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return nil, false
	}
	seen := uint64(len(data))
	for _, d := range chunk.Dropped {
		r.sink(d.Stream).lose(d.Bytes)
		seen += d.Bytes
	}
	var out []byte
	if len(data) > 0 {
		out = r.sink(chunk.Stream).write(data)
	}
	r.seen.Add(seen)
	return out, true
}

// sink is the sink of a stream: standard error has its own, and every other stream is standard output (a run has no terminal).
func (r *runReader) sink(stream execv2.Stream) outputSink {
	if stream == execv2.Stream_STREAM_STDERR {
		return r.stderr
	}
	return r.stdout
}

// seal stops the accounting and returns what each stream kept: a chunk that arrives later is not counted.
func (r *runReader) seal() (stdout, stderr runOutput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
	return r.stdout.result(), r.stderr.result()
}

// ---------------------------------------------------------------------------------------------------------------
// RunStream
// ---------------------------------------------------------------------------------------------------------------

// serveStream is the call of a RunStream. Three goroutines serve it besides the engine's own, and only one of them touches the
// stream:
//
//   - the sender is the one goroutine that sends on the stream, in the order the others queued: a stream takes one sender at a
//     time, and a send may block for as long as the host's window is closed;
//   - the reader takes the command's output as it is queued, accounts for it and queues the part the cap lets through;
//   - the handler's goroutine waits for the end of the run, ends it at its timeout and queues RunStarted, RunProgress and the
//     last event. It never waits for the sender except for the very end, so that the timeout, the post-exit drain and the host
//     that has gone away are decided whatever the sender is held by.
//
// The queue between them is short: a host that does not take events stops the reader, which stops the function (it blocks in
// Write, as a function does that nobody reads), and the run still ends at its timeout.
func (c *runCall) serveStream(stream execv2.Exec_RunStreamServer) error {
	callCtx, stop := context.WithCancel(c.ctx)
	defer stop() // the reader and the sender are not waited for: the sender may be in a send that only the end of the stream releases
	st := newRunStream(c, stream)
	c.reader.deliver = func(s execv2.Stream, offset uint64, data []byte) error {
		// The data is copied: the reader reuses its buffer, and the event is held by the queue and then by the transport.
		return st.enqueue(callCtx, streamItem{kind: itemOutput, stream: s, offset: offset, data: bytes.Clone(data)})
	}
	go st.run(callCtx)
	go c.reader.run(callCtx)
	ticks, stopTicks := c.progressTicks()
	defer stopTicks()

	var lastProgress uint64
	// Whether the reader finished within the drain is not asked: a drain that ran out here is out below as well, where the end is
	// queued, and ends the stream with the same status.
	_, err := c.await(runWatch{
		started:   c.cmd.StartedC(),
		onStarted: func() { st.offer(streamItem{kind: itemStarted}) },
		ticks:     ticks,
		onTick: func() {
			// Only while the output advanced: a tick that finds nothing new queues nothing, and one that finds the sender busy
			// leaves the progress for the next.
			if seen := c.reader.seen.Load(); seen != lastProgress && st.offer(streamItem{kind: itemProgress, bytesOut: seen}) {
				lastProgress = seen
			}
			if handled := c.s.run.tickHandled; handled != nil {
				handled()
			}
		},
		failed:  st.failed,
		failure: func() error { return st.err },
	})
	if err != nil {
		return err
	}
	res, err := c.result(true, false)
	if err != nil {
		return err
	}
	// What is left to deliver, the output that is queued and then the end, has the rest of the post-exit drain.
	over := time.NewTimer(time.Until(c.endedAt.Add(c.drain)))
	defer over.Stop()
	select {
	case st.items <- streamItem{kind: itemEnded, result: res}:
	case <-st.failed:
		return st.err
	case <-over.C:
		return postExitDrainStatus()
	case <-callCtx.Done():
		return status.FromContextError(callCtx.Err()).Err()
	}
	select {
	case <-st.done:
		return st.err // nil once RunEnded was sent
	case <-over.C:
		return postExitDrainStatus()
	case <-callCtx.Done():
		return status.FromContextError(callCtx.Err()).Err()
	}
}

// postExitDrainStatus is how a stream ends when its host did not take what was left within the post-exit drain: with a status of
// the transport's own kind, which says nothing about the command and is never a refusal. The end cannot be delivered either.
func postExitDrainStatus() error {
	return status.Error(codes.DeadlineExceeded, "exec: the output of the run was not taken within the post-exit drain")
}

// progressTicks returns the ticks that pace RunProgress and the function that stops them.
func (c *runCall) progressTicks() (<-chan time.Time, func()) {
	if ticks := c.s.run.progressTicks; ticks != nil {
		return ticks()
	}
	ticker := time.NewTicker(defaultRunProgressEvery)
	return ticker.C, ticker.Stop
}

// The kinds of what the sender is asked to send.
const (
	itemStarted  = iota // RunStarted, if the command has facts to report at its start and the host has not been told
	itemOutput          // one chunk of output
	itemProgress        // RunProgress
	itemEnded           // RunEnded, the last: the sender stops after it
)

// streamItem is one thing for the sender to send.
type streamItem struct {
	kind     int
	stream   execv2.Stream
	offset   uint64
	data     []byte
	bytesOut uint64
	result   *execv2.RunResult
}

// runStream is the sending side of a RunStream: a short queue and the goroutine that empties it onto the stream.
type runStream struct {
	call   *runCall
	stream execv2.Exec_RunStreamServer
	items  chan streamItem
	// failed is closed when a send failed, and err says why, written before it: the stream is broken and nothing more is sent.
	// done is closed when the sender has stopped; err is nil if it stopped after sending RunEnded.
	failed chan struct{}
	done   chan struct{}
	err    error
	// startedSent is the sender's own.
	startedSent bool
}

// runStreamQueue is how many items wait for the sender: enough that the reader is not held by the hand-off, few enough that a host
// that takes nothing holds at most a few frames.
const runStreamQueue = 4

func newRunStream(c *runCall, stream execv2.Exec_RunStreamServer) *runStream {
	return &runStream{
		call: c, stream: stream, items: make(chan streamItem, runStreamQueue),
		failed: make(chan struct{}), done: make(chan struct{}),
	}
}

// enqueue queues an item, waiting while the sender is behind. It returns the error of the stream when a send failed, and the
// error of ctx when the call is over.
func (s *runStream) enqueue(ctx context.Context, item streamItem) error {
	select {
	case s.items <- item:
		return nil
	case <-s.failed:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// offer queues an item if the queue has room and says whether it did. It never waits. An item that RunStarted or RunProgress
// would be is not lost by a refusal: the sender begins every output with RunStarted, and progress is at the SDK's pace.
func (s *runStream) offer(item streamItem) bool {
	select {
	case s.items <- item:
		return true
	default:
		return false
	}
}

// run sends what is queued, in order, until RunEnded has been sent, a send fails or the call is over.
func (s *runStream) run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case item := <-s.items:
			if err := s.send(item); err != nil {
				s.err = err
				close(s.failed)
				return
			}
			if item.kind == itemEnded {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// send sends one item.
func (s *runStream) send(item streamItem) error {
	switch item.kind {
	case itemStarted:
		return s.sendStarted()
	case itemOutput:
		// The facts are recorded before the output that follows them can be read, so RunStarted precedes the first Output.
		if err := s.sendStarted(); err != nil {
			return err
		}
		return s.stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Output{Output: &execv2.Output{
			Stream: item.stream, Offset: item.offset, Data: item.data,
		}}})
	case itemProgress:
		// Progress is queued only for output that was counted, and output has facts to report at its start: RunStarted is sent
		// first, if it has not been.
		if err := s.sendStarted(); err != nil {
			return err
		}
		return s.stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Progress{Progress: &execv2.RunProgress{
			ElapsedMs: uint64(max(time.Since(s.call.accepted).Milliseconds(), 0)), //nolint:gosec // not negative
			BytesOut:  item.bytesOut,
		}}})
	default: // itemEnded
		// RunStarted first when the start was observed and the host has not been told: a command that ended with a status and
		// neither wrote nor reported has no facts of its own, and its status is what observed it. A run whose start was not
		// observed sends RunEnded alone.
		if err := s.sendStarted(); err != nil {
			return err
		}
		if res := item.result; !s.startedSent && res.GetStartObserved() {
			if err := s.sendRunStarted(res.GetTarget(), res.GetProgram()); err != nil {
				return err
			}
		}
		return s.stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Ended{Ended: &execv2.RunEnded{Result: item.result}}})
	}
}

// sendStarted sends RunStarted once the command has facts to report at its start and it has not been sent.
func (s *runStream) sendStarted() error {
	if s.startedSent {
		return nil
	}
	facts, ok := s.call.cmd.startedFacts(s.call.opened)
	if !ok {
		return nil
	}
	return s.sendRunStarted(facts.GetTarget(), facts.GetProgram())
}

func (s *runStream) sendRunStarted(target *execv2.TargetFacts, program *execv2.Program) error {
	err := s.stream.Send(&execv2.RunEvent{Event: &execv2.RunEvent_Started{Started: &execv2.RunStarted{Target: target, Program: program}}})
	if err == nil {
		s.startedSent = true
	}
	return err
}
