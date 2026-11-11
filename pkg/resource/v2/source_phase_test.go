package resourcev2

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func phaseRequest(mode string) SourceOpenRequest {
	values := []string{"雪"}
	return SourceOpenRequest{SessionID: "session-1", Generation: "generation:1", Target: ReadTarget{Route: AccessRoute{Connection: "route"}, Provider: "provider", Authority: "opaque-authority", Type: "resource/type", Representation: "native", Scope: QueryScope{{Dimension: "region", Match: ScopeOneOf, Values: &values}}}, Binding: RepresentationBinding{Representation: codecRecord().Representation}, Mode: mode, Acquisition: SourceAcquisition{Kind: SourceAcquisitionEnumerate}, Limits: SourceLimits{MaxFrameBytes: 64 << 10, MaxBatchItems: 2}}
}
func phaseFrame(sequence uint64) SourceFrame {
	return SourceFrame{SessionID: "session-1", Generation: "generation:1", Sequence: sequence}
}
func phaseBegin(sequence uint64, mode string) SourceFrame {
	f := phaseFrame(sequence)
	f.Begin = &SourceBegin{Mode: mode}
	return f
}
func phaseEnd(sequence uint64, continuity string) SourceFrame {
	f := phaseFrame(sequence)
	f.SnapshotEnd = &SourceSnapshotEnd{Coverage: SourceCoverage{Enumeration: EnumerationExhausted, Consistency: ConsistencyBestEffort}, Continuity: continuity}
	return f
}
func phaseAdmit(t *testing.T, tr *SourcePhaseTracker, f SourceFrame) {
	t.Helper()
	if err := tr.Admit(f); err != nil {
		t.Fatalf("admit: %v", err)
	}
}
func phaseCode(t *testing.T, err error, code string) {
	t.Helper()
	if ErrorCode(err) != code {
		t.Fatalf("got %v (%s), want %s", err, ErrorCode(err), code)
	}
}

func TestSourceRequestValidation(t *testing.T) {
	r := phaseRequest(SourceModeSnapshot)
	if err := ValidateSourceOpenRequest(r); err != nil {
		t.Fatal(err)
	}
	query := Value{Kind: "string", String: "query"}
	for _, tc := range []struct {
		name   string
		change func(*SourceOpenRequest)
		code   string
	}{
		{"token empty", func(r *SourceOpenRequest) { r.SessionID = "" }, "INVALID_REQUEST"},
		{"token characters", func(r *SourceOpenRequest) { r.Generation = "a/b" }, "INVALID_REQUEST"},
		{"token length", func(r *SourceOpenRequest) { r.SessionID = strings.Repeat("x", MaxSourceTokenBytes+1) }, "INVALID_REQUEST"},
		{"target", func(r *SourceOpenRequest) { r.Target.Route.Connection = "" }, "INVALID_REQUEST"},
		{"frame minimum", func(r *SourceOpenRequest) { r.Limits.MaxFrameBytes = MinSourceFrameBytes - 1 }, "INVALID_REQUEST"},
		{"frame maximum", func(r *SourceOpenRequest) { r.Limits.MaxFrameBytes = MaxSourceFrameBytes + 1 }, "INVALID_REQUEST"},
		{"batch zero", func(r *SourceOpenRequest) { r.Limits.MaxBatchItems = 0 }, "INVALID_REQUEST"},
		{"batch maximum", func(r *SourceOpenRequest) { r.Limits.MaxBatchItems = MaxSourceBatchItems + 1 }, "INVALID_REQUEST"},
		{"mode", func(r *SourceOpenRequest) { r.Mode = "other" }, "INVALID_REQUEST"},
		{"acquisition", func(r *SourceOpenRequest) { r.Acquisition.Kind = "other" }, "INVALID_REQUEST"},
		{"enumerate payload", func(r *SourceOpenRequest) { r.Acquisition.Query = &query }, "INVALID_REQUEST"},
		{"missing query", func(r *SourceOpenRequest) {
			r.Acquisition = SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: "sql"}
		}, "INVALID_REQUEST"},
		{"native updates", func(r *SourceOpenRequest) {
			r.Mode = SourceModeSnapshotAndUpdates
			r.Acquisition = SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: "sql", Query: &query}
		}, "UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := phaseRequest(SourceModeSnapshot)
			tc.change(&r)
			phaseCode(t, ValidateSourceOpenRequest(r), tc.code)
		})
	}
	r.Acquisition = SourceAcquisition{Kind: SourceAcquisitionNativeQuery, Language: "sql", Query: &query}
	if err := ValidateSourceOpenRequest(r); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePhaseRules(t *testing.T) {
	mode := SourceModeSnapshotAndUpdates
	newTracker := func() *SourcePhaseTracker { return NewSourcePhaseTracker(phaseRequest(mode), ContinuityBestEffort) }
	begin := phaseBegin(1, mode)
	end := phaseEnd(2, ContinuityBestEffort)
	change := phaseFrame(3)
	change.Changes = &SourceChanges{Changes: []SourceChange{{Kind: SourceChangeRemove, Reference: &codecRecordReference}}}
	for _, tc := range []struct {
		name   string
		prefix []SourceFrame
		frame  SourceFrame
		code   string
	}{
		{"first begin", nil, begin, ""},
		{"sequence gap", nil, phaseBegin(2, mode), "RESET_REQUIRED"},
		{"sequence duplicate", []SourceFrame{begin}, begin, "RESET_REQUIRED"},
		{"echo", nil, func() SourceFrame { f := begin; f.Generation = "other"; return f }(), "PROVIDER_ERROR"},
		{"first snapshot", nil, func() SourceFrame { f := phaseFrame(1); f.Snapshot = &SourceSnapshot{}; return f }(), "PROVIDER_ERROR"},
		{"wrong mode", nil, phaseBegin(1, SourceModeSnapshot), "PROVIDER_ERROR"},
		{"duplicate begin", []SourceFrame{begin}, phaseBegin(2, mode), "PROVIDER_ERROR"},
		{"changes before end", []SourceFrame{begin}, func() SourceFrame { f := change; f.Sequence = 2; return f }(), "PROVIDER_ERROR"},
		{"valid boundary", []SourceFrame{begin}, end, ""},
		{"duplicate boundary", []SourceFrame{begin, end}, phaseEnd(3, ContinuityBestEffort), "PROVIDER_ERROR"},
		{"valid changes", []SourceFrame{begin, end}, change, ""},
		{"too strong continuity", []SourceFrame{begin}, phaseEnd(2, ContinuityGapFree), "PROVIDER_ERROR"},
		{"invalid continuity", []SourceFrame{begin}, phaseEnd(2, ContinuityNotApplicable), "PROVIDER_ERROR"},
		{"bad coverage", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Enumeration = "complete"
			return f
		}(), "PROVIDER_ERROR"},
		{"valid limitations", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Limitations = []string{strings.Repeat("a", 256)}
			return f
		}(), ""},
		{"bad consistency", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Consistency = "other"
			return f
		}(), "PROVIDER_ERROR"},
		{"empty limitation", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Limitations = []string{""}
			return f
		}(), "PROVIDER_ERROR"},
		{"long limitation", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Limitations = []string{strings.Repeat("a", 64<<10+1)}
			return f
		}(), "PROVIDER_ERROR"},
		{"many limitations", []SourceFrame{begin}, func() SourceFrame {
			f := phaseEnd(2, ContinuityBestEffort)
			f.SnapshotEnd.Coverage.Limitations = make([]string, MaxSourceLimitations+1)
			return f
		}(), "PROVIDER_ERROR"},
		{"missing payload", nil, phaseFrame(1), "PROVIDER_ERROR"},
		{"multiple payloads", nil, func() SourceFrame { f := begin; f.Failed = &SourceFailure{Code: "INTERNAL"}; return f }(), "PROVIDER_ERROR"},
		{"snapshot batch limit", []SourceFrame{begin}, func() SourceFrame {
			f := phaseFrame(2)
			f.Snapshot = &SourceSnapshot{Observations: make([]SourceObservation, 3)}
			return f
		}(), "PROVIDER_ERROR"},
		{"changes batch limit", []SourceFrame{begin, end}, func() SourceFrame {
			f := phaseFrame(3)
			f.Changes = &SourceChanges{Changes: make([]SourceChange, 3)}
			return f
		}(), "PROVIDER_ERROR"},
		{"reset valid", []SourceFrame{begin}, func() SourceFrame { f := phaseFrame(2); f.Reset = &SourceReset{Reason: SourceResetOverflow}; return f }(), ""},
		{"reset before begin", nil, func() SourceFrame { f := phaseFrame(1); f.Reset = &SourceReset{Reason: SourceResetOverflow}; return f }(), "PROVIDER_ERROR"},
		{"reset reason", []SourceFrame{begin}, func() SourceFrame { f := phaseFrame(2); f.Reset = &SourceReset{Reason: "other"}; return f }(), "PROVIDER_ERROR"},
		{"reset message", []SourceFrame{begin}, func() SourceFrame {
			f := phaseFrame(2)
			f.Reset = &SourceReset{Reason: SourceResetOverflow, Message: strings.Repeat("a", MaxSourceMessageBytes+1)}
			return f
		}(), "PROVIDER_ERROR"},
		{"failed open", nil, func() SourceFrame { f := phaseFrame(1); f.Failed = &SourceFailure{Code: "UNSUPPORTED"}; return f }(), "PROVIDER_ERROR"},
		{"failure code", nil, func() SourceFrame { f := phaseFrame(1); f.Failed = &SourceFailure{Code: "other"}; return f }(), "PROVIDER_ERROR"},
		{"failure message", nil, func() SourceFrame {
			f := phaseFrame(1)
			f.Failed = &SourceFailure{Code: "INTERNAL", Message: strings.Repeat("a", MaxSourceMessageBytes+1)}
			return f
		}(), "PROVIDER_ERROR"},
		{"frame bytes", []SourceFrame{begin}, func() SourceFrame {
			f := phaseFrame(2)
			r := codecRecord()
			r.Body.Map["large"] = Value{Kind: "string", String: strings.Repeat("x", 64<<10)}
			f.Snapshot = &SourceSnapshot{Observations: []SourceObservation{{Record: CompactSourceRecord(r)}}}
			return f
		}(), "RESOURCE_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker()
			for _, f := range tc.prefix {
				phaseAdmit(t, tr, f)
			}
			phaseCode(t, tr.Admit(tc.frame), tc.code)
		})
	}
}

var codecRecordReference = codecRecord().Reference

func TestSourceObservationBinding(t *testing.T) {
	target := phaseRequest(SourceModeSnapshot).Target
	o := SourceObservation{Record: CompactSourceRecord(codecRecord())}
	if err := ValidateSourceObservation(target, "native", o); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SourceObservation){
		func(o *SourceObservation) { o.Record.Reference.Address.Provider = "other" },
		func(o *SourceObservation) { o.Record.Reference.Address.Authority = "other" },
		func(o *SourceObservation) { o.Record.Reference.Address.Type = "other" },
		func(o *SourceObservation) { o.Record.Reference.Address.Scope[0].Value = "other" },
		func(o *SourceObservation) { o.Record.Representation.ID = "other" },
		func(o *SourceObservation) { o.Record.Body.Kind = "null" },
	} {
		o := SourceObservation{Record: CompactSourceRecord(codecRecord())}
		change(&o)
		if ValidateSourceObservation(target, "native", o) == nil {
			t.Fatal("invalid observation admitted")
		}
	}
	tr := NewSourcePhaseTracker(phaseRequest(SourceModeSnapshotAndUpdates), ContinuityGapFree)
	phaseAdmit(t, tr, phaseBegin(1, SourceModeSnapshotAndUpdates))
	f := phaseFrame(2)
	f.Snapshot = &SourceSnapshot{Observations: []SourceObservation{{Record: CompactSourceRecord(codecRecord())}}}
	phaseAdmit(t, tr, f)
	phaseAdmit(t, tr, phaseEnd(3, ContinuityGapFree))
	r := codecRecord().Reference
	r.Address.Scope[0].Value = "outside"
	f = phaseFrame(4)
	f.Changes = &SourceChanges{Changes: []SourceChange{{Kind: SourceChangeRemove, Reference: &r}}}
	phaseCode(t, tr.Admit(f), "PROVIDER_ERROR")
}

func TestSourceTermination(t *testing.T) {
	tr := NewSourcePhaseTracker(phaseRequest(SourceModeSnapshot), "")
	phaseCode(t, tr.TransportError(io.EOF), "RESET_REQUIRED")
	cause := errors.New("connection lost")
	err := tr.TransportError(cause)
	phaseCode(t, err, "RESET_REQUIRED")
	if !errors.Is(err, cause) {
		t.Fatal("transport cause was lost")
	}
	phaseAdmit(t, tr, phaseBegin(1, SourceModeSnapshot))
	phaseCode(t, tr.Admit(phaseEnd(2, ContinuityBestEffort)), "PROVIDER_ERROR")
	phaseAdmit(t, tr, phaseEnd(2, ContinuityNotApplicable))
	if !errors.Is(tr.TransportError(io.EOF), io.EOF) {
		t.Fatal("expected EOF")
	}
	phaseCode(t, tr.Admit(phaseBegin(3, SourceModeSnapshot)), "PROVIDER_ERROR")
	tr.Close()
	tr.Close()
	phaseCode(t, tr.TransportError(io.EOF), "CANCELLED")
	phaseCode(t, tr.Admit(phaseBegin(3, SourceModeSnapshot)), "CANCELLED")
	for _, reset := range []bool{true, false} {
		tr := NewSourcePhaseTracker(phaseRequest(SourceModeSnapshotAndUpdates), ContinuityBestEffort)
		phaseAdmit(t, tr, phaseBegin(1, SourceModeSnapshotAndUpdates))
		f := phaseFrame(2)
		if reset {
			f.Reset = &SourceReset{Reason: SourceResetContinuityLost}
		} else {
			f.Failed = &SourceFailure{Code: "INTERNAL"}
		}
		phaseAdmit(t, tr, f)
		if !errors.Is(tr.TransportError(io.EOF), io.EOF) {
			t.Fatal("terminal EOF")
		}
	}
}

func TestSourcePhaseAdmitSized(t *testing.T) {
	r := phaseRequest(SourceModeSnapshot)
	tracker := NewSourcePhaseTracker(r, "")
	begin := phaseBegin(1, r.Mode)
	phaseCode(t, tracker.AdmitSized(begin, int(r.Limits.MaxFrameBytes)+1), "RESOURCE_LIMIT")
	if err := tracker.AdmitSized(begin, int(r.Limits.MaxFrameBytes)); err != nil {
		t.Fatalf("retry after size rejection: %v", err)
	}
	end := phaseEnd(2, ContinuityNotApplicable)
	end.SnapshotEnd.Coverage.Limitations = make([]string, MaxSourceLimitations+1)
	phaseCode(t, tracker.AdmitSized(end, 1), "PROVIDER_ERROR")
	end.SnapshotEnd.Coverage.Limitations = nil
	if err := tracker.AdmitSized(end, 1); err != nil {
		t.Fatalf("retry after invalid frame: %v", err)
	}
}
