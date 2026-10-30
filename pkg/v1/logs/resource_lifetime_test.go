package logs

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func awaitLifetime(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("lifetime transition timed out")
	}
}

func TestAcceptedLogsCreationCancellationAndRetirementDrain(t *testing.T) {
	controller, _ := logsSourceController(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	drain := make(chan struct{})
	releaseDrain := sync.OnceFunc(func() { close(drain) })
	released := make(chan struct{})
	producerContexts := make(chan context.Context, 1)
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}, Handlers: map[string]Handler{
		"logs/opaque::pod": {
			Plugin:   "logs",
			Resource: "opaque::pod",
			Handler: func(pc *types.PluginContext, _ LogStreamRequest) (io.ReadCloser, error) {
				producerContexts <- pc.Context
				close(entered)
				<-pc.Context.Done()
				close(canceled)
				<-drain
				return nil, pc.Context.Err()
			},
		},
	}})
	t.Cleanup(func() {
		releaseDrain()
		manager.Close()
	})
	manager.BindConnectionContextProvider(controller)
	creation, cancelCreation := context.WithCancel(context.Background())
	pc := &types.PluginContext{Context: creation, Connection: &types.Connection{ID: "explicit"}}
	session, err := manager.CreateResourceSession(
		pc,
		CreateSessionOptions{
			ResourceKey:  "opaque::pod",
			ResourceID:   "opaque/id",
			ResourceData: exactLogBody(),
			Route:        &resourcev2.AccessRoute{Connection: "explicit"},
			Options:      LogSessionOptions{Follow: true},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	awaitLifetime(t, entered)
	producerContext := <-producerContexts
	cancelCreation()
	if producerContext.Err() != nil {
		t.Fatal("creation cancellation closed accepted producer")
	}
	go func() {
		_, stopErr := controller.StopConnection(context.Background(), "explicit")
		if stopErr != nil {
			t.Errorf("stop native generation: %v", stopErr)
		}
		close(released)
	}()
	awaitLifetime(t, canceled)
	select {
	case <-released:
		t.Fatal("native generation released before producer drained")
	default:
	}
	releaseDrain()
	awaitLifetime(t, released)
	if err := manager.CloseSession(pc, session.ID); err != nil {
		t.Fatal(err)
	}
	manager.Close()
}

type cancelReader struct{ closed chan struct{} }

func (r *cancelReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, errors.New("private native diagnostic")
}
func (r *cancelReader) Close() error { close(r.closed); return nil }

func TestSourceCancellationClosesBlockedReader(t *testing.T) {
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}})
	ctx, cancel := context.WithCancel(context.Background())
	ss := &sessionState{ctx: ctx, session: LogSession{ID: "session"}}
	reader := &cancelReader{closed: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		_ = manager.consumeSourceReader(ctx, ss, LogSource{ID: "source"}, reader, manager.log)
		close(done)
	}()
	cancel()
	awaitLifetime(t, done)
}

type blockedLogsAcquisition struct {
	entered chan struct{}
	proceed chan struct{}
}

func (p blockedLogsAcquisition) WithConnection(
	ctx context.Context,
	_ resourcev2.AccessRoute,
	fn func(context.Context, types.Connection) error,
) error {
	close(p.entered)
	<-p.proceed
	// Model a provider completing acquisition after cancellation. Admission must
	// still reject this callback before it can register or launch a producer.
	return fn(ctx, types.Connection{ID: "explicit"})
}

func TestCreationCanceledDuringAcquisitionCreatesNoOrphan(t *testing.T) {
	entered := make(chan struct{})
	proceed := make(chan struct{})
	native := make(chan struct{}, 1)
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}, Handlers: map[string]Handler{
		"logs/opaque::pod": {
			Plugin:   "logs",
			Resource: "opaque::pod",
			Handler: func(*types.PluginContext, LogStreamRequest) (io.ReadCloser, error) {
				native <- struct{}{}
				return io.NopCloser(strings.NewReader("")), nil
			},
		},
	}})
	defer manager.Close()
	manager.BindConnectionContextProvider(blockedLogsAcquisition{entered: entered, proceed: proceed})
	creation, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var session *LogSession
	var err error
	go func() {
		session, err = manager.CreateResourceSession(
			&types.PluginContext{Context: creation, Connection: &types.Connection{ID: "explicit"}},
			CreateSessionOptions{
				ResourceKey:  "opaque::pod",
				ResourceID:   "opaque/id",
				ResourceData: exactLogBody(),
				Route:        &resourcev2.AccessRoute{Connection: "explicit"},
			},
		)
		close(done)
	}()
	awaitLifetime(t, entered)
	cancel()
	close(proceed)
	awaitLifetime(t, done)
	if session != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition accepted: %v %v", session, err)
	}
	sessions, listErr := manager.ListSessions(nil)
	if listErr != nil || len(sessions) != 0 {
		t.Fatalf("orphan sessions: %v %v", sessions, listErr)
	}
	select {
	case <-native:
		t.Fatal("canceled acquisition launched native producer")
	default:
	}
}

func TestRegistryRemovalRetainsAcceptedProducerLease(t *testing.T) {
	controller, _ := logsSourceController(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	drain := make(chan struct{})
	releaseDrain := sync.OnceFunc(func() { close(drain) })
	managerClosed := make(chan struct{})
	var manager *Manager
	manager = NewManager(ManagerConfig{Sink: nativeLogSink{}, Handlers: map[string]Handler{
		"logs/opaque::pod": {
			Plugin:   "logs",
			Resource: "opaque::pod",
			Handler: func(pc *types.PluginContext, _ LogStreamRequest) (io.ReadCloser, error) {
				close(entered)
				go func() { manager.Close(); close(managerClosed) }()
				<-pc.Context.Done()
				close(canceled)
				<-drain
				return nil, pc.Context.Err()
			},
		},
	}})
	t.Cleanup(func() {
		releaseDrain()
		manager.Close()
	})
	manager.BindConnectionContextProvider(controller)
	created := make(chan struct{})
	go func() {
		_, err := manager.CreateResourceSession(
			&types.PluginContext{Context: context.Background(), Connection: &types.Connection{ID: "explicit"}},
			CreateSessionOptions{
				ResourceKey: "opaque::pod",
				ResourceID:  "opaque/id",
				Route:       &resourcev2.AccessRoute{Connection: "explicit"},
			},
		)
		if err != nil {
			t.Errorf("creation: %v", err)
		}
		close(created)
	}()
	awaitLifetime(t, entered)
	awaitLifetime(t, canceled)
	sessions, err := manager.ListSessions(nil)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("registry did not retire: %v %v", sessions, err)
	}
	retired := make(chan struct{})
	go func() {
		_, stopErr := controller.StopConnection(context.Background(), "explicit")
		if stopErr != nil {
			t.Errorf("stop: %v", stopErr)
		}
		close(retired)
	}()
	select {
	case <-retired:
		t.Fatal("registry removal released a live producer lease")
	default:
	}
	releaseDrain()
	awaitLifetime(t, created)
	awaitLifetime(t, managerClosed)
	awaitLifetime(t, retired)
}

func TestResourceSessionIDCollisionDoesNotReplaceOwner(t *testing.T) {
	manager := NewManager(ManagerConfig{Sink: nativeLogSink{}})
	defer manager.Close()
	pc := &types.PluginContext{Context: context.Background()}
	opts := CreateSessionOptions{SessionID: "00000000-0000-4000-8000-000000000000"}
	first, state, err := manager.createSession(pc, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := manager.createSession(pc, opts)
	if second != nil || resourcev2.ErrorCode(err) != "CONFLICT" {
		t.Fatalf("collision accepted: %v %v", second, err)
	}
	manager.mu.RLock()
	owner := manager.sessions[first.ID]
	manager.mu.RUnlock()
	if owner != state {
		t.Fatal("collision replaced accepted owner")
	}
}
