package exec_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

func TestValidateNativeCommand(t *testing.T) {
	for name, command := range map[string]*exec.NativeCommand{
		"nil":                    nil,
		"empty":                  {},
		"executable NUL":         {Executable: "sh\x00"},
		"argument NUL":           {Executable: "sh", Args: []string{"a\x00b"}},
		"environment empty name": {Executable: "sh", Env: map[string]string{"": "x"}},
		"environment equals":     {Executable: "sh", Env: map[string]string{"A=B": "x"}},
		"environment name NUL":   {Executable: "sh", Env: map[string]string{"A\x00": "x"}},
		"environment value NUL":  {Executable: "sh", Env: map[string]string{"A": "x\x00"}},
		"cwd NUL":                {Executable: "sh", Cwd: "x\x00"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := exec.ValidateNativeCommand(command); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("error=%v", err)
			}
		})
	}
	literal := &exec.NativeCommand{Executable: "program with spaces", Args: []string{"", "$(touch /tmp/no)", "; exit", "a b"}, Env: map[string]string{"A": "x=y"}, Cwd: "directory with spaces"}
	if err := exec.ValidateNativeCommand(literal); err != nil {
		t.Fatal(err)
	}
	if err := exec.ValidateNativeCommand(&exec.NativeCommand{Executable: "sh", Args: []string{strings.Repeat("x", resourcev2.MaxMessageBytes+1)}}); resourcev2.ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("unbounded plan: %v", err)
	}
}

func TestManagerNativeCommandAdmission(t *testing.T) {
	configured := types.Connection{ID: "route", Name: "configured"}
	revision := "catalog-1"
	controller, err := resourcev2.NewController(resourcev2.Config[string]{
		Catalog: func(context.Context, resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
			return resourcev2.Catalog{Provider: "test", Revision: revision, Coverage: resourcev2.Coverage{Kind: "complete", Paths: []string{}}, Types: []resourcev2.ResourceType{}}, nil
		},
		Lifecycle: &resourcev2.Lifecycle[string]{
			LoadConnections: func(context.Context) ([]types.Connection, error) { return []types.Connection{configured}, nil },
			CreateClient:    func(context.Context, types.Connection) (*string, error) { client := "native"; return &client, nil },
			DestroyClient:   func(context.Context, *string) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err := controller.LoadConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartConnection(context.Background(), configured.ID); err != nil {
		t.Fatal(err)
	}
	catalog, err := controller.Catalog(context.Background(), resourcev2.CatalogRequest{Route: resourcev2.AccessRoute{Connection: configured.ID}})
	if err != nil {
		t.Fatal(err)
	}
	opts := exec.SessionOptions{
		Reference: &resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{Provider: "test", Authority: "authority", Type: "pod", Scope: []resourcev2.ScopeEntry{}, ID: "opaque/id"}},
		Route:     &resourcev2.AccessRoute{Connection: configured.ID},
	}
	pc := &types.PluginContext{Context: resourcev2.WithCatalogRevision(context.Background(), catalog.Revision), Connection: &types.Connection{ID: configured.ID}}
	calls, factoryCalls := 0, 0
	plan := &exec.NativeCommand{Executable: "kubectl", Args: []string{"exec", "opaque/id", "--", "sh"}}
	mutateRevision := false
	handler := exec.Handler{NativeCommandHandler: func(bound *types.PluginContext, got exec.SessionOptions) (*exec.NativeCommand, error) {
		calls++
		if bound.Connection.Name != configured.Name || got.ResourcePlugin != "test" || got.ResourceKey != "pod" || !reflect.DeepEqual(got.Reference, opts.Reference) {
			t.Fatalf("admission changed: connection=%+v options=%+v", bound.Connection, got)
		}
		if mutateRevision {
			revision = "catalog-2"
		}
		return plan, nil
	}}
	newManager := func(h exec.Handler) *exec.Manager {
		m := exec.NewManager(exec.ManagerConfig{Handlers: map[string]exec.Handler{"test/pod": h}, TerminalFactory: func() (exec.Terminal, error) { factoryCalls++; t.Fatal("planning allocated PTY"); return nil, nil }})
		m.BindConnectionContextProvider(controller)
		return m
	}
	m := newManager(handler)
	for name, request := range map[string]exec.SessionOptions{"no reference": {Route: opts.Route}, "no route": {Reference: opts.Reference}} {
		t.Run(name, func(t *testing.T) {
			if _, err := m.PlanNativeCommand(pc, request); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("error=%v", err)
			}
		})
	}
	wrong := *pc
	wrong.Connection = &types.Connection{ID: "other"}
	if _, err := m.PlanNativeCommand(&wrong, opts); resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("connection error=%v", err)
	}
	stale := *pc
	stale.Context = resourcev2.WithCatalogRevision(context.Background(), "stale")
	if _, err := m.PlanNativeCommand(&stale, opts); resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("reference error=%v", err)
	}
	if calls != 0 {
		t.Fatalf("rejected request invoked handler %d times", calls)
	}
	if _, err := newManager(exec.Handler{SupportsNativeCommand: true}).PlanNativeCommand(pc, opts); resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("missing callback error=%v", err)
	}
	unbound := exec.NewManager(exec.ManagerConfig{Handlers: map[string]exec.Handler{"test/pod": handler}})
	if got, err := unbound.PlanNativeCommand(pc, opts); got != nil || resourcev2.ErrorCode(err) != "UNSUPPORTED" {
		t.Fatalf("unbound registry result=%+v error=%v", got, err)
	}
	got, err := m.PlanNativeCommand(pc, opts)
	if err != nil || !reflect.DeepEqual(got, plan) {
		t.Fatalf("plan=%+v error=%v", got, err)
	}
	if pc.Connection.Name != "" {
		t.Fatal("planning mutated caller connection")
	}
	plan = &exec.NativeCommand{}
	if got, err := m.PlanNativeCommand(pc, opts); got != nil || resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("malformed result=%+v error=%v", got, err)
	}
	plan = &exec.NativeCommand{Executable: "sh"}
	mutateRevision = true
	if got, err := m.PlanNativeCommand(pc, opts); got != nil || resourcev2.ErrorCode(err) != "RESET_REQUIRED" {
		t.Fatalf("post-admission result=%+v error=%v", got, err)
	}
	sessions, err := m.ListSessions(nil)
	if err != nil || len(sessions) != 0 || factoryCalls != 0 {
		t.Fatalf("session mutation=%v factory=%d error=%v", sessions, factoryCalls, err)
	}
}
