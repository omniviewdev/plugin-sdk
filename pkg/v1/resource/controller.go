package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// resourceController wires all internal services together and satisfies the Provider interface.
// This is the gRPC-boundary implementation on the plugin side.
type resourceController[ClientT any] struct {
	log         logging.Logger
	registry    *resourcerRegistry[ClientT]
	connMgr     *connectionManager[ClientT]
	watchMgr    *watchManager[ClientT]
	typeMgr     *typeManager[ClientT]
	errorClass  ErrorClassifier // global error classifier (optional)
	schemaProvider SchemaProvider[ClientT] // auto-detected (optional)
	closed      atomic.Bool // tracks whether Close() has been called

	// filterFieldCache caches FilterFields results per "resourceKey::connID".
	// Cardinality is bounded by (registered resource types) x (active connections),
	// which is small in practice (typically tens to low hundreds of entries).
	// Entries are evicted when the underlying connection is updated or deleted
	// (driven by UpdateConnection/DeleteConnection handlers); otherwise they
	// remain until controller GC.
	filterFieldCacheMu sync.RWMutex
	filterFieldCache   map[string][]FilterField
}

// BuildResourceController creates a fully wired resourceController from config.
// Returns an error if the config fails validation.
func BuildResourceController[ClientT any](ctx context.Context, cfg ResourcePluginConfig[ClientT]) (*resourceController[ClientT], error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("BuildResourceController: %w", err)
	}
	return buildResourceController[ClientT](ctx, cfg)
}

// buildResourceController is the internal constructor that skips validation.
// Used by tests that intentionally exercise edge-case configs.
func buildResourceController[ClientT any](ctx context.Context, cfg ResourcePluginConfig[ClientT]) (*resourceController[ClientT], error) {
	registry := newResourcerRegistry[ClientT](cfg.DefaultDefinition)
	for _, reg := range cfg.Resources {
		registry.Register(reg)
	}
	for pattern, res := range cfg.Patterns {
		registry.RegisterPattern(pattern, res)
	}

	log := cfg.Logger
	if log == nil {
		log = logging.NewNop()
	}
	log = log.Named("resource")

	connMgr := newConnectionManager[ClientT](ctx, cfg.Connections)
	watchMgr := newWatchManager[ClientT](log, registry)
	typeMgr := newTypeManager[ClientT](registry, cfg.Groups, cfg.Discovery)

	// Auto-detect ScopeProvider on ConnectionProvider.
	if typed, ok := cfg.Connections.(ScopeProvider[ClientT]); ok {
		watchMgr.scopeProvider = typed
	}

	// Auto-detect SchemaProvider on ConnectionProvider.
	var sp SchemaProvider[ClientT]
	if typed, ok := cfg.Connections.(SchemaProvider[ClientT]); ok {
		sp = typed
	}

	return &resourceController[ClientT]{
		log:              log.Named("controller"),
		registry:         registry,
		connMgr:          connMgr,
		watchMgr:         watchMgr,
		typeMgr:          typeMgr,
		errorClass:       cfg.ErrorClassifier,
		schemaProvider:   sp,
		filterFieldCache: make(map[string][]FilterField),
	}, nil
}

// Close stops all connections and watches.
func (c *resourceController[ClientT]) Close() {
	c.closed.Store(true)
	for _, id := range c.connMgr.ActiveConnectionIDs() {
		if err := c.watchMgr.StopConnectionWatch(context.Background(), id); err != nil {
			c.log.Warnw(context.Background(), "failed to stop connection watch during close", "connection_id", id, "error", err)
		}
		if _, err := c.connMgr.StopConnection(context.Background(), id); err != nil {
			c.log.Warnw(context.Background(), "failed to stop connection during close", "connection_id", id, "error", err)
		}
	}
	c.watchMgr.Wait()
}

// --- OperationProvider ---

func (c *resourceController[ClientT]) Get(ctx context.Context, key string, input GetInput) (result *GetResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in Get: %v", r))
		}
	}()
	result, err = res.Get(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

func (c *resourceController[ClientT]) List(ctx context.Context, key string, input ListInput) (result *ListResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	c.maybeEnsureWatch(ctx, key)
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in List: %v", r))
		}
	}()
	result, err = res.List(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

func (c *resourceController[ClientT]) Find(ctx context.Context, key string, input FindInput) (result *FindResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in Find: %v", r))
		}
	}()

	// Validate filters if the Resourcer implements FilterableProvider.
	connID, _ := c.resolveConnectionID(ctx)
	if input.Filters != nil {
		if err := c.validateFilters(ctx, key, connID, input.Filters); err != nil {
			return nil, err
		}
	}

	// Route to TextSearchProvider if TextQuery is set and supported.
	if input.TextQuery != "" {
		if sp, ok := c.registry.GetTextSearchProvider(key); ok {
			result, err = sp.Search(ctx, client, meta, input.TextQuery, input.Pagination.PageSize)
			if err != nil {
				return nil, c.classifyError(key, err)
			}
			return result, nil
		}
		// No TextSearchProvider → ignore TextQuery, fall through to Find().
	}

	result, err = res.Find(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

func (c *resourceController[ClientT]) Create(ctx context.Context, key string, input CreateInput) (result *CreateResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in Create: %v", r))
		}
	}()
	result, err = res.Create(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

func (c *resourceController[ClientT]) Update(ctx context.Context, key string, input UpdateInput) (result *UpdateResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in Update: %v", r))
		}
	}()
	result, err = res.Update(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

func (c *resourceController[ClientT]) Delete(ctx context.Context, key string, input DeleteInput) (result *DeleteResult, retErr error) {
	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = c.classifyError(key, fmt.Errorf("resourcer panic in Delete: %v", r))
		}
	}()
	result, err = res.Delete(ctx, client, meta, input)
	if err != nil {
		return nil, c.classifyError(key, err)
	}
	return result, nil
}

// resolveCRUD resolves the resourcer, client, meta, and session context for a CRUD operation.
func (c *resourceController[ClientT]) resolveCRUD(ctx context.Context, key string) (Resourcer[ClientT], *ClientT, ResourceMeta, context.Context, error) {
	if c.closed.Load() {
		return nil, nil, ResourceMeta{}, ctx, fmt.Errorf("controller is closed")
	}
	if ctx.Err() != nil {
		return nil, nil, ResourceMeta{}, ctx, ctx.Err()
	}

	res, err := c.registry.Lookup(key)
	if err != nil {
		return nil, nil, ResourceMeta{}, ctx, err
	}

	connID, err := c.resolveConnectionID(ctx)
	if err != nil {
		return nil, nil, ResourceMeta{}, ctx, err
	}

	client, err := c.connMgr.GetClient(connID)
	if err != nil {
		return nil, nil, ResourceMeta{}, ctx, err
	}

	meta, _ := c.registry.LookupMeta(key)
	if meta.Key() != key {
		meta = ResourceMetaFromString(key)
	}

	conn, err := c.connMgr.GetConnection(connID)
	if err != nil {
		return nil, nil, ResourceMeta{}, ctx, fmt.Errorf("get connection %q: %w", connID, err)
	}
	existing := SessionFromContext(ctx)
	sess := &Session{Connection: &conn}
	if existing != nil {
		sess.PluginConfig = existing.PluginConfig
	}
	ctx = WithSession(ctx, sess)

	return res, client, meta, ctx, nil
}

// maybeEnsureWatch triggers a lazy watch for SyncOnFirstQuery resources.
func (c *resourceController[ClientT]) maybeEnsureWatch(ctx context.Context, key string) {
	policy := c.registry.GetSyncPolicy(key)
	if policy != SyncOnFirstQuery {
		return
	}
	connID, err := c.resolveConnectionID(ctx)
	if err != nil {
		return
	}
	_ = c.watchMgr.EnsureResourceWatch(ctx, connID, key)
}

// resolveConnectionID extracts the connection ID from the session context,
// or defaults to the first active connection.
func (c *resourceController[ClientT]) resolveConnectionID(ctx context.Context) (string, error) {
	if sess := SessionFromContext(ctx); sess != nil && sess.Connection != nil {
		return sess.Connection.ID, nil
	}
	ids := c.connMgr.ActiveConnectionIDs()
	if len(ids) == 0 {
		return "", fmt.Errorf("no active connections")
	}
	return ids[0], nil
}

// classifyError applies error classification (per-resource then global).
// Recovers from panics in the classifier itself.
func (c *resourceController[ClientT]) classifyError(key string, err error) (classified error) {
	defer func() {
		if r := recover(); r != nil {
			classified = fmt.Errorf("error classifier panic: %v (original: %w)", r, err)
		}
	}()

	// Per-resource classifier wins.
	if ec, ok := c.registry.GetErrorClassifier(key); ok {
		result := ec.ClassifyError(err)
		if result != nil {
			return result
		}
		// nil return means classifier declined — fall through to global.
	}
	// Global classifier fallback.
	if c.errorClass != nil {
		result := c.errorClass.ClassifyError(err)
		if result != nil {
			return result
		}
	}
	return err
}

// validateFilters checks filter predicates against the FilterableProvider's declared fields.
// If the Resourcer does not implement FilterableProvider, validation is skipped.
func (c *resourceController[ClientT]) validateFilters(ctx context.Context, key, connID string, expr *FilterExpression) error {
	if expr == nil {
		return nil
	}
	_, ok := c.registry.GetFilterableProvider(key)
	if !ok {
		// Not a FilterableProvider — skip validation, pass through.
		return nil
	}

	fields, err := c.getFilterFields(ctx, key, connID)
	if err != nil {
		return err
	}

	// Build lookup map: path → FilterField.
	fieldMap := make(map[string]FilterField, len(fields))
	for _, f := range fields {
		fieldMap[f.Path] = f
	}

	return c.validateFilterExpr(expr, fieldMap)
}

// getFilterFields returns cached filter fields or fetches and caches them.
func (c *resourceController[ClientT]) getFilterFields(ctx context.Context, key, connID string) ([]FilterField, error) {
	cacheKey := key + "::" + connID

	c.filterFieldCacheMu.RLock()
	if cached, ok := c.filterFieldCache[cacheKey]; ok {
		c.filterFieldCacheMu.RUnlock()
		return cached, nil
	}
	c.filterFieldCacheMu.RUnlock()

	fp, ok := c.registry.GetFilterableProvider(key)
	if !ok {
		return nil, nil
	}

	fields, err := fp.FilterFields(ctx, connID)
	if err != nil {
		return nil, err
	}

	c.filterFieldCacheMu.Lock()
	c.filterFieldCache[cacheKey] = fields
	c.filterFieldCacheMu.Unlock()

	return fields, nil
}

// evictFilterFieldCache removes all cached filter field entries for a given connection ID.
// Cache keys have the format "resourceKey::connID".
func (c *resourceController[ClientT]) evictFilterFieldCache(connID string) {
	suffix := "::" + connID
	c.filterFieldCacheMu.Lock()
	maps.DeleteFunc(c.filterFieldCache, func(k string, _ []FilterField) bool {
		return strings.HasSuffix(k, suffix)
	})
	c.filterFieldCacheMu.Unlock()
}

// validateFilterExpr recursively validates predicates and groups.
func (c *resourceController[ClientT]) validateFilterExpr(expr *FilterExpression, fieldMap map[string]FilterField) error {
	for _, pred := range expr.Predicates {
		ff, ok := fieldMap[pred.Field]
		if !ok {
			return ErrFilterUnknownField(pred.Field)
		}
		allowed := ff.Operators
		if len(allowed) == 0 {
			allowed = []FilterOperator{OpEqual, OpNotEqual}
		}
		if !slices.Contains(allowed, pred.Operator) {
			return ErrFilterInvalidOperator(pred.Field, pred.Operator, allowed)
		}
	}
	for i := range expr.Groups {
		if err := c.validateFilterExpr(&expr.Groups[i], fieldMap); err != nil {
			return err
		}
	}
	return nil
}

// --- ConnectionLifecycleProvider ---

func (c *resourceController[ClientT]) StartConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error) {
	status, err := c.connMgr.StartConnection(ctx, connectionID)
	if err != nil {
		return status, err
	}

	conn, err := c.connMgr.GetConnection(connectionID)
	if err != nil {
		return status, fmt.Errorf("start connection %q: get connection: %w", connectionID, err)
	}
	if err := c.typeMgr.DiscoverForConnection(ctx, &conn); err != nil {
		return status, fmt.Errorf("start connection %q: discovery: %w", connectionID, err)
	}

	discoveredTypes := c.typeMgr.DiscoveredKeySet(connectionID)

	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return status, fmt.Errorf("start connection %q: get client: %w", connectionID, err)
	}
	connCtx, err := c.connMgr.GetConnectionCtx(connectionID)
	if err != nil {
		return status, fmt.Errorf("start connection %q: get connection ctx: %w", connectionID, err)
	}
	if err := c.watchMgr.StartConnectionWatch(ctx, connectionID, client, connCtx, discoveredTypes); err != nil {
		return status, fmt.Errorf("start connection %q: start watches: %w", connectionID, err)
	}

	return status, nil
}

func (c *resourceController[ClientT]) StopConnection(ctx context.Context, connectionID string) (types.Connection, error) {
	// Stop watches first (best-effort).
	if err := c.watchMgr.StopConnectionWatch(ctx, connectionID); err != nil {
		c.log.Warnw(ctx, "failed to stop connection watch", "connection_id", connectionID, "error", err)
	}

	conn, err := c.connMgr.StopConnection(ctx, connectionID)
	return conn, err
}

func (c *resourceController[ClientT]) CheckConnection(ctx context.Context, connectionID string) (types.ConnectionStatus, error) {
	return c.connMgr.CheckConnection(ctx, connectionID)
}

func (c *resourceController[ClientT]) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	return c.connMgr.LoadConnections(ctx)
}

func (c *resourceController[ClientT]) ListConnections(ctx context.Context) ([]types.Connection, error) {
	return c.connMgr.ListConnections(ctx)
}

func (c *resourceController[ClientT]) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	return c.connMgr.GetConnection(id)
}

func (c *resourceController[ClientT]) GetConnectionNamespaces(ctx context.Context, id string) ([]string, error) {
	return c.connMgr.GetNamespaces(ctx, id)
}

func (c *resourceController[ClientT]) UpdateConnection(ctx context.Context, conn types.Connection) (types.Connection, error) {
	wasActive := c.connMgr.IsStarted(conn.ID)

	result, err := c.connMgr.UpdateConnection(ctx, conn)
	if errors.Is(err, ErrConnectionUnchanged) {
		return result, nil // no-op, skip watch restart
	}
	if err != nil {
		return result, err
	}

	// Evict stale filter field cache entries for this connection.
	c.evictFilterFieldCache(conn.ID)

	// If the connection was active, the client was restarted — re-discover and
	// restart watches.
	if wasActive && c.connMgr.IsStarted(conn.ID) {
		if err := c.watchMgr.StopConnectionWatch(ctx, conn.ID); err != nil {
			c.log.Warnw(ctx, "failed to stop connection watch before restart", "connection_id", conn.ID, "error", err)
		}

		updated, err := c.connMgr.GetConnection(conn.ID)
		if err != nil {
			return result, fmt.Errorf("update connection %q: get connection: %w", conn.ID, err)
		}
		if err := c.typeMgr.DiscoverForConnection(ctx, &updated); err != nil {
			return result, fmt.Errorf("update connection %q: discovery: %w", conn.ID, err)
		}
		discoveredTypes := c.typeMgr.DiscoveredKeySet(conn.ID)

		client, err := c.connMgr.GetClient(conn.ID)
		if err != nil {
			return result, fmt.Errorf("update connection %q: get client: %w", conn.ID, err)
		}
		connCtx, err := c.connMgr.GetConnectionCtx(conn.ID)
		if err != nil {
			return result, fmt.Errorf("update connection %q: get connection ctx: %w", conn.ID, err)
		}
		if err := c.watchMgr.StartConnectionWatch(ctx, conn.ID, client, connCtx, discoveredTypes); err != nil {
			return result, fmt.Errorf("update connection %q: start watches: %w", conn.ID, err)
		}
	}

	return result, nil
}

func (c *resourceController[ClientT]) DeleteConnection(ctx context.Context, id string) error {
	// Stop watches first if running.
	if c.watchMgr.HasWatch(id) {
		if err := c.watchMgr.StopConnectionWatch(ctx, id); err != nil {
			c.log.Warnw(ctx, "failed to stop connection watch before delete", "connection_id", id, "error", err)
		}
	}

	// Retrieve connection data before deletion for OnConnectionRemoved.
	conn, connErr := c.connMgr.GetConnection(id)

	if err := c.connMgr.DeleteConnection(ctx, id); err != nil {
		return err
	}

	// Post-deletion cleanup: evict caches and notify type manager.
	c.evictFilterFieldCache(id)
	if connErr == nil {
		if err := c.typeMgr.OnConnectionRemoved(ctx, &conn); err != nil {
			c.log.Warnw(ctx, "failed to clean up type manager after connection delete", "connection_id", id, "error", err)
		}
	}
	return nil
}

func (c *resourceController[ClientT]) WatchConnections(ctx context.Context, stream chan<- []types.Connection) error {
	watcher, ok := c.connMgr.provider.(ConnectionWatcher)
	if !ok {
		<-ctx.Done()
		return nil
	}
	ch, err := watcher.WatchConnections(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case conns, ok := <-ch:
			if !ok {
				return nil
			}
			removed := c.connMgr.reconcileLoaded(conns)
			// Clean up stale runtimes for connections that disappeared
			// from the snapshot, using the normal teardown paths.
			for _, id := range removed {
				if err := c.watchMgr.StopConnectionWatch(ctx, id); err != nil {
					c.log.Warnw(ctx, "failed to stop connection watch during reconcile", "connection_id", id, "error", err)
				}
				conn, stopErr := c.connMgr.StopConnection(ctx, id)
				if stopErr != nil {
					c.log.Warnw(ctx, "failed to stop connection during reconcile", "connection_id", id, "error", stopErr)
				}
				c.evictFilterFieldCache(id)
				// Clean up type manager state so GetResourceTypes/GetResourceGroups
				// don't serve stale data for removed connections.
				if stopErr == nil {
					if err := c.typeMgr.OnConnectionRemoved(ctx, &conn); err != nil {
						c.log.Warnw(ctx, "failed to clean up type manager during reconcile", "connection_id", id, "error", err)
					}
				}
			}
			select {
			case stream <- conns:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// --- WatchProvider ---

func (c *resourceController[ClientT]) StartConnectionWatch(ctx context.Context, connectionID string) error {
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return err
	}
	connCtx, err := c.connMgr.GetConnectionCtx(connectionID)
	if err != nil {
		return err
	}
	discoveredTypes := c.typeMgr.DiscoveredKeySet(connectionID)
	return c.watchMgr.StartConnectionWatch(ctx, connectionID, client, connCtx, discoveredTypes)
}

func (c *resourceController[ClientT]) StopConnectionWatch(ctx context.Context, connectionID string) error {
	return c.watchMgr.StopConnectionWatch(ctx, connectionID)
}

func (c *resourceController[ClientT]) HasWatch(ctx context.Context, connectionID string) bool {
	return c.watchMgr.HasWatch(connectionID)
}

func (c *resourceController[ClientT]) GetWatchState(ctx context.Context, connectionID string) (*WatchConnectionSummary, error) {
	return c.watchMgr.GetWatchState(connectionID)
}

func (c *resourceController[ClientT]) ListenForEvents(ctx context.Context, sink WatchEventSink) error {
	c.watchMgr.AddListener(sink)
	defer c.watchMgr.RemoveListener(sink)
	<-ctx.Done()
	return nil
}

func (c *resourceController[ClientT]) EnsureResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	return c.watchMgr.EnsureResourceWatch(ctx, connectionID, resourceKey)
}

func (c *resourceController[ClientT]) StopResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	return c.watchMgr.StopResourceWatch(ctx, connectionID, resourceKey)
}

func (c *resourceController[ClientT]) RestartResourceWatch(ctx context.Context, connectionID string, resourceKey string) error {
	return c.watchMgr.RestartResourceWatch(ctx, connectionID, resourceKey)
}

func (c *resourceController[ClientT]) IsResourceWatchRunning(ctx context.Context, connectionID string, resourceKey string) (bool, error) {
	return c.watchMgr.IsResourceWatchRunning(connectionID, resourceKey), nil
}

// --- TypeProvider ---

func (c *resourceController[ClientT]) GetResourceGroups(ctx context.Context, connectionID string) map[string]ResourceGroup {
	return c.typeMgr.GetResourceGroups(ctx, connectionID)
}

func (c *resourceController[ClientT]) GetResourceGroup(ctx context.Context, id string) (ResourceGroup, error) {
	return c.typeMgr.GetResourceGroup(ctx, id)
}

func (c *resourceController[ClientT]) GetResourceTypes(ctx context.Context, connectionID string) map[string]ResourceMeta {
	return c.typeMgr.GetResourceTypes(ctx, connectionID)
}

func (c *resourceController[ClientT]) GetResourceType(ctx context.Context, id string) (*ResourceMeta, error) {
	return c.typeMgr.GetResourceType(ctx, id)
}

func (c *resourceController[ClientT]) HasResourceType(ctx context.Context, id string) bool {
	return c.typeMgr.HasResourceType(ctx, id)
}

func (c *resourceController[ClientT]) GetResourceDefinition(ctx context.Context, id string) (ResourceDefinition, error) {
	return c.typeMgr.GetResourceDefinition(ctx, id)
}

func (c *resourceController[ClientT]) GetResourceCapabilities(ctx context.Context, resourceKey string) (*ResourceCapabilities, error) {
	return c.typeMgr.GetResourceCapabilities(ctx, resourceKey)
}

func (c *resourceController[ClientT]) GetResourceSchema(ctx context.Context, connectionID string, resourceKey string) (json.RawMessage, error) {
	sr, ok := c.registry.GetSchemaResourcer(resourceKey)
	if !ok {
		return nil, nil
	}
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(resourceKey)
	return sr.GetResourceSchema(ctx, client, meta)
}

func (c *resourceController[ClientT]) GetFilterFields(ctx context.Context, connectionID string, resourceKey string) ([]FilterField, error) {
	fp, ok := c.registry.GetFilterableProvider(resourceKey)
	if !ok {
		return nil, nil
	}
	return fp.FilterFields(ctx, connectionID)
}

// --- ActionProvider ---

func (c *resourceController[ClientT]) GetActions(ctx context.Context, key string) ([]ActionDescriptor, error) {
	ar, ok := c.registry.GetActionResourcer(key)
	if !ok {
		return nil, nil
	}
	connID, err := c.resolveConnectionID(ctx)
	if err != nil {
		return nil, err
	}
	client, err := c.connMgr.GetClient(connID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(key)
	return ar.GetActions(ctx, client, meta)
}

func (c *resourceController[ClientT]) ExecuteAction(ctx context.Context, key string, actionID string, input ActionInput) (*ActionResult, error) {
	ar, ok := c.registry.GetActionResourcer(key)
	if !ok {
		return nil, fmt.Errorf("resource %q does not support actions", key)
	}
	connID, err := c.resolveConnectionID(ctx)
	if err != nil {
		return nil, err
	}
	client, err := c.connMgr.GetClient(connID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(key)
	return ar.ExecuteAction(ctx, client, meta, actionID, input)
}

func (c *resourceController[ClientT]) StreamAction(ctx context.Context, key string, actionID string, input ActionInput, stream chan<- ActionEvent) error {
	ar, ok := c.registry.GetActionResourcer(key)
	if !ok {
		return fmt.Errorf("resource %q does not support actions", key)
	}
	connID, err := c.resolveConnectionID(ctx)
	if err != nil {
		return err
	}
	client, err := c.connMgr.GetClient(connID)
	if err != nil {
		return err
	}
	meta, _ := c.registry.LookupMeta(key)
	return ar.StreamAction(ctx, client, meta, actionID, input, stream)
}

// --- EditorSchemaProvider ---

func (c *resourceController[ClientT]) GetEditorSchemas(ctx context.Context, connectionID string) ([]EditorSchema, error) {
	if c.schemaProvider == nil {
		return nil, nil
	}
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return nil, err
	}
	return c.schemaProvider.GetEditorSchemas(ctx, client)
}

// ============================================================================
// Relationship + Health
// ============================================================================

func (c *resourceController[ClientT]) GetRelationships(_ context.Context, resourceKey string) ([]RelationshipDescriptor, error) {
	if rd, ok := c.registry.GetRelationshipDeclarer(resourceKey); ok {
		return rd.DeclareRelationships(), nil
	}
	return nil, nil
}

func (c *resourceController[ClientT]) ResolveRelationships(ctx context.Context, connectionID string, resourceKey string, id string, namespace string) ([]ResolvedRelationship, error) {
	resolver, ok := c.registry.GetRelationshipResolver(resourceKey)
	if !ok {
		return nil, nil
	}
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(resourceKey)
	return resolver.ResolveRelationships(ctx, client, meta, id, namespace)
}

func (c *resourceController[ClientT]) GetHealth(ctx context.Context, connectionID string, resourceKey string, data json.RawMessage) (*ResourceHealth, error) {
	assessor, ok := c.registry.GetHealthAssessor(resourceKey)
	if !ok {
		return nil, nil
	}
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(resourceKey)
	return assessor.AssessHealth(ctx, client, meta, data)
}

func (c *resourceController[ClientT]) GetResourceEvents(ctx context.Context, connectionID string, resourceKey string, id string, namespace string, limit int32) ([]ResourceEvent, error) {
	retriever, ok := c.registry.GetEventRetriever(resourceKey)
	if !ok {
		return nil, nil
	}
	client, err := c.connMgr.GetClient(connectionID)
	if err != nil {
		return nil, err
	}
	meta, _ := c.registry.LookupMeta(resourceKey)
	return retriever.GetEvents(ctx, client, meta, id, namespace, limit)
}

// Compile-time check.
var _ Provider = (*resourceController[string])(nil)
