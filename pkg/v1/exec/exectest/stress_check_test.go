package exectest

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The checks of the stress harness are plain functions of what a reader saw and what the engine reported, so that each
// one can be shown red on a synthetic violation and green on the same input without it. A check that cannot fail would
// make the end-state invariant a hope, which is what these tests keep it from being.

// mustFind fails the test unless one of the violations holds every one of the phrases.
func mustFind(t *testing.T, violations []string, phrases ...string) {
	t.Helper()
	for _, v := range violations {
		found := true
		for _, phrase := range phrases {
			found = found && strings.Contains(v, phrase)
		}
		if found {
			return
		}
	}
	t.Errorf("no violation holds %q; the violations are %q", phrases, violations)
}

// mustFindNothing fails the test when there is a violation.
func mustFindNothing(t *testing.T, violations []string) {
	t.Helper()
	if len(violations) != 0 {
		t.Errorf("violations %q on input that breaks no rule", violations)
	}
}

func TestTheAuditTakesAnUnbrokenSequenceOfBytesAndReportedDropsWithoutComplaint(t *testing.T) {
	audit := outputAudit{patterned: true}
	mustFindNothing(t, audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 0, N: 10}, patternBytes(0, 10), false))
	mustFindNothing(t, audit.take(exechook.Chunk{
		Stream: execv2.Stream_STREAM_STDOUT, Offset: 15, N: 3,
		Dropped: []exechook.Drop{{Stream: execv2.Stream_STREAM_STDOUT, From: 10, Bytes: 5}},
	}, patternBytes(15, 3), false))
	mustFindNothing(t, audit.take(exechook.Chunk{Offset: 20, Dropped: []exechook.Drop{{From: 18, Bytes: 2}}}, nil, false))
	mustFindNothing(t, audit.take(exechook.Chunk{Offset: 20}, nil, true))
	if audit.next != 20 || audit.read != 13 || audit.dropped != 7 {
		t.Errorf("the audit is at %d having read %d and dropped %d, want 20, 13 and 7", audit.next, audit.read, audit.dropped)
	}
}

func TestAHoleThatWasNotReportedAsADropIsFound(t *testing.T) {
	audit := outputAudit{}
	mustFindNothing(t, audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 0, N: 10}, make([]byte, 10), false))
	violations := audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 11, N: 3}, make([]byte, 3), false)
	mustFind(t, violations, "offset 11", "reader is at 10")
}

func TestBytesReadTwiceAreFound(t *testing.T) {
	audit := outputAudit{}
	mustFindNothing(t, audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 0, N: 10}, make([]byte, 10), false))
	violations := audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 8, N: 4}, make([]byte, 4), false)
	mustFind(t, violations, "offset 8", "reader is at 10")
}

func TestADropThatDoesNotStartWhereTheReaderIsIsFound(t *testing.T) {
	audit := outputAudit{}
	mustFindNothing(t, audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 0, N: 4}, make([]byte, 4), false))
	violations := audit.take(exechook.Chunk{Offset: 9, Dropped: []exechook.Drop{{From: 6, Bytes: 3}}}, nil, false)
	mustFind(t, violations, "dropped range", "starts at 6", "reader is at 4")
}

func TestAnEmptyDroppedRangeIsFound(t *testing.T) {
	audit := outputAudit{}
	violations := audit.take(exechook.Chunk{Offset: 0, Dropped: []exechook.Drop{{From: 0, Bytes: 0}}}, nil, false)
	mustFind(t, violations, "empty dropped range")
}

func TestABytePatternThatIsNotWhatTheTargetWroteAtItsOffsetIsFound(t *testing.T) {
	data := patternBytes(100, 8)
	data[5] ^= 0xff
	audit := outputAudit{patterned: true, next: 100}
	violations := audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 100, N: 8}, data, false)
	mustFind(t, violations, "offset 105", "not what was written")

	// The same bytes of a target that writes no pattern are no violation.
	plain := outputAudit{next: 100}
	mustFindNothing(t, plain.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 100, N: 8}, data, false))
}

func TestAStreamThatTheCommandCannotHaveIsFound(t *testing.T) {
	terminal := outputAudit{tty: true}
	mustFind(t, terminal.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDERR, Offset: 0, N: 1}, []byte{0}, false), "stream", "terminal")
	mustFindNothing(t, (&outputAudit{tty: true}).take(exechook.Chunk{Stream: execv2.Stream_STREAM_TTY, Offset: 0, N: 1}, []byte{0}, false))
	plain := outputAudit{}
	mustFind(t, plain.take(exechook.Chunk{Stream: execv2.Stream_STREAM_TTY, Offset: 0, N: 1}, []byte{0}, false), "stream")
	mustFindNothing(t, (&outputAudit{}).take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDERR, Offset: 0, N: 1}, []byte{0}, false))
}

func TestAReadThatReturnsNothingAndIsNotTheEndIsFound(t *testing.T) {
	audit := outputAudit{}
	mustFind(t, audit.take(exechook.Chunk{Offset: 0}, nil, false), "neither bytes nor a dropped range")
	mustFindNothing(t, (&outputAudit{}).take(exechook.Chunk{Offset: 0}, nil, true))
}

func TestTheEndOfTheOutputAtAnOffsetThatIsNotWhereTheReaderIsIsFound(t *testing.T) {
	audit := outputAudit{}
	mustFindNothing(t, audit.take(exechook.Chunk{Stream: execv2.Stream_STREAM_STDOUT, Offset: 0, N: 6}, make([]byte, 6), false))
	mustFind(t, audit.take(exechook.Chunk{Offset: 9}, nil, true), "offset 9", "reader is at 6")
}

// ---------------------------------------------------------------------------------------------------------------
// the result at rest
// ---------------------------------------------------------------------------------------------------------------

func exitedEnd(code int64) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Exited{Exited: &execv2.Exited{ExitCode: code, Source: execv2.ExitSource_EXIT_SOURCE_PROCESS}}, RemoteMaySurvive: new(false)}
}

func closedEnd(by execv2.ClosedBy) *execv2.End {
	return &execv2.End{Kind: &execv2.End_Closed{Closed: &execv2.Closed{By: by}}, RemoteMaySurvive: new(false)}
}

// goodResult is the result of a command that ran to its exit 0 and wrote 100 bytes, all of which the reader read.
func goodResult() (exechook.Result, outputAudit) {
	return exechook.Result{
		End: exitedEnd(0), Cause: "finished", BytesOut: 100, Output: execv2.OutputCompleteness_OUTPUT_COMPLETENESS_COMPLETE,
	}, outputAudit{next: 100, read: 100}
}

func TestAResultIsHeldToTheInvariantsOfTheEndState(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*exechook.Result, *outputAudit)
		want   []string // the phrases of the violation that must be found; none: the result is good
	}{
		{"a command that exited and was read to the end", func(*exechook.Result, *outputAudit) {}, nil},
		{"a command whose output was truncated and every dropped byte accounted for", func(r *exechook.Result, a *outputAudit) {
			r.Output, r.DroppedBytes = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED, 40
			a.read, a.dropped = 60, 40
		}, nil},
		{"a command ended by the plugin's shutdown", func(r *exechook.Result, _ *outputAudit) {
			r.End, r.Cause = closedEnd(execv2.ClosedBy_CLOSED_BY_PLUGIN_SHUTDOWN), "shutdown"
			r.Output = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_UNSPECIFIED
		}, nil},
		{"complete output with bytes dropped", func(r *exechook.Result, a *outputAudit) {
			r.DroppedBytes = 5
			a.read, a.dropped = 95, 5
		}, []string{"COMPLETE", "5 bytes were dropped"}},
		{"complete output the reader did not read in full", func(r *exechook.Result, a *outputAudit) {
			r.BytesOut = 101
			a.read, a.next = 100, 100
		}, []string{"COMPLETE", "read 100 of 101"}},
		{"bytes counted twice", func(_ *exechook.Result, a *outputAudit) {
			a.read, a.dropped = 100, 3
		}, []string{"read 100", "dropped 3", "wrote 100"}},
		{"bytes missing", func(_ *exechook.Result, a *outputAudit) {
			a.read = 99
		}, []string{"read 99", "wrote 100"}},
		{"a reader told of more bytes lost than the result says", func(r *exechook.Result, a *outputAudit) {
			r.Output, r.BytesOut = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED, 100
			r.DroppedBytes = 10
			a.read, a.dropped = 80, 20
		}, []string{"20 bytes", "result says 10"}},
		{"no end", func(r *exechook.Result, _ *outputAudit) { r.End = nil }, []string{"no arm"}},
		{"an end with no kind", func(r *exechook.Result, _ *outputAudit) { r.End = &execv2.End{} }, []string{"no arm"}},
		{"an arm that holds nothing", func(r *exechook.Result, _ *outputAudit) {
			r.End = &execv2.End{Kind: &execv2.End_Exited{}}
		}, []string{"no arm"}},
		{"a cause that is not a word of the engine", func(r *exechook.Result, _ *outputAudit) { r.Cause = "bogus" }, []string{`cause "bogus"`}},
		{"no cause", func(r *exechook.Result, _ *outputAudit) { r.Cause = "" }, []string{`cause ""`}},
		{"a command that finished and ended Closed", func(r *exechook.Result, _ *outputAudit) {
			r.End = closedEnd(execv2.ClosedBy_CLOSED_BY_REQUEST)
		}, []string{"finished", "closed"}},
		{"a command that was ended by a request and ended Exited", func(r *exechook.Result, _ *outputAudit) {
			r.Cause = "requested"
		}, []string{"requested", "exited"}},
		{"a command ended by shutdown that says it was closed by request", func(r *exechook.Result, _ *outputAudit) {
			r.End, r.Cause = closedEnd(execv2.ClosedBy_CLOSED_BY_REQUEST), "shutdown"
		}, []string{"shutdown", "CLOSED_BY_REQUEST", "CLOSED_BY_PLUGIN_SHUTDOWN"}},
		{"an output completeness the contract does not know", func(r *exechook.Result, _ *outputAudit) {
			r.Output = execv2.OutputCompleteness(99)
		}, []string{"completeness", "99"}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			res, audit := goodResult()
			tc.mutate(&res, &audit)
			violations := checkResult(res, audit)
			if tc.want == nil {
				mustFindNothing(t, violations)
				return
			}
			mustFind(t, violations, tc.want...)
		})
	}
}

func TestEveryArmOfAnEndIsNamedAndOnlyOneArmAtATime(t *testing.T) {
	for want, end := range map[string]*execv2.End{
		"exited":   {Kind: &execv2.End_Exited{Exited: &execv2.Exited{}}},
		"signaled": {Kind: &execv2.End_Signaled{Signaled: &execv2.Signaled{}}},
		"closed":   {Kind: &execv2.End_Closed{Closed: &execv2.Closed{}}},
		"failed":   {Kind: &execv2.End_Failed{Failed: &execv2.Failed{}}},
		"lost":     {Kind: &execv2.End_Lost{Lost: &execv2.Lost{}}},
	} {
		if arm, ok := endArm(end); !ok || arm != want {
			t.Errorf("endArm = %q, %v; want %q", arm, ok, want)
		}
	}
	// The five arms of the contract are the five this check knows: a new arm must be added to the harness on purpose.
	arms := execv2.File_proto_exec_v2_exec_proto.Messages().ByName("End").Oneofs().ByName("kind").Fields()
	if arms.Len() != 5 {
		t.Errorf("End has %d arms, the stress harness knows 5: teach endArm the new one", arms.Len())
	}
	if arm, ok := endArm(nil); ok {
		t.Errorf("a nil End has the arm %q", arm)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// a second end: the result of an ended command never changes
// ---------------------------------------------------------------------------------------------------------------

func TestAResultThatChangesAfterTheEndIsASecondEnd(t *testing.T) {
	first, _ := goodResult()
	if diff := sameResult(first, first); diff != "" {
		t.Fatalf("the same result differs from itself: %s", diff)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*exechook.Result)
		want   string
	}{
		{"another arm", func(r *exechook.Result) { r.End = closedEnd(execv2.ClosedBy_CLOSED_BY_REQUEST) }, "end"},
		{"another exit code", func(r *exechook.Result) { r.End = exitedEnd(9) }, "end"},
		{"another cause", func(r *exechook.Result) { r.Cause = "requested" }, "cause"},
		{"more bytes", func(r *exechook.Result) { r.BytesOut++ }, "bytes out"},
		{"a drop that came later", func(r *exechook.Result) { r.DroppedBytes = 1 }, "dropped"},
		{"a write that came late", func(r *exechook.Result) { r.LateWrites = 1 }, "late writes"},
		{"another completeness", func(r *exechook.Result) { r.Output = execv2.OutputCompleteness_OUTPUT_COMPLETENESS_TRUNCATED }, "output"},
		{"a start that was not observed", func(r *exechook.Result) { r.StartObserved = !r.StartObserved }, "start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, _ := goodResult()
			tc.mutate(&now)
			if diff := sameResult(first, now); !strings.Contains(diff, tc.want) {
				t.Errorf("sameResult = %q, want it to name %q", diff, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// refusals
// ---------------------------------------------------------------------------------------------------------------

func TestARefusalIsNamedByItsClassAndReasonAndAnythingElseIsSaidToBeUnknown(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&exec.Error{Class: exec.ErrorClassUnavailable}, "UNAVAILABLE"},
		{&exec.Error{Class: exec.ErrorClassLimit, Reason: exec.ErrorReasonTooManySessions}, "LIMIT/TOO_MANY_SESSIONS"},
		{fmt.Errorf("wrapped: %w", &exec.Error{Class: exec.ErrorClassConflict, Reason: exec.ErrorReasonSessionExists}), "CONFLICT/SESSION_EXISTS"},
		{resourcev2.NewError("CONNECTION_ERROR", "not started"), "resource:CONNECTION_ERROR"},
		{errors.New("boom"), "unknown"},
	} {
		if got := refusalName(tc.err); got != tc.want {
			t.Errorf("refusalName(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestAStartAfterCloseIsHeldToBeingRefusedAsUnavailable(t *testing.T) {
	if v := checkRefusedAfterClose(&exec.Error{Class: exec.ErrorClassUnavailable}); v != "" {
		t.Errorf("a refusal as UNAVAILABLE is a violation: %s", v)
	}
	if v := checkRefusedAfterClose(nil); !strings.Contains(v, "admitted") {
		t.Errorf("a start that was admitted after Close: %q, want it said to be admitted", v)
	}
	if v := checkRefusedAfterClose(&exec.Error{Class: exec.ErrorClassLimit}); !strings.Contains(v, "LIMIT") || !strings.Contains(v, "UNAVAILABLE") {
		t.Errorf("a start refused as LIMIT after Close: %q, want both classes named", v)
	}
	if v := checkRefusedAfterClose(errors.New("boom")); !strings.Contains(v, "unknown") {
		t.Errorf("a start refused with a plain error after Close: %q, want it called unknown", v)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// the violations the harness reports
// ---------------------------------------------------------------------------------------------------------------

func TestTheViolationsOfARoundAreBoundedAndSayHowManyWereLeftOut(t *testing.T) {
	var f findings
	for i := range 3*maxFindings + 7 {
		f.add("command c%d broke a rule", i)
	}
	got := f.list()
	if len(got) != maxFindings+1 {
		t.Fatalf("%d violations listed, want the first %d and a line for the rest", len(got), maxFindings)
	}
	if !strings.Contains(got[0], "c0 ") || !strings.Contains(got[maxFindings-1], fmt.Sprintf("c%d ", maxFindings-1)) {
		t.Errorf("the first violations are not the first found: %q ... %q", got[0], got[maxFindings-1])
	}
	if !strings.Contains(got[maxFindings], fmt.Sprintf("%d more", 2*maxFindings+7)) {
		t.Errorf("the last line %q does not say how many were left out", got[maxFindings])
	}
	var none findings
	if got := none.list(); len(got) != 0 {
		t.Errorf("a round that found nothing lists %q", got)
	}
}
