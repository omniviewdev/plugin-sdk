//go:build unix

package exec_test

// Stress of the ends of sessions on real terminals.
//
// Several workers run sessions on one manager and one stream, as the host does: each session writes its
// output through a real terminal and ends in one of the ways a session ends, while other goroutines look at
// the manager. Each worker holds its own sessions to what the lane promises for that way of ending, and
// when the workers have stopped the test checks what must be true of a manager at rest.
//
//   - a session that ends by itself while the host is attached delivers every byte its process wrote,
//     in order, and then says goodbye once; an error it ends with comes before the goodbye;
//   - one that ends before the host attaches gives its output to the first attach, all of it, and to no
//     second one;
//   - one that ends while the host is detached gives it the same way;
//   - one that the host closes delivers a prefix of what its process wrote, and nothing after the goodbye;
//   - one whose process ends at the moment the host closes it does both at once without a hang: a prefix,
//     one goodbye, nothing kept;
//   - at rest: no session, no output kept for an attach, no frame that follows a goodbye or has no
//     session to belong to, no descriptor and no goroutine left over from the sessions.
//
// The run lasts as long as EXEC_CLOSE_STRESS_SECONDS says (a number, fractions allowed), 1.5 seconds by
// default so that the suite stays fast. The runs that back the claim in the change that introduced it are:
//
//	EXEC_CLOSE_STRESS_SECONDS=30 go test ./pkg/v1/exec -run TestStressTheEndsOfSessions -count=1
//	EXEC_CLOSE_STRESS_SECONDS=30 go test -race ./pkg/v1/exec -run TestStressTheEndsOfSessions -count=1
//
// The manager runs on a clock that does not move, so no bound of its is part of what is checked here
// (the bound of the wait for a tail is the subject of TestTheWaitForTheTailIsBounded).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// mailbox is what the stream delivered for one session.
type mailbox struct {
	mu       sync.Mutex
	data     []byte
	signals  []exec.StreamSignal // the signals that came before the goodbye
	closes   int
	late     int           // frames that came after the goodbye
	goodbye  chan struct{} // closed when the goodbye comes
	closedAt int           // how many bytes of data had come when the goodbye did
}

func newMailbox() *mailbox { return &mailbox{goodbye: make(chan struct{})} }

func (b *mailbox) put(o exec.StreamOutput) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closes > 0:
		b.late++
	case o.Signal == exec.StreamSignalClose:
		b.closes++
		b.closedAt = len(b.data)
		close(b.goodbye)
	case o.Signal == exec.StreamSignalNone:
		b.data = append(b.data, o.Data...)
	default:
		b.signals = append(b.signals, o.Signal)
	}
}

func (b *mailbox) received() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

// said returns what the session delivered, once it has said goodbye.
func (b *mailbox) said() (data []byte, signals []exec.StreamSignal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), append([]exec.StreamSignal(nil), b.signals...)
}

// mailboxes routes the frames of one stream to the sessions they belong to.
type mailboxes struct {
	mu      sync.Mutex
	boxes   map[string]*mailbox
	strays  atomic.Int64 // frames for a session nobody has made
	started int
}

func (m *mailboxes) make(id string) *mailbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	box := newMailbox()
	m.boxes[id] = box
	m.started++
	return box
}

func (m *mailboxes) route(out <-chan exec.StreamOutput) {
	for o := range out {
		m.mu.Lock()
		box := m.boxes[o.SessionID]
		m.mu.Unlock()
		if box == nil {
			m.strays.Add(1)
			continue
		}
		box.put(o)
	}
}

func openDescriptors(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("no way to count descriptors here: %v", err)
	}
	return len(entries)
}

func TestStressTheEndsOfSessions(t *testing.T) {
	duration := stressDuration(t)
	const workers = 8

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
	awaitClosed(t, opened, "the server to open the stream")
	boxes := &mailboxes{boxes: make(map[string]*mailbox)}
	routed := make(chan struct{})
	go func() {
		defer close(routed)
		boxes.route(out)
	}()

	// One session before the baseline, so that what the manager and the stream keep for good is counted in it.
	warm := boxes.make("warm")
	p.expectNow("warm", processScript{output: patterned(100, 0)})
	if _, err = client.CreateSession(pluginContext(), exec.SessionOptions{ID: "warm"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !awaitGoodbye(t, warm, "warm") {
		t.FailNow()
	}
	if _, _, err = client.AttachSession(pluginContext(), "warm"); err != nil {
		t.Fatalf("AttachSession: %v", err)
	}
	runtime.GC()
	baselineGoroutines, baselineDescriptors := runtime.NumGoroutine(), openDescriptors(t)

	var stop atomic.Bool
	var ran [5]atomic.Int64
	var bytesWritten atomic.Int64
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 17))
			for n := 0; !stop.Load(); n++ {
				id := fmt.Sprintf("w%d-%d", w, n)
				kind := rng.IntN(22)
				switch {
				case kind < 9:
					ran[0].Add(1)
					bytesWritten.Add(int64(endAttached(t, client, p, boxes, id, rng)))
				case kind < 14:
					ran[1].Add(1)
					bytesWritten.Add(int64(endBeforeAttach(t, client, p, boxes, id, rng)))
				case kind < 17:
					ran[2].Add(1)
					bytesWritten.Add(int64(endWhileDetached(t, client, p, boxes, id, rng)))
				case kind < 20:
					ran[3].Add(1)
					closedByTheHost(t, client, p, boxes, id, rng)
				default:
					ran[4].Add(1)
					endRacingTheClose(t, client, p, boxes, id, rng)
				}
				if t.Failed() {
					return
				}
			}
		})
	}
	for range 2 {
		wg.Go(func() { // the host looking at the manager
			for !stop.Load() {
				if _, listErr := m.ListSessions(nil); listErr != nil {
					t.Errorf("ListSessions: %v", listErr)
					return
				}
				if _, getErr := m.GetSession(nil, "w0-0"); getErr != nil && !errors.Is(getErr, exec.ErrSessionNotFound) {
					t.Errorf("GetSession: %v", getErr)
					return
				}
				runtime.Gosched()
			}
		})
	}
	time.Sleep(duration)
	stop.Store(true)
	wg.Wait()

	// At rest.
	awaitWithin(t, func() bool { return sessionsOf(m) == 0 }, "every session to be gone")
	if kept := exec.RetainedSessions(m); kept != 0 {
		t.Errorf("the output of %d ended sessions is still kept for an attach: every attach to one took it", kept)
	}
	boxes.mu.Lock()
	var late int
	var noGoodbye []string
	for id, box := range boxes.boxes {
		box.mu.Lock()
		late += box.late
		if box.closes != 1 {
			noGoodbye = append(noGoodbye, fmt.Sprintf("%s: %d goodbyes", id, box.closes))
		}
		box.mu.Unlock()
	}
	started := boxes.started
	boxes.mu.Unlock()
	if len(noGoodbye) > 0 {
		t.Errorf("sessions that did not say goodbye exactly once: %v", noGoodbye[:min(5, len(noGoodbye))])
	}
	if late > 0 {
		t.Errorf("%d frames came after their session's goodbye", late)
	}
	if strays := boxes.strays.Load(); strays > 0 {
		t.Errorf("%d frames came for sessions nobody made", strays)
	}
	awaitWithin(t, func() bool { return len(sessionGoroutines()) == 0 }, "the goroutines of the sessions to be gone")
	awaitWithin(t, func() bool { runtime.GC(); return openDescriptors(t) <= baselineDescriptors },
		fmt.Sprintf("the descriptors of the sessions to be closed (baseline %d, now %d)", baselineDescriptors, openDescriptors(t)))
	awaitWithin(t, func() bool { return runtime.NumGoroutine() <= baselineGoroutines },
		fmt.Sprintf("the goroutines of the sessions to be gone (baseline %d, now %d)", baselineGoroutines, runtime.NumGoroutine()))

	cancel()
	<-routed
	t.Logf("%d sessions in %s (%d ended with the host attached, %d before it attached, %d while it was detached, %d closed by it, %d ended as it closed them), %d KiB written",
		started, duration, ran[0].Load(), ran[1].Load(), ran[2].Load(), ran[3].Load(), ran[4].Load(), bytesWritten.Load()>>10)
}

// A worker's sessions. Each returns the number of bytes its process wrote, and reports what it finds with Errorf.

func randomEnd(rng *rand.Rand) (end error, wantsError bool) {
	switch rng.IntN(4) {
	case 0:
		return &exec.ExecError{Err: errors.New("exit status 1"), Title: "Exited", Message: "The command failed."}, true
	case 1:
		return errors.New("connection reset"), true
	default:
		return nil, false
	}
}

func createAndAttach(t testing.TB, client *exec.PluginClient, id string) (head []byte, ok bool) {
	t.Helper()
	if _, err := client.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
		t.Errorf("%s: CreateSession: %v", id, err)
		return nil, false
	}
	_, head, err := client.AttachSession(pluginContext(), id)
	if err != nil {
		t.Errorf("%s: AttachSession: %v", id, err)
		return nil, false
	}
	return head, true
}

func awaitGoodbye(t testing.TB, box *mailbox, id string) bool {
	t.Helper()
	select {
	case <-box.goodbye:
		return true
	case <-time.After(mustHappen):
		t.Errorf("%s: no goodbye within %v (%d bytes came)", id, mustHappen, box.received())
		return false
	}
}

// endAttached: the host is attached from the start and the process writes its output and is over at once.
func endAttached(t testing.TB, client *exec.PluginClient, p *process, boxes *mailboxes, id string, rng *rand.Rand) int {
	payload := patterned(rng.IntN(300<<10), rng.IntN(251))
	end, wantsError := randomEnd(rng)
	box := boxes.make(id)
	start := p.expect(id, processScript{output: payload, chunk: 1 + rng.IntN(8<<10), end: end})
	head, ok := createAndAttach(t, client, id)
	if !ok {
		return 0
	}
	start()
	if !awaitGoodbye(t, box, id) {
		return len(payload)
	}
	data, signals := box.said()
	if got := append(head, data...); !bytes.Equal(got, payload) {
		t.Errorf("%s: %d of the %d bytes the process wrote came before the goodbye", id, len(got), len(payload))
	}
	if wantsError != (len(signals) == 1 && signals[0] == exec.StreamSignalError) || len(signals) > 1 {
		t.Errorf("%s: the signals before the goodbye were %v, the process ended with the error %v", id, signals, end)
	}
	return len(payload)
}

// endBeforeAttach: the process is over before the host has attached. The first attach has all of its output.
func endBeforeAttach(t testing.TB, client *exec.PluginClient, p *process, boxes *mailboxes, id string, rng *rand.Rand) int {
	payload := patterned(rng.IntN(3<<10), rng.IntN(251))
	end, _ := randomEnd(rng)
	box := boxes.make(id)
	p.expectNow(id, processScript{output: payload, chunk: 1 + rng.IntN(2<<10), end: end})
	if _, err := client.CreateSession(pluginContext(), exec.SessionOptions{ID: id}); err != nil {
		t.Errorf("%s: CreateSession: %v", id, err)
		return 0
	}
	if !awaitGoodbye(t, box, id) {
		return len(payload)
	}
	if data, _ := box.said(); len(data) != 0 {
		t.Errorf("%s: %d bytes were delivered to a host that was not attached", id, len(data))
	}
	_, output, err := client.AttachSession(pluginContext(), id)
	if err != nil {
		t.Errorf("%s: the first attach after the end: %v", id, err)
		return len(payload)
	}
	if !bytes.Equal(output, payload) {
		t.Errorf("%s: the first attach after the end returned %d of the %d bytes the process wrote", id, len(output), len(payload))
	}
	if _, _, err = client.AttachSession(pluginContext(), id); err == nil {
		t.Errorf("%s: a second attach after the end found the session", id)
	}
	return len(payload)
}

// endWhileDetached: the host attaches, takes the first of the output, and detaches; the rest is written and the
// process ends with nobody attached. The next attach has all of it: the host asks for the whole of the buffer.
func endWhileDetached(t testing.TB, client *exec.PluginClient, p *process, boxes *mailboxes, id string, rng *rand.Rand) int {
	payload := patterned(2+rng.IntN(3<<10), rng.IntN(251))
	split := 1 + rng.IntN(len(payload)-1)
	box := boxes.make(id)
	resume := make(chan struct{})
	start := p.expect(id, processScript{output: payload, pauseAt: split, resume: resume})
	if _, ok := createAndAttach(t, client, id); !ok {
		close(resume)
		return 0
	}
	start()
	if !pollWithin(func() bool { return box.received() >= split }) {
		t.Errorf("%s: the first %d bytes did not arrive (%d did)", id, split, box.received())
		close(resume)
		return len(payload)
	}
	if _, err := client.DetachSession(pluginContext(), id); err != nil {
		t.Errorf("%s: DetachSession: %v", id, err)
	}
	close(resume)
	if !awaitGoodbye(t, box, id) {
		return len(payload)
	}
	if data, _ := box.said(); len(data) != split {
		t.Errorf("%s: %d bytes were delivered, want the %d before the detach", id, len(data), split)
	}
	_, output, err := client.AttachSession(pluginContext(), id)
	if err != nil {
		t.Errorf("%s: the attach after the end: %v", id, err)
		return len(payload)
	}
	if !bytes.Equal(output, payload) {
		t.Errorf("%s: the attach after the end returned %d of the %d bytes the process wrote", id, len(output), len(payload))
	}
	return len(payload)
}

// closedByTheHost: the process is writing when the host closes the session, at the point where it happens to be.
func closedByTheHost(t testing.TB, client *exec.PluginClient, p *process, boxes *mailboxes, id string, rng *rand.Rand) {
	payload := patterned(64<<10, rng.IntN(251))
	pause := 1 + rng.IntN(len(payload)-1)
	box := boxes.make(id)
	resume := make(chan struct{})
	start := p.expect(id, processScript{output: payload, chunk: 1 + rng.IntN(4<<10), pauseAt: pause, resume: resume})
	if _, ok := createAndAttach(t, client, id); !ok {
		close(resume)
		return
	}
	start()
	if !pollWithin(func() bool { return box.received() > 0 }) {
		t.Errorf("%s: no output came before the host closed the session", id)
	}
	if err := client.CloseSession(pluginContext(), id); err != nil {
		t.Errorf("%s: CloseSession: %v", id, err)
	}
	close(resume) // the process, if it is still waiting, finds its terminal closed
	if !awaitGoodbye(t, box, id) {
		return
	}
	data, _ := box.said()
	if !bytes.HasPrefix(payload, data) || len(data) > pause {
		t.Errorf("%s: a session that the host closed delivered %d bytes that are not a prefix of what the process wrote, up to %d", id, len(data), pause)
	}
	if _, _, err := client.AttachSession(pluginContext(), id); err == nil {
		t.Errorf("%s: an attach to a session the host closed found it, or what it left", id)
	}
}

// endRacingTheClose: the process is over at the moment the host closes the session. Whichever wins, the session ends
// once, delivers a prefix of what the process wrote, and keeps nothing for an attach.
func endRacingTheClose(t testing.TB, client *exec.PluginClient, p *process, boxes *mailboxes, id string, rng *rand.Rand) {
	payload := patterned(1+rng.IntN(8<<10), rng.IntN(251))
	yields := rng.IntN(9)
	box := boxes.make(id)
	start := p.expect(id, processScript{output: payload, chunk: 1 + rng.IntN(2<<10)})
	if _, ok := createAndAttach(t, client, id); !ok {
		return
	}
	closed := make(chan error, 1)
	go func() {
		for range yields {
			runtime.Gosched()
		}
		closed <- client.CloseSession(pluginContext(), id)
	}()
	start()
	if err := <-closed; err != nil && grpcCode(err) != codes.NotFound {
		t.Errorf("%s: CloseSession: %v", id, err) // NotFound: the process was over first, and the session with it
	}
	if !awaitGoodbye(t, box, id) {
		return
	}
	if data, _ := box.said(); !bytes.HasPrefix(payload, data) {
		t.Errorf("%s: %d bytes were delivered that are not the beginning of what the process wrote", id, len(data))
	}
	if _, _, err := client.AttachSession(pluginContext(), id); err == nil {
		t.Errorf("%s: an attach to a session that was attached when it ended found it, or what it left", id)
	}
}
