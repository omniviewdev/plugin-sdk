package exec_test

// What the legacy exec lane promises the host about the end of a stream and of a session.
//
// The host opens ONE stream per plugin, creates sessions with unary calls, attaches to them,
// and learns that one has ended from its Close signal. The tests below hold the lane to four
// promises that it broke:
//
//   - a stream that ends, by whatever exit, ends the sessions it served: their output can no
//     longer be delivered, so they cannot be left running (the exit by EOF left them running);
//   - the stream the host opened last carries the output: a stream opened after another one
//     ended gets all of it (it got half);
//   - the output of a session that ended before the host attached to it is kept for the first
//     attach (it was gone, and the attach was refused as if the session had never existed);
//   - the end of a session is the last thing the session says (the tail of its output used to be
//     lost; that needs a real terminal, so it is in legacy_repairs_pty_test.go).
//
// The sessions here run on pipe-backed terminals and are driven from the test's side: the test
// is the process behind each, so what the manager does with its output and its end is all there
// is to observe. Nothing here decides by how long it waits: a test waits for what has to happen,
// for as long as it takes, up to repairWait, which a correct run never reaches.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

// repairWait bounds a wait for something that has to happen. It is shorter than mustHappen
// because the tests here are run against the code they were written to fail, and a failure
// should not take half a minute.
const repairWait = 10 * time.Second

// pollWithin reports whether cond came true within repairWait. It polls state that goroutines
// reach by themselves, and does not stop the test, so goroutines other than the test's can use it.
func pollWithin(cond func() bool) bool {
	deadline := time.Now().Add(repairWait)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// awaitWithin fails the test when cond does not come true within repairWait.
func awaitWithin(t testing.TB, cond func() bool, what string) {
	t.Helper()
	if !pollWithin(cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

func sessionsOf(m *exec.Manager) int {
	sessions, err := m.ListSessions(nil)
	if err != nil {
		return -1
	}
	return len(sessions)
}

// patterned returns n bytes that tell a lost, repeated or reordered run from the right one: the
// pattern is 251 bytes long, a prime that does not divide the sizes of a frame or a write.
func patterned(n, seed int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte((i + seed*7) % 251)
	}
	return out
}

// ---------------------------------------------------------------------------
// Sessions that are driven from the test
// ---------------------------------------------------------------------------

// scripted is the handler of sessions that do nothing of their own. The test plays the process
// behind each one: it writes the process's output to the terminal and ends the process.
type scripted struct {
	terms *terminalLedger

	mu       sync.Mutex
	sessions map[string]*scriptedSession
}

type scriptedSession struct {
	terminal *ledgerTerminal
	stop     chan error
}

func newScripted() *scripted {
	return &scripted{terms: &terminalLedger{}, sessions: make(map[string]*scriptedSession)}
}

func (s *scripted) handler(
	_ *types.PluginContext,
	opts exec.SessionOptions,
	tty *os.File,
	stop chan error,
	_ <-chan exec.SessionResizeInput,
) error {
	value, _ := s.terms.byTTY.Load(tty)
	terminal, ok := value.(*ledgerTerminal)
	if !ok {
		return errors.New("the terminal is not the rig's")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[opts.ID] = &scriptedSession{terminal: terminal, stop: stop}
	return nil
}

// lookup returns the session id, or nil when its handler has not been called. It does not stop the test, so
// goroutines other than the test's can use it.
func (s *scripted) lookup(id string) *scriptedSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *scripted) session(t testing.TB, id string) *scriptedSession {
	t.Helper()
	session := s.lookup(id)
	if session == nil {
		t.Fatalf("session %q never reached its handler", id)
	}
	return session
}

// write is the process writing to its terminal.
func (p *scriptedSession) write(t testing.TB, data []byte) {
	t.Helper()
	if _, err := p.terminal.InjectOutput(data); err != nil {
		t.Fatalf("writing to the terminal: %v", err)
	}
}

// end is the process ending, with err as what it reports (nil for a clean end).
func (p *scriptedSession) end(err error) { p.stop <- err }

type scriptedOptions struct {
	sink  exec.OutputSink
	clock timeutil.Clock
	hook  func(msg string) // sees every debug message of the manager
}

// manager returns a manager of scripted sessions, closed when the test ends. Without a sink in the options
// it makes the sink of every stream a test opens.
func (s *scripted) manager(t testing.TB, options scriptedOptions) *exec.Manager {
	t.Helper()
	var hook atomic.Pointer[func(string)]
	if options.hook != nil {
		hook.Store(&options.hook)
	}
	m := exec.NewManager(exec.ManagerConfig{
		Logger:          hookLogger{Logger: logging.NewNop(), hook: &hook},
		Handlers:        map[string]exec.Handler{"/": {TTYHandler: s.handler}},
		Sink:            options.sink,
		TerminalFactory: s.terms.factory(),
		Clock:           options.clock,
	})
	t.Cleanup(m.Close)
	return m
}

func pluginContext() *types.PluginContext { return &types.PluginContext{Context: context.Background()} }

func createSessions(t testing.TB, m *exec.Manager, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := m.CreateSession(pluginContext(), exec.SessionOptions{ID: id, Command: []string{"sh"}}); err != nil {
			t.Fatalf("CreateSession(%q): %v", id, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The end of a stream
// ---------------------------------------------------------------------------

// A stream's end has three exits: its context is done, its input is closed (the host closed its
// side of the RPC: EOF), or both, which is what a stream that breaks looks like to the manager.
// Each one has to end the sessions the stream served, once.
type streamEnding struct {
	name        string
	closesInput bool // the ending closes the input of the stream, which a test that does not do it closes at its end
	end         func(cancel context.CancelFunc, input chan exec.StreamInput)
}

var streamEndings = []streamEnding{
	{"the context is done", false, func(cancel context.CancelFunc, _ chan exec.StreamInput) { cancel() }},
	{"the input is closed (EOF)", true, func(_ context.CancelFunc, input chan exec.StreamInput) { close(input) }},
	{"both at once", true, func(cancel context.CancelFunc, input chan exec.StreamInput) {
		var started sync.WaitGroup
		started.Add(1)
		go func() {
			started.Done()
			cancel()
		}()
		started.Wait()
		close(input)
	}},
}

func TestAStreamThatEndsEndsTheSessionsItServed(t *testing.T) {
	for _, ending := range streamEndings {
		t.Run(ending.name, func(t *testing.T) {
			sc := newScripted()
			var ended atomic.Int64
			m := sc.manager(t, scriptedOptions{hook: func(msg string) {
				if msg == "stream ended" {
					ended.Add(1)
				}
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := make(chan exec.StreamInput)
			if !ending.closesInput {
				defer close(input)
			}
			if _, err := m.Stream(ctx, input); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			createSessions(t, m, "a", "b", "c")

			ending.end(cancel, input)

			awaitWithin(t, func() bool { return sessionsOf(m) == 0 },
				"the sessions to end after the stream they were served by ended: they are still running")
			awaitWithin(t, func() bool { return sc.terms.closed.Load() == sc.terms.created.Load() }, "the terminals of the sessions to close")
			awaitWithin(t, func() bool { return ended.Load() >= 1 }, "the end of the stream to be noted")
			// The end of a stream is handled once: it is the last thing the input loop of the stream does, whichever
			// exit it took first, or whether both came. (Nothing here waits to see that no second pass follows:
			// there is no second place that ends a stream.)
			if got := ended.Load(); got != 1 {
				t.Fatalf("the end of one stream was handled %d times, want 1", got)
			}
		})
	}
}

// The end mark is found wherever the output is cut: the scanner of the reader delivers everything before it,
// and nothing of it, however the terminal chops up the stream; what it holds back as a possible beginning of
// the mark is delivered with the next chunk when it turns out not to be one, and dropped if the output ends.
func TestTheEndMarkIsFoundWhereverTheOutputIsCut(t *testing.T) {
	mark := []byte("QX7Z9K2M4P8R1T6V")
	outputs := [][]byte{nil, []byte("hello"), []byte("Q"), []byte("QX7Z"), []byte("QX7ZQX7Z9K2M"), bytes.Repeat([]byte("Q"), 40), patterned(60, 5)}
	for _, before := range outputs {
		whole := append(bytes.Clone(before), mark...)
		check := func(chunks ...[]byte) {
			t.Helper()
			if got, found := exec.ScanMark(mark, chunks...); !found || !bytes.Equal(got, before) {
				t.Fatalf("the output %q cut as %q: delivered %q (mark reached: %v), want %q and the mark", whole, chunks, got, found, before)
			}
		}
		for i := 0; i <= len(whole); i++ {
			check(whole[:i], whole[i:])
			for j := i; j <= len(whole); j++ {
				check(whole[:i], whole[i:j], whole[j:])
			}
		}
		// Without the mark everything is delivered but for the longest end of the output that is a beginning of
		// the mark, which is dropped when the output ends: exactly that, whatever the cut.
		held := 0
		for n := min(len(mark)-1, len(before)); n > 0; n-- {
			if bytes.HasSuffix(before, mark[:n]) {
				held = n
				break
			}
		}
		for i := 0; i <= len(before); i++ {
			got, found := exec.ScanMark(mark, before[:i], before[i:])
			if found || !bytes.Equal(got, before[:len(before)-held]) {
				t.Fatalf("the output %q cut at %d, with no mark in it: delivered %q (mark reached: %v), want %q", before, i, got, found, before[:len(before)-held])
			}
		}
	}
	if got, found := exec.ScanMark(mark, append(bytes.Clone(mark), "after"...)); !found || len(got) != 0 {
		t.Fatalf("what comes after the mark was delivered: %q (mark reached: %v)", got, found)
	}
	if got, found := exec.ScanMark(nil, []byte("QX7Z"), []byte("9K")); found || string(got) != "QX7Z9K" {
		t.Fatalf("without a mark to look for the output must pass through: %q (%v)", got, found)
	}
}

// A stream that has been replaced, or has ended, must not hold up a session by a send to a consumer that does not
// read: the sink of the stream is ended with it. The output of the test is the proof: the reader of the session is held
// up by a stream that nobody reads, until the stream is replaced (and the session then delivers to the
// new one), or ends by its input being closed (and the manager then closes).
func TestARetiredStreamsSinkDoesNotHoldUpTheReaderOfASession(t *testing.T) {
	for _, retire := range []string{"a newer stream replaces it", "its input is closed"} {
		t.Run(retire, func(t *testing.T) {
			sc := newScripted()
			m := sc.manager(t, scriptedOptions{})
			ctxA, cancelA := context.WithCancel(context.Background())
			defer cancelA()
			inputA := make(chan exec.StreamInput)
			if _, err := m.Stream(ctxA, inputA); err != nil { // its output is never read
				t.Fatalf("Stream: %v", err)
			}
			createSessions(t, m, "s")
			if _, _, err := m.AttachSession(nil, "s"); err != nil {
				t.Fatalf("AttachSession: %v", err)
			}
			process := sc.session(t, "s")
			// The process writes until the reader is held up: the channel of the stream holds a few thousand frames.
			for i := 0; !anyGoroutineIn("select", "(*ChannelSink).OnOutput", "(*Manager).emitData"); i++ {
				if i > 1<<20 {
					t.Fatal("the reader of the session was not held up by a stream that nobody reads")
				}
				for range 256 {
					process.write(t, []byte{'x'})
				}
			}

			if retire == "its input is closed" {
				close(inputA)
				closed := make(chan struct{})
				go func() {
					m.Close()
					close(closed)
				}()
				select {
				case <-closed:
				case <-time.After(repairWait):
					t.Fatal("the manager did not close: the reader of a session is held up by the stream that ended")
				}
				return
			}

			ctxB, cancelB := context.WithCancel(context.Background())
			defer cancelB()
			outB, err := m.Stream(ctxB, make(chan exec.StreamInput))
			if err != nil {
				t.Fatalf("second Stream: %v", err)
			}
			process.write(t, []byte("END"))
			var got []byte
			deadline := time.After(repairWait)
			for !bytes.HasSuffix(got, []byte("END")) {
				select {
				case output := <-outB:
					got = append(got, output.Data...)
				case <-deadline:
					t.Fatal("the output of the session did not reach the newer stream: its reader is held up by the stream it replaced")
				}
			}
		})
	}
}

// The end of a stream that a newer stream has replaced ends nothing: the sessions are served by
// the newer one, which is where their output goes.
func TestAStreamThatANewerOneReplacedEndsNothingWhenItEnds(t *testing.T) {
	sc := newScripted()
	var ended atomic.Int64
	m := sc.manager(t, scriptedOptions{hook: func(msg string) {
		if msg == "stream ended" {
			ended.Add(1)
		}
	}})
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	outA, err := m.Stream(ctxA, make(chan exec.StreamInput))
	if err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	createSessions(t, m, "s")
	if _, _, err = m.AttachSession(pluginContext(), "s"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}

	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	inputB := make(chan exec.StreamInput)
	outB, err := m.Stream(ctxB, inputB)
	if err != nil {
		t.Fatalf("second Stream: %v", err)
	}

	cancelA()
	// The end of the replaced stream has been handled when it says so, or when it has ended the session.
	awaitWithin(t, func() bool { return ended.Load() == 1 || sessionsOf(m) == 0 }, "the end of the replaced stream to be handled")
	if n := sessionsOf(m); n != 1 {
		t.Fatalf("%d sessions after the replaced stream ended, want the one the newer stream serves", n)
	}

	// The session is served by the newer stream: its output goes there, and none went to the other.
	sc.session(t, "s").write(t, []byte("hello"))
	select {
	case output := <-outB:
		if output.SessionID != "s" || string(output.Data) != "hello" {
			t.Fatalf("the newer stream got %+v, want the session's output", output)
		}
	case <-time.After(repairWait):
		t.Fatal("the output of the session did not reach the newer stream")
	}
	select {
	case output := <-outA:
		t.Fatalf("the replaced stream still got output: %+v", output)
	default:
	}

	// The newer stream's end ends the session.
	close(inputB)
	awaitWithin(t, func() bool { return sessionsOf(m) == 0 }, "the session to end after the stream that serves it ended")
}

// The host's own path: the stream of the PluginClient, over gRPC, whose input the host closes (the
// RPC's CloseSend) or whose context it cancels. 25 rounds, because the server end of the RPC
// closes the input and cancels the context one after the other, and which one the manager sees first is
// not up to anybody.
func TestTheEndOfAStreamEndsItsSessionsOverGRPC(t *testing.T) {
	endings := streamEndings[:2] // "both at once" is what the server does by itself in the second
	for _, ending := range endings {
		t.Run(ending.name, func(t *testing.T) {
			for round := range 25 {
				if !t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
					sc := newScripted()
					m := sc.manager(t, scriptedOptions{})
					client := nativeExecClient(t, m, nil)

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					input := make(chan exec.StreamInput)
					if !ending.closesInput {
						defer close(input) // the sender of the client ends with its input
					}
					out, err := client.Stream(ctx, input)
					if err != nil {
						t.Fatalf("Stream: %v", err)
					}
					go func() {
						for range out { // the host reads what the plugin sends
						}
					}()
					for _, id := range []string{"a", "b"} {
						if _, err = client.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
							t.Fatalf("CreateSession(%q): %v", id, err)
						}
					}

					ending.end(cancel, input)

					awaitWithin(t, func() bool { return sessionsOf(m) == 0 },
						"the sessions to end after the host ended its stream: they are still running")
				}) {
					t.Fatalf("round %d broke the promise (above)", round)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The stream that carries the output
// ---------------------------------------------------------------------------

// A stream opened after another one ended gets all of the output. The sink of the first stream
// belongs to its context, which is done: it used to be the sink of the second one as well, and
// it delivers a frame or drops it, one in two, when its context is done.
func TestSecondStreamDeliversAllOutput(t *testing.T) {
	sc := newScripted()
	m := sc.manager(t, scriptedOptions{})

	ctx1, cancel1 := context.WithCancel(context.Background())
	if _, err := m.Stream(ctx1, make(chan exec.StreamInput)); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	cancel1() // the host's stream broke, or the host opened another one and let this one go

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	out, err := m.Stream(ctx2, make(chan exec.StreamInput))
	if err != nil {
		t.Fatalf("second Stream: %v", err)
	}
	createSessions(t, m, "s")
	if _, _, err = m.AttachSession(pluginContext(), "s"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}

	const frames, size = 2000, 64
	var want []byte
	for i := range frames {
		want = append(want, patterned(size, i)...)
	}
	process := sc.session(t, "s")
	go func() {
		for i := range frames {
			if _, err := process.terminal.InjectOutput(patterned(size, i)); err != nil {
				return
			}
		}
	}()

	var got []byte
	deadline := time.After(repairWait)
	for len(got) < len(want) {
		select {
		case output := <-out:
			if output.SessionID == "s" && output.Signal == exec.StreamSignalNone {
				got = append(got, output.Data...)
			}
		case <-deadline:
			t.Fatalf("the second stream received %d of %d bytes (%.0f%%) of a session's output",
				len(got), len(want), 100*float64(len(got))/float64(len(want)))
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the second stream received all %d bytes, but not the ones the session wrote", len(got))
	}
}

// A frame carries the bytes that were read, and does not hold on to the buffer they were read into: on
// macOS a terminal gives a kilobyte at a time, and a frame that kept the four-kilobyte buffer of the read
// would make three kilobytes of garbage for each kilobyte of output.
func TestAFrameDoesNotHoldTheBufferItWasReadInto(t *testing.T) {
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: sink})
	createSessions(t, m, "s")
	if _, _, err := m.AttachSession(nil, "s"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	sc.session(t, "s").write(t, patterned(100, 1))

	outputs := sink.WaitForOutputs(1, repairWait)
	if len(outputs) != 1 || len(outputs[0].Data) != 100 {
		t.Fatalf("got %d outputs (%+v), want one of 100 bytes", len(outputs), outputs)
	}
	if held := cap(outputs[0].Data); held >= exec.DefaultStreamBufferSize/2 {
		t.Errorf("a frame of 100 bytes holds %d bytes of memory, the size of the buffer of the read is %d", held, exec.DefaultStreamBufferSize)
	}
}

// ---------------------------------------------------------------------------
// The output of a session that ended before the host attached
// ---------------------------------------------------------------------------

// endUnattached makes a session write data and end by itself with nobody attached, and returns
// once its Close signal is out: the session has ended, and the output is whatever it left. The
// output is read (counted by the manager) before the process ends, so that what is built on these sessions does
// not depend on the wait for the tail, which has tests of its own.
func endUnattached(t testing.TB, sc *scripted, m *exec.Manager, closes *exectest.RecordingOutput, id string, data []byte, end error) {
	t.Helper()
	createSessions(t, m, id)
	process := sc.session(t, id)
	process.write(t, data)
	awaitWithin(t, func() bool {
		total, ok := exec.BufferTotal(m, id)
		return ok && total == uint64(len(data))
	}, "the manager to read what the process wrote")
	process.end(end)
	awaitWithin(t, func() bool { return closeSignalsFor(closes, id) >= 1 }, "the session to say goodbye")
}

func closeSignalsFor(sink *exectest.RecordingOutput, id string) int {
	n := 0
	for _, o := range sink.Outputs() {
		if o.SessionID == id && o.Signal == exec.StreamSignalClose {
			n++
		}
	}
	return n
}

func TestOutputOfASessionThatEndedBeforeAnyAttachIsKeptForTheFirstAttach(t *testing.T) {
	sc := newScripted()
	closes := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: closes})
	data := []byte("total 8\ndrwxr-xr-x  2 root root 4096 Nov 22 12:00 .\n")

	endUnattached(t, sc, m, closes, "short", data, nil)

	// The session has ended: it is not a live session, for any call but the attach.
	if _, err := m.GetSession(nil, "short"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("GetSession of an ended session: %v, want %v", err, exec.ErrSessionNotFound)
	}
	if n := sessionsOf(m); n != 0 {
		t.Errorf("ListSessions lists %d sessions after the only one ended", n)
	}
	if _, err := m.DetachSession(nil, "short"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("DetachSession of an ended session: %v, want %v", err, exec.ErrSessionNotFound)
	}
	if err := m.CloseSession(nil, "short"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("CloseSession of an ended session: %v, want %v", err, exec.ErrSessionNotFound)
	}

	session, output, err := m.AttachSession(nil, "short")
	if err != nil {
		t.Fatalf("the first attach to a session that ended before it: %v; want its output", err)
	}
	if !bytes.Equal(output, data) {
		t.Fatalf("the attach returned %q, want %q", output, data)
	}
	if session == nil || session.ID != "short" || !session.Attached {
		t.Errorf("the attach returned the session %+v, want session %q, attached", session, "short")
	}

	// The output waited for the first attach: the second finds nothing.
	if _, _, err = m.AttachSession(nil, "short"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("the second attach: %v, want %v", err, exec.ErrSessionNotFound)
	}
	if n := exec.RetainedSessions(m); n != 0 {
		t.Errorf("%d sessions still wait for a first attach, want 0", n)
	}
}

// A session that was attached, and detached, and ended while nobody was attached, keeps what
// was written after the detach (and what came before: an attach returns the whole buffer).
func TestOutputOfASessionThatEndedAfterADetachIsKeptForTheNextAttach(t *testing.T) {
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: sink})
	createSessions(t, m, "s")
	process := sc.session(t, "s")

	if _, _, err := m.AttachSession(nil, "s"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	process.write(t, []byte("while attached;"))
	if outputs := sink.WaitForOutputs(1, repairWait); len(outputs) != 1 {
		t.Fatalf("got %d outputs while attached, want 1", len(outputs))
	}
	if _, err := m.DetachSession(nil, "s"); err != nil {
		t.Fatalf("DetachSession: %v", err)
	}
	process.write(t, []byte(" while detached"))
	awaitWithin(t, func() bool {
		total, ok := exec.BufferTotal(m, "s")
		return ok && total == uint64(len("while attached; while detached"))
	}, "the manager to read what the process wrote after the detach")
	process.end(nil)
	awaitWithin(t, func() bool { return closeSignalsFor(sink, "s") == 1 }, "the session to say goodbye")

	_, output, err := m.AttachSession(nil, "s")
	if err != nil {
		t.Fatalf("the attach after a detach and an end: %v; want the output", err)
	}
	if string(output) != "while attached; while detached" {
		t.Fatalf("the attach returned %q", output)
	}
}

// What is kept is what nobody could have taken: a session that was attached when it ended
// delivered its output to the one attached (and the host has its Close signal); a session the host
// closed, or that ended because the manager did, was ended by the host.
func TestTheOutputOfASessionThatNobodyWaitsForIsNotKept(t *testing.T) {
	data := []byte("output")
	endings := []struct {
		name string
		end  func(t *testing.T, sc *scripted, m *exec.Manager, closes *exectest.RecordingOutput)
	}{
		{"attached when it ended", func(t *testing.T, sc *scripted, m *exec.Manager, closes *exectest.RecordingOutput) {
			if _, _, err := m.AttachSession(nil, "s"); err != nil {
				t.Fatalf("AttachSession: %v", err)
			}
			sc.session(t, "s").end(nil)
			awaitWithin(t, func() bool { return closeSignalsFor(closes, "s") == 1 }, "the session to say goodbye")
		}},
		{"closed by the host", func(t *testing.T, _ *scripted, m *exec.Manager, closes *exectest.RecordingOutput) {
			if err := m.CloseSession(nil, "s"); err != nil {
				t.Fatalf("CloseSession: %v", err)
			}
			awaitWithin(t, func() bool { return closeSignalsFor(closes, "s") == 1 }, "the session to say goodbye")
		}},
		{"ended by the manager closing", func(t *testing.T, _ *scripted, m *exec.Manager, _ *exectest.RecordingOutput) {
			m.Close()
		}},
		{"ended by its stream ending", func(t *testing.T, _ *scripted, m *exec.Manager, _ *exectest.RecordingOutput) {
			ctx, cancel := context.WithCancel(context.Background())
			if _, err := m.Stream(ctx, make(chan exec.StreamInput)); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			cancel()
			awaitWithin(t, func() bool { return sessionsOf(m) == 0 }, "the session to end with its stream")
		}},
	}
	for _, ending := range endings {
		t.Run(ending.name, func(t *testing.T) {
			sc := newScripted()
			closes := exectest.NewRecordingOutput()
			m := sc.manager(t, scriptedOptions{sink: closes})
			createSessions(t, m, "s")
			process := sc.session(t, "s")
			process.write(t, data)
			awaitWithin(t, func() bool {
				total, ok := exec.BufferTotal(m, "s")
				return ok && total == uint64(len(data))
			}, "the manager to read what the process wrote")

			ending.end(t, sc, m, closes)
			awaitWithin(t, func() bool { return sessionsOf(m) == 0 }, "the session to be gone")

			if _, _, err := m.AttachSession(nil, "s"); !errors.Is(err, exec.ErrSessionNotFound) {
				t.Errorf("an attach to a session that nobody could have waited for: %v, want %v", err, exec.ErrSessionNotFound)
			}
			if n := exec.RetainedSessions(m); n != 0 {
				t.Errorf("%d sessions wait for a first attach, want 0", n)
			}
		})
	}
}

// The output that waits for an attach is bounded: by the number of sessions it is kept for, and
// by the time it is kept. A host that never attaches must not make the plugin keep what it never asks for.
func TestTheOutputKeptForAnAttachIsBounded(t *testing.T) {
	t.Run("by the number of sessions: the oldest goes first", func(t *testing.T) {
		sc := newScripted()
		closes := exectest.NewRecordingOutput()
		m := sc.manager(t, scriptedOptions{sink: closes})
		const extra = 3
		total := exec.RetainedOutputLimit + extra
		for i := range total {
			endUnattached(t, sc, m, closes, fmt.Sprintf("s%d", i), fmt.Appendf(nil, "output of s%d", i), nil)
		}
		if n := exec.RetainedSessions(m); n != exec.RetainedOutputLimit {
			t.Fatalf("%d sessions' output is kept after %d ended, want the limit %d", n, total, exec.RetainedOutputLimit)
		}
		for i := range total {
			id := fmt.Sprintf("s%d", i)
			_, output, err := m.AttachSession(nil, id)
			if i < extra {
				if !errors.Is(err, exec.ErrSessionNotFound) {
					t.Errorf("%s, one of the %d oldest: attach = %v, want %v", id, extra, err, exec.ErrSessionNotFound)
				}
				continue
			}
			if err != nil || string(output) != "output of "+id {
				t.Errorf("%s: attach = (%q, %v), want its output", id, output, err)
			}
		}
	})

	t.Run("by time: it is gone after the time it is kept", func(t *testing.T) {
		sc := newScripted()
		clock := timeutil.NewFakeClock()
		closes := exectest.NewRecordingOutput()
		m := sc.manager(t, scriptedOptions{sink: closes, clock: clock})

		endUnattached(t, sc, m, closes, "old", []byte("old"), nil)
		clock.Advance(exec.RetainedOutputTTL - time.Second)
		endUnattached(t, sc, m, closes, "young", []byte("young"), nil)
		clock.Advance(2 * time.Second) // "old" is past its time, "young" is not

		if _, _, err := m.AttachSession(nil, "old"); !errors.Is(err, exec.ErrSessionNotFound) {
			t.Errorf("an attach after the output's time: %v, want %v", err, exec.ErrSessionNotFound)
		}
		if _, output, err := m.AttachSession(nil, "young"); err != nil || string(output) != "young" {
			t.Errorf("an attach within the output's time: (%q, %v), want its output", output, err)
		}

		// An output that nobody asked for goes when the next one comes, not only when its own attach does.
		endUnattached(t, sc, m, closes, "next", []byte("next"), nil)
		clock.Advance(exec.RetainedOutputTTL + time.Second)
		endUnattached(t, sc, m, closes, "last", []byte("last"), nil)
		if n := exec.RetainedSessions(m); n != 1 {
			t.Errorf("%d outputs are kept after all but the last were past their time, want 1", n)
		}
	})
}

// What a session left is the manager's, and goes with it: a closed manager has no sessions, and no output
// for one that comes to attach.
func TestClosingTheManagerDropsTheOutputItKept(t *testing.T) {
	sc := newScripted()
	closes := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: closes})
	endUnattached(t, sc, m, closes, "left", []byte("output"), nil)
	if n := exec.RetainedSessions(m); n != 1 {
		t.Fatalf("%d outputs are kept after a session ended, want 1", n)
	}

	m.Close()

	if n := exec.RetainedSessions(m); n != 0 {
		t.Errorf("%d outputs are kept after Close, want 0", n)
	}
	if _, _, err := m.AttachSession(nil, "left"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("an attach after Close found %v, want %v", err, exec.ErrSessionNotFound)
	}
}

// An ID is free once its session has ended, and a session started with it must not meet what the
// session before it left.
func TestASessionStartedWithTheIDOfAnEndedOneDoesNotGetItsOutput(t *testing.T) {
	sc := newScripted()
	closes := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: closes})
	endUnattached(t, sc, m, closes, "reused", []byte("the old session's output"), nil)

	createSessions(t, m, "reused")
	if n := exec.RetainedSessions(m); n != 0 {
		t.Errorf("%d outputs are kept after an ID was taken again, want 0", n)
	}
	session, output, err := m.AttachSession(nil, "reused")
	if err != nil || len(output) != 0 || session == nil || !session.Attached {
		t.Fatalf("an attach to the new session: (%+v, %q, %v), want the live session and no output", session, output, err)
	}
}

// Two attaches to the same ended session: the output is for the first, and the first is one of them.
func TestTwoAttachesToAnEndedSessionOneGetsTheOutput(t *testing.T) {
	sc := newScripted()
	closes := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: closes})
	endUnattached(t, sc, m, closes, "s", []byte("output"), nil)

	const callers = 8
	var wins, misses atomic.Int64
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-begin
			_, output, err := m.AttachSession(nil, "s")
			switch {
			case err == nil && string(output) == "output":
				wins.Add(1)
			case errors.Is(err, exec.ErrSessionNotFound):
				misses.Add(1)
			default:
				t.Errorf("an attach to an ended session returned (%q, %v)", output, err)
			}
		})
	}
	close(begin)
	wg.Wait()
	if wins.Load() != 1 || misses.Load() != callers-1 {
		t.Fatalf("%d attaches got the output and %d found nothing, want 1 and %d", wins.Load(), misses.Load(), callers-1)
	}
}

// An attach that found a session and lost it to the end must not take the output of the session that took
// its ID in the meantime: what it finds is the output of the session it found, or nothing. (Whoever keys
// state on an ID may reuse it once the session has ended: the SessionOptions say that a restart needs a new one,
// and a host that does not follows the rule of the manager: an ID is free once its session has ended.)
func TestAnAttachThatFoundASessionDoesNotTakeTheOutputOfItsSuccessor(t *testing.T) {
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	var held atomic.Pointer[gate]
	m := sc.manager(t, scriptedOptions{sink: sink, hook: func(msg string) {
		if g := held.Load(); g != nil && msg == "attach found the session" {
			g.hold()
		}
	}})
	attachHeld := newGate(t) // registered after the manager: a failing test lets the attach go before the manager closes
	held.Store(attachHeld)

	firstData, secondData := []byte("the output of the first session"), []byte("the output of the one that took its ID")
	createSessions(t, m, "s")
	first := sc.session(t, "s")
	first.write(t, firstData)
	awaitWithin(t, func() bool {
		total, ok := exec.BufferTotal(m, "s")
		return ok && total == uint64(len(firstData))
	}, "the manager to read what the first session wrote")

	type attachResult struct {
		output []byte
		err    error
	}
	got := make(chan attachResult, 1)
	go func() {
		_, output, err := m.AttachSession(nil, "s")
		got <- attachResult{output, err}
	}()
	awaitClosed(t, attachHeld.reached, "the attach to find the first session")
	first.end(nil)
	awaitWithin(t, func() bool { return closeSignalsFor(sink, "s") == 1 }, "the first session to end")

	createSessions(t, m, "s") // the ID is free: the new session drops what the first one left
	second := sc.session(t, "s")
	second.write(t, secondData)
	awaitWithin(t, func() bool {
		total, ok := exec.BufferTotal(m, "s")
		return ok && total == uint64(len(secondData))
	}, "the manager to read what the second session wrote")
	second.end(nil)
	awaitWithin(t, func() bool { return closeSignalsFor(sink, "s") == 2 }, "the second session to end")
	attachHeld.release()

	select {
	case result := <-got:
		if !errors.Is(result.err, exec.ErrSessionNotFound) {
			t.Fatalf("the attach that found the first session returned (%q, %v): it must not get the output of the session that took its ID",
				result.output, result.err)
		}
	case <-time.After(mustHappen):
		t.Fatal("the attach did not return")
	}
	if _, output, err := m.AttachSession(nil, "s"); err != nil || !bytes.Equal(output, secondData) {
		t.Errorf("the first attach to the second session returned (%q, %v), want its own output", output, err)
	}
}

// A close can find a session and lose it to the end before it has stopped it. The session then ended by itself
// and left its output, and the close says that the session is gone: it is never both a success, which promises that
// the session keeps nothing, and a session that kept its output. Both orders are forced.
func TestACloseThatFoundASessionBeforeItEndedSaysSoAndTheOutputIsKept(t *testing.T) {
	for _, order := range []struct {
		name         string
		endInBetween bool
	}{
		{"the process ends between the lookup and the stop", true},
		{"the close comes first", false},
	} {
		t.Run(order.name, func(t *testing.T) {
			sc := newScripted()
			sink := exectest.NewRecordingOutput()
			var held atomic.Pointer[gate]
			m := sc.manager(t, scriptedOptions{sink: sink, hook: func(msg string) {
				if g := held.Load(); g != nil && msg == "close found the session" {
					g.hold()
				}
			}})
			closeHeld := newGate(t) // registered after the manager: a failing test lets the close go before the manager closes
			if order.endInBetween {
				held.Store(closeHeld)
			}
			data := []byte("what the process wrote")
			createSessions(t, m, "s")
			process := sc.session(t, "s")
			process.write(t, data)
			awaitWithin(t, func() bool {
				total, ok := exec.BufferTotal(m, "s")
				return ok && total == uint64(len(data))
			}, "the manager to read what the process wrote")

			closed := make(chan error, 1)
			go func() { closed <- m.CloseSession(nil, "s") }()
			if order.endInBetween {
				select {
				case <-closeHeld.reached: // the close has the session, and has not stopped it
				case <-time.After(repairWait):
					t.Fatal("the close did not find the session")
				}
				process.end(nil)
				awaitWithin(t, func() bool { return closeSignalsFor(sink, "s") == 1 }, "the session to end")
				closeHeld.release()
			}
			var closeErr error
			select {
			case closeErr = <-closed:
			case <-time.After(mustHappen):
				t.Fatal("CloseSession did not return")
			}

			_, output, attachErr := m.AttachSession(nil, "s")
			if order.endInBetween {
				if !errors.Is(closeErr, exec.ErrSessionNotFound) {
					t.Errorf("a close that lost its session to the end returned %v, want %v: the session ended by itself", closeErr, exec.ErrSessionNotFound)
				}
				if attachErr != nil || !bytes.Equal(output, data) {
					t.Errorf("the session that ended by itself kept (%q, %v) for its first attach, want its output", output, attachErr)
				}
				return
			}
			if closeErr != nil {
				t.Errorf("CloseSession of a live session: %v", closeErr)
			}
			if !errors.Is(attachErr, exec.ErrSessionNotFound) {
				t.Errorf("a session that the host closed kept (%q, %v) for an attach, want nothing", output, attachErr)
			}
		})
	}
}

// A pipe-backed terminal has no kernel to keep what a process wrote, and plugins test with it: it must end a session
// as a real terminal does, with what the process wrote last delivered before the Close signal, however soon the
// process is reported over. Nothing here waits for the manager to read before it ends the session.
func TestAFakeTerminalSessionDeliversItsTailBeforeClose(t *testing.T) {
	const runs = 200
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	m := sc.manager(t, scriptedOptions{sink: sink})

	var short, lost int
	for run := range runs {
		id := fmt.Sprintf("fake-%d", run)
		data := patterned(100+run*13%3000, run)
		createSessions(t, m, id)
		if _, _, err := m.AttachSession(nil, id); err != nil {
			t.Fatalf("AttachSession: %v", err)
		}
		process := sc.session(t, id)
		process.write(t, data)
		process.end(nil)
		awaitWithin(t, func() bool { return closeSignalsFor(sink, id) == 1 }, "the session to say goodbye")

		var got []byte
		for _, output := range sink.Outputs() {
			if output.SessionID != id {
				continue
			}
			if output.Signal == exec.StreamSignalClose {
				break
			}
			if output.Signal == exec.StreamSignalNone {
				got = append(got, output.Data...)
			}
		}
		if !bytes.Equal(got, data) {
			short++
			lost += len(data) - len(got)
		}
	}
	if short > 0 {
		t.Errorf("%d of %d sessions on a pipe-backed terminal lost the end of their output (%d bytes in all): the Close signal came before it was read", short, runs, lost)
	}
}

// An attach can find a session and lose it before it has taken it: the session ends between the two steps.
// The test stops the attach there, ends the session, and lets the attach go on. It must find what the
// session left, and take it: the output is not left where nobody asks for it, and is not taken twice.
func TestAnAttachThatFoundASessionBeforeItEndedGetsWhatItLeft(t *testing.T) {
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	var held atomic.Pointer[gate]
	m := sc.manager(t, scriptedOptions{sink: sink, hook: func(msg string) {
		if g := held.Load(); g != nil && msg == "attach found the session" {
			g.hold()
		}
	}})
	attachHeld := newGate(t) // registered after the manager: a failing test lets the attach go before the manager closes
	held.Store(attachHeld)

	data := []byte("the last of the output")
	createSessions(t, m, "s")
	process := sc.session(t, "s")
	process.write(t, data)
	awaitWithin(t, func() bool {
		total, ok := exec.BufferTotal(m, "s")
		return ok && total == uint64(len(data))
	}, "the manager to read what the process wrote")

	type attachResult struct {
		output []byte
		err    error
	}
	result := make(chan attachResult, 1)
	go func() {
		_, output, err := m.AttachSession(nil, "s")
		result <- attachResult{output, err}
	}()
	awaitClosed(t, attachHeld.reached, "the attach to find the session") // it has the session, and has not taken it
	process.end(nil)
	awaitWithin(t, func() bool { return closeSignalsFor(sink, "s") == 1 }, "the session to end")
	attachHeld.release()

	select {
	case got := <-result:
		if got.err != nil || !bytes.Equal(got.output, data) {
			t.Fatalf("an attach that lost its session to the end returned (%q, %v), want what the session left", got.output, got.err)
		}
	case <-time.After(mustHappen):
		t.Fatal("the attach did not return")
	}
	if n := exec.RetainedSessions(m); n != 0 {
		t.Errorf("%d outputs are kept after the attach took the output of the session it lost", n)
	}
	if _, _, err := m.AttachSession(nil, "s"); !errors.Is(err, exec.ErrSessionNotFound) {
		t.Errorf("a second attach found %v, want %v: the output was taken once", err, exec.ErrSessionNotFound)
	}
}

// The attach that arrives while the session ends. The output of a session that nobody attached to is
// there for whoever asks, whenever he asks: from the session while it runs, or from what it left
// when it ended. The session must never be both gone from the manager and not yet kept: no attach
// may find nothing, whichever side of the end it falls on, and the output is taken once.
func TestAnAttachRacingTheEndOfASessionNeverMissesItsOutput(t *testing.T) {
	sc := newScripted()
	sink := exectest.NewRecordingOutput()
	var live, left atomic.Int64
	m := sc.manager(t, scriptedOptions{sink: sink, hook: func(msg string) {
		switch msg {
		case "session attached":
			live.Add(1)
		case "output of an ended session attached":
			left.Add(1)
		}
	}})

	const workers, perWorker = 8, 150
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for n := range perWorker {
				id := fmt.Sprintf("w%d-%d", w, n)
				data := patterned(40+n%50, n)
				if _, err := m.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
					t.Errorf("CreateSession(%q): %v", id, err)
					return
				}
				process := sc.lookup(id)
				if _, err := process.terminal.InjectOutput(data); err != nil {
					t.Errorf("%s: writing to the terminal: %v", id, err)
					return
				}
				if !pollWithin(func() bool {
					total, ok := exec.BufferTotal(m, id)
					return ok && total == uint64(len(data))
				}) {
					t.Errorf("%s: the manager did not read what the process wrote", id)
					return
				}

				// The attach and the end start together, and which goes first varies, so that the attach
				// falls before the end, after it, and between the steps of it.
				attach := func() {
					for range n % 9 {
						runtime.Gosched()
					}
					session, output, err := m.AttachSession(nil, id)
					switch {
					case err != nil:
						t.Errorf("%s: an attach racing the end of the session found %v", id, err)
					case !bytes.Equal(output, data):
						t.Errorf("%s: the attach returned %d bytes, want the %d the process wrote", id, len(output), len(data))
					case session == nil || !session.Attached:
						t.Errorf("%s: the attach returned %+v", id, session)
					}
				}
				attached := make(chan struct{})
				if n%2 == 0 {
					go func() { defer close(attached); attach() }()
					process.end(nil)
				} else {
					process.end(nil)
					go func() { defer close(attached); attach() }()
				}
				<-attached
				if !pollWithin(func() bool { return closeSignalsFor(sink, id) == 1 }) {
					t.Errorf("%s: the session did not say goodbye", id)
					return
				}
				if _, _, err := m.AttachSession(nil, id); !errors.Is(err, exec.ErrSessionNotFound) {
					t.Errorf("%s: a second attach found %v, want %v: the output is taken once", id, err, exec.ErrSessionNotFound)
				}
			}
		})
	}
	wg.Wait()
	if n := exec.RetainedSessions(m); n != 0 {
		t.Errorf("%d outputs are still kept at the end; every one was taken", n)
	}
	t.Logf("%d attaches found the session running, %d found what it had left", live.Load(), left.Load())
}
