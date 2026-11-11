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
	return SourceOpenRequest{SessionID: "session", Generation: "generation", Target: controllerTarget(), Binding: RepresentationBinding{Representation: controllerRepresentation()}, Mode: mode, Acquisition: SourceAcquisition{Kind: SourceAcquisitionEnumerate}, Limits: SourceLimits{MaxFrameBytes: 64 << 10, MaxBatchItems: 1}}
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

func TestControllerSourceRejectsCursorCycle(t *testing.T) {
	cfg := controllerConfig()
	cursors := []string{"a", "b", "a"}
	index := 0
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Enumerate: func(context.Context, *string, EnumerateRequest) (SourcePage, error) {
		cursor := cursors[index]
		index++
		return SourcePage{Records: []Record{}, NextCursor: &cursor, Count: Count{Kind: "unknown"}, Coverage: Coverage{Kind: "complete", Paths: []string{}}, Consistency: ConsistencyBestEffort}, nil
	}}}
	c := newTestController(t, cfg)
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshot), &recordingSourceEmitter{})
	assertControllerCode(t, err, "PROVIDER_ERROR")
}

func TestControllerSourceFenceUsesSelectedContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Catalog)
		reset  string
	}{
		{"unrelated revision and presentation", func(c *Catalog) {
			c.Revision = "changed"
			c.Types[0].DisplayName = "renamed"
			c.Types[0].Description = "new display description"
			c.Types[0].Representations = append(c.Types[0].Representations, Representation{ID: "other", Revision: "1"})
		}, ""},
		{"selected format", func(c *Catalog) {
			c.Types[0].Representations[0].Revision = "changed"
		}, SourceResetBindingChanged},
		{"selected capability withdrawal", func(c *Catalog) {
			c.Types[0].Capabilities = nil
		}, SourceResetBindingChanged},
		{"selected continuity", func(c *Catalog) {
			for i := range c.Types[0].Capabilities {
				if c.Types[0].Capabilities[i].Operation == OperationSourceUpdates {
					c.Types[0].Capabilities[i].Guarantee = ContinuityGapFree
				}
			}
		}, SourceResetBindingChanged},
		{"native identity", func(c *Catalog) {
			c.Types[0].NativeAuthorityHint = &NativeAuthorityHint{Scheme: "fixture.v1", ID: "different", Continuity: ContinuityConnectionLocal}
		}, SourceResetBindingChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := controllerConfig()
			catalog := cfg.Catalog
			changed := false
			cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
				result, err := catalog(ctx, request)
				if changed {
					tc.change(&result)
				}
				return result, err
			}
			cfg.Registrations = []Registration[string]{{Type: "opaque/type", Source: func(context.Context, *string, SourceRequest, SourceEmitter) error {
				changed = true
				return nil
			}}}
			c := newTestController(t, cfg)
			emitter := &recordingSourceEmitter{}
			err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshotAndUpdates), emitter)
			if err != nil || emitter.reset != tc.reset || len(emitter.observations) != 0 {
				t.Fatalf("reset=%q want=%q observations=%d err=%v", emitter.reset, tc.reset, len(emitter.observations), err)
			}
		})
	}
}

func TestControllerSourceDoesNotRebuildCatalogPerEmission(t *testing.T) {
	cfg := controllerConfig()
	catalog := cfg.Catalog
	calls := 0
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		calls++
		return catalog(ctx, request)
	}
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Source: func(ctx context.Context, _ *string, _ SourceRequest, emitter SourceEmitter) error {
		before := calls
		for range 100 {
			if err := emitter.Snapshot(ctx, nil); err != nil {
				return err
			}
		}
		if calls != before {
			t.Fatalf("catalog calls during emissions: %d", calls-before)
		}
		return nil
	}}}
	c := newTestController(t, cfg)
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshotAndUpdates), &recordingSourceEmitter{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestControllerSourceCatalogFailureIsNotBindingReset(t *testing.T) {
	cfg := controllerConfig()
	catalog := cfg.Catalog
	fail := false
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		if fail {
			return Catalog{}, NewError("INTERNAL", "catalog unavailable")
		}
		return catalog(ctx, request)
	}
	cfg.Registrations = []Registration[string]{{Type: "opaque/type", Source: func(context.Context, *string, SourceRequest, SourceEmitter) error {
		fail = true
		return nil
	}}}
	c := newTestController(t, cfg)
	emitter := &recordingSourceEmitter{}
	err := c.OpenSource(acceptedControllerContext(context.Background(), t, c), controllerSourceOpen(SourceModeSnapshotAndUpdates), emitter)
	if err != nil {
		t.Fatalf("source completion returned catalog error: %v", err)
	}
	if emitter.reset != "" {
		t.Fatalf("unexpected reset: %q", emitter.reset)
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

func TestControllerLiveSourceRejectsUnboundRecords(t *testing.T) {
	for _, mutation := range []string{"revision", "identity"} {
		for _, phase := range []string{"snapshot", "upsert", "remove"} {
			if phase == "remove" && mutation != "identity" {
				continue
			}
			t.Run(mutation+"/"+phase, func(t *testing.T) {
				record := controllerRecord()
				switch mutation {
				case "revision":
					record.Representation.Revision = "unaccepted-revision"
				case "identity":
					record.Reference.Address.Scope = []ScopeEntry{{Dimension: "undeclared", Value: "value"}}
				}
				if err := ValidateRecord(record); err != nil {
					t.Fatalf("regression requires a structurally valid record: %v", err)
				}
				cfg := controllerConfig()
				cfg.Registrations = []Registration[string]{{Type: "opaque/type", Source: func(ctx context.Context, _ *string, _ SourceRequest, emitter SourceEmitter) error {
					observation := SourceObservation{Record: CompactSourceRecord(record)}
					if phase == "snapshot" {
						return emitter.Snapshot(ctx, []SourceObservation{observation})
					}
					if err := emitter.EndSnapshot(ctx, SourceCoverage{Enumeration: EnumerationExhausted, Consistency: ConsistencyBestEffort}, ContinuityBestEffort); err != nil {
						return err
					}
					change := SourceChange{Kind: SourceChangeUpsert, Observation: &observation}
					if phase == "remove" {
						change = SourceChange{Kind: SourceChangeRemove, Reference: &record.Reference}
					}
					return emitter.Changes(ctx, []SourceChange{change}, nil)
				}}}
				controller := newTestController(t, cfg)
				err := controller.OpenSource(acceptedControllerContext(context.Background(), t, controller), controllerSourceOpen(SourceModeSnapshotAndUpdates), &recordingSourceEmitter{})
				code := "RESET_REQUIRED"
				if mutation == "identity" {
					code = "PROVIDER_ERROR"
				}
				assertControllerCode(t, err, code)
			})
		}
	}
}
