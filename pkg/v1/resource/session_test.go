package resource_test

import (
	"context"
	"testing"

	resource "github.com/omniviewdev/plugin-sdk/pkg/v1/resource"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

// --- SR-001: WithSession → SessionFromContext round-trip ---
func TestSession_RoundTrip(t *testing.T) {
	session := &resource.Session{
		Connection:  &types.Connection{ID: "c1"},
		RequestID:   "r1",
		RequesterID: "u1",
	}
	ctx := resource.WithSession(context.Background(), session)
	got := resource.SessionFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil session")
	}
	if got.Connection.ID != "c1" {
		t.Fatalf("expected c1, got %s", got.Connection.ID)
	}
	if got.RequestID != "r1" {
		t.Fatalf("expected r1, got %s", got.RequestID)
	}
	if got.RequesterID != "u1" {
		t.Fatalf("expected u1, got %s", got.RequesterID)
	}
	if got != session {
		t.Fatal("expected same pointer")
	}
}

// --- SR-002: SessionFromContext with no session returns nil ---
func TestSession_MissingReturnsNil(t *testing.T) {
	got := resource.SessionFromContext(context.Background())
	if got != nil {
		t.Fatal("expected nil")
	}
}

// --- SR-003: ConnectionFromContext returns session's connection ---
func TestSession_ConnectionFromContext(t *testing.T) {
	session := &resource.Session{Connection: &types.Connection{ID: "c1"}}
	ctx := resource.WithSession(context.Background(), session)
	conn := resource.ConnectionFromContext(ctx)
	if conn == nil || conn.ID != "c1" {
		t.Fatal("expected connection c1")
	}
}

// --- SR-004: ConnectionFromContext with no session returns nil ---
func TestSession_ConnectionFromContextNoSession(t *testing.T) {
	conn := resource.ConnectionFromContext(context.Background())
	if conn != nil {
		t.Fatal("expected nil")
	}
}

// --- SR-005: Session survives context wrapping ---
type testCtxKey struct{}

func TestSession_SurvivesContextWrapping(t *testing.T) {
	session := &resource.Session{Connection: &types.Connection{ID: "c1"}}
	ctx := resource.WithSession(context.Background(), session)
	ctx = context.WithValue(ctx, testCtxKey{}, "val")
	got := resource.SessionFromContext(ctx)
	if got != session {
		t.Fatal("expected session to survive wrapping")
	}
}

// --- SR-006: Session with cancelled context ---
func TestSession_CancelledContext(t *testing.T) {
	session := &resource.Session{Connection: &types.Connection{ID: "c1"}}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = resource.WithSession(ctx, session)
	cancel()
	got := resource.SessionFromContext(ctx)
	if got != session {
		t.Fatal("expected session retrievable on cancelled ctx")
	}
}

// --- SR-007: Session with nil connection ---
func TestSession_NilConnection(t *testing.T) {
	session := &resource.Session{Connection: nil, RequestID: "r1"}
	ctx := resource.WithSession(context.Background(), session)
	conn := resource.ConnectionFromContext(ctx)
	if conn != nil {
		t.Fatal("expected nil connection (no panic)")
	}
}

// --- SR-008: Overwriting session in nested context ---
func TestSession_OverwriteNested(t *testing.T) {
	s1 := &resource.Session{RequestID: "s1"}
	s2 := &resource.Session{RequestID: "s2"}
	ctx1 := resource.WithSession(context.Background(), s1)
	ctx2 := resource.WithSession(ctx1, s2)

	got1 := resource.SessionFromContext(ctx1)
	got2 := resource.SessionFromContext(ctx2)
	if got1 != s1 {
		t.Fatal("ctx1 should have s1")
	}
	if got2 != s2 {
		t.Fatal("ctx2 should have s2")
	}
}

// --- SR-009: WithSession with nil session ---
func TestSession_NilSession(t *testing.T) {
	ctx := resource.WithSession(context.Background(), nil)
	got := resource.SessionFromContext(ctx)
	if got != nil {
		t.Fatal("expected nil session")
	}
}

// --- SR-010: Concurrent reads of SessionFromContext ---
func TestSession_ConcurrentReads(t *testing.T) {
	session := &resource.Session{RequestID: "concurrent"}
	ctx := resource.WithSession(context.Background(), session)

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			got := resource.SessionFromContext(ctx)
			if got != session {
				t.Error("expected same session")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}

// --- SR-011: Session fields individually nil ---
func TestSession_IndividualNilFields(t *testing.T) {
	session := &resource.Session{
		Connection:  nil,
		RequestID:   "",
		RequesterID: "",
	}
	ctx := resource.WithSession(context.Background(), session)
	got := resource.SessionFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil session")
	}
	if got.Connection != nil {
		t.Fatal("expected nil connection")
	}
	if got.RequestID != "" {
		t.Fatal("expected empty RequestID")
	}
}

// --- SR-012: Deeply nested context chain ---
func TestSession_DeeplyNested(t *testing.T) {
	session := &resource.Session{RequestID: "deep"}
	ctx := resource.WithSession(context.Background(), session)
	for i := 0; i < 100; i++ {
		ctx = context.WithValue(ctx, struct{ n int }{n: i}, i)
	}
	got := resource.SessionFromContext(ctx)
	if got != session {
		t.Fatal("expected session through 100 levels of nesting")
	}
}
