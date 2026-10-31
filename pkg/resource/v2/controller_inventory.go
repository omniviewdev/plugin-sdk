package resourcev2

import (
	"context"
	"errors"
	"maps"
	"sort"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// LoadConnections refreshes native discovery through Lifecycle.LoadConnections,
// applies that snapshot beneath the retained host overlay, and returns the
// effective inventory as independent snapshots sorted by ID. It never starts
// clients. Native discovery failure leaves the previous inventory in place;
// application failure follows ApplyConnections retirement semantics.
// A nil discovered slice is a successful empty native snapshot, not a failure.
func (c *Controller[T]) LoadConnections(ctx context.Context) (connections []types.Connection, err error) {
	var r *Registration[T]
	defer c.guard(&r, &err)
	if err = c.requireLifecycle(); err != nil {
		return nil, err
	}
	ctx, done, err := c.routeContext(ctx, AccessRoute{Connection: "connection-discovery"})
	if err != nil {
		return nil, err
	}
	defer done()
	connections, err = c.cfg.Lifecycle.LoadConnections(ctx)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = c.ApplyConnections(ctx, connections); err != nil {
		return nil, err
	}
	return c.ListConnections(ctx)
}

// GetConnection returns an independently owned configured-profile snapshot, not a
// native client or a lease. Editing it does not apply configuration. Empty IDs
// are invalid, unknown IDs are NOT_FOUND, and absent Lifecycle is UNSUPPORTED.
func (c *Controller[T]) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	connection, err := c.connection(ctx, id)
	if err != nil {
		return types.Connection{}, err
	}
	return cloneConnection(connection), nil
}

// ListConnections returns the managed connection inventory, not an implicit
// selection. Native inventory watching belongs to a ConnectionBrowser provider.
// Profiles are independently owned and sorted by ID. No discovery refresh, client
// start, or health probe occurs; absent Lifecycle returns UNSUPPORTED.
func (c *Controller[T]) ListConnections(ctx context.Context) ([]types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, c.classify(nil, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, NewError("CANCELLED", "Provider is closed.")
	}
	return connectionSnapshot(c.connections), nil
}

// NativeConnections snapshots the latest successful native discovery before
// overlay application. It neither reloads discovery nor exposes manual profiles.
// Returned profiles are independent snapshots sorted by ID, including private
// configuration; hosts must not persist source-owned credentials in overlays.
func (c *Controller[T]) NativeConnections(ctx context.Context) ([]types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, c.classify(nil, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, NewError("CANCELLED", "Provider is closed.")
	}
	return connectionSnapshot(c.nativeConnections), nil
}

func connectionSnapshot(inventory map[string]types.Connection) []types.Connection {
	ids := make([]string, 0, len(inventory))
	for id := range inventory {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	connections := make([]types.Connection, 0, len(ids))
	for _, id := range ids {
		connections = append(connections, cloneConnection(inventory[id]))
	}
	return connections
}

// ApplyConnections replaces only the native discovery snapshot. Explicit host
// overrides and disabled IDs survive refreshes and native disappearance.
// Nil replaces discovery with an empty snapshot. Inputs are copied, but callers
// must not mutate them during the call. Display-only edits preserve clients;
// changed client configuration cancels and drains old generations before
// publication. Teardown failure or cancellation preserves prior desired inventory,
// but cannot resurrect retired clients. No new clients start automatically.
// Do not reenter this method from inventory-teardown callbacks or leased work
// whose generation the replacement can retire.
func (c *Controller[T]) ApplyConnections(ctx context.Context, connections []types.Connection) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if err := ValidateMetadata(connections); err != nil {
		return err
	}
	loaded := make(map[string]types.Connection, len(connections))
	for i := range connections {
		connection := &connections[i]
		if connection.ID == "" {
			return NewError("INVALID_REQUEST", "Connection requires an explicit ID.")
		}
		if _, exists := loaded[connection.ID]; exists {
			return NewError("INVALID_REQUEST", "Connection inventory contains duplicate IDs.")
		}
		loaded[connection.ID] = cloneConnection(*connection)
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.applyInventory(ctx, loaded, c.overrides, c.disabled)
}

// ApplyConnectionOverlay replaces the entire host-owned desired overlay.
// Validation and client teardown must succeed before the new desired state is
// published. The controller never writes native configuration files.
// A zero overlay restores native discovery; inputs are copied. Nil private
// override data may inherit source credentials only for unchanged public client
// configuration; explicit empty private data clears it. Hosts own durable storage
// and startup replay and must not persist native private credentials.
// Failure keeps prior desired state, but canceled/drained clients remain retired.
// Reapplication cannot resurrect a generation. Calls serialize inventory mutation
// and must not reenter it from teardown callbacks or affected leased work.
func (c *Controller[T]) ApplyConnectionOverlay(ctx context.Context, overlay ConnectionOverlay) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if err := ValidateConnectionOverlay(overlay); err != nil {
		return err
	}
	overrides := make(map[string]types.Connection, len(overlay.Upserts))
	for i := range overlay.Upserts {
		connection := &overlay.Upserts[i]
		overrides[connection.ID] = cloneConnection(*connection)
	}
	disabled := make(map[string]struct{}, len(overlay.DisabledIDs))
	for _, id := range overlay.DisabledIDs {
		disabled[id] = struct{}{}
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	return c.applyInventory(ctx, c.nativeConnections, overrides, disabled)
}

// applyInventory is serialized by configMu. Staged route changes block new
// clients until canceled generations drain, then publish one effective inventory.
// Failed teardown leaves the prior desired state, but retired clients stay retired.
func (c *Controller[T]) applyInventory(
	ctx context.Context,
	native, overrides map[string]types.Connection,
	disabled map[string]struct{},
) error {
	if err := ctx.Err(); err != nil {
		return c.classify(nil, err)
	}
	effective := make(map[string]types.Connection, len(native)+len(overrides))
	for id := range native {
		if _, hidden := disabled[id]; !hidden {
			effective[id] = native[id]
		}
	}
	maps.Copy(effective, overrides)
	if c.cfg.ConnectionMetadata != nil {
		for id := range effective {
			if err := ValidateConnectionMetadataSnapshot(ConnectionMetadataSnapshot{ConnectionID: id, Revision: "1", Entries: map[string]ConnectionMetadataEntry{}}); err != nil {
				return err
			}
		}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return NewError("CANCELLED", "Provider is closed.")
	}
	// Host overlays omit private credentials. Merge only into the ephemeral
	// effective inventory, never into the host-owned desired overlay.
	c.inheritOverlayCredentials(effective, native, overrides)
	changed, retired := c.stageInventory(effective)
	c.mu.Unlock()
	var err error
	for _, slot := range retired {
		err = errors.Join(err, c.stopSlot(slot))
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = errors.Join(err, ctxErr)
	}
	c.mu.Lock()
	if c.closed && err == nil {
		err = NewError("CANCELLED", "Provider is closed.")
	}
	if err == nil {
		previousIDs := make(map[string]bool, len(c.connections))
		nextIDs := make(map[string]bool, len(effective))
		for id := range c.connections {
			previousIDs[id] = true
		}
		for id := range effective {
			nextIDs[id] = true
		}
		c.metadataInventoryLocked(previousIDs, nextIDs)
		c.nativeConnections = native
		c.overrides = overrides
		c.disabled = disabled
		c.connections = effective
	}
	for _, id := range changed {
		delete(c.changing, id)
	}
	c.mu.Unlock()
	return c.classify(nil, err)
}

// inheritOverlayCredentials runs under mu and never changes desired overrides.
// Only unchanged public configuration may inherit source-owned credentials.
func (c *Controller[T]) inheritOverlayCredentials(effective, native, overrides map[string]types.Connection) {
	for id := range overrides {
		override := overrides[id]
		if override.GetSensitiveData() != nil {
			continue
		}
		if candidate, ok := inheritConnectionCredentials(override, native, id); ok {
			effective[id] = candidate
			continue
		}
		if candidate, ok := inheritConnectionCredentials(override, c.connections, id); ok {
			effective[id] = candidate
		}
	}
}

func inheritConnectionCredentials(
	override types.Connection,
	source map[string]types.Connection,
	id string,
) (types.Connection, bool) {
	connection, exists := source[id]
	if !exists {
		return override, false
	}
	override.SetSensitiveData(connection.GetSensitiveData())
	return override, SameConnectionClientConfig(override, connection)
}

// stageInventory holds mu while blocking starts and retiring changed generations.
// Draining those generations must happen after releasing mu.
func (c *Controller[T]) stageInventory(effective map[string]types.Connection) ([]string, []*clientSlot[T]) {
	changed := make([]string, 0)
	for id := range c.connections {
		previous := c.connections[id]
		current, exists := effective[id]
		if !exists || !SameConnectionClientConfig(previous, current) {
			c.changing[id] = true
			changed = append(changed, id)
			continue
		}
		effective[id] = preserveConnectionRuntime(current, previous)
	}
	for id := range effective {
		if _, exists := c.connections[id]; !exists {
			c.changing[id] = true
			changed = append(changed, id)
		}
	}
	var retired []*clientSlot[T]
	for _, id := range changed {
		delete(c.failedStarts, id)
		if _, existed := c.connections[id]; existed && c.cfg.ConnectionMetadata != nil {
			m := c.metadataStateLocked(id)
			m.slot = nil
			m.pending = false
			c.metadataPublishLocked(m, ConnectionMetadataSnapshot{ConnectionID: id, Revision: m.snapshot.Revision, Entries: map[string]ConnectionMetadataEntry{}}, false)
		}
		if slot := c.clients[id]; slot != nil {
			slot.stopping = true
			slot.cancel()
			retired = append(retired, slot)
		}
	}
	return changed, retired
}

func preserveConnectionRuntime(current, previous types.Connection) types.Connection {
	if current.LastRefresh.IsZero() {
		current.LastRefresh = previous.LastRefresh
	}
	if current.ExpiryTime == 0 {
		current.ExpiryTime = previous.ExpiryTime
	}
	if current.Client == nil {
		current.Client = previous.Client
	}
	return current
}

func copyConnectionOverlay(
	upserts map[string]types.Connection,
	disabledIDs map[string]struct{},
) (map[string]types.Connection, map[string]struct{}) {
	overrides := make(map[string]types.Connection, len(upserts)+1)
	maps.Copy(overrides, upserts)
	disabled := make(map[string]struct{}, len(disabledIDs))
	for id := range disabledIDs {
		disabled[id] = struct{}{}
	}
	return overrides, disabled
}

// UpdateConnection upserts an explicitly identified desired record. Native
// refreshes cannot overwrite it; ApplyConnectionOverlay owns reset/restore.
// The input and returned profile own independent mutable configuration. This
// replaces the whole desired record, not a field patch, and clears its tombstone.
// Changed client configuration drains its generation before publishing; failure
// retains prior desired state but may leave that generation retired. See
// ApplyConnectionOverlay for credential inheritance and reentrancy restrictions.
func (c *Controller[T]) UpdateConnection(ctx context.Context, connection types.Connection) (types.Connection, error) {
	if err := c.requireLifecycle(); err != nil {
		return types.Connection{}, err
	}
	if err := ValidateConnectionOverlay(ConnectionOverlay{Upserts: []types.Connection{connection}}); err != nil {
		return types.Connection{}, err
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	overrides, disabled := copyConnectionOverlay(c.overrides, c.disabled)
	connection = cloneConnection(connection)
	overrides[connection.ID] = connection
	delete(disabled, connection.ID)
	if err := c.applyInventory(ctx, c.nativeConnections, overrides, disabled); err != nil {
		return types.Connection{}, err
	}
	return cloneConnection(connection), nil
}

// DeleteConnection hides a native route or removes a manual desired profile.
// Its tombstone survives native refresh until the host replaces the overlay.
// Unknown IDs return NOT_FOUND. Existing clients are canceled and drained before
// publication; failure retains prior desired state but not retired clients.
// This does not delete native configuration or durable host storage. Inventory
// teardown reentrancy restrictions from ApplyConnectionOverlay also apply.
func (c *Controller[T]) DeleteConnection(ctx context.Context, id string) error {
	if err := c.requireLifecycle(); err != nil {
		return err
	}
	if id == "" {
		return NewError("INVALID_REQUEST", "An explicit connection ID is required.")
	}
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.mu.Lock()
	_, exists := c.connections[id]
	c.mu.Unlock()
	if !exists {
		return NewError("NOT_FOUND", "Connection was not loaded.")
	}
	overrides, disabled := copyConnectionOverlay(c.overrides, c.disabled)
	delete(overrides, id)
	if _, native := c.nativeConnections[id]; native {
		disabled[id] = struct{}{}
	} else {
		delete(disabled, id)
	}
	return c.applyInventory(ctx, c.nativeConnections, overrides, disabled)
}
