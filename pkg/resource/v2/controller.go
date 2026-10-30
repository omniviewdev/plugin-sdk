package resourcev2

import (
	"context"
	"errors"
	"sync"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// Controller dispatches finite source operations and independently optional
// best-effort updates. Strong continuity remains P05's separate contract;
// neither a finite handler nor advisory updates imply that guarantee.
// Construct it with NewController; the zero value and nil receiver are not usable.
// Methods require nonnil contexts. Request/result containers remain borrowed
// during SDK use unless a method explicitly returns a connection snapshot.
// Its broad method set does not advertise operations: catalog declarations,
// installed registrations, and ancillary feature negotiation determine support.
//
// The controller owns one configured-client registry. configMu serializes desired
// inventory changes; when both locks are needed, acquire configMu before mu.
// mu protects registry membership, retirement flags and inventory publication.
// Never hold mu while invoking provider callbacks, waiting for readiness or
// leases, or stopping update sources. Inventory teardown still holds configMu:
// callbacks must not reenter inventory mutations.
//
// Lease admission increments users under mu before retirement can begin.
// Retirement marks the slot stopping and cancels its context under mu, then
// waits outside mu for readiness, update sources and all leases before teardown.
// Each lease combines caller, controller and client-generation cancellation and
// releases its user count exactly once. Callbacks must honor cancellation and
// must not synchronously stop or close their own leased generation, or wait for
// an operation that does so. Callbacks may run concurrently across requests and
// connections; shared provider state needs its own synchronization.
type Controller[ClientT any] struct {
	cfg               Config[ClientT]
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	configMu          sync.Mutex
	closed            bool
	connections       map[string]types.Connection
	nativeConnections map[string]types.Connection
	overrides         map[string]types.Connection
	disabled          map[string]struct{}
	changing          map[string]bool
	clients           map[string]*clientSlot[ClientT]
	// failedStarts retains only safe lifecycle outcome, never clients or errors.
	// Entries are bounded by configured IDs and survive failed-client cleanup.
	failedStarts map[string]bool
	closeOnce    sync.Once
	closeErr     error
	updates      *updateManager[ClientT]
	catalogNonce [32]byte
}

type clientSlot[T any] struct {
	connection   types.Connection
	client       *T
	ctx          context.Context
	cancel       context.CancelFunc
	ready        chan struct{}
	err          error
	stopping     bool
	users        sync.WaitGroup
	destroy      sync.Once
	destroyErr   error
	catalogNonce [32]byte
}

// NewController constructs the optional controller without starting connections.
// It copies the registration slice, but retains nested registration scopes,
// lifecycle configuration and callback-owned state. Callers must keep these
// retained inputs immutable after construction; callbacks remain responsible
// for synchronizing their own mutable implementation state.
// Catalog is required. A nonnil Lifecycle requires load, create, and destroy
// callbacks; dynamic update declarations require ResolveRegistration. Invalid
// construction inputs return an error and no controller. Connections remain
// absent until explicitly loaded/applied, and unstarted until explicitly started.
func NewController[T any](cfg Config[T]) (*Controller[T], error) {
	if cfg.Catalog == nil {
		return nil, NewError("INVALID_REQUEST", "Catalog handler is required.")
	}
	if cfg.Lifecycle != nil &&
		(cfg.Lifecycle.LoadConnections == nil || cfg.Lifecycle.CreateClient == nil || cfg.Lifecycle.DestroyClient == nil) {
		return nil, NewError("INVALID_REQUEST", "Lifecycle requires load, create, and destroy handlers.")
	}
	if cfg.DynamicUpdates && cfg.ResolveRegistration == nil {
		return nil, NewError("INVALID_REQUEST", "Dynamic updates require an exact registration resolver.")
	}
	for _, r := range cfg.Registrations {
		if r.Type == "" {
			return nil, NewError("INVALID_REQUEST", "Registration requires an exact type.")
		}
		if r.SyncPolicy < SyncOnConnect || r.SyncPolicy > SyncNever {
			return nil, NewError("INVALID_REQUEST", "Registration has an unknown sync policy.")
		}
		if r.Scope != nil {
			if err := ValidateScope(r.Scope); err != nil {
				return nil, err
			}
		}
	}
	cfg.Registrations = append([]Registration[T](nil), cfg.Registrations...)
	for i := range cfg.Registrations {
		cfg.Registrations[i].Actions = cloneActionDescriptors(cfg.Registrations[i].Actions)
	}
	nonce, err := newCatalogNonce()
	if err != nil {
		return nil, err
	}
	controller := &Controller[T]{
		cfg:               cfg,
		connections:       make(map[string]types.Connection),
		nativeConnections: make(map[string]types.Connection),
		overrides:         make(map[string]types.Connection),
		disabled:          make(map[string]struct{}),
		changing:          make(map[string]bool),
		clients:           make(map[string]*clientSlot[T]),
		failedStarts:      make(map[string]bool),
	}
	// Close owns cancellation after construction; the controller outlives this call.
	//nolint:gosec // G118: Close cancels the owned controller lifetime.
	controller.ctx, controller.cancel = context.WithCancel(context.Background())
	controller.catalogNonce = nonce
	controller.updates = newUpdateManager(controller)
	return controller, nil
}

func (c *Controller[T]) classify(r *Registration[T], err error) (out error) {
	if err == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			out = NewError("INTERNAL", "Provider error classifier panicked.")
		}
	}()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ResourceOperationError{Code: ErrorCode(err), Err: err}
	}
	var typed *ResourceOperationError
	if errors.As(err, &typed) {
		return err
	}
	if r != nil && r.ErrorClassifier != nil {
		if out := r.ErrorClassifier.ClassifyError(err); out != nil {
			return out
		}
	}
	if c.cfg.ErrorClassifier != nil {
		if out := c.cfg.ErrorClassifier.ClassifyError(err); out != nil {
			return out
		}
	}
	return err
}

func (c *Controller[T]) guard(r **Registration[T], err *error) {
	if recover() != nil {
		*err = NewError("INTERNAL", "Provider handler panicked.")
	}
	*err = c.classify(*r, *err)
}

var (
	_ Provider                    = (*Controller[struct{}])(nil)
	_ ConnectionLifecycle         = (*Controller[struct{}])(nil)
	_ ConnectionReadinessProvider = (*Controller[struct{}])(nil)
	_ Closer                      = (*Controller[struct{}])(nil)
)

// Features reports installed ancillary implementations rather than Controller's
// static method set. Wrappers with additional native traits extend this result.
// An empty Traits slice means no optional ancillary implementation is installed,
// not lack of finite catalog support. This method does not start or probe clients.
func (c *Controller[T]) Features(ctx context.Context) (FeatureSet, error) {
	if err := ctx.Err(); err != nil {
		return FeatureSet{}, c.classify(nil, err)
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return FeatureSet{}, NewError("CANCELLED", "Provider is closed.")
	}
	features := FeatureSet{Traits: []Feature{}}
	if c.cfg.Lifecycle != nil {
		features.Traits = append(
			features.Traits,
			FeatureConnections,
			FeatureConnectionReadiness,
			FeatureConnectionOverlay,
		)
	}
	actionCatalog := c.cfg.ResolveRegistration != nil
	for _, registration := range c.cfg.Registrations {
		actionCatalog = actionCatalog || len(registration.Actions) != 0
	}
	if actionCatalog {
		features.Traits = append(features.Traits, FeatureActionCatalog)
	}
	updates := c.cfg.DynamicUpdates
	for _, registration := range c.cfg.Registrations {
		if handles(registration, OperationUpdate) {
			updates = true
			break
		}
	}
	if updates {
		features.Traits = append(features.Traits, FeatureBestEffortUpdates)
	}
	return features, nil
}

var _ FeatureNegotiator = (*Controller[struct{}])(nil)

var _ ConnectionOverlayProvider = (*Controller[struct{}])(nil)
