package exec_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
)

func TestManagerLegacyRejectsResourceIntentBeforeNative(t *testing.T) {
	factoryCalls, handlerCalls := 0, 0
	h := exectest.Mount(t, exectest.WithTerminalFactory(func() (exec.Terminal, error) {
		factoryCalls++
		return exectest.NewFakeTerminal()
	}), exectest.WithHandler(exec.Handler{TTYHandler: func(*types.PluginContext, exec.SessionOptions, *os.File, chan error, <-chan exec.SessionResizeInput) error {
		handlerCalls++
		return nil
	}}))
	body := resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{}}
	for _, opts := range []exec.SessionOptions{
		{ResourcePlugin: "test"},
		{ResourceKey: "pod"},
		{ResourceData: &body},
		{Reference: &resourcev2.ResourceReference{}},
		{Route: &resourcev2.AccessRoute{}},
	} {
		if _, err := h.Manager.CreateSession(nil, opts); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
			t.Fatalf("legacy resource intent must be unsupported: %v", err)
		}
	}
	if factoryCalls != 0 || handlerCalls != 0 {
		t.Fatalf("native side effects: factory=%d handler=%d", factoryCalls, handlerCalls)
	}
}

func TestManagerNonresourceTerminalLifecycle(t *testing.T) {
	terminal, err := exectest.NewFakeTerminal()
	if err != nil {
		t.Fatal(err)
	}
	h := exectest.Mount(
		t,
		exectest.WithTerminalFactory(exectest.FakeTerminalFactory(terminal)),
		exectest.WithHandler(exec.Handler{TTYHandler: exectest.NoopTTYHandler()}),
	)
	session, err := h.CreateSession(exec.SessionOptions{Command: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.AttachSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.InjectOutput([]byte("terminal output")); err != nil {
		t.Fatal(err)
	}
	h.Output.WaitForOutputs(1, 5*time.Second)
	if got := string(h.Output.AllData()); got != "terminal output" {
		t.Fatalf("output=%q", got)
	}
	if err := h.CloseSession(session.ID); err != nil {
		t.Fatal(err)
	}
	if h.WaitForClose(5*time.Second) == nil {
		t.Fatal("missing close signal")
	}
}

func TestManagerResourceNativeReceivesExactValueAndDetachedLifetime(t *testing.T) {
	body := resourcev2.Value{Kind: "map", Map: map[string]resourcev2.Value{
		"int":       {Kind: "int64", Int: "-9223372036854775808"},
		"uint":      {Kind: "uint64", Uint: "18446744073709551615"},
		"double":    {Kind: "double", Double: 1.2345678901234567},
		"timestamp": {Kind: "timestamp", Timestamp: "2026-10-28T01:02:03.123456789Z"},
		"nested": {
			Kind: "list",
			List: []resourcev2.Value{
				{Kind: "map", Map: map[string]resourcev2.Value{"text": {Kind: "string", String: "exact"}}},
			},
		},
	}}
	var native *types.PluginContext
	handler := defaultHandler()
	handler.TTYHandler = func(pctx *types.PluginContext, opts exec.SessionOptions, _ *os.File, _ chan error, _ <-chan exec.SessionResizeInput) error {
		if !reflect.DeepEqual(opts.ResourceData, &body) {
			t.Fatalf("native body changed: %#v", opts.ResourceData)
		}
		if _, ok := pctx.Context.Deadline(); !ok {
			t.Fatal("native admission lost caller deadline")
		}
		native = pctx
		return nil
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	session, err := h.Manager.CreateResourceSession(
		&types.PluginContext{Context: ctx},
		exec.SessionOptions{ResourcePlugin: "test", ResourceKey: "pod", ResourceData: &body},
	)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if native.Context.Err() != nil {
		t.Fatalf("accepted native lifetime canceled: %v", native.Context.Err())
	}
	if _, ok := native.Context.Deadline(); ok {
		t.Fatal("accepted session retained RPC deadline")
	}
	if err := h.CloseSession(session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerResourceCancellationDuringNativeAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := defaultHandler()
	handler.TTYHandler = func(pctx *types.PluginContext, _ exec.SessionOptions, _ *os.File, _ chan error, _ <-chan exec.SessionResizeInput) error {
		cancel()
		select {
		case <-pctx.Context.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("native admission ignored caller cancellation")
		}
		return nil
	}
	terminal, err := exectest.NewFakeTerminal()
	if err != nil {
		t.Fatal(err)
	}
	h := exectest.Mount(
		t,
		exectest.WithHandler(handler),
		exectest.WithTerminalFactory(exectest.FakeTerminalFactory(terminal)),
	)
	if _, createErr := h.Manager.CreateResourceSession(
		&types.PluginContext{Context: ctx},
		exec.SessionOptions{ResourcePlugin: "test", ResourceKey: "pod"},
	); !errors.Is(
		createErr,
		context.Canceled,
	) {
		t.Fatalf("expected cancellation, got %v", createErr)
	}
	sessions, err := h.Manager.ListSessions(nil)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("orphan sessions=%v err=%v", sessions, err)
	}
	if _, err := terminal.InjectOutput([]byte("orphan")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("canceled admission retained terminal: %v", err)
	}
}

func TestManagerResourceCanceledBeforeFactory(t *testing.T) {
	calls := 0
	h := exectest.Mount(
		t,
		exectest.WithHandler(defaultHandler()),
		exectest.WithTerminalFactory(func() (exec.Terminal, error) {
			calls++
			return exectest.NewFakeTerminal()
		}),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.Manager.CreateResourceSession(
		&types.PluginContext{Context: ctx},
		exec.SessionOptions{ResourcePlugin: "test", ResourceKey: "pod"},
	)
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err=%v factory calls=%d", err, calls)
	}
}

func TestManagerResourceDeadlineDuringNativeAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	handler := defaultHandler()
	handler.TTYHandler = func(pctx *types.PluginContext, _ exec.SessionOptions, _ *os.File, _ chan error, _ <-chan exec.SessionResizeInput) error {
		select {
		case <-pctx.Context.Done():
			return pctx.Context.Err()
		case <-time.After(5 * time.Second):
			t.Fatal("native admission ignored deadline")
			return nil
		}
	}
	h := exectest.Mount(t, exectest.WithHandler(handler))
	_, err := h.Manager.CreateResourceSession(
		&types.PluginContext{Context: ctx},
		exec.SessionOptions{ResourcePlugin: "test", ResourceKey: "pod"},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	sessions, err := h.Manager.ListSessions(nil)
	if err != nil || len(sessions) != 0 {
		t.Fatalf("orphan sessions=%v err=%v", sessions, err)
	}
}
