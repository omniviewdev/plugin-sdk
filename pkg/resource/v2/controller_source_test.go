package resourcev2

import (
	"context"
	"fmt"
	"testing"
)

type recordingSourceEmitter struct {
	observations []SourceObservation
	coverage     SourceCoverage
	continuity   string
	reset        string
}

func (e *recordingSourceEmitter) Snapshot(_ context.Context, observations []SourceObservation) error {
	e.observations = append(e.observations, observations...)
	return nil
}
func (e *recordingSourceEmitter) EndSnapshot(_ context.Context, coverage SourceCoverage, continuity string) error {
	e.coverage = coverage
	e.continuity = continuity
	return nil
}
func (e *recordingSourceEmitter) Changes(context.Context, []SourceChange, *SourceCoverage) error {
	return nil
}
func (e *recordingSourceEmitter) Reset(_ context.Context, reason, _ string) error {
	e.reset = reason
	return nil
}

func controllerSourceOpen(mode string) SourceOpenRequest {
	return SourceOpenRequest{SessionID: "session", Generation: "generation", Target: controllerTarget(), Mode: mode, Acquisition: SourceAcquisition{Kind: SourceAcquisitionEnumerate}, Limits: SourceLimits{MaxFrameBytes: MinSourceFrameBytes, MaxBatchItems: 1}}
}

func TestControllerSourcePagesAndCoverage(t *testing.T) {
	cfg := controllerConfig()
	revision, cursor := "revision", "next"
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(_ context.Context, _ *string, request EnumerateRequest) (SourcePage, error) {
		page := SourcePage{Records: []Record{controllerRecord()}, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revision}
		if request.PageSize != 1 {
			t.Fatalf("page size = %d", request.PageSize)
		}
		if request.Cursor == nil {
			page.NextCursor = &cursor
		} else {
			page.Coverage = Coverage{Kind: "partial", Paths: []string{}, Reason: "incomplete"}
		}
		return page, nil
	}}}
	c := newTestController(t, cfg)
	emitter := &recordingSourceEmitter{}
	if err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), emitter); err != nil {
		t.Fatal(err)
	}
	if len(emitter.observations) != 2 || emitter.coverage.Enumeration != EnumerationPartial || emitter.coverage.Consistency != ConsistencyPointInTime || emitter.continuity != ContinuityNotApplicable || len(emitter.coverage.Limitations) != 1 {
		t.Fatalf("unexpected source result: %+v", emitter)
	}
}

func TestControllerSourceRejectsRepeatedCursor(t *testing.T) {
	cfg := controllerConfig()
	cursor := "same"
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
		return SourcePage{Records: []Record{}, NextCursor: &cursor, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyBestEffort}, nil
	}}}
	c := newTestController(t, cfg)
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), &recordingSourceEmitter{})
	assertControllerCode(t, err, "PROVIDER_ERROR")
}

func TestControllerSourceFenceResetsChangedCatalog(t *testing.T) {
	cfg := controllerConfig()
	catalog := cfg.Catalog
	changed := false
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		result, err := catalog(ctx, request)
		if changed {
			result.Revision = "changed"
		}
		return result, err
	}
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Source: func(ctx context.Context, _ *string, _ SourceRequest, emitter SourceEmitter) error {
		changed = true
		return emitter.Snapshot(ctx, []SourceObservation{{Record: controllerRecord()}})
	}}}
	c := newTestController(t, cfg)
	emitter := &recordingSourceEmitter{}
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshotAndUpdates), emitter)
	if err != nil || emitter.reset != SourceResetBindingChanged || len(emitter.observations) != 0 {
		t.Fatalf("reset=%q observations=%d err=%v", emitter.reset, len(emitter.observations), err)
	}
}

func TestControllerSourceCoverageAndConsistency(t *testing.T) {
	revisionA, revisionB := "revision-a", "revision-b"
	tests := []struct {
		name                     string
		pages                    []SourcePage
		enumeration, consistency string
		limitations              []string
	}{
		{"single point in time", []SourcePage{{Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime}}, EnumerationExhausted, ConsistencyPointInTime, nil},
		{"best effort page", []SourcePage{{Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyBestEffort}}, EnumerationExhausted, ConsistencyBestEffort, nil},
		{"matching revisions", []SourcePage{{Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revisionA}, {Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revisionA}}, EnumerationExhausted, ConsistencyPointInTime, nil},
		{"missing revisions", []SourcePage{{Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime}, {Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime}}, EnumerationExhausted, ConsistencyBestEffort, nil},
		{"different revisions", []SourcePage{{Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revisionA}, {Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyPointInTime, SourceRevision: &revisionB}}, EnumerationExhausted, ConsistencyBestEffort, nil},
		{"partial reasons deduplicate", []SourcePage{{Coverage: Coverage{Kind: "partial", Paths: []string{}, Reason: "limited"}, Consistency: ConsistencyBestEffort}, {Coverage: Coverage{Kind: "partial", Paths: []string{}, Reason: "limited"}, Consistency: ConsistencyBestEffort}}, EnumerationPartial, ConsistencyBestEffort, []string{"limited"}},
		{"unknown dominates partial", []SourcePage{{Coverage: Coverage{Kind: "unknown", Paths: []string{}, Reason: "unknown"}, Consistency: ConsistencyBestEffort}, {Coverage: Coverage{Kind: "partial", Paths: []string{}, Reason: "partial"}, Consistency: ConsistencyBestEffort}}, EnumerationUnknown, ConsistencyBestEffort, []string{"unknown", "partial"}},
		{"partial then unknown", []SourcePage{{Coverage: Coverage{Kind: "partial", Paths: []string{}, Reason: "partial"}, Consistency: ConsistencyBestEffort}, {Coverage: Coverage{Kind: "unknown", Paths: []string{}, Reason: "unknown"}, Consistency: ConsistencyBestEffort}}, EnumerationUnknown, ConsistencyBestEffort, []string{"partial", "unknown"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := controllerConfig()
			next := "next"
			index := 0
			cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(_ context.Context, _ *string, request EnumerateRequest) (SourcePage, error) {
				page := test.pages[index]
				page.Records = []Record{}
				page.Count = Count{Kind: "unknown"}
				index++
				if index < len(test.pages) {
					page.NextCursor = &next
				}
				return page, nil
			}}}
			c := newTestController(t, cfg)
			emitter := &recordingSourceEmitter{}
			err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), emitter)
			if err != nil {
				t.Fatal(err)
			}
			if emitter.coverage.Enumeration != test.enumeration || emitter.coverage.Consistency != test.consistency || emitter.continuity != ContinuityNotApplicable {
				t.Fatalf("coverage = %+v, continuity = %q", emitter.coverage, emitter.continuity)
			}
			if len(emitter.coverage.Limitations) != len(test.limitations) {
				t.Fatalf("limitations = %v", emitter.coverage.Limitations)
			}
			for i, limitation := range test.limitations {
				if emitter.coverage.Limitations[i] != limitation {
					t.Fatalf("limitations = %v", emitter.coverage.Limitations)
				}
			}
		})
	}
}

func TestControllerSourceLimitationsAreBounded(t *testing.T) {
	cfg := controllerConfig()
	index := 0
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(_ context.Context, _ *string, _ EnumerateRequest) (SourcePage, error) {
		index++
		reason := fmt.Sprintf("reason-%d", index)
		page := SourcePage{Records: []Record{}, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "partial", Paths: []string{}, Reason: reason}, Consistency: ConsistencyBestEffort}
		if index <= MaxSourceLimitations {
			page.NextCursor = &reason
		}
		return page, nil
	}}}
	c := newTestController(t, cfg)
	emitter := &recordingSourceEmitter{}
	if err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), emitter); err != nil {
		t.Fatal(err)
	}
	if len(emitter.coverage.Limitations) != MaxSourceLimitations {
		t.Fatalf("limitations = %v", emitter.coverage.Limitations)
	}
}
