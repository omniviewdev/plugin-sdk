package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// The names of the engine's measurements, as the host reads them.
const (
	metricSessions   = "omniview.plugin.self.sdk.exec.sessions"
	metricEnded      = "omniview.plugin.self.sdk.exec.session.ended"
	metricBytes      = "omniview.plugin.self.sdk.exec.bytes"
	metricDropped    = "omniview.plugin.self.sdk.exec.dropped_bytes"
	metricStalls     = "omniview.plugin.self.sdk.exec.stalls"
	metricViolations = "omniview.plugin.self.sdk.exec.violations"
	metricAbandoned  = "omniview.plugin.self.sdk.exec.abandoned"
)

// attrsOf renders the attributes of a point as a map from the key's word to its value.
func attrsOf(point *lifecyclepb.DiagPoint) map[string]string {
	attrs := map[string]string{}
	for _, a := range point.GetAttrs() {
		attrs[strings.ToLower(strings.TrimPrefix(a.GetKey().String(), "DIAG_KEY_"))] = a.GetValue()
	}
	return attrs
}

// snapshotMetrics lists every point of the rig's diagnostics as one line: "name{key=value,...} value".
func snapshotMetrics(rig *engineRig) []string {
	metrics, _ := rig.diag.Snapshot()
	var lines []string
	for _, m := range metrics {
		for _, p := range m.GetPoints() {
			var parts []string
			for k, v := range attrsOf(p) {
				parts = append(parts, k+"="+v)
			}
			sort.Strings(parts)
			lines = append(lines, fmt.Sprintf("%s{%s} %v", m.GetName(), strings.Join(parts, ","), p.GetValue()))
		}
	}
	return lines
}

// metricValue returns the value of the point of a metric whose attributes are exactly attrs.
func metricValue(rig *engineRig, name string, attrs map[string]string) (float64, bool) {
	metrics, _ := rig.diag.Snapshot()
	for _, m := range metrics {
		if m.GetName() != name {
			continue
		}
		for _, p := range m.GetPoints() {
			got := attrsOf(p)
			if len(got) != len(attrs) {
				continue
			}
			match := true
			for k, v := range attrs {
				if got[k] != v {
					match = false
				}
			}
			if match {
				return p.GetValue(), true
			}
		}
	}
	return 0, false
}

// wantMetric asserts the value of one point.
func wantMetric(t testing.TB, rig *engineRig, name string, attrs map[string]string, want float64) {
	t.Helper()
	got, ok := metricValue(rig, name, attrs)
	if !ok || got != want {
		t.Errorf("%s%v is %v (present %v), want %v; all: %v", name, attrs, got, ok, want, snapshotMetrics(rig))
	}
}

func TestTheEnginePublishesItsSessionsTrafficAndEndsToTheDiagnostics(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, exectest.NewFakeTarget(exectest.FakeScript(
		exectest.WriteOut("0123456789"),
		exectest.WaitInput(5),
	)))
	cmd := rig.mustOpen("s1")
	if n, err := cmd.Write(context.Background(), []byte("abcde")); n != 5 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	awaitDone(t, cmd)
	running := map[string]string{"state": "running"}
	wantMetric(t, rig, metricSessions, running, 0)
	wantMetric(t, rig, metricEnded, map[string]string{"outcome": "exited"}, 1)
	wantMetric(t, rig, metricBytes, map[string]string{"direction": "out"}, 10)
	wantMetric(t, rig, metricBytes, map[string]string{"direction": "in"}, 5)

	// Each kind of end is counted under its own word.
	for _, tc := range []struct {
		id      string
		fn      exec.ExecFunc
		outcome string
	}{
		{"failed", func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.Exit{}, errors.New("x") }, "failed"},
		{"lost", func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitLost(exec.LostNoStatus), nil }, "lost"},
		{"signaled", func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.ExitSignal("SIGINT", 2), nil }, "signaled"},
	} {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(tc.fn))
		awaitDone(t, rig.mustOpen(tc.id))
		wantMetric(t, rig, metricEnded, map[string]string{"outcome": tc.outcome}, 1)
	}
	closed := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	c := closed.mustOpen("closed")
	wantMetric(t, closed, metricSessions, running, 1)
	c.Close(false)
	awaitDone(t, c)
	wantMetric(t, closed, metricEnded, map[string]string{"outcome": "closed"}, 1)
	wantMetric(t, closed, metricSessions, running, 0)
}

func TestTheSessionsGaugeCountsTheRunningSessions(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(blockUntilCancelled))
	a, b, c := rig.mustOpen("a"), rig.mustOpen("b"), rig.mustOpen("c")
	running := map[string]string{"state": "running"}
	wantMetric(t, rig, metricSessions, running, 3)
	b.Close(false)
	awaitDone(t, b)
	wantMetric(t, rig, metricSessions, running, 2)
	a.Close(false)
	c.Close(false)
	awaitDone(t, a)
	awaitDone(t, c)
	wantMetric(t, rig, metricSessions, running, 0)
}

// A session that runs for hours reports its traffic while it runs, not only when it ends.
func TestARunningSessionReportsItsTrafficPeriodically(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{metricsEvery: time.Millisecond}, exectest.NewFakeTarget(exectest.FakeScript(
		exectest.WriteOut(strings.Repeat("x", 1000)),
		exectest.Block(),
	)))
	cmd := rig.mustOpen("s1")
	eventually(t, "the traffic of a session that is still running", func() bool {
		v, ok := metricValue(rig, metricBytes, map[string]string{"direction": "out"})
		return ok && v == 1000
	})
	select {
	case <-cmd.Done():
		t.Fatal("the session ended before it reported")
	default:
	}
	cmd.Close(false)
	awaitDone(t, cmd)
	wantMetric(t, rig, metricBytes, map[string]string{"direction": "out"}, 1000) // not counted twice by the final report
}

func TestADroppedByteIsCountedUnderTheReasonThatCausedIt(t *testing.T) {
	t.Run("a lossy target", func(t *testing.T) {
		rig := newEngineRig(t, engineRigConfig{}, engTarget(writePattern(2<<20), func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallLossy }))
		res := awaitDone(t, rig.mustOpen("s1"))
		if res.DroppedBytes == 0 {
			t.Fatal("nothing was dropped")
		}
		wantMetric(t, rig, metricDropped, map[string]string{"reason": "eager_drain_overflow"}, float64(res.DroppedBytes))
	})
	t.Run("a blocking target whose session ended under a reader that did not read", func(t *testing.T) {
		filled, more := exectest.NewGate(), exectest.NewGate()
		rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
			chunk := make([]byte, 64<<10)
			for range 4 {
				_, _ = p.Stdout.Write(chunk)
			}
			filled.Open()
			<-more.Opened()
			_, err := p.Stdout.Write(make([]byte, 2<<20)) // more than the ring holds: what it loses is counted
			return exec.Exit{}, err
		}, func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
		cmd := rig.mustOpen("s1")
		awaitChannel(t, filled.Opened(), "the queue to fill")
		more.Open()
		cmd.Close(false)
		res := awaitDone(t, cmd)
		wantMetric(t, rig, metricDropped, map[string]string{"reason": "replay_full"}, float64(res.DroppedBytes))
	})
	t.Run("a blocking target whose function has more to write than its reader may leave unread is stalled", func(t *testing.T) {
		// 300 KiB cannot be left unread at once (the bound is 256 KiB): the write waits for a reader that has not started,
		// and the wait is published while the session runs, before anything reads.
		rig := newEngineRig(t, engineRigConfig{metricsEvery: time.Millisecond}, engTarget(writePattern(300<<10), func(i *exec.TargetInfo) { i.Capabilities.Stall = exec.StallPropagates }))
		cmd := rig.mustOpen("s1")
		eventually(t, "the held write to be counted as a stall", func() bool {
			v, ok := metricValue(rig, metricStalls, map[string]string{"queue": "output"})
			return ok && v >= 1
		})
		verifyPattern(t, cmd, 300<<10)
		awaitDone(t, cmd)
	})
}

// Each kind of violation is counted once for a session, however often the function repeats it.
func TestAViolationIsCountedOncePerSessionAndKind(t *testing.T) {
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		for range 100 {
			p.Link(exec.LinkGood) // undeclared
		}
		panic("x")
	}))
	for i := range 3 {
		awaitDone(t, rig.mustOpen(fmt.Sprintf("s%d", i)))
	}
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "link_undeclared"}, 3)
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "panic"}, 3)
}

func TestTheAbandonedGaugeCountsTheFunctionsTheEngineGaveUpOnUntilTheyReturn(t *testing.T) {
	stuck := exectest.NewGate()
	rig := newEngineRig(t, engineRigConfig{grace: 50 * time.Millisecond}, exectest.NewFakeTarget(exectest.FakeScript(exectest.IgnoreCancel(stuck))))
	t.Cleanup(stuck.Open)
	cmd := rig.mustOpen("s1")
	cmd.Close(false)
	awaitDone(t, cmd)
	wantMetric(t, rig, metricAbandoned, map[string]string{}, 1)
	wantMetric(t, rig, metricViolations, map[string]string{"reason": "ignored_cancel"}, 1)
	stuck.Open()
	eventually(t, "the gauge to fall", func() bool {
		v, ok := metricValue(rig, metricAbandoned, map[string]string{})
		return ok && v == 0
	})
}

// ---------------------------------------------------------------------------------------------------------------
// the session span
// ---------------------------------------------------------------------------------------------------------------

// recordSpans makes the process's tracer record what the engine starts, for the test.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	return recorder
}

// spanAttrs flattens the attributes of a span.
func spanAttrs(attrs []attribute.KeyValue) map[string]string {
	out := map[string]string{}
	for _, a := range attrs {
		out[string(a.Key)] = a.Value.Emit()
	}
	return out
}

func TestEachSessionHasOneSpanWithItsOutcomeExitDurationAndCounts(t *testing.T) {
	recorder := recordSpans(t)
	canary := "CANARY-span-error-text-6a40"
	rig := newEngineRig(t, engineRigConfig{}, engTarget(func(_ context.Context, p *exec.Proc) (exec.Exit, error) {
		_, _ = p.Stdout.Write([]byte("hello"))
		if p.Request.Labels["fail"] == "yes" {
			return exec.Exit{}, errors.New(canary)
		}
		return exec.ExitCode(7), nil
	}))
	ok := awaitDone(t, rig.mustOpen("s-ok"))
	bad := awaitDone(t, rig.mustOpen("s-bad", func(s *execv2.Start) {
		s.Labels = []*execv2.NameValue{{Name: "fail", Value: "yes"}}
	}))
	_, _ = ok, bad

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("%d spans, want one for each session", len(spans))
	}
	bySession := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		if stub.Name != "sdk.exec.session" {
			t.Errorf("span %q", stub.Name)
		}
		bySession[spanAttrs(stub.Attributes)["session_id"]] = stub
	}
	good := spanAttrs(bySession["s-ok"].Attributes)
	for key, want := range map[string]string{
		"exec.end": "exited", "exec.exit_code": "7", "exec.exit_source": "process", "exec.bytes_out": "5", "exec.bytes_in": "0",
		"exec.dropped_bytes": "0", "exec.cause": "finished", "exec.output": "complete", "resource_type": "thing",
		"connection_id": "conn-1", "tty": "true", "exec.violations": "0",
	} {
		if good[key] != want {
			t.Errorf("span attribute %s is %q, want %q (all: %v)", key, good[key], want, good)
		}
	}
	if bySession["s-ok"].Status.Code == codes.Error {
		t.Error("a command that exited with a code is not an error of the span")
	}
	failedAttrs := spanAttrs(bySession["s-bad"].Attributes)
	if failedAttrs["exec.end"] != "failed" || failedAttrs["error_code"] != "failed" || bySession["s-bad"].Status.Code != codes.Error {
		t.Errorf("the failed session's span: %v status %v", failedAttrs, bySession["s-bad"].Status)
	}
	for _, s := range spans {
		for _, a := range tracetest.SpanStubFromReadOnlySpan(s).Attributes {
			if strings.Contains(a.Value.Emit(), canary) {
				t.Errorf("the error text is in a span attribute: %v", a)
			}
		}
		for _, e := range s.Events() {
			if strings.Contains(e.Name, canary) {
				t.Errorf("the error text is in a span event: %v", e)
			}
		}
	}
}
