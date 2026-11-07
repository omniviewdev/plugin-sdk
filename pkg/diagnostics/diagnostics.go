// Package diagnostics lets a plugin publish a small, bounded set of in-process
// measurements. The host pulls them through the PluginLifecycle GetDiagnostics
// RPC and merges them into its own diagnostics collector.
//
// The package is deliberately narrow so a plugin cannot leak data or flood the
// host: attribute keys are a closed enum, attribute values and metric names are
// pattern-checked, and the number of metrics, series per metric and attributes
// per series is capped. Observations that break a rule are dropped (never
// clamped) and flag the next snapshot as truncated. Never record resource
// names, queries, identifiers, error text or credentials as attribute values.
package diagnostics

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	lifecyclepb "github.com/omniviewdev/plugin-sdk/proto/v1/lifecycle"
)

// Metric names live under omniview.plugin.self. so they can never collide with
// host-owned omniview.plugin.* metrics.

// Wire limits. Hosts enforce the same limits and drop nonconforming data.
const (
	SchemaVersion = 1
	MaxMetrics    = 24
	MaxPoints     = 16
	MaxAttrs      = 4
	MaxBounds     = 16
)

// Key is a closed set of attribute keys.
type Key = lifecyclepb.DiagKey

// Allowed attribute keys.
const (
	KeyOperation = lifecyclepb.DiagKey_DIAG_KEY_OPERATION
	KeyOutcome   = lifecyclepb.DiagKey_DIAG_KEY_OUTCOME
	KeyPhase     = lifecyclepb.DiagKey_DIAG_KEY_PHASE
	KeyState     = lifecyclepb.DiagKey_DIAG_KEY_STATE
	KeyReason    = lifecyclepb.DiagKey_DIAG_KEY_REASON
	KeyCache     = lifecyclepb.DiagKey_DIAG_KEY_CACHE
	KeyQueue     = lifecyclepb.DiagKey_DIAG_KEY_QUEUE
	KeyDirection = lifecyclepb.DiagKey_DIAG_KEY_DIRECTION
	KeyEvent     = lifecyclepb.DiagKey_DIAG_KEY_EVENT
)

// Attr is one attribute of a series. Values must match ^[a-z][a-z0-9_]{0,31}$.
type Attr struct {
	Key   Key
	Value string
}

var (
	namePattern  = regexp.MustCompile(`^omniview\.plugin\.self\.[a-z0-9_.]{1,72}$`)
	valuePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	unitPattern  = regexp.MustCompile(`^[a-z0-9_{}/]{0,24}$`)
)

// ValidName reports whether name is an allowed metric name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidValue reports whether value is an allowed attribute value.
func ValidValue(value string) bool { return valuePattern.MatchString(value) }

// ValidUnit reports whether unit is an allowed unit string.
func ValidUnit(unit string) bool { return unitPattern.MatchString(unit) }

// ValidKey reports whether key is one of the closed attribute keys.
func ValidKey(key Key) bool {
	return key >= KeyOperation && key <= KeyEvent
}

var (
	// ErrInvalidMetric means a name, unit or bucket layout was rejected.
	ErrInvalidMetric = errors.New("invalid diagnostics metric")
	// ErrTooManyMetrics means the registry already holds MaxMetrics metrics.
	ErrTooManyMetrics = errors.New("too many diagnostics metrics")
)

type series struct {
	attrs   []*lifecyclepb.DiagAttr
	counter uint64
	gauge   float64
	count   uint64
	sum     float64
	buckets []uint64
}

type instrument struct {
	registry *Registry
	name     string
	unit     string
	kind     lifecyclepb.DiagKind
	bounds   []float64

	mu     sync.Mutex
	series map[string]*series
}

// Registry holds a plugin's instruments. Use Default for the process registry.
type Registry struct {
	mu        sync.Mutex
	metrics   map[string]*instrument
	truncated bool
	start     time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{metrics: map[string]*instrument{}, start: time.Now()}
}

var defaultRegistry = NewRegistry()

// Default returns the process-wide registry served by the SDK lifecycle service.
func Default() *Registry { return defaultRegistry }

func (r *Registry) markTruncated() {
	r.mu.Lock()
	r.truncated = true
	r.mu.Unlock()
}

func (r *Registry) register(name, unit string, kind lifecyclepb.DiagKind, bounds []float64) (*instrument, error) {
	if !ValidName(name) || !ValidUnit(unit) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidMetric, name)
	}
	if kind == lifecyclepb.DiagKind_DIAG_KIND_HISTOGRAM {
		if len(bounds) == 0 || len(bounds) > MaxBounds {
			return nil, fmt.Errorf("%w: %q needs 1..%d bounds", ErrInvalidMetric, name, MaxBounds)
		}
		for i, b := range bounds {
			if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= bounds[i-1]) {
				return nil, fmt.Errorf("%w: %q bounds must be finite and increasing", ErrInvalidMetric, name)
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.metrics[name]; ok {
		if existing.kind != kind || existing.unit != unit || !equalFloats(existing.bounds, bounds) {
			return nil, fmt.Errorf("%w: %q already registered differently", ErrInvalidMetric, name)
		}
		return existing, nil
	}
	if len(r.metrics) >= MaxMetrics {
		return nil, ErrTooManyMetrics
	}
	in := &instrument{registry: r, name: name, unit: unit, kind: kind, bounds: append([]float64(nil), bounds...), series: map[string]*series{}}
	r.metrics[name] = in
	return in, nil
}

func equalFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// normalize validates attrs and returns them sorted by key plus a series key.
func normalize(attrs []Attr) ([]*lifecyclepb.DiagAttr, string, bool) {
	if len(attrs) > MaxAttrs {
		return nil, "", false
	}
	sorted := make([]Attr, len(attrs))
	copy(sorted, attrs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	out := make([]*lifecyclepb.DiagAttr, len(sorted))
	var id strings.Builder
	for i, a := range sorted {
		if !ValidKey(a.Key) || !ValidValue(a.Value) || (i > 0 && sorted[i-1].Key == a.Key) {
			return nil, "", false
		}
		out[i] = &lifecyclepb.DiagAttr{Key: a.Key, Value: a.Value}
		fmt.Fprintf(&id, "%d=%s;", a.Key, a.Value)
	}
	return out, id.String(), true
}

// locate finds or creates the series. The caller holds in.mu.
func (in *instrument) locate(attrs []Attr) *series {
	normalized, id, ok := normalize(attrs)
	if !ok {
		in.registry.markTruncated()
		return nil
	}
	if s, exists := in.series[id]; exists {
		return s
	}
	if len(in.series) >= MaxPoints {
		in.registry.markTruncated()
		return nil
	}
	s := &series{attrs: normalized}
	if in.kind == lifecyclepb.DiagKind_DIAG_KIND_HISTOGRAM {
		s.buckets = make([]uint64, len(in.bounds)+1)
	}
	in.series[id] = s
	return s
}

// Counter is a monotonically increasing total. A nil Counter ignores calls.
type Counter struct{ in *instrument }

// Counter registers or returns a counter.
func (r *Registry) Counter(name, unit string) (*Counter, error) {
	in, err := r.register(name, unit, lifecyclepb.DiagKind_DIAG_KIND_COUNTER, nil)
	if err != nil {
		return nil, err
	}
	return &Counter{in: in}, nil
}

// Add increases the counter, saturating at the maximum uint64.
func (c *Counter) Add(delta uint64, attrs ...Attr) {
	if c == nil {
		return
	}
	c.in.mu.Lock()
	defer c.in.mu.Unlock()
	if s := c.in.locate(attrs); s != nil {
		if math.MaxUint64-s.counter < delta {
			s.counter = math.MaxUint64
		} else {
			s.counter += delta
		}
	}
}

// Gauge is a current value. A nil Gauge ignores calls.
type Gauge struct{ in *instrument }

// Gauge registers or returns a gauge.
func (r *Registry) Gauge(name, unit string) (*Gauge, error) {
	in, err := r.register(name, unit, lifecyclepb.DiagKind_DIAG_KIND_GAUGE, nil)
	if err != nil {
		return nil, err
	}
	return &Gauge{in: in}, nil
}

// Set replaces the value. Non-finite values are dropped.
func (g *Gauge) Set(value float64, attrs ...Attr) {
	if g == nil {
		return
	}
	g.in.mu.Lock()
	defer g.in.mu.Unlock()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		g.in.registry.markTruncated()
		return
	}
	if s := g.in.locate(attrs); s != nil {
		s.gauge = value
	}
}

// Histogram is a cumulative distribution. A nil Histogram ignores calls.
type Histogram struct{ in *instrument }

// Histogram registers or returns a histogram with 1..MaxBounds increasing bounds.
func (r *Registry) Histogram(name, unit string, bounds []float64) (*Histogram, error) {
	in, err := r.register(name, unit, lifecyclepb.DiagKind_DIAG_KIND_HISTOGRAM, bounds)
	if err != nil {
		return nil, err
	}
	return &Histogram{in: in}, nil
}

// Observe records one value. Non-finite values are dropped.
func (h *Histogram) Observe(value float64, attrs ...Attr) {
	if h == nil {
		return
	}
	h.in.mu.Lock()
	defer h.in.mu.Unlock()
	if math.IsNaN(value) || math.IsInf(value, 0) {
		h.in.registry.markTruncated()
		return
	}
	s := h.in.locate(attrs)
	if s == nil {
		return
	}
	idx := sort.SearchFloat64s(h.in.bounds, value) // first bound >= value (le semantics)
	s.buckets[idx]++
	s.count++
	s.sum += value
}

// StartUnixNano is when this registry was created; it changes on restart.
func (r *Registry) StartUnixNano() int64 { return r.start.UnixNano() }

// Snapshot copies current state. Metrics without points are omitted, so an
// unobserved instrument is absent rather than reported as zero. It is safe for
// concurrent use and holds each instrument lock only while copying.
func (r *Registry) Snapshot() ([]*lifecyclepb.DiagMetric, bool) {
	r.mu.Lock()
	instruments := make([]*instrument, 0, len(r.metrics))
	for _, in := range r.metrics {
		instruments = append(instruments, in)
	}
	truncated := r.truncated
	r.mu.Unlock()
	sort.Slice(instruments, func(i, j int) bool { return instruments[i].name < instruments[j].name })
	out := make([]*lifecyclepb.DiagMetric, 0, len(instruments))
	for _, in := range instruments {
		in.mu.Lock()
		if len(in.series) == 0 {
			in.mu.Unlock()
			continue
		}
		metric := &lifecyclepb.DiagMetric{Name: in.name, Unit: in.unit, Kind: in.kind, Points: make([]*lifecyclepb.DiagPoint, 0, len(in.series))}
		ids := make([]string, 0, len(in.series))
		for id := range in.series {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			s := in.series[id]
			p := &lifecyclepb.DiagPoint{Attrs: s.attrs}
			switch in.kind {
			case lifecyclepb.DiagKind_DIAG_KIND_COUNTER:
				p.Value = float64(s.counter)
			case lifecyclepb.DiagKind_DIAG_KIND_GAUGE:
				p.Value = s.gauge
			case lifecyclepb.DiagKind_DIAG_KIND_HISTOGRAM:
				p.Count, p.Sum = s.count, s.sum
				p.Bounds = append([]float64(nil), in.bounds...)
				p.BucketCounts = append([]uint64(nil), s.buckets...)
			}
			metric.Points = append(metric.Points, p)
		}
		in.mu.Unlock()
		out = append(out, metric)
	}
	return out, truncated
}
