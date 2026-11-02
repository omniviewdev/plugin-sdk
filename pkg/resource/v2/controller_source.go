package resourcev2

import (
	"context"
	"sync/atomic"
	"unicode/utf8"
)

// OpenSource dispatches a source session under a route lease and catalog fence.
func (c *Controller[T]) OpenSource(ctx context.Context, request SourceOpenRequest, emitter SourceEmitter) (err error) {
	var registration *Registration[T]
	defer c.guard(&registration, &err)
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
	sourceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	fenced := &controllerSourceEmitter[T]{controller: c, route: target.Route, emitter: emitter, cancel: cancel, streamCtx: streamCtx}
	if operation == OperationSourceUpdates {
		var representation Representation
		for _, candidate := range descriptor.Representations {
			if candidate.ID == target.Representation {
				representation = candidate
				break
			}
		}
		err = registration.Source(sourceCtx, client, SourceRequest{Target: target, Representation: representation}, fenced)
		if fenced.reset.Load() {
			return nil
		}
		if ctx.Err() != nil && streamCtx.Err() == nil {
			return fenced.bindingReset(streamCtx)
		}
		if err == nil {
			if fenceErr := c.fenceCatalog(ctx, target.Route); fenceErr != nil {
				return fenced.bindingReset(ctx)
			}
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
	pages := 0
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
		if err = c.fenceCatalog(ctx, target.Route); err != nil {
			return err
		}
		if err = checkPage(page, descriptor, target, size); err != nil {
			return err
		}
		pages++
		if page.Coverage.Kind == "unknown" {
			coverage.Enumeration = EnumerationUnknown
		} else if page.Coverage.Kind == "partial" && coverage.Enumeration != EnumerationUnknown {
			coverage.Enumeration = EnumerationPartial
		}
		if page.Coverage.Reason != "" {
			reason := page.Coverage.Reason
			if len(reason) > 256 {
				reason = reason[:256]
				for !utf8.ValidString(reason) {
					reason = reason[:len(reason)-1]
				}
			}
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
			observations[i] = SourceObservation{Record: page.Records[i]}
		}
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
	controller *Controller[T]
	route      AccessRoute
	emitter    SourceEmitter
	reset      atomic.Bool
	cancel     context.CancelFunc
	streamCtx  context.Context
}

func (e *controllerSourceEmitter[T]) bindingReset(ctx context.Context) error {
	err := e.emitter.Reset(e.streamCtx, SourceResetBindingChanged, "Source binding changed.")
	e.reset.Store(true)
	e.cancel()
	return err
}
func (e *controllerSourceEmitter[T]) fence(ctx context.Context) error {
	if err := e.controller.fenceCatalog(ctx, e.route); err != nil {
		if resetErr := e.bindingReset(ctx); resetErr != nil {
			return resetErr
		}
		return NewError("RESET_REQUIRED", "Source binding changed.")
	}
	return nil
}
func (e *controllerSourceEmitter[T]) Snapshot(ctx context.Context, observations []SourceObservation) error {
	if err := e.fence(ctx); err != nil {
		return err
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
