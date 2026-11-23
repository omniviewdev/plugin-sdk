package exec

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"weak"

	sdkdiag "github.com/omniviewdev/plugin-sdk/pkg/diagnostics"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// The measurements of the exec engine, published through the plugin's diagnostics channel (pkg/diagnostics), which the
// host pulls with GetDiagnostics. They follow that channel's rules: the attribute keys are the closed set it defines,
// every value is a short word from a closed list below (never an id, a name, an argument or an error text), and a
// metric has at most a few series. The metric names are the engine's own, under omniview.plugin.self.sdk.exec.
const (
	metricSessions         = "omniview.plugin.self.sdk.exec.sessions"
	metricEnded            = "omniview.plugin.self.sdk.exec.session.ended"
	metricBytes            = "omniview.plugin.self.sdk.exec.bytes"
	metricDropped          = "omniview.plugin.self.sdk.exec.dropped_bytes"
	metricStalls           = "omniview.plugin.self.sdk.exec.stalls"
	metricViolations       = "omniview.plugin.self.sdk.exec.violations"
	metricAbandoned        = "omniview.plugin.self.sdk.exec.abandoned"
	metricFrames           = "omniview.plugin.self.sdk.exec.frames.ignored"
	metricAbandonedRefused = "omniview.plugin.self.sdk.exec.abandoned.refused"
)

// The closed words of the violations metric: what an author's function did that the contract forbids. Each is also the
// word the engine's log uses for it.
const (
	violationPanic           = "panic"             // the function panicked
	violationIgnoredCancel   = "ignored_cancel"    // the function did not return after its context was cancelled
	violationLateWrite       = "late_write"        // output written after the function returned or the session ended
	violationStartedLate     = "started_late"      // Proc.Started after the first output or after a first report
	violationLateCall        = "late_call"         // Proc.Started or Proc.Link after the function returned
	violationStartedFacts    = "started_facts"     // Proc.Started carried a fact the wire cannot carry
	violationWidened         = "capabilities_wide" // a Started or a choice claimed more than its bound
	violationExit            = "exit_shape"        // an Exit that breaks its contract
	violationExitAndError    = "exit_and_error"    // the function returned a status and an error
	violationLinkUndeclared  = "link_undeclared"   // Proc.Link on a target that did not declare LinkHealth
	violationLinkInvalid     = "link_invalid"      // Proc.Link with a state that is none of the three
	violationStalledStatus   = "stalled_status"    // a process status returned while the last Link report was STALLED
	violationResizeUnread    = "resize_unread"     // the function never read the sizes it was sent
	violationClassifyPanic   = "classify_panic"    // Target.Classify panicked
	violationOptionsContract = "options_contract"  // Target.Options broke its contract during a start
	violationOptionsHung     = "options_hung"      // Target.Options did not return within its deadline and the grace
	violationClassifyHung    = "classify_hung"     // Target.Classify did not return within its deadline
)

// telemetry holds the instruments of one engine. Instruments are registered once per registry: building a telemetry on a
// registry that already holds them returns the same instruments, so engines share their series, as the plugin's engines
// are one plugin. The counts that the two gauges report are shared the same way (see gaugeCounts).
type telemetry struct {
	sessions   *sdkdiag.Gauge
	ended      *sdkdiag.Counter
	bytes      *sdkdiag.Counter
	dropped    *sdkdiag.Counter
	stalls     *sdkdiag.Counter
	violations *sdkdiag.Counter
	abandoned  *sdkdiag.Gauge
	frames     *sdkdiag.Counter
	refused    *sdkdiag.Counter
	counts     *gaugeCounts
	own        atomic.Int64 // the functions this engine gave up on that have not returned; the gauge counts every engine of the registry
}

// gaugeCounts is what the two gauges of a registry report. The counts belong to the registry and not to an engine: engines
// that publish to one registry share its gauges, and a gauge set from one engine's own count would overwrite the others'.
type gaugeCounts struct {
	// mu orders the updates of a gauge with the count it reports, so that the last value written is the newest.
	mu        sync.Mutex
	live      int64
	abandoned int64
}

// registryCounts holds the counts of each registry an engine publishes to. The key is a weak reference, so the map does not
// keep a registry alive: the process registry lives as long as the process, and a registry that a test made goes with it.
var registryCounts sync.Map // weak.Pointer[sdkdiag.Registry] -> *gaugeCounts

// countsOf returns the counts that the engines publishing to r share.
func countsOf(r *sdkdiag.Registry) *gaugeCounts {
	key := weak.Make(r)
	if counts, ok := registryCounts.Load(key); ok {
		return counts.(*gaugeCounts)
	}
	counts, loaded := registryCounts.LoadOrStore(key, &gaugeCounts{})
	if !loaded {
		runtime.AddCleanup(r, func(k weak.Pointer[sdkdiag.Registry]) { registryCounts.Delete(k) }, key)
	}
	return counts.(*gaugeCounts)
}

// newTelemetry registers the engine's instruments on r (the process registry when r is nil). A registry that is full
// or refuses a name leaves that instrument nil, which ignores every call, as the SDK's other lanes do.
func newTelemetry(r *sdkdiag.Registry) *telemetry {
	if r == nil {
		r = sdkdiag.Default()
	}
	t := &telemetry{counts: countsOf(r)}
	t.sessions, _ = r.Gauge(metricSessions, "1")
	t.ended, _ = r.Counter(metricEnded, "1")
	t.bytes, _ = r.Counter(metricBytes, "{byte}")
	t.dropped, _ = r.Counter(metricDropped, "{byte}")
	t.stalls, _ = r.Counter(metricStalls, "1")
	t.violations, _ = r.Counter(metricViolations, "1")
	t.abandoned, _ = r.Gauge(metricAbandoned, "1")
	t.frames, _ = r.Counter(metricFrames, "1")
	t.refused, _ = r.Counter(metricAbandonedRefused, "1")
	return t
}

// word turns a wire enum name into an attribute value: lower case, without its prefix.
func word(name, prefix string) string { return strings.ToLower(strings.TrimPrefix(name, prefix)) }

// sessionStarted counts a session that was handed to its function.
func (t *telemetry) sessionStarted() {
	t.counts.mu.Lock()
	defer t.counts.mu.Unlock()
	t.counts.live++
	t.sessions.Set(float64(t.counts.live), sdkdiag.Attr{Key: sdkdiag.KeyState, Value: "running"})
}

// sessionEnded counts a session that ended, by the kind of its end.
func (t *telemetry) sessionEnded(end *execv2.End) {
	outcome := "unknown"
	switch end.GetKind().(type) {
	case *execv2.End_Exited:
		outcome = "exited"
	case *execv2.End_Signaled:
		outcome = "signaled"
	case *execv2.End_Closed:
		outcome = "closed"
	case *execv2.End_Failed:
		outcome = "failed"
	case *execv2.End_Lost:
		outcome = "lost"
	}
	t.counts.mu.Lock()
	defer t.counts.mu.Unlock()
	t.counts.live--
	t.sessions.Set(float64(t.counts.live), sdkdiag.Attr{Key: sdkdiag.KeyState, Value: "running"})
	t.ended.Add(1, sdkdiag.Attr{Key: sdkdiag.KeyOutcome, Value: outcome})
}

// abandonedChanged moves the count of functions the engine gave up on that have not returned yet.
func (t *telemetry) abandonedChanged(delta int64) {
	t.own.Add(delta)
	t.counts.mu.Lock()
	defer t.counts.mu.Unlock()
	t.counts.abandoned += delta
	t.abandoned.Set(float64(t.counts.abandoned))
}

// violation counts a contract violation of an author's function.
func (t *telemetry) violation(reason string) {
	t.violations.Add(1, sdkdiag.Attr{Key: sdkdiag.KeyReason, Value: reason})
}

// frameIgnored counts a frame of the host that this build does not serve, by the closed word of its kind.
func (t *telemetry) frameIgnored(kind string) {
	t.frames.Add(1, sdkdiag.Attr{Key: sdkdiag.KeyEvent, Value: kind})
}

// abandonedRefused counts a start refused because abandoned author code holds the places of its target.
func (t *telemetry) abandonedRefused() { t.refused.Add(1) }

// traffic adds what a session moved since it last reported: the bytes its function wrote and the host sent, the bytes
// the output queue overwrote (and why), and the writes that had to wait for a reader.
func (t *telemetry) traffic(out, in, dropped, stalls uint64, dropReason execv2.DropReason) {
	if out > 0 {
		t.bytes.Add(out, sdkdiag.Attr{Key: sdkdiag.KeyDirection, Value: "out"})
	}
	if in > 0 {
		t.bytes.Add(in, sdkdiag.Attr{Key: sdkdiag.KeyDirection, Value: "in"})
	}
	if dropped > 0 {
		t.dropped.Add(dropped, sdkdiag.Attr{Key: sdkdiag.KeyReason, Value: word(dropReason.String(), "DROP_REASON_")})
	}
	if stalls > 0 {
		t.stalls.Add(stalls, sdkdiag.Attr{Key: sdkdiag.KeyQueue, Value: "output"})
	}
}

// abandonedCount returns how many functions this engine gave up on that have not returned.
func (t *telemetry) abandonedCount() int64 { return t.own.Load() }
