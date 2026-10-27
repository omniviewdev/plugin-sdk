package resource_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/resource/resourcetest"
)

type replayStateSink struct {
	*resourcetest.RecordingSink
	onState func(resource.WatchStateEvent)
}

func (s *replayStateSink) OnStateChange(event resource.WatchStateEvent) {
	s.RecordingSink.OnStateChange(event)
	if s.onState != nil {
		s.onState(event)
	}
}

func bufferedStateWatch(t *testing.T) (*resource.WatchManagerForTest, resource.WatchEventSink) {
	t.Helper()
	producer := make(chan resource.WatchEventSink, 1)
	registry := resource.NewResourcerRegistryForTest(resource.ResourceDefinition{})
	registry.Register(resource.ResourceRegistration[string]{
		Meta: resourcetest.PodMeta,
		Resourcer: &resourcetest.WatchableResourcer[string]{
			WatchFunc: func(ctx context.Context, _ *string, meta resource.ResourceMeta, sink resource.WatchEventSink) error {
				for _, count := range []int{1, 2} {
					sink.OnStateChange(resource.WatchStateEvent{ResourceKey: meta.Key(), State: resource.WatchStateSyncing, ResourceCount: count})
				}
				producer <- sink
				<-ctx.Done()
				return nil
			},
		},
	})
	manager := resource.NewWatchManagerForTest(registry)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		manager.Wait()
	})
	client := "native-client"
	if err := manager.StartConnectionWatch(ctx, "conn-1", &client, ctx, nil); err != nil {
		t.Fatal(err)
	}
	return manager, <-producer
}

func TestWM_RegistrationReplayPrecedesLiveState(t *testing.T) {
	for _, interval := range []string{"readiness", "replay"} {
		t.Run(interval, func(t *testing.T) {
			manager, producer := bufferedStateWatch(t)
			sink := &replayStateSink{RecordingSink: resourcetest.NewRecordingSink()}
			live := func() {
				producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 3})
			}
			var onReady func() error
			if interval == "readiness" {
				onReady = func() error {
					live()
					producer.OnAdd(resource.WatchAddPayload{Key: resourcetest.PodMeta.Key(), ID: "first"})
					return nil
				}
			} else {
				sink.onState = func(event resource.WatchStateEvent) {
					if event.ResourceCount == 1 {
						live()
					}
				}
			}
			if _, err := manager.AddListener(t.Context(), sink, onReady); err != nil {
				t.Fatal(err)
			}
			var counts []int
			for _, event := range sink.States {
				if event.ResourceCount > 0 {
					counts = append(counts, event.ResourceCount)
				}
			}
			if !slices.Equal(counts, []int{1, 2, 3}) {
				t.Fatalf("consumer state regressed: progress counts %v, want [1 2 3]", counts)
			}
			last := sink.States[len(sink.States)-1]
			if last.State != resource.WatchStateSynced || last.Connection != "conn-1" || last.ResourceCount != 3 {
				t.Fatalf("last consumer state must remain SYNCED: %+v", last)
			}
			if interval == "readiness" && sink.AddCount() != 1 {
				t.Fatal("registration must retain the initial native ADD without buffering data")
			}
			producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSyncing, ResourceCount: 4})
			if last = sink.States[len(sink.States)-1]; last.ResourceCount != 4 {
				t.Fatalf("active listener lost subsequent live state: %+v", last)
			}
			manager.RemoveListener(sink)
			before := sink.StateCount()
			producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 5})
			if sink.StateCount() != before {
				t.Fatal("removed consumer received another state")
			}
		})
	}
}

func TestWM_RejectedRegistrationRetainsLatestState(t *testing.T) {
	manager, producer := bufferedStateWatch(t)
	rejected := resourcetest.NewRecordingSink()
	failed := errors.New("consumer rejected readiness")
	_, err := manager.AddListener(t.Context(), rejected, func() error {
		producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 3})
		return failed
	})
	if !errors.Is(err, failed) || rejected.StateCount() != 0 {
		t.Fatalf("rejected consumer received state or lost its failure: states=%d error=%v", rejected.StateCount(), err)
	}
	next := resourcetest.NewRecordingSink()
	if _, err := manager.AddListener(t.Context(), next, nil); err != nil {
		t.Fatal(err)
	}
	last := next.States[len(next.States)-1]
	if last.State != resource.WatchStateSynced || last.ResourceCount != 3 {
		t.Fatalf("next accepted consumer received stale state after rejection: %+v", last)
	}
}

func TestWM_StateDeliveryAllowsReentrantRegistrationAndRemoval(t *testing.T) {
	manager, producer := bufferedStateWatch(t)
	first := &replayStateSink{RecordingSink: resourcetest.NewRecordingSink()}
	nested := resourcetest.NewRecordingSink()
	if _, err := manager.AddListener(t.Context(), first, func() error {
		if _, err := manager.AddListener(t.Context(), nested, nil); err != nil {
			return err
		}
		producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 3})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, sink := range []*resourcetest.RecordingSink{first.RecordingSink, nested} {
		if last := sink.States[len(sink.States)-1]; last.ResourceCount != 3 {
			t.Fatalf("nested registration ended with stale consumer state: %+v", last)
		}
	}
	manager.RemoveListener(nested)
	first.onState = func(event resource.WatchStateEvent) {
		if event.ResourceCount == 4 {
			manager.RemoveListener(first)
			producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 5})
		}
	}
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSyncing, ResourceCount: 4})
	before := first.StateCount()
	next := resourcetest.NewRecordingSink()
	if _, err := manager.AddListener(t.Context(), next, nil); err != nil {
		t.Fatal(err)
	}
	if first.StateCount() != before || next.StateCount() != 1 || next.States[0].ResourceCount != 5 {
		t.Fatalf("reentrant removal lost retirement or next-consumer state: retired=%d next=%+v", first.StateCount(), next.States)
	}
}

func TestWM_RegistrationStateOverflowFails(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted=%t", existing), func(t *testing.T) {
			manager, producer := bufferedStateWatch(t)
			active := resourcetest.NewRecordingSink()
			if existing {
				if _, err := manager.AddListener(t.Context(), active, nil); err != nil {
					t.Fatal(err)
				}
			}
			rejected := resourcetest.NewRecordingSink()
			_, err := manager.AddListener(t.Context(), rejected, func() error {
				for count := 3; count < 2003; count++ {
					producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSyncing, ResourceCount: count})
				}
				return nil
			})
			if err == nil || rejected.StateCount() != 0 {
				t.Fatalf("overflow must fail registration before claiming replay success: error=%v states=%d", err, rejected.StateCount())
			}
			if !existing {
				ready := false
				if _, err := manager.AddListener(t.Context(), resourcetest.NewRecordingSink(), func() error { ready = true; return nil }); err == nil || ready {
					t.Fatal("incomplete pre-listener history must fail closed, including retry readiness")
				}
			} else {
				producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 2003})
				if last := active.States[len(active.States)-1]; last.State != resource.WatchStateSynced || last.ResourceCount != 2003 {
					t.Fatalf("overflowing registration disrupted an accepted consumer: %+v", last)
				}
			}
		})
	}
}

func TestWM_ConcurrentRegistrationRejectionPreservesNewestState(t *testing.T) {
	manager, producer := bufferedStateWatch(t)
	first := resourcetest.NewRecordingSink()
	second := resourcetest.NewRecordingSink()
	firstReady, secondReady := make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		select {
		case <-releaseSecond:
		default:
			close(releaseSecond)
		}
	}()
	failed := errors.New("first consumer rejected readiness")
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := manager.AddListener(t.Context(), first, func() error {
			close(firstReady)
			<-releaseFirst
			return failed
		})
		firstDone <- err
	}()
	<-firstReady
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSyncing, ResourceCount: 3})
	go func() {
		_, err := manager.AddListener(t.Context(), second, func() error {
			close(secondReady)
			<-releaseSecond
			return nil
		})
		secondDone <- err
	}()
	<-secondReady
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 4})
	close(releaseSecond)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	if err := <-firstDone; !errors.Is(err, failed) || first.StateCount() != 0 {
		t.Fatalf("rejected concurrent registration leaked state: error=%v states=%d", err, first.StateCount())
	}
	var counts []int
	for _, event := range second.States {
		if event.ResourceCount > 0 {
			counts = append(counts, event.ResourceCount)
		}
	}
	if !slices.Equal(counts, []int{1, 2, 3, 4}) || second.States[len(second.States)-1].State != resource.WatchStateSynced {
		t.Fatalf("accepted concurrent consumer lost or reordered newest progress: %v", counts)
	}
}

func TestWM_CancellationDuringReplayPreservesNewestState(t *testing.T) {
	for _, interval := range []string{"readiness", "replay"} {
		t.Run(interval, func(t *testing.T) {
			manager, producer := bufferedStateWatch(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sink := &replayStateSink{RecordingSink: resourcetest.NewRecordingSink()}
			stop := func() {
				producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 3})
				cancel()
			}
			var onReady func() error
			if interval == "readiness" {
				onReady = func() error { stop(); return nil }
			} else {
				sink.onState = func(event resource.WatchStateEvent) {
					if event.ResourceCount == 1 {
						stop()
					}
				}
			}
			if _, err := manager.AddListener(ctx, sink, onReady); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled replay reported registration success: %v", err)
			}
			before := sink.StateCount()
			next := resourcetest.NewRecordingSink()
			if _, err := manager.AddListener(t.Context(), next, nil); err != nil {
				t.Fatal(err)
			}
			last := next.States[len(next.States)-1]
			if sink.StateCount() != before || last.State != resource.WatchStateSynced || last.ResourceCount != 3 {
				t.Fatalf("canceled registration leaked callbacks or lost retry state: retired=%d next=%+v", sink.StateCount(), last)
			}
		})
	}
}

func TestWM_BlockedCallbackRetirementDoesNotBlockReplacement(t *testing.T) {
	manager, producer := bufferedStateWatch(t)
	entered, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ownedStorage := make(chan resource.WatchStateEvent, 1)
	old := &replayStateSink{RecordingSink: resourcetest.NewRecordingSink()}
	if _, err := manager.AddListener(t.Context(), old, nil); err != nil {
		t.Fatal(err)
	}
	old.onState = func(event resource.WatchStateEvent) {
		if event.ResourceCount == 3 {
			close(entered)
			<-release
		}
		ownedStorage <- event
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		defer close(completed)
		producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 3})
	}()
	<-entered
	// This is retirement, not a callback join. The old sink remains owned until
	// completed below; a caller must not close/reuse its storage before that ack.
	manager.RemoveListener(old)
	next := resourcetest.NewRecordingSink()
	if _, err := manager.AddListener(t.Context(), next, nil); err != nil {
		t.Fatal(err)
	}
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 4})
	producer.OnAdd(resource.WatchAddPayload{Key: resourcetest.PodMeta.Key(), ID: "replacement"})
	if next.StateCount() != 1 || next.States[0].ResourceCount != 4 || next.AddCount() != 1 {
		t.Fatalf("blocked retired callback prevented replacement progress: states=%+v adds=%d", next.States, next.AddCount())
	}
	close(release)
	<-completed
	if next.StateCount() != 1 || old.States[len(old.States)-1].ResourceCount != 3 {
		t.Fatal("old in-flight delivery was redirected into replacement storage")
	}
	close(ownedStorage) // only after the selected callback's completion ack
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 5})
	if next.StateCount() != 2 || next.States[1].ResourceCount != 5 {
		t.Fatal("closed retired storage interfered with replacement progress")
	}
}

func TestListenerCancellationRetiresOnlyOwnedRegistration(t *testing.T) {
	producer := make(chan resource.WatchEventSink, 1)
	provider, err := resource.BuildResourceController(t.Context(), resource.ResourcePluginConfig[string]{
		Connections: &resourcetest.StubConnectionProvider[string]{},
		Resources: []resource.ResourceRegistration[string]{{Meta: resourcetest.PodMeta, Resourcer: &resourcetest.WatchableResourcer[string]{
			WatchFunc: func(ctx context.Context, _ *string, _ resource.ResourceMeta, sink resource.WatchEventSink) error {
				producer <- sink
				<-ctx.Done()
				return nil
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if _, err := provider.LoadConnections(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.StartConnection(t.Context(), "conn-1"); err != nil {
		t.Fatal(err)
	}
	source := <-producer
	sink := resourcetest.NewRecordingSink()
	ctx1, cancel1 := context.WithCancel(t.Context())
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel1()
	defer cancel2()
	ready1, ready2 := make(chan struct{}), make(chan struct{})
	done1, done2 := make(chan error, 1), make(chan error, 1)
	go func() { done1 <- provider.ListenForEvents(ctx1, sink, func() error { close(ready1); return nil }) }()
	<-ready1
	go func() { done2 <- provider.ListenForEvents(ctx2, sink, func() error { close(ready2); return nil }) }()
	<-ready2
	cancel1()
	<-done1
	source.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 1})
	sink.WaitForState(t, resourcetest.PodMeta.Key(), resource.WatchStateSynced, time.Second)
	count := 0
	for _, event := range sink.States {
		if event.ResourceCount == 1 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("canceling one registration removed or duplicated its same-sink replacement: %d", count)
	}
	cancel2()
	<-done2
}

func TestWM_OverflowDuringReplayFailsRegistration(t *testing.T) {
	manager, producer := bufferedStateWatch(t)
	sink := &replayStateSink{RecordingSink: resourcetest.NewRecordingSink()}
	sink.onState = func(event resource.WatchStateEvent) {
		if event.ResourceCount == 1 {
			for count := 3; count < 2003; count++ {
				producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSyncing, ResourceCount: count})
			}
		}
	}
	if _, err := manager.AddListener(t.Context(), sink, nil); err == nil || sink.StateCount() == 0 {
		t.Fatalf("overflow during actual replay must fail, not claim completion: states=%d error=%v", sink.StateCount(), err)
	}
	before := sink.StateCount()
	producer.OnStateChange(resource.WatchStateEvent{ResourceKey: resourcetest.PodMeta.Key(), State: resource.WatchStateSynced, ResourceCount: 2003})
	if sink.StateCount() != before {
		t.Fatal("failed replay consumer remained active")
	}
}
