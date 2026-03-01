package resource

import (
	"context"
	"time"
)

// export_test.go exposes unexported types for testing via the resource_test package.
// This is the idiomatic Go pattern for testing internal types without import cycles.

// --- resourcerRegistry ---

func NewResourcerRegistryForTest(defaultDef ResourceDefinition) *resourcerRegistry[string] {
	return newResourcerRegistry[string](defaultDef)
}

type ResourcerRegistryForTest = resourcerRegistry[string]

// --- connectionManager ---

func NewConnectionManagerForTest(rootCtx context.Context, provider ConnectionProvider[string]) *connectionManager[string] {
	return newConnectionManager[string](rootCtx, provider)
}

type ConnectionManagerForTest = connectionManager[string]

// --- watchManager ---

func NewWatchManagerForTest(registry *resourcerRegistry[string]) *watchManager[string] {
	return newWatchManager[string](registry)
}

// SetWatchManagerBackoff sets backoff params for testing (faster retries).
func SetWatchManagerBackoff(wm *watchManager[string], maxRetries int, baseBackoff time.Duration) {
	wm.maxRetries = maxRetries
	wm.baseBackoff = baseBackoff
}

// SetWatchManagerClock sets an injectable clock for deterministic backoff testing.
func SetWatchManagerClock(wm *watchManager[string], clock Clock) {
	wm.clock = clock
}

type WatchManagerForTest = watchManager[string]

// --- typeManager ---

func NewTypeManagerForTest(
	registry *resourcerRegistry[string],
	groups []ResourceGroup,
	discovery DiscoveryProvider,
) *typeManager[string] {
	return newTypeManager[string](registry, groups, discovery)
}

type TypeManagerForTest = typeManager[string]

// --- resourceController ---

func BuildResourceControllerForTest(ctx context.Context, cfg ResourcePluginConfig[string]) (*resourceController[string], error) {
	return BuildResourceController[string](ctx, cfg)
}

type ResourceControllerForTest = resourceController[string]

// IsControllerClosed returns the closed state for testing.
func IsControllerClosed(ctrl *resourceController[string]) bool {
	return ctrl.closed
}

// --- synchronous listener helpers (avoids go ListenForEvents + sleep) ---

// AddListenerForTest registers a sink synchronously, without spawning a goroutine.
func AddListenerForTest(ctrl *resourceController[string], sink WatchEventSink) {
	ctrl.watchMgr.AddListener(sink)
}

// RemoveListenerForTest unregisters a sink synchronously.
func RemoveListenerForTest(ctrl *resourceController[string], sink WatchEventSink) {
	ctrl.watchMgr.RemoveListener(sink)
}

// --- watch wait helpers for controller tests ---

func WaitForWatchReadyForTest(ctx context.Context, ctrl *resourceController[string], connID, key string) error {
	return ctrl.watchMgr.WaitForWatchReady(ctx, connID, key)
}

func WaitForWatchDoneForTest(ctx context.Context, ctrl *resourceController[string], connID, key string) error {
	return ctrl.watchMgr.WaitForWatchDone(ctx, connID, key)
}

func WaitForConnectionReadyForTest(ctx context.Context, ctrl *resourceController[string], connID string) error {
	return ctrl.watchMgr.WaitForConnectionReady(ctx, connID)
}

// --- watch wait helpers for watchManager tests ---

func WaitForWatchReadyWM(ctx context.Context, wm *watchManager[string], connID, key string) error {
	return wm.WaitForWatchReady(ctx, connID, key)
}

func WaitForWatchDoneWM(ctx context.Context, wm *watchManager[string], connID, key string) error {
	return wm.WaitForWatchDone(ctx, connID, key)
}

func WaitForConnectionReadyWM(ctx context.Context, wm *watchManager[string], connID string) error {
	return wm.WaitForConnectionReady(ctx, connID)
}
