package logs_test

// Manager.Wait and Manager.Close both stop the manager taking sessions, and a start that races
// either is admitted and waited for, or refused: never admitted after the wait is over. Wait was
// public and, unlike Close, did not take the lock that admits a session before it waited, so a
// start that raced it added to the manager's WaitGroup while Wait could be returning.

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/utils/timeutil"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/logs/logtest"
)

// finiteManager returns a manager whose sessions read one line and end by themselves.
func finiteManager(t *testing.T) (*logs.Manager, *logtest.RecordingOutput) {
	t.Helper()
	output := logtest.NewRecordingOutput()
	mgr := logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Handler: func(*types.PluginContext, logs.LogStreamRequest) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("line\n")), nil
			}},
		},
		Sink:  output,
		Clock: timeutil.NewInstantFakeClock(),
	})
	t.Cleanup(mgr.Close)
	return mgr, output
}

func endedSessions(output *logtest.RecordingOutput) (ended int) {
	for _, event := range output.Events() {
		if event.Type == logs.StreamEventStreamEnded {
			ended++
		}
	}
	return ended
}

func startFinite(mgr *logs.Manager, id string) (*logs.LogSession, error) {
	ctx := types.NewPluginContext(context.Background(), "log", nil, nil, nil)
	return mgr.CreateResourceSession(ctx, logs.CreateSessionOptions{ResourceKey: "pod", ResourceID: id})
}

func startLegacy(mgr *logs.Manager) (*logs.LogSession, error) {
	return mgr.CreateSession(types.NewPluginContext(context.Background(), "log", nil, nil, nil), logs.CreateSessionOptions{})
}

func isRefusal(err error) bool { return resourcev2.ErrorCode(err) == "CONNECTION_ERROR" }

// Once Wait has begun, the manager takes no session: a start is refused with the refusal that
// Close's starts get, and Wait waited for the session it held.
func TestWaitStopsTheManagerTakingSessions(t *testing.T) {
	mgr, output := finiteManager(t)
	if _, err := startFinite(mgr, "first"); err != nil {
		t.Fatal(err)
	}
	mgr.Wait()
	if ended := endedSessions(output); ended != 1 {
		t.Fatalf("Wait returned with %d of the manager's 1 sessions ended", ended)
	}

	if session, err := startFinite(mgr, "late"); !isRefusal(err) || session != nil {
		t.Errorf("a resource start after Wait returned (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}
	if session, err := startLegacy(mgr); !isRefusal(err) || session != nil {
		t.Errorf("a start after Wait returned (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}

	mgr.Wait() // it may be called again
	var concurrent sync.WaitGroup
	for range 4 {
		concurrent.Go(mgr.Wait) // and by many
	}
	concurrent.Wait()
}

func TestCloseStopsTheManagerTakingSessions(t *testing.T) {
	mgr, _ := finiteManager(t)
	mgr.Close()
	if session, err := startFinite(mgr, "late"); !isRefusal(err) || session != nil {
		t.Errorf("a resource start after Close returned (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}
	if session, err := startLegacy(mgr); !isRefusal(err) || session != nil {
		t.Errorf("a start after Close returned (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}
	mgr.Close() // and it may be called again
}

// Starts race Wait: whichever way each falls, an admitted session ended by the time Wait
// returned, a start that began after it returned was refused, and nothing is refused while the
// manager is open.
func TestStartsRacingWaitAreWaitedForOrRefused(t *testing.T) {
	for round := range 80 {
		mgr, output := finiteManager(t)
		var (
			waiting, waited atomic.Bool
			endedAtWait     atomic.Int64
			admitted        atomic.Int64
			fresh           atomic.Int64
			stop            atomic.Bool
			callers         sync.WaitGroup
		)
		for range 4 {
			callers.Go(func() {
				for !stop.Load() {
					afterWait := waited.Load()
					id := fmt.Sprintf("round-%d-%d", round, fresh.Add(1))
					_, err := startFinite(mgr, id)
					switch {
					case err == nil:
						if afterWait {
							t.Errorf("round %d: a start that began after Wait returned was admitted", round)
						}
						admitted.Add(1)
					case isRefusal(err):
						if !waiting.Load() {
							t.Errorf("round %d: a start was refused while the manager was open", round)
						}
					default:
						t.Errorf("round %d: a start failed with %v", round, err)
					}
				}
			})
		}
		rng := rand.New(rand.NewPCG(uint64(round), 1))
		time.Sleep(time.Duration(rng.Int64N(int64(2 * time.Millisecond))))
		waiting.Store(true)
		mgr.Wait()
		endedAtWait.Store(int64(endedSessions(output)))
		waited.Store(true)
		time.Sleep(time.Duration(rng.Int64N(int64(2 * time.Millisecond)))) // the callers go on: every start is refused now
		stop.Store(true)
		callers.Wait()

		if got, want := endedAtWait.Load(), admitted.Load(); got != want {
			t.Fatalf("round %d: %d sessions had ended when Wait returned, %d were admitted: a session was admitted after the wait was over, or lost", round, got, want)
		}
	}
}

// holdSink takes lines and events, except that it does not take a line until it is released: a
// session that is delivering one cannot end meanwhile, whatever its context says. reached is
// closed when the first line arrives.
type holdSink struct {
	*logtest.RecordingOutput

	release <-chan struct{}
	reached chan struct{}
	once    *sync.Once
}

func (s holdSink) OnLine(line logs.LogLine) {
	s.once.Do(func() { close(s.reached) })
	<-s.release
	s.RecordingOutput.OnLine(line)
}

// holdingManager returns a manager whose sessions cannot end until the returned function
// releases the sink they deliver to, and a channel closed when a session has reached the sink.
func holdingManager(t *testing.T) (mgr *logs.Manager, open func(), reached <-chan struct{}) {
	t.Helper()
	release, arrived := make(chan struct{}), make(chan struct{})
	var once sync.Once
	open = func() { once.Do(func() { close(release) }) }
	mgr = logs.NewManager(logs.ManagerConfig{
		Handlers: map[string]logs.Handler{
			"test/pod": {Handler: func(*types.PluginContext, logs.LogStreamRequest) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("held\n")), nil
			}},
		},
		Sink:  holdSink{RecordingOutput: logtest.NewRecordingOutput(), release: release, reached: arrived, once: new(sync.Once)},
		Clock: timeutil.NewInstantFakeClock(),
	})
	t.Cleanup(mgr.Close)
	t.Cleanup(open) // runs before the manager's own Close, which would wait on the held sink
	return mgr, open, arrived
}

// startHeld starts a session and returns once it is delivering a line to the held sink.
func startHeld(t *testing.T, mgr *logs.Manager, reached <-chan struct{}) {
	t.Helper()
	if _, err := startFinite(mgr, "held"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the session never reached the sink")
	}
}

// While Wait is waiting for a session, the manager takes no other: the start is refused, not
// admitted into a group that is only closing.
func TestAStartWhileWaitIsWaitingIsRefused(t *testing.T) {
	mgr, open, reached := holdingManager(t)
	startHeld(t, mgr, reached)
	waited := make(chan struct{})
	go func() { mgr.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned while a session could not end")
	case <-time.After(100 * time.Millisecond):
	}

	if session, err := startFinite(mgr, "during"); !isRefusal(err) || session != nil {
		t.Errorf("a start while Wait was waiting (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}
	open()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return once the session could end")
	}
}

// The same while Close is waiting for a session that cannot end yet.
func TestAStartWhileCloseIsWaitingIsRefused(t *testing.T) {
	mgr, open, reached := holdingManager(t)
	startHeld(t, mgr, reached)
	closed := make(chan struct{})
	go func() { mgr.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a session could not end")
	case <-time.After(100 * time.Millisecond):
	}

	if session, err := startFinite(mgr, "during"); !isRefusal(err) || session != nil {
		t.Errorf("a start while Close was waiting (%+v, %v); want the refusal CONNECTION_ERROR", session, err)
	}
	open()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return once the session could end")
	}
}
