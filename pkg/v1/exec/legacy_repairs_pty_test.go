//go:build unix

package exec_test

// The end of a session, on a real terminal.
//
// A process that ends has written its last bytes to the terminal and is gone before anyone
// has read them. The kernel keeps them for whoever reads the master side, and the manager used
// to close the master the moment the process was reported gone: with it went everything still in
// the kernel's queue, the tail of the output. How much was lost is a matter of timing (on a
// machine at rest, 12 to 29 runs in 60 lost some of 64 KiB on macOS, and 48 to 54 on Linux),
// which is why the tests below run an end many times, and hold it to the one thing a lane can
// promise: every byte the process wrote, in order, and then the Close signal.
//
// Nothing here decides by how long it waits. The managers run on a clock that does not move
// unless the test moves it, so that no timeout of the manager's is part of what is checked, except
// in the test that checks it.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// process is a handler that does what an executor does with a remote command: it copies the
// command's output into the terminal, in chunks, and when the command is over it reports it, at once.
type process struct {
	mu      sync.Mutex
	scripts map[string]*processScript
}

type processScript struct {
	output  []byte
	chunk   int           // bytes to a write; 4 KiB when 0
	pauseAt int           // after this many bytes the process waits for resume; 0: it does not
	resume  chan struct{} // closed to let a paused process go on
	end     error         // what the process reports when it is over
	begin   chan struct{} // closed to start the process
}

func newProcess() *process { return &process{scripts: make(map[string]*processScript)} }

// expect declares what the session id will write and how it ends. The process starts when the
// returned function is called, which the test does once it is ready for the output.
func (p *process) expect(id string, script processScript) (start func()) {
	if script.chunk == 0 {
		script.chunk = 4 << 10
	}
	script.begin = make(chan struct{})
	p.mu.Lock()
	p.scripts[id] = &script
	p.mu.Unlock()
	return sync.OnceFunc(func() { close(script.begin) })
}

// expectNow is expect for a process that starts as its session does: it writes while the host is
// still on its way to attach.
func (p *process) expectNow(id string, script processScript) { p.expect(id, script)() }

func (p *process) handler(
	_ *types.PluginContext,
	opts exec.SessionOptions,
	tty *os.File,
	stop chan error,
	_ <-chan exec.SessionResizeInput,
) error {
	p.mu.Lock()
	script := p.scripts[opts.ID]
	p.mu.Unlock()
	if script == nil {
		return fmt.Errorf("no script for session %q", opts.ID)
	}
	go func() {
		<-script.begin
		for written := 0; written < len(script.output); {
			if script.pauseAt > 0 && written == script.pauseAt {
				<-script.resume
			}
			n := min(script.chunk, len(script.output)-written)
			if script.pauseAt > written {
				n = min(n, script.pauseAt-written)
			}
			if _, err := tty.Write(script.output[written : written+n]); err != nil {
				stop <- err
				return
			}
			written += n
		}
		stop <- script.end
	}()
	return nil
}

func realTerminalManager(t testing.TB, p *process, options scriptedOptions) *exec.Manager {
	t.Helper()
	m := exec.NewManager(exec.ManagerConfig{
		Logger:          hookedLogger(options.hook),
		Handlers:        map[string]exec.Handler{"/": {TTYHandler: p.handler}},
		Sink:            options.sink,
		TerminalFactory: exec.NewRealTerminalFactory(),
		Clock:           options.clock,
	})
	t.Cleanup(m.Close)
	return m
}

// frames reads a stream that carries several sessions, one session at a time, and
// holds each to the rule that the Close signal is the last thing a session says.
type frames struct {
	out      <-chan exec.StreamOutput
	finished map[string]bool
}

func newFrames(out <-chan exec.StreamOutput) *frames {
	return &frames{out: out, finished: make(map[string]bool)}
}

// until returns the data the stream carried for the session id up to its Close signal, and the
// other signals it carried before that, in order. Output for a session that has said goodbye, or
// for one that is not the session waited for, fails the test.
func (f *frames) until(t testing.TB, id string) (data []byte, signals []exec.StreamSignal) {
	t.Helper()
	timeout := time.NewTimer(mustHappen)
	defer timeout.Stop()
	for {
		select {
		case o, ok := <-f.out:
			if !ok {
				t.Fatalf("the stream ended before the Close signal of %q", id)
			}
			if f.finished[o.SessionID] {
				t.Fatalf("output for %q after its Close signal: signal %v, %d bytes", o.SessionID, o.Signal, len(o.Data))
			}
			if o.SessionID != id {
				t.Fatalf("output for %q while waiting for %q", o.SessionID, id)
			}
			switch o.Signal {
			case exec.StreamSignalNone:
				data = append(data, o.Data...)
			case exec.StreamSignalClose:
				f.finished[id] = true
				return data, signals
			default:
				signals = append(signals, o.Signal)
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for the Close signal of %q (%d bytes so far)", id, len(data))
		}
	}
}

// 200 times a command writes 64 KiB and is over at once. The host has attached: every byte comes,
// and then the Close signal.
func TestTailIsDeliveredBeforeClose(t *testing.T) {
	const runs, size = 200, 64 << 10
	p := newProcess()
	hook, opened := streamOpened()
	m := realTerminalManager(t, p, scriptedOptions{clock: timeutil.NewFakeClock(), hook: hook})
	client := nativeExecClient(t, m, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan exec.StreamInput)
	defer close(input) // the client's sender ends with its input
	out, err := client.Stream(ctx, input)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	awaitClosed(t, opened, "the server to open the stream") // output of a command that is over before it has none to go to
	collected := newFrames(out)

	var lost, short int
	for run := range runs {
		id := fmt.Sprintf("tail-%d", run)
		payload := patterned(size, run)
		start := p.expect(id, processScript{output: payload})
		if _, err = client.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		_, head, err := client.AttachSession(pluginContext(), id)
		if err != nil {
			t.Fatalf("AttachSession: %v", err)
		}
		start()

		data, _ := collected.until(t, id)
		data = append(head, data...)
		if !bytes.Equal(data, payload) {
			short++
			lost += len(payload) - len(data)
			if short == 1 {
				t.Logf("run %d: %d of %d bytes came before the Close signal", run, len(data), len(payload))
			}
		}
	}
	if short > 0 {
		t.Errorf("%d of %d runs lost the end of the output (%d bytes in all): the Close signal came before the last bytes were read",
			short, runs, lost)
	}
}

// The same end for a command that is over before the host has asked to attach: the host attaches
// when the session has ended, and gets all of what the command wrote.
func TestAShortCommandsOutputIsThereForTheFirstAttach(t *testing.T) {
	const runs, size = 100, 2 << 10
	p := newProcess()
	closes := exectest.NewRecordingOutput()
	m := realTerminalManager(t, p, scriptedOptions{sink: closes, clock: timeutil.NewFakeClock()})
	client := nativeExecClient(t, m, nil)

	var gone, short int
	for run := range runs {
		id := fmt.Sprintf("short-%d", run)
		payload := patterned(size, run)
		p.expectNow(id, processScript{output: payload})
		if _, err := client.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		// The command is over when its Close signal is out, and only then does the host come.
		awaitWithin(t, func() bool { return closeSignalsFor(closes, id) == 1 }, "the command to be over")

		_, output, err := client.AttachSession(pluginContext(), id)
		switch {
		case err != nil:
			gone++
		case !bytes.Equal(output, payload):
			short++
		}
	}
	if gone+short > 0 {
		t.Errorf("of %d commands that were over before the host attached, the attach found %d gone and %d with only part of their output",
			runs, gone, short)
	}
}

// holdingSink holds one frame of the output it is given until the test lets it go: a consumer that is slow
// to take a frame. The frame it holds is the one that brings the data it has been given to holdAt bytes.
type holdingSink struct {
	*exectest.RecordingOutput

	held   *gate
	holdAt int

	mu        sync.Mutex
	delivered int
}

func (s *holdingSink) OnOutput(o exec.StreamOutput) {
	if o.Signal == exec.StreamSignalNone && len(o.Data) > 0 {
		s.mu.Lock()
		before := s.delivered
		s.delivered += len(o.Data)
		hold := before < s.holdAt && s.delivered >= s.holdAt
		s.mu.Unlock()
		if hold {
			s.held.hold()
		}
	}
	s.RecordingOutput.OnOutput(o)
}

// slowEnd runs the scene of the tests below: a command writes the head of its output, and then the tail, and is
// over; the consumer is slow to take a frame, the first one or the last one. The end of the session then
// has a consumer that is not taking output, and waits. startSlowEnd returns when it does (the manager's
// clock has a timer pending), with the consumer still holding the frame.
//
// With the first frame held the tail is still in the terminal, unread, when the process is over; with the
// last held everything was read, and the reader is stuck handing over the last of it. The wait of the end of
// the session has two steps (the terminal is hung up, and the reader comes to its end) and each of the
// two holds one case.
type slowEnd struct {
	head, tail []byte
	holdLast   bool
	sink       *holdingSink
	clock      *timeutil.FakeClock
	m          *exec.Manager
}

var slowConsumers = []struct {
	name     string
	holdLast bool
}{
	{"the first frame is held", false},
	{"the last frame is held", true},
}

func startSlowEnd(t *testing.T, holdLast bool) *slowEnd {
	t.Helper()
	s := &slowEnd{head: patterned(512, 1), tail: patterned(512, 2), holdLast: holdLast} // each fits the queue of any terminal
	holdAt := 1
	if holdLast {
		holdAt = len(s.head) + len(s.tail)
	}
	s.sink = &holdingSink{RecordingOutput: exectest.NewRecordingOutput(), held: newGate(t), holdAt: holdAt}
	s.clock = timeutil.NewFakeClock()
	p := newProcess()
	s.m = realTerminalManager(t, p, scriptedOptions{sink: s.sink, clock: s.clock})
	// Cleanups run last to first: a test that fails while the consumer holds a frame lets it go before the
	// manager is closed, which waits for the session whose end is waiting for the consumer.
	t.Cleanup(s.sink.held.release)

	resume := make(chan struct{})
	start := p.expect("s", processScript{output: append(bytes.Clone(s.head), s.tail...), pauseAt: len(s.head), resume: resume})
	createSessions(t, s.m, "s")
	if _, _, err := s.m.AttachSession(nil, "s"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	start()
	if holdLast {
		awaitWithin(t, func() bool { return len(s.sink.AllData()) == len(s.head) }, "the consumer to take the head")
	} else {
		awaitClosed(t, s.sink.held.reached, "the consumer to be given the first frame")
	}
	close(resume) // the process writes the tail, and is over
	if holdLast {
		awaitClosed(t, s.sink.held.reached, "the consumer to be given the last frame")
	}

	ctx, cancel := context.WithTimeout(context.Background(), repairWait)
	defer cancel()
	if err := s.clock.WaitForTimers(ctx, 1); err != nil {
		t.Fatalf("the end of the session did not wait for the output that was left to be read: no wait began (%v)", err)
	}
	return s
}

// finish lets the consumer go, and checks what the session delivered: the output in order, up to a point that it
// says (the whole of it, or the head at least), and then the Close signal, and nothing after.
func (s *slowEnd) finish(t *testing.T, wantAll bool) {
	t.Helper()
	s.sink.held.release()
	if s.sink.WaitForSignal(exec.StreamSignalClose, mustHappen) == nil {
		t.Fatal("the session did not say goodbye")
	}
	awaitWithin(t, func() bool { return sessionsOf(s.m) == 0 }, "the session to be gone")

	outputs := s.sink.Outputs()
	if last := outputs[len(outputs)-1]; last.Signal != exec.StreamSignalClose {
		t.Fatalf("the last thing the session said is %+v, want its Close signal", last)
	}
	got, whole := s.sink.AllData(), append(bytes.Clone(s.head), s.tail...)
	switch {
	case wantAll && !bytes.Equal(got, whole):
		t.Fatalf("the session delivered %d of the %d bytes the process wrote before it said goodbye", len(got), len(whole))
	case !wantAll && !bytes.HasPrefix(whole, got):
		t.Fatalf("the session delivered %d bytes that are not the beginning of what the process wrote", len(got))
	case !wantAll && len(got) < len(s.head):
		t.Fatalf("the session lost the frame the consumer was holding: %d bytes delivered, want at least %d", len(got), len(s.head))
	}
}

// The end of a session waits for the consumer: what the process wrote last is delivered, all of it, before
// the Close signal, however slow the consumer is, because the wait ends when the output has been read and not
// when some time has passed (the clock does not move here).
func TestTailIsDeliveredToAConsumerThatIsSlowAtTheEnd(t *testing.T) {
	for _, consumer := range slowConsumers {
		t.Run(consumer.name, func(t *testing.T) {
			startSlowEnd(t, consumer.holdLast).finish(t, true)
		})
	}
}

// The wait is bounded. A consumer that is stuck does not keep the session from ending: when the
// bound has passed the session leaves the manager with the consumer still holding its frame, and when
// the consumer lets go it has delivered what it had, in order, and the Close signal is still the last
// thing it says. (The goroutine that writes the end mark to the terminal can be waiting for the kernel when the
// wait is given up: the close of the terminal sets it free, and the test checks that it is gone.)
func TestTheWaitForTheTailIsBounded(t *testing.T) {
	for _, consumer := range slowConsumers {
		t.Run(consumer.name, func(t *testing.T) {
			s := startSlowEnd(t, consumer.holdLast)
			s.clock.Advance(time.Minute) // longer than any bound the manager may set
			awaitWithin(t, func() bool { return sessionsOf(s.m) == 0 },
				"the session to leave the manager when the wait for the tail was over, though its consumer is still stuck")
			s.finish(t, consumer.holdLast) // with the last frame held everything was read: all of it is delivered
			awaitWithin(t, func() bool { return !anyGoroutine("exec.(*realTerminal).markEnd") },
				"the goroutine that writes the end mark to be gone")
		})
	}
}

// The end mark is written after what the process wrote, and comes out of the master after it, whatever the master
// did meanwhile: the terminal is not left to tell the end of its output.
func TestAMarkComesOutOfATerminalAfterWhatWasWrittenBeforeIt(t *testing.T) {
	terminal, err := exec.NewRealTerminalFactory()()
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	payload, mark := patterned(40<<10, 3), []byte("QX7Z9K2M4P8R1T6V") // more than a terminal queues for a master that is not reading
	go func() {
		_, _ = terminal.SlaveFd().Write(payload)
		_, _ = exec.MarkEnd(terminal, mark)
	}()

	var got []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(got, mark) {
		_ = terminal.MasterFd().SetReadDeadline(time.Now().Add(mustHappen))
		n, readErr := terminal.MasterFd().Read(buf)
		got = append(got, buf[:n]...)
		if readErr != nil {
			t.Fatalf("reading the master: %v (%d bytes so far)", readErr, len(got))
		}
	}
	if want := append(bytes.Clone(payload), mark...); !bytes.Equal(got, want) {
		t.Fatalf("the master read %d bytes ending in the mark, want the %d the slave was given in that order", len(got), len(want))
	}
}

// A write of the mark that waits for a master that does not read is set free by the close of the terminal.
func TestTheCloseOfATerminalSetsAWriteOfTheMarkFree(t *testing.T) {
	terminal, err := exec.NewRealTerminalFactory()()
	if err != nil {
		t.Fatalf("open terminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	go func() { _, _ = terminal.SlaveFd().Write(patterned(64<<10, 4)) }() // fills the queue of any terminal, and waits for the master
	awaitWithin(t, func() bool {
		return anyGoroutine("TestTheCloseOfATerminalSetsAWriteOfTheMarkFree.func", "waitWrite")
	}, "the queue of the terminal to be full")
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = exec.MarkEnd(terminal, []byte("QX7Z9K2M4P8R1T6V"))
	}()
	// The call is in progress until the close of the terminal sets it free; it waits for the writer before it (a
	// file serializes its writes) as for the master.
	awaitWithin(t, func() bool { return anyGoroutine("exec.(*realTerminal).markEnd") },
		"the write of the mark to be waiting")

	if closeErr := terminal.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	awaitClosed(t, written, "the write of the mark to return after the terminal was closed")
}

// A session that is waited for gives way to a cancellation: the host that closes it, the manager that closes,
// and the stream that ends are not waiting for its output, and the wait for it must not make them wait.
func TestTheWaitForTheTailGivesWayToACancellation(t *testing.T) {
	cancellations := []struct {
		name   string
		cancel func(t *testing.T, s *slowEnd)
	}{
		{"the host closes the session", func(_ *testing.T, s *slowEnd) { go func() { _ = s.m.CloseSession(nil, "s") }() }},
		{"the manager closes", func(_ *testing.T, s *slowEnd) { go s.m.Close() }},
		{"the stream ends", func(t *testing.T, s *slowEnd) {
			ctx, cancel := context.WithCancel(context.Background())
			if _, err := s.m.Stream(ctx, make(chan exec.StreamInput)); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			cancel()
		}},
	}
	for _, c := range cancellations {
		t.Run(c.name, func(t *testing.T) {
			s := startSlowEnd(t, false)
			c.cancel(t, s) // the clock does not move: only the cancellation can end the wait
			awaitWithin(t, func() bool { return sessionsOf(s.m) == 0 },
				"the session to leave the manager when it was cancelled during the wait for its tail, though its consumer is still stuck")
			s.finish(t, false)
		})
	}
}
