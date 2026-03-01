package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// resourceController wires all internal services together and satisfies the Provider interface.
// This is the gRPC-boundary implementation on the plugin side.
type resourceController[ClientT any] struct {
	registry    *resourcerRegistry[ClientT]
	connMgr     *connectionManager[ClientT]
	watchMgr    *watchManager[ClientT]
	typeMgr     *typeManager[ClientT]
	errorClass  ErrorClassifier // global error classifier (optional)
	schemaProvider SchemaProvider[ClientT] // auto-detected (optional)
	closed      bool // tracks whether Close() has been called

	// filterFieldCache caches FilterFields results per "resourceKey::connID".
	filterFieldCacheMu sync.RWMutex
	filterFieldCache   map[string][]FilterField
}

// BuildResourceController creates a fully wired resourceController from config.
func BuildResourceController[ClientT any](ctx context.Context, cfg ResourcePluginConfig[ClientT]) (*resourceController[ClientT], error) {
	if cfg.Connections == nil {
		return nil, fmt.Errorf("Connections provider is required")
	}

	registry := newResourcerRegistry[ClientT](cfg.DefaultDefinition)
	for _, reg := range cfg.Resources {
		registry.Register(reg)
	}
	for pattern, res := range cfg.Patterns {
		registry.RegisterPattern(pattern, res)
	}

	connMgr := newConnectionManager[ClientT](ctx, cfg.Connections)
	watchMgr := newWatchManager[ClientT](registry)
	typeMgr := newTypeManager[ClientT](registry, cfg.Groups, cfg.Discovery)

	// Auto-detect SchemaProvider on ConnectionProvider.
	var sp SchemaProvider[ClientT]
	if typed, ok := cfg.Connections.(SchemaProvider[ClientT]); ok {
		sp = typed
	}

	return &resourceController[ClientT]{
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
	c.closed = true
	for _, id := range c.connMgr.ActiveConnectionIDs() {
		_ = c.watchMgr.StopConnectionWatch(context.Background(), id)
		_, _ = c.connMgr.StopConnection(context.Background(), id)
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
	c.maybeEnsureWatch(ctx, key)

	res, client, meta, ctx, err := c.resolveCRUD(ctx, key)
	if err != nil {
		return nil, err
	}
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
		if err := c.validateFilters(key, connID, input.Filters); err != nil {
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
	if c.closed {
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

	conn, _ := c.connMgr.GetConnection(connID)
	ctx = WithSession(ctx, &Session{Connection: &conn})

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
func (c *resourceController[ClientT]) validateFilters(key, connID string, expr *FilterExpression) error {
	if expr == nil {
		return nil
	}
	_, ok := c.registry.GetFilterableProvider(key)
	if !ok {
		// Not a FilterableProvider — skip validation, pass through.
		return nil
	}

	fields, err := c.getFilterFields(key, connID)
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
func (c *resourceController[ClientT]) getFilterFields(key, connID string) ([]FilterField, error) {
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

	fields, err := fp.FilterFields(context.Background(), connID)
	if err != nil {
		return nil, err
	}

	c.filterFieldCacheMu.Lock()
	c.filterFieldCache[cacheKey] = fields
	c.filterFieldCacheMu.Unlock()

	return fields, nil
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
		found := false
		for _, a := range allowed {
			if a == pred.Operator {
				found = true
				break
			}
		}
		if !found {
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

	// Start watches for SyncOnConnect resources.
	client, _ := c.connMgr.GetClient(connectionID)
	connCtx, _ := c.connMgr.GetConnectionCtx(connectionID)
	_ = c.watchMgr.StartConnectionWatch(ctx, connectionID, client, connCtx)

	// Run discovery.
	conn, _ := c.connMgr.GetConnection(connectionID)
	_ = c.typeMgr.DiscoverForConnection(ctx, &conn)

	return status, nil
}

func (c *resourceController[ClientT]) StopConnection(ctx context.Context, connectionID string) (types.Connection, error) {
	// Stop watches first.
	_ = c.watchMgr.StopConnectionWatch(ctx, connectionID)

	conn, err := c.connMgr.StopConnection(ctx, connectionID)
	return conn, err
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

	// If the connection was active, the client was restarted — restart watches.
	if wasActive && c.connMgr.IsStarted(conn.ID) {
		_ = c.watchMgr.StopConnectionWatch(ctx, conn.ID)
		client, _ := c.connMgr.GetClient(conn.ID)
		connCtx, _ := c.connMgr.GetConnectionCtx(conn.ID)
		_ = c.watchMgr.StartConnectionWatch(ctx, conn.ID, client, connCtx)
	}

	return result, nil
}

func (c *resourceController[ClientT]) DeleteConnection(ctx context.Context, id string) error {
	// Stop watches first if running.
	if c.watchMgr.HasWatch(id) {
		_ = c.watchMgr.StopConnectionWatch(ctx, id)
	}
	conn, _ := c.connMgr.GetConnection(id)
	_ = c.typeMgr.OnConnectionRemoved(ctx, &conn)
	return c.connMgr.DeleteConnection(ctx, id)
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
	return c.watchMgr.StartConnectionWatch(ctx, connectionID, client, connCtx)
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
