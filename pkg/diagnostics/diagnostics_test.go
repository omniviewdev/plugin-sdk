package diagnostics

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestRegistrationRules(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"api.calls", "omniview.plugin.active", "omniview.plugin.self.", "omniview.plugin.self.Bad", "omniview.plugin.self.a b"} {
		if _, err := r.Counter(name, "1"); !errors.Is(err, ErrInvalidMetric) {
			t.Errorf("%q accepted: %v", name, err)
		}
	}
	if _, err := r.Histogram("omniview.plugin.self.h", "s", []float64{2, 1}); !errors.Is(err, ErrInvalidMetric) {
		t.Errorf("decreasing bounds accepted: %v", err)
	}
	if _, err := r.Histogram("omniview.plugin.self.h", "s", []float64{math.NaN()}); err == nil {
		t.Error("NaN bound accepted")
	}
	if _, err := r.Counter("omniview.plugin.self.c", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Gauge("omniview.plugin.self.c", "1"); err == nil {
		t.Error("kind change accepted")
	}
	for i := 0; i < MaxMetrics; i++ {
		_, _ = r.Counter("omniview.plugin.self.m"+string(rune('a'+i)), "1")
	}
	if _, err := r.Counter("omniview.plugin.self.overflow", "1"); !errors.Is(err, ErrTooManyMetrics) {
		t.Errorf("metric cap: %v", err)
	}
}

func TestInvalidObservationsAreDroppedAndFlagged(t *testing.T) {
	r := NewRegistry()
	c, _ := r.Counter("omniview.plugin.self.calls", "1")
	g, _ := r.Gauge("omniview.plugin.self.g", "1")
	c.Add(1, Attr{KeyOperation, "get"}, Attr{KeyOutcome, "ok"})
	if _, truncated := r.Snapshot(); truncated {
		t.Fatal("valid data flagged")
	}
	c.Add(1, Attr{KeyOperation, "Pod/default/secret"}) // value rule
	c.Add(1, Attr{Key(99), "x"})                       // closed enum
	c.Add(1, Attr{KeyOperation, "a"}, Attr{KeyOperation, "b"})
	c.Add(1, Attr{KeyOperation, "a"}, Attr{KeyOutcome, "a"}, Attr{KeyPhase, "a"}, Attr{KeyState, "a"}, Attr{KeyReason, "a"})
	g.Set(math.Inf(1))
	metrics, truncated := r.Snapshot()
	if !truncated || len(metrics) != 1 || len(metrics[0].Points) != 1 || metrics[0].Points[0].Value != 1 {
		t.Fatalf("invalid data must be dropped: %v truncated=%v", metrics, truncated)
	}
}

func TestSeriesCapAndUnobservedAbsence(t *testing.T) {
	r := NewRegistry()
	c, _ := r.Counter("omniview.plugin.self.calls", "1")
	_, _ = r.Gauge("omniview.plugin.self.never", "1")
	for i := 0; i < MaxPoints+3; i++ {
		c.Add(1, Attr{KeyOperation, "op" + string(rune('a'+i))})
	}
	c.Add(2, Attr{KeyOperation, "opa"}) // existing series still updates at the cap
	metrics, truncated := r.Snapshot()
	if !truncated || len(metrics) != 1 || len(metrics[0].Points) != MaxPoints {
		t.Fatalf("metrics=%d truncated=%v", len(metrics), truncated)
	}
	if metrics[0].Points[0].Value != 3 {
		t.Fatalf("existing series not updated: %v", metrics[0].Points[0].Value)
	}
}

func TestHistogramBucketsAreUpperInclusive(t *testing.T) {
	r := NewRegistry()
	h, _ := r.Histogram("omniview.plugin.self.latency", "s", []float64{1, 2})
	for _, v := range []float64{0.5, 1, 1.5, 2, 9} {
		h.Observe(v)
	}
	p := mustOne(t, r).Points[0]
	if p.Count != 5 || p.Sum != 14 || p.BucketCounts[0] != 2 || p.BucketCounts[1] != 2 || p.BucketCounts[2] != 1 {
		t.Fatalf("%+v", p)
	}
}

func TestAttributeOrderDoesNotSplitSeries(t *testing.T) {
	r := NewRegistry()
	c, _ := r.Counter("omniview.plugin.self.calls", "1")
	c.Add(1, Attr{KeyOutcome, "ok"}, Attr{KeyOperation, "get"})
	c.Add(1, Attr{KeyOperation, "get"}, Attr{KeyOutcome, "ok"})
	if p := mustOne(t, r).Points; len(p) != 1 || p[0].Value != 2 {
		t.Fatalf("%v", p)
	}
}

func TestNilInstrumentsAreSafe(t *testing.T) {
	var c *Counter
	var g *Gauge
	var h *Histogram
	c.Add(1)
	g.Set(1)
	h.Observe(1)
}

func TestConcurrentSnapshotAndSpeed(t *testing.T) {
	r := NewRegistry()
	c, _ := r.Counter("omniview.plugin.self.calls", "1")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 2000; j++ {
				c.Add(1, Attr{KeyOperation, "get"})
				r.Snapshot()
			}
		}()
	}
	wg.Wait()
	if mustOne(t, r).Points[0].Value != 16000 {
		t.Fatal("lost increments")
	}
	// Full registry snapshot stays inside the plan's 200 µs budget.
	full := NewRegistry()
	for i := 0; i < MaxMetrics; i++ {
		h, _ := full.Histogram("omniview.plugin.self.h"+string(rune('a'+i)), "s", []float64{1, 2, 3, 4})
		for j := 0; j < MaxPoints; j++ {
			h.Observe(1, Attr{KeyOperation, "op" + string(rune('a'+j))})
		}
	}
	best := time.Hour
	for i := 0; i < 20; i++ {
		start := time.Now()
		full.Snapshot()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if best > 200*time.Microsecond {
		t.Fatalf("snapshot too slow: %v", best)
	}
}

func mustOne(t *testing.T, r *Registry) *struct {
	Points []*pt
} {
	t.Helper()
	m, _ := r.Snapshot()
	if len(m) != 1 {
		t.Fatalf("metrics=%d", len(m))
	}
	out := &struct{ Points []*pt }{}
	for _, p := range m[0].Points {
		out.Points = append(out.Points, &pt{p.Value, p.Count, p.Sum, p.BucketCounts})
	}
	return out
}

type pt struct {
	Value        float64
	Count        uint64
	Sum          float64
	BucketCounts []uint64
}
