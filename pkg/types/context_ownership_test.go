package types

import (
	"context"
	"sync"
	"testing"
)

func TestPluginContextStoreAndReadDoNotMutateCaller(t *testing.T) {
	original := t.Context()
	caller := &PluginContext{Context: original, RequestID: "request", RequesterID: "diagnostic"}
	stored := WithPluginContext(context.Background(), caller)
	if caller.Context != original {
		t.Fatal("storing context mutated caller")
	}
	child, stop := context.WithCancel(stored)
	defer stop()
	first := PluginContextFromContext(child)
	second := PluginContextFromContext(stored)
	if first == caller || first == second || first.Context != child || second.Context != stored {
		t.Fatal("request snapshots share or retain another request context")
	}
	first.RequesterID = "mutated"
	if PluginContextFromContext(stored).RequesterID != "diagnostic" {
		t.Fatal("read snapshot mutated stored diagnostics")
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Go(func() {
			ctx, done := context.WithCancel(stored)
			defer done()
			got := PluginContextFromContext(ctx)
			if got.Context != ctx || got.RequestID != "request" {
				t.Error("concurrent request context mismatch")
			}
		})
	}
	wait.Wait()
}

func TestPluginContextFromContextRejectsInvalidEntries(t *testing.T) {
	for _, value := range []any{"not a plugin context", (*PluginContext)(nil)} {
		ctx := context.WithValue(t.Context(), pluginCtxKey{}, value)
		if PluginContextFromContext(ctx) != nil {
			t.Fatal("invalid context entry produced a plugin context")
		}
	}
}
