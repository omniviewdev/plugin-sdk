package metric

import (
	"context"
	"errors"
	"sync"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

	"github.com/google/uuid"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/settings"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

const (
	pollingOutputBuffer               = 256
	defaultPollingInterval            = 10 * time.Second
	maxLaneSubscriptionIDs            = 4096
	admissionBindingOverhead   uint64 = 64
	admissionReferenceOverhead uint64 = 64
	admissionScopeOverhead     uint64 = 32
)

type streamBinding struct {
	reference *resourcev2.ResourceReference
	revision  string
	active    bool
	bytes     uint64
}

// streamAdmission retains immutable catalog pins and ID tombstones for one lane.
// Only the input goroutine writes; output delivery reads under mu.
type streamAdmission struct {
	mu            sync.Mutex
	bindings      map[string]streamBinding
	retainedBytes uint64
}

func (a *streamAdmission) lookup(id string) (streamBinding, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bound, known := a.bindings[id]
	return bound, known
}

func (a *streamAdmission) checkCapacity(input StreamInput, size uint64) error {
	if input.Command != StreamCommandSubscribe {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, used := a.bindings[input.SubscriptionID]; used {
		return metricError("CONFLICT", "Metric subscription ID was already used in this lane.")
	}
	if len(a.bindings) >= maxLaneSubscriptionIDs || size > uint64(resourcev2.MaxMessageBytes)-a.retainedBytes {
		return metricError("RESOURCE_LIMIT", "Metric lane admission limit reached.")
	}
	return nil
}

func (a *streamAdmission) record(input StreamInput, size uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if input.Command != StreamCommandSubscribe {
		if previous, exists := a.bindings[input.SubscriptionID]; exists && previous.active {
			compact := streamAdmissionMetadataBytes(input.SubscriptionID, nil, "")
			a.retainedBytes -= previous.bytes - compact
			a.bindings[input.SubscriptionID] = streamBinding{bytes: compact}
		}
		return
	}
	var reference *resourcev2.ResourceReference
	if input.Reference != nil {
		cloned := *input.Reference
		cloned.Address.Scope = append([]resourcev2.ScopeEntry{}, input.Reference.Address.Scope...)
		if input.Reference.Incarnation != nil {
			incarnation := *input.Reference.Incarnation
			cloned.Incarnation = &incarnation
		}
		reference = &cloned
	}
	if a.bindings == nil {
		a.bindings = make(map[string]streamBinding)
	}
	a.bindings[input.SubscriptionID] = streamBinding{
		reference: reference,
		revision:  input.CatalogRevision,
		active:    true,
		bytes:     size,
	}
	a.retainedBytes += size
}

func (m *Manager) validateStreamAdmission(
	ctx context.Context,
	input StreamInput,
	resource bool,
	admission *streamAdmission,
) (uint64, error) {
	err := validateStream(input)
	pc := types.PluginContextFromContext(ctx)
	if err == nil {
		err = validateQuery(QueryRequest{
			ResourceKey: input.ResourceKey, ResourceData: input.ResourceData,
			Reference: input.Reference, Route: input.Route,
		}, pc)
	}
	if err == nil && input.Route != nil && (pc == nil || pc.Connection == nil) {
		err = metricError("INVALID_REQUEST", "Resource route requires an actual selected connection.")
	}
	if !resource && input.HasResourceIntent() {
		return 0, metricError("UNSUPPORTED", "Use StreamResourceMetrics for resource metrics.")
	}
	if err != nil {
		return 0, err
	}
	var size uint64
	if input.Command == StreamCommandSubscribe {
		size = streamAdmissionMetadataBytes(input.SubscriptionID, input.Reference, input.CatalogRevision)
	}
	if err = admission.checkCapacity(input, size); err != nil {
		return 0, err
	}
	return size, m.checkStreamCatalog(ctx, input.Reference, input.CatalogRevision)
}

func streamRejection(input StreamInput, err error) StreamOutput {
	var operation *resourcev2.ResourceOperationError
	if !errors.As(err, &operation) {
		operation = &resourcev2.ResourceOperationError{
			Code: "INVALID_REQUEST", Message: "Invalid metric stream input.",
		}
	}
	id := input.SubscriptionID
	if resourcev2.ValidateMetadata(struct{ ID string }{id}) != nil {
		id = ""
	}
	return StreamOutput{SubscriptionID: id, OperationError: operation}
}

func (m *Manager) filterStream(
	ctx context.Context,
	in <-chan StreamInput,
	filtered chan<- StreamInput,
	rejected chan<- StreamOutput,
	resource bool,
	admission *streamAdmission,
) {
	defer close(filtered)
	for {
		select {
		case <-ctx.Done():
			return
		case input, ok := <-in:
			if !ok {
				return
			}
			if input.Command == StreamCommandSubscribe && input.SubscriptionID == "" {
				input.SubscriptionID = uuid.NewString()
			}
			size, err := m.validateStreamAdmission(ctx, input, resource, admission)
			if err != nil {
				select {
				case rejected <- streamRejection(input, err):
				case <-ctx.Done():
					return
				}
				continue
			}
			admission.record(input, size)
			select {
			case filtered <- input:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Manager manages metric queries and streams within a plugin process.
type Manager struct {
	log                logging.Logger
	settingsProvider   settings.Provider
	providerInfo       ProviderInfo
	handlers           map[string]Handler
	queryFunc          QueryFunc
	streamFunc         StreamFunc
	subscriptions      map[subscriptionKey]*managedSubscription
	mux                sync.Mutex
	connectionProvider resourcev2.ConnectionContextProvider
}

type subscriptionKey struct {
	owner context.Context
	id    string
}

type managedSubscription struct {
	id           string
	resourceKey  string
	resourceID   string
	resourceNS   string
	resourceData *resourcev2.Value
	reference    *resourcev2.ResourceReference
	route        *resourcev2.AccessRoute
	metricIDs    []string
	interval     time.Duration
	params       map[string]string
	owner        context.Context
	ctx          context.Context
	cancel       context.CancelFunc
	out          chan StreamOutput
}

var _ Provider = (*Manager)(nil)

// NewManager creates a process-local manager. A nil logger disables logging.
// Bind the connection provider before starting concurrent queries or streams.
func NewManager(
	log logging.Logger,
	sp settings.Provider,
	providerInfo ProviderInfo,
	handlers map[string]Handler,
	queryFunc QueryFunc,
	streamFunc StreamFunc,
) *Manager {
	if log == nil {
		log = logging.NewNop()
	}
	return &Manager{
		log:              log.Named("metric.manager"),
		settingsProvider: sp,
		providerInfo:     providerInfo,
		handlers:         handlers,
		queryFunc:        queryFunc,
		streamFunc:       streamFunc,
		subscriptions:    make(map[subscriptionKey]*managedSubscription),
	}
}

// BindConnectionContextProvider installs the sole configured native registry
// during capability setup, before any stream or query starts.
func (m *Manager) BindConnectionContextProvider(provider resourcev2.ConnectionContextProvider) {
	m.connectionProvider = provider
}

// GetSupportedResources returns provider metadata and an unordered handler snapshot.
func (m *Manager) GetSupportedResources(_ *types.PluginContext) (*ProviderInfo, []Handler) {
	handlers := make([]Handler, 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	return &m.providerInfo, handlers
}

// Query runs a non-resource query; resource intent requires QueryResource.
func (m *Manager) Query(ctx *types.PluginContext, req QueryRequest) (*QueryResponse, error) {
	if req.HasResourceIntent() {
		return nil, resourcev2.NewError("UNSUPPORTED", "Use QueryResource for resource metrics.")
	}
	return m.query(ctx, req)
}

// QueryResource validates resource input before leasing its configured connection.
func (m *Manager) QueryResource(ctx *types.PluginContext, req QueryRequest) (*QueryResponse, error) {
	if !req.HasResourceIntent() {
		return nil, resourcev2.NewError("INVALID_REQUEST", "Resource query requires resource input.")
	}
	if err := validateQuery(req, ctx); err != nil {
		return nil, err
	}
	return m.query(ctx, req)
}

func (m *Manager) query(ctx *types.PluginContext, req QueryRequest) (*QueryResponse, error) {
	if m.queryFunc == nil {
		return &QueryResponse{
			Success: false,
			Error:   "query not supported by this provider",
		}, nil
	}

	var response *QueryResponse
	err := resourcev2.WithUtilityConnection(
		ctx,
		m.connectionProvider,
		req.Reference,
		req.Route,
		func(pc *types.PluginContext) error {
			native := context.Background()
			if pc != nil && pc.Context != nil {
				native = pc.Context
			}
			var err error
			response, err = m.queryFunc(m.queryContext(native, pc), req)
			return err
		},
	)
	return response, err
}

func (m *Manager) queryContext(ctx context.Context, pc *types.PluginContext) *QueryContext {
	qctx := &QueryContext{PluginID: m.providerInfo.ID, Context: ctx}
	if pc != nil && pc.Connection != nil {
		connection := resourcev2.CloneConnection(*pc.Connection)
		qctx.Connection, qctx.ConnectionID = &connection, connection.ID
	}
	return qctx
}

// StreamMetrics starts an owner-isolated non-resource lane until cancellation or input closure.
func (m *Manager) StreamMetrics(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	return m.admitStream(ctx, in, false, nil)
}

func (m *Manager) admitStream(
	ctx context.Context,
	in chan StreamInput,
	resource bool,
	finished chan struct{},
) (chan StreamOutput, error) {
	filtered := make(chan StreamInput)
	ctx, cancel := context.WithCancel(ctx)
	rejected := make(chan StreamOutput)
	admission := &streamAdmission{}
	go m.filterStream(ctx, in, filtered, rejected, resource, admission)
	native, err := m.stream(ctx, filtered)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan StreamOutput)
	go func() {
		defer func() {
			cancel()
			close(out)
			if finished != nil {
				close(finished)
			}
		}()
		m.forwardAdmittedStream(ctx, native, rejected, out, admission)
	}()
	return out, nil
}

// forwardAdmittedStream merges local rejections with native output, applying the
// lane's retirement and catalog fences immediately before publication.
func (m *Manager) forwardAdmittedStream(
	ctx context.Context,
	native <-chan StreamOutput,
	rejected <-chan StreamOutput,
	out chan<- StreamOutput,
	admission *streamAdmission,
) {
	for {
		var output StreamOutput
		rejection := false
		select {
		case <-ctx.Done():
			return
		case output = <-rejected:
			rejection = true
		case value, ok := <-native:
			if !ok {
				return
			}
			output = value
		}
		if ctx.Err() != nil {
			return
		}
		admitted, publish := m.admitStreamOutput(ctx, admission, output, rejection)
		if !publish {
			continue
		}
		select {
		case out <- admitted:
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) admitStreamOutput(
	ctx context.Context,
	admission *streamAdmission,
	output StreamOutput,
	rejection bool,
) (StreamOutput, bool) {
	bound, known := admission.lookup(output.SubscriptionID)
	if known && !bound.active && !rejection {
		return output, false
	}
	if output.OperationError != nil {
		return output, true
	}
	if err := m.checkStreamCatalog(ctx, bound.reference, bound.revision); err != nil {
		var operation *resourcev2.ResourceOperationError
		if !errors.As(err, &operation) {
			operation = &resourcev2.ResourceOperationError{
				Code:    resourcev2.ErrorCode(err),
				Message: "Metric catalog admission failed.",
			}
		}
		output = StreamOutput{SubscriptionID: output.SubscriptionID, OperationError: operation}
	}
	return output, true
}

func (m *Manager) checkStreamCatalog(
	ctx context.Context,
	reference *resourcev2.ResourceReference,
	revision string,
) error {
	if reference == nil {
		return nil
	}
	if err := resourcev2.ValidateCatalogRevision(revision); err != nil {
		return err
	}
	pc := types.PluginContextFromContext(ctx)
	if pc == nil || pc.Connection == nil {
		return metricError("RESET_REQUIRED", "Canonical metric subscription requires its admitted connection.")
	}
	provider, ok := m.connectionProvider.(resourcev2.Provider)
	if !ok {
		return metricError("UNSUPPORTED", "Canonical metric catalog admission is unavailable.")
	}
	catalog, err := provider.Catalog(
		ctx,
		resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: pc.Connection.ID}},
	)
	if err != nil {
		return err
	}
	return resourcev2.CheckCatalogRevision(resourcev2.WithCatalogRevision(ctx, revision), catalog)
}

// StreamResourceMetrics leases the selected connection for the lane's lifetime.
// Cancellation stops native work and prevents further delivery.
func (m *Manager) StreamResourceMetrics(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	return m.connectedStream(ctx, in)
}

func (m *Manager) connectedStream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	pc := types.PluginContextFromContext(ctx)
	if pc == nil || pc.Connection == nil {
		return m.admitStream(ctx, in, true, nil)
	}
	if m.connectionProvider == nil {
		return nil, metricError("UNSUPPORTED", "Configured metric connection context is unavailable.")
	}
	type result struct {
		out chan StreamOutput
		err error
	}
	ready := make(chan result, 1)
	go func() {
		published := false
		err := m.connectionProvider.WithConnection(
			ctx,
			resourcev2.AccessRoute{Connection: pc.Connection.ID},
			func(bound context.Context, connection types.Connection) error {
				boundPC := *pc
				boundPC.Connection = &connection
				bound = types.WithPluginContext(bound, &boundPC)
				finished := make(chan struct{})
				out, err := m.admitStream(bound, in, true, finished)
				ready <- result{out: out, err: err}
				published = true
				if err != nil {
					return err
				}
				select {
				case <-finished:
					return nil
				case <-bound.Done():
					return bound.Err()
				}
			},
		)
		if !published {
			ready <- result{err: err}
		}
	}()
	select {
	case result := <-ready:
		return result.out, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Manager) stream(ctx context.Context, in chan StreamInput) (chan StreamOutput, error) {
	if m.streamFunc != nil {
		qctx := m.queryContext(ctx, types.PluginContextFromContext(ctx))
		return m.streamFunc(
			&StreamContext{
				Context:      ctx,
				PluginID:     qctx.PluginID,
				Connection:   qctx.Connection,
				ConnectionID: qctx.ConnectionID,
			},
			in,
		)
	}

	out := make(chan StreamOutput, pollingOutputBuffer)
	var polls sync.WaitGroup
	// Otherwise, implement polling-based streaming using QueryFunc.
	go func() {
		defer func() {
			m.mux.Lock()
			defer func() {
				m.mux.Unlock()
				polls.Wait()
				close(out)
			}()
			for id, sub := range m.subscriptions {
				if sub.owner == ctx {
					sub.cancel()
					delete(m.subscriptions, id)
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return

			case input, ok := <-in:
				if !ok {
					return
				}
				m.handleStreamCommand(ctx, input, out, &polls)
			}
		}
	}()

	return out, nil
}

func (m *Manager) handleStreamCommand(
	parentCtx context.Context,
	input StreamInput,
	out chan StreamOutput,
	polls *sync.WaitGroup,
) {
	switch input.Command {
	case StreamCommandSubscribe:
		m.subscribe(parentCtx, input, out, polls)
	case StreamCommandUnsubscribe:
		m.unsubscribe(parentCtx, input.SubscriptionID)
	case StreamCommandClose:
		m.unsubscribe(parentCtx, input.SubscriptionID)
	}
}

func (m *Manager) subscribe(
	parentCtx context.Context,
	input StreamInput,
	out chan StreamOutput,
	polls *sync.WaitGroup,
) {
	subID := input.SubscriptionID
	if subID == "" {
		subID = uuid.NewString()
	}

	interval := input.Interval
	if interval <= 0 {
		interval = defaultPollingInterval
	}

	native := parentCtx
	if input.Reference != nil {
		native = resourcev2.WithCatalogRevision(native, input.CatalogRevision)
	}
	ctx, cancel := context.WithCancel(native)
	sub := &managedSubscription{
		id:           subID,
		resourceKey:  input.ResourceKey,
		resourceID:   input.ResourceID,
		resourceNS:   input.ResourceNamespace,
		resourceData: input.ResourceData,
		reference:    input.Reference,
		route:        input.Route,
		metricIDs:    input.MetricIDs,
		interval:     interval,
		params:       input.Params,
		owner:        parentCtx,
		ctx:          ctx,
		cancel:       cancel,
		out:          out,
	}

	m.mux.Lock()
	key := subscriptionKey{owner: parentCtx, id: subID}
	if previous := m.subscriptions[key]; previous != nil {
		previous.cancel()
	}
	m.subscriptions[key] = sub
	m.mux.Unlock()

	polls.Go(func() {
		m.pollSubscription(sub)
	})
}

func (m *Manager) unsubscribe(owner context.Context, subscriptionID string) {
	m.mux.Lock()
	key := subscriptionKey{owner: owner, id: subscriptionID}
	sub, ok := m.subscriptions[key]
	if ok {
		sub.cancel()
		delete(m.subscriptions, key)
	}
	m.mux.Unlock()
}

func (m *Manager) pollSubscription(sub *managedSubscription) {
	ticker := time.NewTicker(sub.interval)
	defer ticker.Stop()

	// Immediately poll once.
	m.pollOnce(sub)

	for {
		select {
		case <-sub.ctx.Done():
			return
		case <-ticker.C:
			m.pollOnce(sub)
		}
	}
}

func (m *Manager) pollOnce(sub *managedSubscription) {
	if m.queryFunc == nil || sub.ctx.Err() != nil {
		return
	}

	req := QueryRequest{
		ResourceKey:       sub.resourceKey,
		ResourceID:        sub.resourceID,
		ResourceNamespace: sub.resourceNS,
		ResourceData:      sub.resourceData,
		Reference:         sub.reference,
		Route:             sub.route,
		MetricIDs:         sub.metricIDs,
		Shape:             ShapeCurrent,
		Params:            sub.params,
	}

	pc := types.PluginContextFromContext(sub.ctx)
	if pc == nil {
		pc = &types.PluginContext{}
	}
	pc.Context = sub.ctx
	resp, err := m.query(pc, req)
	if sub.ctx.Err() != nil {
		return
	}
	if err != nil {
		var operation *resourcev2.ResourceOperationError
		errors.As(metricDomainError(err), &operation)
		select {
		case sub.out <- StreamOutput{
			SubscriptionID: sub.id,
			Timestamp:      time.Now(),
			Error:          err.Error(),
			OperationError: operation,
		}:
		case <-sub.ctx.Done():
		}
		return
	}

	select {
	case sub.out <- StreamOutput{
		SubscriptionID: sub.id,
		Results:        resp.Results,
		Timestamp:      time.Now(),
	}:
	case <-sub.ctx.Done():
	}
}

// Account only immutable admitted metadata, not resource bodies or native query
// options. Retirement retains a compact ID tombstone until this lane closes.
func streamAdmissionMetadataBytes(id string, reference *resourcev2.ResourceReference, revision string) uint64 {
	size := admissionBindingOverhead + uint64(len(id)) + uint64(len(revision))
	if reference != nil {
		a := reference.Address
		size += uint64(len(a.Provider)) + uint64(len(a.Authority)) + uint64(len(a.Type)) + uint64(len(a.ID)) +
			admissionReferenceOverhead
		for _, entry := range a.Scope {
			size += uint64(len(entry.Dimension)) + uint64(len(entry.Value)) + admissionScopeOverhead
		}
		if reference.Incarnation != nil {
			size += uint64(len(*reference.Incarnation))
		}
	}
	return size
}
