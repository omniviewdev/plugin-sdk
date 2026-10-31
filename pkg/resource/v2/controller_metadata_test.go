package resourcev2

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func metadataTestController(t *testing.T, observe func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error)) *Controller[string] {
	t.Helper()
	cfg := controllerConfig()
	cfg.Lifecycle = controllerLifecycle(&atomic.Int32{})
	cfg.ConnectionMetadata = observe
	c := newTestController(t, cfg)
	if _, err := c.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}
func metadataWaitIdle(t *testing.T, c *Controller[string], id string) ConnectionMetadataSnapshot {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		m := c.metadata[id]
		var done <-chan struct{}
		if m != nil && m.running != nil {
			done = m.running.done
		}
		c.mu.Unlock()
		if done == nil {
			snapshot, err := c.GetConnectionMetadata(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			return snapshot
		}
		select {
		case <-done:
		case <-deadline:
			t.Fatal("metadata acquisition did not finish")
		}
	}
}
func TestMetadataStopFencesLateCommitAndRevisionsSurviveRemoval(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := metadataTestController(t, func(ctx context.Context, _ types.Connection, _ *string) (ConnectionMetadataUpdate, error) {
		close(entered)
		<-release
		return ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{"test.version": {State: "success", Value: "late", ObservedAt: time.Now().UTC()}}}, nil
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	<-entered
	stopped := make(chan error, 1)
	go func() { _, err := c.StopConnection(context.Background(), "explicit"); stopped <- err }()
	// Observe the lifecycle fence before allowing the callback to return.
	fenceDeadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		retired := c.clients["explicit"].stopping
		c.mu.Unlock()
		if retired {
			break
		}
		if time.Now().After(fenceDeadline) {
			close(release)
			t.Fatal("stop did not revoke generation")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	snapshot := metadataWaitIdle(t, c, "explicit")
	if snapshot.Active || snapshot.Refreshing || len(snapshot.Entries) != 0 {
		t.Fatalf("late result revived metadata: %#v", snapshot)
	}
	previous, _ := strconv.ParseUint(snapshot.Revision, 10, 64)
	if err := c.ApplyConnections(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetConnectionMetadata(context.Background(), "explicit"); ErrorCode(err) != "NOT_FOUND" {
		t.Fatalf("removed connection: %v", err)
	}
	if err := c.ApplyConnections(context.Background(), []types.Connection{{ID: "explicit"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.GetConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := strconv.ParseUint(snapshot.Revision, 10, 64)
	if revision <= previous+1 || snapshot.Active || len(snapshot.Entries) != 0 {
		t.Fatalf("removal/re-add lost high water: %#v", snapshot)
	}
}
func TestMetadataPartialFailureOmissionAndPublicIsolation(t *testing.T) {
	observed := time.Now().UTC()
	var calls atomic.Int32
	source := map[string]any{"nested": []any{false, float64(0), nil, ""}}
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		switch calls.Add(1) {
		case 1:
			return ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{
				"test.value":   {State: "success", Value: source, ObservedAt: observed},
				"test.version": {State: "success", Value: "v1", ObservedAt: observed},
			}}, nil
		case 2:
			return ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{
				"test.value":   {State: "unavailable", Error: &ConnectionMetadataError{Code: "UNAVAILABLE", Message: "Not available."}},
				"test.version": {State: "success", Value: "v2", ObservedAt: observed.Add(time.Second)},
			}}, nil
		case 3:
			return ConnectionMetadataUpdate{}, nil
		default:
			return ConnectionMetadataUpdate{}, errors.New("private credential must not escape")
		}
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	metadataWaitIdle(t, c, "explicit")
	source["nested"].([]any)[0] = true
	snapshot, err := c.RefreshConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Entries["test.value"].State != "stale" || snapshot.Entries["test.version"].Value != "v2" {
		t.Fatalf("partial failure lost siblings: %#v", snapshot)
	}
	values := snapshot.Entries["test.value"].Value.(map[string]any)["nested"].([]any)
	if values[0] != false || values[1] != float64(0) || values[2] != nil || values[3] != "" {
		t.Fatalf("values mutated: %#v", values)
	}
	values[0] = "caller mutation"
	snapshot, err = c.RefreshConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Entries["test.value"].State != "stale" || snapshot.Entries["test.value"].Value.(map[string]any)["nested"].([]any)[0] != false {
		t.Fatal("omission or caller mutation changed retained state")
	}
	snapshot, err = c.RefreshConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LastRefreshError == nil || snapshot.LastRefreshError.Message == "private credential must not escape" || snapshot.Entries["test.version"].State != "stale" {
		t.Fatalf("unsafe overall failure: %#v", snapshot)
	}
}
func TestMetadataRefreshCancellationDetachesAndInvalidationCoalesces(t *testing.T) {
	entered := make(chan int32, 8)
	release := make(chan struct{}, 8)
	var calls atomic.Int32
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		n := calls.Add(1)
		entered <- n
		<-release
		return ConnectionMetadataUpdate{}, nil
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	detached := make(chan error, 1)
	go func() { _, err := c.RefreshConnectionMetadata(ctx, "explicit"); detached <- err }()
	cancel()
	if err := <-detached; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	for range 10 {
		if err := c.InvalidateConnectionMetadata(context.Background(), "explicit"); err != nil {
			t.Fatal(err)
		}
	}
	release <- struct{}{}
	select {
	case n := <-entered:
		if n != 2 {
			t.Fatalf("unexpected pass %d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing coalesced pass")
	}
	release <- struct{}{}
	snapshot := metadataWaitIdle(t, c, "explicit")
	if calls.Load() != 2 || !snapshot.Active || snapshot.Refreshing || snapshot.LastRefreshError != nil {
		t.Fatalf("shared work canceled or duplicated: calls=%d snapshot=%#v", calls.Load(), snapshot)
	}
}
func TestMetadataUnsupportedAndReadNeverStarts(t *testing.T) {
	c := newTestController(t, controllerConfig())
	if _, err := c.GetConnectionMetadata(context.Background(), "explicit"); ErrorCode(err) != "UNSUPPORTED" {
		t.Fatal(err)
	}
	var calls atomic.Int32
	supported := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		calls.Add(1)
		return ConnectionMetadataUpdate{}, nil
	})
	snapshot, err := supported.GetConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active || calls.Load() != 0 {
		t.Fatal("read acquired metadata")
	}
	if _, err := supported.RefreshConnectionMetadata(context.Background(), "explicit"); ErrorCode(err) != "CONNECTION_ERROR" {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("refresh started a client")
	}
	profile, err := supported.GetConnection(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Data) != 0 {
		t.Fatal("metadata changed connection configuration")
	}
}

func TestMetadataConnectionsAcquireIndependentlyAndConfigChangeClearsFacts(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	c := metadataTestController(t, func(ctx context.Context, connection types.Connection, _ *string) (ConnectionMetadataUpdate, error) {
		if connection.ID == "explicit" {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
				return ConnectionMetadataUpdate{}, ctx.Err()
			}
		}
		return ConnectionMetadataUpdate{Set: map[string]ConnectionMetadataObservation{"test.id": {State: "success", Value: connection.ID, ObservedAt: time.Now().UTC()}}}, nil
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	<-blocked
	if _, err := c.StartConnection(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	snapshot := metadataWaitIdle(t, c, "other")
	if snapshot.Entries["test.id"].Value != "other" {
		t.Fatal("another connection blocked acquisition")
	}
	close(release)
	metadataWaitIdle(t, c, "explicit")
	if err := c.ApplyConnections(context.Background(), []types.Connection{{ID: "explicit", Data: map[string]any{"endpoint": "changed"}}, {ID: "other", Name: "Display edit"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.GetConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Active || snapshot.Generation != "" || len(snapshot.Entries) != 0 {
		t.Fatalf("changed endpoint retained old facts: %#v", snapshot)
	}
	other, err := c.GetConnectionMetadata(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if !other.Active || other.Entries["test.id"].Value != "other" {
		t.Fatal("display edit retired unrelated facts")
	}
}

func TestMetadataSlowWatcherRequiresResyncRatherThanDroppingTombstones(t *testing.T) {
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		return ConnectionMetadataUpdate{}, nil
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		first := true
		result <- c.WatchConnectionMetadata(ctx, func(ConnectionMetadataEvent) error {
			if first {
				first = false
				close(entered)
				<-release
			}
			return nil
		})
	}()
	<-entered
	// Blocked initial replay cannot drain the bounded live-event queue.
	for range 40 {
		if err := c.ApplyConnections(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if err := c.ApplyConnections(context.Background(), []types.Connection{{ID: "explicit"}}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case err := <-result:
		if ErrorCode(err) != "RESET_REQUIRED" {
			t.Fatalf("slow watcher lost ordering: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow watcher did not terminate")
	}
}

func TestMetadataIncrementalAggregateLimitRejectsWholeUpdate(t *testing.T) {
	var calls atomic.Int32
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		start, count := 0, 14
		if calls.Add(1) > 1 {
			start, count = 14, 3
		}
		set := make(map[string]ConnectionMetadataObservation, count)
		for i := start; i < start+count; i++ {
			set["test.key_"+strconv.Itoa(i)] = ConnectionMetadataObservation{State: "success", Value: strings.Repeat("x", 16000), ObservedAt: time.Now().UTC()}
		}
		return ConnectionMetadataUpdate{Set: set}, nil
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	before := metadataWaitIdle(t, c, "explicit")
	if len(before.Entries) != 14 || before.LastRefreshError != nil {
		t.Fatalf("initial bounded snapshot rejected: %#v", before.LastRefreshError)
	}
	after, err := c.RefreshConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != 14 || after.LastRefreshError == nil {
		t.Fatalf("oversized patch partially committed: keys=%d error=%#v", len(after.Entries), after.LastRefreshError)
	}
	for key, entry := range after.Entries {
		if entry.State != "stale" || entry.Value != before.Entries[key].Value {
			t.Fatalf("old observation lost: %s", key)
		}
	}
}

func TestMetadataResubscribeReplaysRemovalTombstone(t *testing.T) {
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		return ConnectionMetadataUpdate{}, nil
	})
	before, err := c.GetConnectionMetadata(context.Background(), "explicit")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyConnections(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	found := errors.New("received removal")
	var removed ConnectionMetadataSnapshot
	err = c.WatchConnectionMetadata(ctx, func(event ConnectionMetadataEvent) error {
		if event.Removed && event.Snapshot.ConnectionID == "explicit" {
			removed = event.Snapshot
			return found
		}
		return nil
	})
	if !errors.Is(err, found) {
		t.Fatalf("reconnect did not replay removal: %v", err)
	}
	prior, _ := strconv.ParseUint(before.Revision, 10, 64)
	revision, _ := strconv.ParseUint(removed.Revision, 10, 64)
	if revision <= prior || removed.Active || len(removed.Entries) != 0 {
		t.Fatalf("invalid replay tombstone: %#v", removed)
	}
}

func TestMetadataCanceledAdmittedSlotDoesNotReschedule(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var calls atomic.Int32
	c := metadataTestController(t, func(context.Context, types.Connection, *string) (ConnectionMetadataUpdate, error) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return ConnectionMetadataUpdate{}, nil
	})
	if _, err := c.StartConnection(context.Background(), "explicit"); err != nil {
		t.Fatal(err)
	}
	<-entered
	c.mu.Lock()
	m := c.metadata["explicit"]
	done := m.running.done
	// Startup cancellation can win after the start admission check without
	// setting stopping. Reproduce that exact canceled-but-admitted slot.
	m.slot.cancel()
	c.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled observation did not finish")
	}
	c.mu.Lock()
	running := m.running != nil
	active := m.snapshot.Active
	refreshing := m.snapshot.Refreshing
	c.mu.Unlock()
	if running || active || refreshing || calls.Load() != 1 {
		t.Fatalf("canceled slot rescheduled acquisition: running=%v active=%v refreshing=%v calls=%d", running, active, refreshing, calls.Load())
	}
}
