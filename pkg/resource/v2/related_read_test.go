package resourcev2

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func relatedRequestFixture() RelatedReadRequest {
	uid := "uid-1"
	return RelatedReadRequest{Operation: "installed/edges@1", Route: controllerTarget().Route,
		Root: ResourceReference{Address: controllerAddress(), Incarnation: &uid}, Sources: []ReadTarget{controllerTarget()}}
}

func relatedSession(ctx context.Context, in RelatedReadRequest) context.Context {
	return WithSession(ctx, Session{RequestID: "req", RequesterID: "origin", PluginID: "installed", Connection: &types.Connection{ID: in.Route.Connection}})
}

type relatedCallback = func(context.Context, *string, RelatedReadRequest) (RelatedReadPage, error)

func relatedConfig(read relatedCallback) Config[string] {
	cfg := controllerConfig()
	cfg.Lifecycle = controllerLifecycle(new(atomic.Int32))
	cfg.Registrations = []Registration[string]{{Type: controllerAddress().Type,
		Get: func(context.Context, *string, GetRequest) (Record, error) { return controllerRecord(), nil }}}
	cfg.RelatedReads = []RelatedReadRegistration[string]{{Operation: "installed/edges@1", RootType: controllerAddress().Type,
		Sources: []string{controllerAddress().Type, "another-family"}, Read: read}}
	return cfg
}

func TestFixedRelatedRegistrationAndLease(t *testing.T) {
	var reads, catalogs atomic.Int32
	cfg := relatedConfig(func(ctx context.Context, client *string, in RelatedReadRequest) (RelatedReadPage, error) {
		s, _ := SessionFromContext(ctx)
		if client == nil || *client != "explicit" || s.Connection == nil || s.Connection.Name != "Explicit" || s.PluginID != "installed" {
			t.Fatal("did not receive the authenticated, configured client generation")
		}
		reads.Add(1)
		return RelatedReadPage{Coverage: "complete", Sources: []string{in.Root.Address.Type}, Rows: []RelatedReadCandidate{{
			Row: map[string]any{"count": 1}, Evidence: []ResourceReference{in.Root},
		}}}, nil
	})
	base := cfg.Catalog
	cfg.Catalog = func(ctx context.Context, request CatalogRequest) (Catalog, error) {
		catalogs.Add(1)
		return base(ctx, request)
	}
	c := newTestController(t, cfg)
	cfg.RelatedReads[0].Sources[0] = "changed-after-construction"
	in := relatedRequestFixture()
	ctx := relatedSession(t.Context(), in)
	discovery := in
	discovery.Sources = nil
	initial := WithCatalogRevision(ctx, "observation")
	owner := c.RelatedReadOwnerV1()
	ids, err := owner.RelatedSourcesV1(initial, discovery)
	if err != nil || !slices.Equal(ids, []string{"opaque/type", "another-family"}) || catalogs.Load() != 0 || reads.Load() != 0 {
		t.Fatalf("static declaration performed native work or changed: %v %v", ids, err)
	}
	ids[0] = "changed-after-discovery"
	if _, err := owner.ReadRelatedV1(initial, in); err == nil || reads.Load() != 0 {
		t.Fatal("unstarted native connection admitted")
	}
	startTestConnection(t, c)
	ctx = acceptedControllerContext(ctx, t, c)
	for _, rejected := range []context.Context{WithCatalogRevision(ctx, "stale"), WithSession(ctx, Session{}),
		WithSession(ctx, Session{PluginID: "another-owner", Connection: &types.Connection{ID: "explicit"}, RequestID: "req", RequesterID: "origin"})} {
		if _, err := owner.ReadRelatedV1(rejected, in); err == nil {
			t.Fatal("stale or ambiguous admission dispatched")
		}
	}
	for _, mutate := range []func(*RelatedReadRequest){
		func(r *RelatedReadRequest) { r.Sources = nil },
		func(r *RelatedReadRequest) { r.Operation = "installed/other@1" },
		func(r *RelatedReadRequest) { r.Root.Incarnation = nil },
		func(r *RelatedReadRequest) { r.Sources[0].Type = "undeclared" },
		func(r *RelatedReadRequest) { r.Sources[0].Route.Connection = "other" },
		func(r *RelatedReadRequest) { r.Sources[0].Authority = "other-root-authority" },
		func(r *RelatedReadRequest) { r.Root.Address.ID = strings.Repeat("x", 16<<10) },
	} {
		bad := relatedRequestFixture()
		mutate(&bad)
		if _, err := owner.ReadRelatedV1(ctx, bad); err == nil || reads.Load() != 0 {
			t.Fatal("invalid target dispatched")
		}
	}
	p, err := owner.ReadRelatedV1(ctx, in)
	if err != nil || reads.Load() != 1 || len(p.Rows) != 1 || p.Coverage != "partial" {
		t.Fatalf("withheld source not reflected in coverage: %+v, %v", p, err)
	}
}

func TestRelatedReadFencesAfterCallback(t *testing.T) {
	for _, how := range []string{"catalog", "error after catalog change", "cancel", "borrowed uid", "another root object"} {
		t.Run(how, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var changed atomic.Bool
			cfg := relatedConfig(func(_ context.Context, _ *string, req RelatedReadRequest) (RelatedReadPage, error) {
				switch how {
				case "catalog":
					changed.Store(true)
				case "error after catalog change":
					changed.Store(true)
					return RelatedReadPage{}, NewError("FORBIDDEN", "obsolete result")
				case "cancel":
					cancel()
				case "borrowed uid":
					*req.Root.Incarnation = "replacement"
				case "another root object":
					req.Root.Address.ID = "not-the-selected-root"
				}
				return RelatedReadPage{Coverage: "partial", Sources: []string{req.Root.Address.Type}, Rows: []RelatedReadCandidate{{
					Row: map[string]any{"value": "withheld"}, Evidence: []ResourceReference{req.Root},
				}}}, nil
			})
			base := cfg.Catalog
			cfg.Catalog = func(ctx context.Context, req CatalogRequest) (Catalog, error) {
				catalog, err := base(ctx, req)
				if changed.Load() {
					catalog.Revision = "new-revision"
				}
				return catalog, err
			}
			c := newTestController(t, cfg)
			startTestConnection(t, c)
			in := relatedRequestFixture()
			p, err := c.ReadRelatedV1(acceptedControllerContext(relatedSession(ctx, in), t, c), in)
			if err == nil || len(p.Rows) != 0 || *in.Root.Incarnation != "uid-1" {
				t.Fatalf("stale result escaped: %+v %v", p, err)
			}
			if how == "error after catalog change" && ErrorCode(err) != "RESET_REQUIRED" {
				t.Fatalf("stale failure escaped: %v", err)
			}
			if how == "cancel" && !errors.Is(err, context.Canceled) && ErrorCode(err) != "CANCELLED" {
				t.Fatalf("lost cancel: %v", err)
			}
		})
	}
}

func TestRelatedScalarAndEvidenceBounds(t *testing.T) {
	in := relatedRequestFixture()
	other := controllerTarget()
	other.Type, other.Authority = "edge", "other-authority"
	in.Sources = append(in.Sources, other)
	newPage := func() RelatedReadPage {
		ref := in.Root
		ref.Address.Type, ref.Address.Authority = other.Type, other.Authority
		return RelatedReadPage{Coverage: "partial", Sources: []string{other.Type}, Rows: []RelatedReadCandidate{{
			Row: map[string]any{"text": strings.Repeat("𐐷", 2048), "number": 1.25}, Evidence: []ResourceReference{ref, in.Root}, Neighbor: &ref,
		}}}
	}
	if err := validateRelatedPage(in, newPage()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RelatedReadPage){
		"empty acquisition": func(p *RelatedReadPage) { p.Sources = nil },
		"coverage lies":     func(p *RelatedReadPage) { p.Coverage = "complete" },
		"withheld source":   func(p *RelatedReadPage) { p.Sources = []string{in.Root.Address.Type} },
		"wrong authority":   func(p *RelatedReadPage) { p.Rows[0].Evidence[0].Address.Authority = in.Root.Address.Authority },
		"no uid":            func(p *RelatedReadPage) { p.Rows[0].Neighbor.Incarnation = nil },
		"no evidence":       func(p *RelatedReadPage) { p.Rows[0].Evidence = nil },
		"five citations":    func(p *RelatedReadPage) { p.Rows[0].Evidence = make([]ResourceReference, 5) },
		"130 candidates":    func(p *RelatedReadPage) { p.Rows = make([]RelatedReadCandidate, 130) },
		"empty row":         func(p *RelatedReadPage) { clear(p.Rows[0].Row) },
		"scalar too long":   func(p *RelatedReadPage) { p.Rows[0].Row["text"] = strings.Repeat("𐐷", 2049) },
		"invalid UTF8":      func(p *RelatedReadPage) { p.Rows[0].Row["text"] = string([]byte{255}) },
		"nonfinite":         func(p *RelatedReadPage) { p.Rows[0].Row["number"] = math.Inf(1) },
		"nested":            func(p *RelatedReadPage) { p.Rows[0].Row["text"] = []any{"secret"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPage()
			mutate(&p)
			if validateRelatedPage(in, p) == nil {
				t.Fatal("invalid page accepted")
			}
		})
	}
}

func TestRelatedReadConfigurationFailsClosed(t *testing.T) {
	c := newTestController(t, controllerConfig())
	if c.RelatedReadOwnerV1() != nil {
		t.Fatal("unconfigured controller advertised an owner")
	}
	for _, change := range []func(*Config[string]){
		func(c *Config[string]) { c.Lifecycle = nil },
		func(c *Config[string]) { c.RelatedReads[0].Operation = "unversioned" },
		func(c *Config[string]) { c.RelatedReads = append(c.RelatedReads, c.RelatedReads[0]) },
		func(c *Config[string]) { c.RelatedReads[0].Read = nil },
		func(c *Config[string]) { c.RelatedReads[0].Sources = []string{"same", "same"} },
	} {
		cfg := relatedConfig(func(context.Context, *string, RelatedReadRequest) (RelatedReadPage, error) {
			return RelatedReadPage{}, errors.New("must not run")
		})
		change(&cfg)
		if _, err := NewController(cfg); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
}

func TestRelatedRegistrationChargesEncodedDiscovery(t *testing.T) {
	const limit = 16 << 10
	// At this length, BytesValue has one tag byte and a two-byte length.
	const exact = limit - len(`{"sources":[""]}`) - 3
	eight := make([]string, 8)
	for i := range eight {
		eight[i] = strings.Repeat(strconv.Itoa(i), 2048)
	}
	for _, tc := range []struct {
		name   string
		ids    []string
		wantOK bool
	}{
		{"exact", []string{strings.Repeat("x", exact)}, true},
		{"over by one", []string{strings.Repeat("x", exact+1)}, false},
		{"escaping", []string{strings.Repeat("\n", exact/2+1)}, false},
		{"array overhead", eight, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := relatedConfig(func(context.Context, *string, RelatedReadRequest) (RelatedReadPage, error) {
				panic("discovery must not run the handler")
			})
			cfg.RelatedReads[0].Sources = tc.ids
			c, err := NewController(cfg)
			if c != nil {
				t.Cleanup(func() { _ = c.Close() })
			}
			if (err == nil) != tc.wantOK {
				t.Fatalf("registration error = %v, want success = %t", err, tc.wantOK)
			}
		})
	}
}

func TestRelatedDeliveryChecksHeldGenerationBeforeReturningRows(t *testing.T) {
	c := newTestController(t, relatedConfig(func(context.Context, *string, RelatedReadRequest) (RelatedReadPage, error) {
		panic("delivery must not invoke the handler")
	}))
	startTestConnection(t, c)
	in := relatedRequestFixture()
	lease, _, done, err := c.lease(t.Context(), in.Route)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(done)
	defer release()
	// Keep the held slot but delay observing context cancellation deterministically.
	delivery := context.WithoutCancel(lease)
	page := RelatedReadPage{Coverage: "complete", Sources: []string{in.Root.Address.Type}, Rows: []RelatedReadCandidate{{
		Row: map[string]any{"value": "private"}, Evidence: []ResourceReference{in.Root},
	}}}
	if got, readErr := c.finishRelatedRead(delivery, in, page, 1); readErr != nil || len(got.Rows) != 1 {
		t.Fatalf("live generation rejected: %+v %v", got, readErr)
	}
	slot := lease.Value(catalogSlotKey{}).(*clientSlot[string])
	stopped := make(chan error, 1)
	go func() { _, stopErr := c.StopConnection(t.Context(), in.Route.Connection); stopped <- stopErr }()
	select {
	case <-slot.ctx.Done():
	case <-time.After(testWait):
		t.Fatal("connection did not begin retirement")
	}
	if got, readErr := c.finishRelatedRead(delivery, in, page, 1); ErrorCode(readErr) != "RESET_REQUIRED" || len(got.Rows) != 0 {
		t.Fatalf("retiring generation escaped delivery fence: %+v %v", got, readErr)
	}
	release()
	if stopErr := waitForError(t, "stop after delivery", stopped); stopErr != nil {
		t.Fatal(stopErr)
	}
}

func TestRelatedReaderStaysReservedUntilCanceledCallbackReturns(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := newTestController(t, relatedConfig(func(_ context.Context, _ *string, in RelatedReadRequest) (RelatedReadPage, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release // a noncooperative native read survives client cancellation
		}
		return RelatedReadPage{Coverage: "partial", Sources: []string{in.Root.Address.Type}, Rows: []RelatedReadCandidate{}}, nil
	}))
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock() // Release the native callback before controller cleanup on every failure path.
	startTestConnection(t, c)
	in := relatedRequestFixture()
	ctx := acceptedControllerContext(relatedSession(t.Context(), in), t, c)
	first, cancel := context.WithCancel(ctx)
	defer cancel()
	readAsync := func(ctx context.Context) <-chan error {
		done := make(chan error, 1)
		go func() { _, err := c.ReadRelatedV1(ctx, in); done <- err }()
		return done
	}
	done := readAsync(first)
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("native callback never entered: %v", err)
	case <-time.After(testWait):
		t.Fatal("native callback did not enter")
	}
	cancel()
	overlap := waitForError(t, "overlapping read", readAsync(ctx))
	s, _ := SessionFromContext(ctx)
	s.RequesterID = "independent-window"
	independent := waitForError(t, "independent read", readAsync(WithSession(ctx, s)))
	unblock()
	if err := waitForError(t, "canceled read", done); err == nil || ErrorCode(overlap) != "UNAVAILABLE" || independent != nil || calls.Load() != 2 {
		t.Fatalf("physical reservation failed: first=%v overlap=%v independent=%v calls=%d", err, overlap, independent, calls.Load())
	}
	if _, err := c.ReadRelatedV1(ctx, in); err != nil || calls.Load() != 3 {
		t.Fatalf("did not release settled reservation: %v", err)
	}
}

func TestRelatedReaderCapsDistinctWindows(t *testing.T) {
	c := newTestController(t, controllerConfig())
	for i := range 64 {
		if !c.acquireRelatedReader(strconv.Itoa(i)) {
			t.Fatalf("window %d refused inside the realm budget", i)
		}
	}
	if c.acquireRelatedReader("next") {
		t.Fatal("new window displaced a live read")
	}
	c.releaseRelatedReader("0")
	if !c.acquireRelatedReader("next") {
		t.Fatal("released slot remained unavailable")
	}
}
