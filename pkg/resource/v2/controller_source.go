package resourcev2

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omniviewdev/plugin-sdk/internal/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// OpenSource dispatches a source session under a route lease and catalog fence.
func (c *Controller[T]) OpenSource(ctx context.Context, request SourceOpenRequest, emitter SourceEmitter) (err error) {
	var registration *Registration[T]
	var pages, snapshotItems int
	ctx, span := obs.Tracer().Start(ctx, "sdk.source.open", trace.WithAttributes(
		attribute.String("source_id", request.SessionID),
		attribute.String("resource_type", request.Target.Type),
		attribute.String("connection_id", request.Target.Route.Connection),
		attribute.String("mode", request.Mode),
	))
	var openEnded sync.Once
	// endOpen closes the handshake span exactly once. A following source ends it
	// when the provider callback starts, so the span never spans the stream lifetime.
	endOpen := func(openErr error) {
		openEnded.Do(func() {
			span.SetAttributes(attribute.Int("snapshot_items", snapshotItems), attribute.Int("pages", pages))
			obs.End(span, openErr, ErrorCode(openErr))
		})
	}
	defer func() { endOpen(err) }()
	defer c.guardContext(ctx, "open_source", &registration, &err)
	if err = ValidateSourceOpenRequest(request); err != nil {
		return err
	}
	if emitter == nil {
		return NewError("INVALID_REQUEST", "Source emitter is required.")
	}
	streamCtx := ctx
	ctx, client, done, err := c.lease(ctx, request.Target.Route)
	if err != nil {
		return err
	}
	defer done()
	operation := OperationEnumerate
	if request.Mode == SourceModeSnapshotAndUpdates {
		operation = OperationSourceUpdates
	} else if request.Acquisition.Kind == SourceAcquisitionNativeQuery {
		operation = OperationNativeQuery
	}
	target := request.Target
	registration, descriptor, err := c.resolve(ctx, target.Route, target.Provider, target.Type, target.Representation, target.Scope, nil, operation)
	if err != nil {
		return err
	}
	foundBinding := false
	for _, candidate := range descriptor.Representations {
		if candidate.ID == target.Representation && candidate == request.Binding.Representation {
			foundBinding = true
			break
		}
	}
	if !foundBinding {
		obs.Reject(ctx, lifecycleLogger(), "open_source", "RESET_REQUIRED", "reason", "binding_changed", "source_id", request.SessionID)
		sourceResetRequired(ctx, "binding_changed")
		return NewError("RESET_REQUIRED", "Source binding changed.")
	}
	sourceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fenced := &controllerSourceEmitter[T]{controller: c, route: target.Route, descriptor: descriptor, request: request, target: target, binding: request.Binding.Representation, emitter: emitter, cancel: cancel, streamCtx: streamCtx, leaseCtx: ctx, updates: operation == OperationSourceUpdates, nextCatalogCheck: time.Now().Add(time.Second)}
	if operation == OperationSourceUpdates {
		var representation Representation
		for _, candidate := range descriptor.Representations {
			if candidate.ID == target.Representation {
				representation = candidate
				break
			}
		}
		endOpen(nil)
		err = registration.Source(sourceCtx, client, SourceRequest{Target: target, Representation: representation, Limits: request.Limits}, fenced)
		if fenced.reset.Load() {
			return nil
		}
		if ctx.Err() != nil && streamCtx.Err() == nil {
			return fenced.bindingReset(streamCtx)
		}
		if err == nil {
			// A transient catalog failure cannot turn an otherwise completed
			// source into a terminal failure. A selected-contract mismatch emits
			// bindingChanged; otherwise the transport emits sourceInvalidated.
			if catalogErr := fenced.checkCatalog(ctx); catalogErr != nil {
				lifecycleLogger().Debugw(ctx, "source completion catalog check failed", "source_id", request.SessionID, "error_code", obs.ErrorCode(catalogErr, ErrorCode(catalogErr)))
			}
			return nil
		}
		return err
	}
	size := request.Limits.MaxBatchItems
	if size > MaxPageRecords {
		size = MaxPageRecords
	}
	var cursor *string
	seen := make(map[string]struct{})
	coverage := SourceCoverage{Enumeration: EnumerationExhausted, Consistency: ConsistencyPointInTime, Limitations: []string{}}
	var revision *string
	pages = 0
	for {
		var page SourcePage
		if operation == OperationEnumerate {
			page, err = registration.Enumerate(ctx, client, EnumerateRequest{Target: target, PageSize: size, Cursor: cursor})
		} else {
			query := NativeQueryRequest{Target: target, Language: request.Acquisition.Language, Query: *request.Acquisition.Query, PageSize: size, Cursor: cursor}
			if err = ValidateNativeQueryInput(descriptor, query); err != nil {
				return err
			}
			page, err = registration.NativeQuery(ctx, client, query)
		}
		if err != nil {
			return err
		}
		if err = fenced.checkCatalog(ctx); err != nil {
			if fenced.reset.Load() {
				return nil
			}
			return err
		}
		if err = checkPage(ctx, page, descriptor, target, size); err != nil {
			return err
		}
		pages++
		lifecycleLogger().Debugw(ctx, "source page received", "source_id", request.SessionID, "pages", pages, "records", len(page.Records))
		if page.Coverage.Kind == "unknown" {
			coverage.Enumeration = EnumerationUnknown
		} else if page.Coverage.Kind == "partial" && coverage.Enumeration != EnumerationUnknown {
			coverage.Enumeration = EnumerationPartial
		}
		if page.Coverage.Reason != "" {
			reason := page.Coverage.Reason
			found := false
			for _, existing := range coverage.Limitations {
				if existing == reason {
					found = true
					break
				}
			}
			if !found && len(coverage.Limitations) < MaxSourceLimitations {
				coverage.Limitations = append(coverage.Limitations, reason)
			}
		}
		if page.Consistency != ConsistencyPointInTime {
			coverage.Consistency = ConsistencyBestEffort
		}
		if pages == 1 {
			revision = page.SourceRevision
		} else if revision == nil || page.SourceRevision == nil || *revision != *page.SourceRevision {
			coverage.Consistency = ConsistencyBestEffort
		}
		observations := make([]SourceObservation, len(page.Records))
		for i := range page.Records {
			if page.Records[i].Representation != request.Binding.Representation {
				sourceResetRequired(ctx, "record_representation_changed")
				return NewError("RESET_REQUIRED", "Source binding changed.")
			}
			observations[i] = SourceObservation{Record: CompactSourceRecord(page.Records[i])}
		}
		snapshotItems += len(observations)
		if len(observations) != 0 {
			if err = fenced.Snapshot(ctx, observations); err != nil {
				return err
			}
		}
		if page.NextCursor == nil {
			break
		}
		if _, ok := seen[*page.NextCursor]; ok {
			return NewError("PROVIDER_ERROR", "Source repeated a cursor.")
		}
		seen[*page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return fenced.EndSnapshot(ctx, coverage, ContinuityNotApplicable)
}

type controllerSourceEmitter[T any] struct {
	controller       *Controller[T]
	route            AccessRoute
	descriptor       ResourceType
	request          SourceOpenRequest
	target           ReadTarget
	binding          Representation
	emitter          SourceEmitter
	reset            atomic.Bool
	cancel           context.CancelFunc
	streamCtx        context.Context
	leaseCtx         context.Context
	updates          bool
	mu               sync.Mutex
	nextCatalogCheck time.Time
}

func (e *controllerSourceEmitter[T]) bindingReset(ctx context.Context) error {
	sourceResetRequired(ctx, "binding_changed")
	err := e.emitter.Reset(e.streamCtx, SourceResetBindingChanged, "Source binding changed.")
	e.reset.Store(true)
	e.cancel()
	return err
}
func (e *controllerSourceEmitter[T]) checkCatalog(ctx context.Context) error {
	catalog, err := e.controller.Catalog(ctx, CatalogRequest{Route: e.route})
	if err != nil {
		return err
	}
	if err = CheckSourceCatalog(ctx, catalog, e.request, e.descriptor); err != nil {
		if ErrorCode(err) != "RESET_REQUIRED" {
			return err
		}
		if resetErr := e.bindingReset(ctx); resetErr != nil {
			return resetErr
		}
		return NewError("RESET_REQUIRED", "Source binding changed.")
	}
	return nil
}
func (e *controllerSourceEmitter[T]) fence(ctx context.Context) error {
	if e.leaseCtx.Err() != nil && e.streamCtx.Err() == nil {
		if err := e.bindingReset(ctx); err != nil {
			return err
		}
		return NewError("RESET_REQUIRED", "Source binding changed.")
	}
	if !e.updates {
		return ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Now().Before(e.nextCatalogCheck) {
		return ctx.Err()
	}
	e.nextCatalogCheck = time.Now().Add(time.Second)
	return e.checkCatalog(ctx)
}
func (e *controllerSourceEmitter[T]) Snapshot(ctx context.Context, observations []SourceObservation) error {
	if err := e.fence(ctx); err != nil {
		return err
	}
	expected := e.binding
	for _, observation := range observations {
		if observation.Record.Representation != expected {
			sourceResetRequired(ctx, "record_representation_changed")
			return NewError("RESET_REQUIRED", "Source binding changed.")
		}
		if err := checkSourceObservation(e.descriptor, e.target, observation); err != nil {
			return err
		}
	}
	return e.emitter.Snapshot(ctx, observations)
}
func (e *controllerSourceEmitter[T]) EndSnapshot(ctx context.Context, coverage SourceCoverage, continuity string) error {
	if err := e.fence(ctx); err != nil {
		return err
	}
	return e.emitter.EndSnapshot(ctx, coverage, continuity)
}
func (e *controllerSourceEmitter[T]) Changes(ctx context.Context, changes []SourceChange, coverage *SourceCoverage) error {
	if err := e.fence(ctx); err != nil {
		return err
	}
	expected := e.binding
	for _, change := range changes {
		switch change.Kind {
		case SourceChangeUpsert:
			if change.Observation == nil || change.Reference != nil || change.SourceRevision != nil {
				return NewError("PROVIDER_ERROR", "invalid source upsert")
			}
			if change.Observation.Record.Representation != expected {
				sourceResetRequired(ctx, "record_representation_changed")
				return NewError("RESET_REQUIRED", "Source binding changed.")
			}
			if err := checkSourceObservation(e.descriptor, e.target, *change.Observation); err != nil {
				return err
			}
		case SourceChangeRemove:
			if change.Reference == nil || change.Observation != nil {
				return NewError("PROVIDER_ERROR", "invalid source removal")
			}
			if err := token(change.SourceRevision); err != nil {
				return NewError("PROVIDER_ERROR", "invalid source removal")
			}
			if err := checkSourceReference(e.descriptor, e.target, *change.Reference); err != nil {
				return err
			}
		default:
			return NewError("PROVIDER_ERROR", "invalid source change")
		}
	}
	return e.emitter.Changes(ctx, changes, coverage)
}
func (e *controllerSourceEmitter[T]) Reset(ctx context.Context, reason, message string) error {
	if err := e.fence(ctx); err != nil {
		return err
	}
	err := e.emitter.Reset(ctx, reason, message)
	if err == nil {
		e.reset.Store(true)
	}
	return err
}

var _ SourceProvider = (*Controller[struct{}])(nil)
