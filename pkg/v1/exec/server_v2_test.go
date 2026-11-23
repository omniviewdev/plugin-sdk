package exec_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/internal/exectestkit"
	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/sdk"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec/exectest"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const secretCause = "CANARY-callback-secret-91b2"

// safety bounds a wait that the test expects an event to end: it never decides an outcome, it only turns a hang
// into a failure.
const safety = 60 * time.Second

// podContainers are the options of a Pod: its containers, each with a state.
func podContainers() []exec.Option {
	return []exec.Option{{
		Name:     "container",
		Required: true,
		Choices: []exec.Choice{
			{Value: "app", DisplayName: "app", State: exec.ChoiceStateReady, IsDefault: true, Kind: "container"},
			{Value: "migrate", State: exec.ChoiceStateStopped, Detail: "terminated", Kind: "init"},
			{Value: "debugger", State: exec.ChoiceStateStarting, Kind: "ephemeral"},
		},
	}}
}

// execErrorDomain is the google.rpc.ErrorInfo domain of every exec v2 refusal, as the contract spells it. The SDK's
// constant for it is not exported: this is the value a host reads off the wire.
const execErrorDomain = "omniview.sdk.exec.v2"

type fixture struct {
	connections *exectestkit.Connections
	server      *exectestkit.Server
	client      exec.Client
	target      exec.Target
}

func newFixture(t *testing.T, opts ...exectest.FakeTargetOption) *fixture {
	t.Helper()
	target := exectest.NewFakeTarget(append([]exectest.FakeTargetOption{exectest.FakeRouting("k8s", "pods")}, opts...)...)
	connections := exectestkit.NewConnectionsT(t, target.Info.Provider)
	server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}, Connections: connections})
	return &fixture{connections: connections, server: server, client: server.V2(t), target: target}
}

func (f *fixture) ref(id string) exec.TargetRef {
	return f.connections.TargetRef(f.target.Info.Type, id)
}

func (f *fixture) ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), safety)
	t.Cleanup(cancel)
	return f.connections.Context(ctx)
}

// rawCtx is ctx as the SDK's client sends it: with the call's connection and catalog pin serialized into the
// outgoing metadata, for a test that calls the generated client directly.
func (f *fixture) rawCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, err := resourceplugin.OutgoingSessionContext(f.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func asExecError(t *testing.T, err error) *exec.Error {
	t.Helper()
	var typed *exec.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v (%T) is not an *exec.Error", err, err)
	}
	return typed
}

func asResourceError(t *testing.T, err error) *resourcev2.ResourceOperationError {
	t.Helper()
	var typed *resourcev2.ResourceOperationError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v (%T) is not a resource-lane error", err, err)
	}
	return typed
}

func TestDescribeOverTheWire(t *testing.T) {
	f := newFixture(t, exectest.FakeStaticOptions(podContainers()...))
	got, err := f.client.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetContractVersion() != exec.ContractVersion {
		t.Errorf("contract version = %d", got.GetContractVersion())
	}
	if want := []execv2.Feature{execv2.Feature_FEATURE_SESSION, execv2.Feature_FEATURE_ENV, execv2.Feature_FEATURE_CWD}; !slices.Equal(got.GetFeatures(), want) {
		t.Errorf("this build serves Describe, Options and Open; it lists %v, want %v", got.GetFeatures(), want)
	}
	if len(got.GetTargets()) != 1 {
		t.Fatalf("targets = %v", got.GetTargets())
	}
	info, declared := got.GetTargets()[0], f.target.Info
	if info.GetDisplayName() != declared.DisplayName || info.GetProvider() != "k8s" || info.GetType() != "pods" {
		t.Errorf("info = %v", info)
	}
	caps := info.GetCapabilities()
	if caps.GetKill() != execv2.KillBehavior_KILL_BEHAVIOR_TERMINATES || caps.GetExitReport() != execv2.ExitReport_EXIT_REPORT_CODE_AND_SIGNAL ||
		caps.GetStall() != execv2.StallBehavior_STALL_BEHAVIOR_PROPAGATES || !caps.GetTty() {
		t.Errorf("capabilities = %v", caps)
	}
	// The SDK writes every capability it knows: a false is present and false, an empty list is a present empty set.
	if caps.LinkHealth == nil || caps.GetLinkHealth() || caps.Integration == nil || len(caps.GetIntegration().GetModes()) != 0 ||
		caps.GetCwdFact() != execv2.FactMode_FACT_MODE_NONE || caps.GetHostRestart() != execv2.Survival_SURVIVAL_NONE {
		t.Errorf("what the target declared as none did not cross as none: %v", caps)
	}
	session, wantSession := info.GetSession(), exec.DefaultSessionLimits()
	if session.GetMaxSessions() != uint32(wantSession.MaxSessions) || session.GetMaxInputFrame() != uint32(wantSession.MaxInputFrame) ||
		session.GetReplayBytes() != uint32(wantSession.ReplayBytes) || session.GetMaxReplayBytes() != uint32(wantSession.MaxReplayBytes) ||
		session.GetDetachTimeoutMs() != uint32(wantSession.DetachTimeout.Milliseconds()) ||
		session.GetMaxDetachTimeoutMs() != uint32(wantSession.MaxDetachTimeout.Milliseconds()) || session.GetMaxDimension() != uint32(wantSession.MaxDimension) {
		t.Errorf("session limits are not the effective defaults: %v", session)
	}
	run, wantRun := info.GetRun(), exec.DefaultRunLimits()
	if run.GetDefaultTimeoutMs() != uint32(wantRun.DefaultTimeout.Milliseconds()) || run.GetMaxTimeoutMs() != uint32(wantRun.MaxTimeout.Milliseconds()) ||
		run.GetDefaultOutputBytes() != uint32(wantRun.DefaultOutputBytes) || run.GetMaxOutputBytes() != uint32(wantRun.MaxOutputBytes) ||
		run.GetMaxStdinBytes() != uint32(wantRun.MaxStdinBytes) || run.GetMaxConcurrent() != uint32(wantRun.MaxConcurrent) ||
		run.GetMaxArgvBytes() != uint32(wantRun.MaxArgvBytes) || run.GetMaxEnvBytes() != uint32(wantRun.MaxEnvBytes) ||
		run.GetPostExitDrainMs() != uint32(wantRun.PostExitDrain.Milliseconds()) {
		t.Errorf("run limits are not the effective defaults: %v", run)
	}
}

func TestOptionsListsTheContainersOfAPodWithTheirStates(t *testing.T) {
	var got exec.TargetRef
	var seen atomic.Int32
	f := newFixture(t, exectest.FakeOptions(func(pc *types.PluginContext, ref resourcev2.ResourceReference) ([]exec.Option, error) {
		seen.Add(1)
		if pc == nil || pc.Connection == nil || pc.Connection.Name != "configured" {
			t.Errorf("the callback did not receive the configured connection of the lease: %+v", pc)
		}
		got.Reference = ref
		return podContainers(), nil
	}))
	ref := f.ref("pod-1")
	inc := "incarnation-7"
	ref.Reference.Incarnation = &inc

	lower := time.Now()
	result, err := f.client.Options(f.ctx(t), ref)
	upper := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Fatalf("the callback ran %d times", seen.Load())
	}
	if got.Reference.Address.ID != "pod-1" || got.Reference.Incarnation == nil || *got.Reference.Incarnation != inc {
		t.Errorf("the callback received %+v: the reference must arrive whole, incarnation included", got.Reference)
	}
	if len(result.Options) != 1 || result.Options[0].Name != "container" || !result.Options[0].Required {
		t.Fatalf("options = %+v", result.Options)
	}
	want := podContainers()[0].Choices
	if fmt.Sprint(result.Options[0].Choices) != fmt.Sprint(want) {
		t.Errorf("choices = %+v\nwant     %+v", result.Options[0].Choices, want)
	}
	requireObservedDuring(t, result.ObservedAt, lower, upper)
}

// requireObservedDuring fails unless observed, which the wire carries in whole milliseconds, belongs to a call made
// between lower and upper: a zero time, a stale one and one from the future all fail.
func requireObservedDuring(t *testing.T, observed, lower, upper time.Time) {
	t.Helper()
	if observed.IsZero() {
		t.Error("the observation time is missing")
		return
	}
	if got := observed.UnixMilli(); got < lower.UnixMilli() || got > upper.UnixMilli() {
		t.Errorf("ObservedAt is %d ms, outside the call that ran from %d to %d ms", got, lower.UnixMilli(), upper.UnixMilli())
	}
}

// optionsTarget is a kind of target an Options call can reach.
type optionsTarget struct {
	name string
	opts []exectest.FakeTargetOption
	asks bool // an admitted call runs the author's callback
}

// optionsTargets are the two kinds of target an Options call can reach: one whose author registered a callback, which
// counts the times it ran in calls, and one that has none and answers an empty list.
func optionsTargets(calls *atomic.Int32) []optionsTarget {
	return []optionsTarget{
		{"a target with an Options callback", []exectest.FakeTargetOption{
			exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
				calls.Add(1)
				return podContainers(), nil
			}),
		}, true},
		{"a target with no Options callback", nil, false},
	}
}

// A target with no Options callback has nothing to ask, so an admitted request is answered with no options and not with
// an error. The answer carries the time it was taken at, like any other.
func TestOptionsOfATargetWithNoCallbackIsEmptyNotAnError(t *testing.T) {
	f := newFixture(t)
	lower := time.Now()
	result, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
	upper := time.Now()
	if err != nil || result == nil || len(result.Options) != 0 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	requireObservedDuring(t, result.ObservedAt, lower, upper)
}

// Options is a resource utility call whatever the target: the connection is the caller's, the route must match it and
// the catalog the host pinned must be the current one, all of it checked before the author's callback runs. A target
// with no callback is refused the same requests as one that has it: having nothing to ask does not make a request that
// cannot be admitted admissible. The refusals are the resource lane's own, the host's existing contract.
func TestOptionsIsAdmittedThroughTheConnectionLease(t *testing.T) {
	var calls atomic.Int32
	for _, kind := range optionsTargets(&calls) {
		t.Run(kind.name, func(t *testing.T) {
			f := newFixture(t, kind.opts...)
			ref := f.ref("pod-1")
			before := calls.Load()

			refused := []struct {
				name string
				ctx  func() context.Context
				ref  exec.TargetRef
				code string
			}{
				{"no connection in the call", func() context.Context { return context.Background() }, ref, "RESET_REQUIRED"},
				{"no catalog pin", func() context.Context { return f.connections.ContextPinned(context.Background(), "") }, ref, "RESET_REQUIRED"},
				{"a stale catalog pin", func() context.Context { return f.connections.ContextPinned(context.Background(), "catalog-0") }, ref, "RESET_REQUIRED"},
				{"a route that is not the caller's connection", func() context.Context { return f.connections.Context(context.Background()) },
					exec.TargetRef{Reference: ref.Reference, Route: resourcev2.AccessRoute{Connection: "somebody-else"}}, "INVALID_REQUEST"},
			}
			for _, tc := range refused {
				t.Run(tc.name, func(t *testing.T) {
					_, err := f.client.Options(tc.ctx(), tc.ref)
					if got := asResourceError(t, err); got.Code != tc.code {
						t.Errorf("code = %s, want %s (%v)", got.Code, tc.code, err)
					}
				})
			}
			t.Run("the catalog changed since the host pinned it", func(t *testing.T) {
				pinned := f.connections.Context(context.Background())
				f.connections.SetRevision("catalog-2")
				t.Cleanup(func() { f.connections.SetRevision("catalog-1") })
				_, err := f.client.Options(pinned, ref)
				if got := asResourceError(t, err); got.Code != "RESET_REQUIRED" {
					t.Errorf("code = %s", got.Code)
				}
			})
			t.Run("a catalog pin sent twice", func(t *testing.T) {
				// The pin travels as binary metadata, and a second value leaves the plugin no one pin to read: the
				// session context of the call is refused before anything else runs. (The SDK's own client never sends
				// two, so the call goes through the generated client.)
				ctx := metadata.AppendToOutgoingContext(f.rawCtx(t), "omniview-resource-catalog-revision-bin", "catalog-1")
				_, err := f.server.Raw().Options(ctx, mustOptionsRequest(t, f, "pod-1"))
				if got := asResourceError(t, resourceplugin.ResourceDomainError(err)); got.Code != "RESET_REQUIRED" {
					t.Errorf("code = %s, want RESET_REQUIRED (%v)", got.Code, err)
				}
			})
			if calls.Load() != before {
				t.Fatalf("the author's callback ran %d times for requests that were refused before it", calls.Load()-before)
			}
			result, err := f.client.Options(f.ctx(t), ref)
			if err != nil {
				t.Fatalf("an admitted call: %v", err)
			}
			ran := calls.Load() - before
			if kind.asks && (ran != 1 || len(result.Options) != 1) || !kind.asks && (ran != 0 || len(result.Options) != 0) {
				t.Fatalf("an admitted call: the callback ran %d times and the answer has %d options", ran, len(result.Options))
			}
		})
	}
}

func TestOptionsWithAnUnboundConnectionRegistryIsRefusedByTheResourceLayer(t *testing.T) {
	for _, kind := range optionsTargets(new(atomic.Int32)) {
		t.Run(kind.name, func(t *testing.T) {
			target := exectest.NewFakeTarget(append([]exectest.FakeTargetOption{exectest.FakeRouting("k8s", "pods")}, kind.opts...)...)
			connections := exectestkit.NewConnectionsT(t, "k8s")
			server := exectestkit.Serve(t, exectestkit.ServeConfig{Targets: []exec.Target{target}}) // nothing bound
			_, err := server.V2(t).Options(connections.Context(context.Background()), connections.TargetRef("pods", "p"))
			if got := asResourceError(t, err); got.Code != "UNSUPPORTED" {
				t.Errorf("code = %s, want UNSUPPORTED: a plugin with no resource provider cannot lease a connection", got.Code)
			}
		})
	}
}

func TestOptionsRefusesWhatIsNotAnExecRequest(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		calls.Add(1)
		return nil, nil
	}))
	good := f.ref("pod-1")

	t.Run("a selection, sent raw: the SDK's own client has no way to send one", func(t *testing.T) {
		wire := &execv2.OptionsRequest{Target: f.connections.WireTarget(t, "pods", "pod-1", &execv2.NameValue{Name: "container", Value: "app"})}
		_, err := f.server.Raw().Options(f.rawCtx(t), wire)
		st, _ := status.FromError(err)
		if info := execErrorInfo(st); st.Code() != codes.InvalidArgument || info == nil || info.GetReason() != "INVALID_REQUEST" {
			t.Errorf("code = %v, details = %v: want the typed INVALID_REQUEST", st.Code(), st.Details())
		}
	})
	t.Run("a target kind that is not registered", func(t *testing.T) {
		other := f.connections.TargetRef("nodes", "n1")
		_, err := f.client.Options(f.ctx(t), other)
		if got := asExecError(t, err); got.Class != exec.ErrorClassUnsupported {
			t.Errorf("class = %s", got.Class)
		}
	})
	t.Run("a reference that is not canonical, caught by the client before the call", func(t *testing.T) {
		bad := good
		bad.Reference.Address.ID = ""
		_, err := f.client.Options(f.ctx(t), bad)
		if got := asExecError(t, err); got.Class != exec.ErrorClassInvalidRequest {
			t.Errorf("class = %s", got.Class)
		}
	})
	t.Run("a route with no connection, caught by the client", func(t *testing.T) {
		bad := good
		bad.Route = resourcev2.AccessRoute{}
		_, err := f.client.Options(f.ctx(t), bad)
		if got := asExecError(t, err); got.Class != exec.ErrorClassInvalidRequest {
			t.Errorf("class = %s", got.Class)
		}
	})
	t.Run("a request that carries no target, sent raw", func(t *testing.T) {
		_, err := f.server.Raw().Options(f.ctx(t), &execv2.OptionsRequest{})
		if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
			t.Errorf("code = %v", st.Code())
		}
	})
	t.Run("a target with no route, sent raw", func(t *testing.T) {
		// A valid reference and nothing else missing, with the session in place: only the route is absent, so only the
		// route requirement can refuse it.
		wire := mustOptionsRequest(t, f, "pod-1")
		wire.Target.Route = nil
		_, err := f.server.Raw().Options(f.rawCtx(t), wire)
		st, _ := status.FromError(err)
		// The refusal is admission's own, the typed one of the exec domain: not the resource lane's (a route that is not
		// the caller's connection), which is what a request that got past admission without a route would meet.
		if info := execErrorInfo(st); st.Code() != codes.InvalidArgument || info == nil || info.GetReason() != "INVALID_REQUEST" {
			t.Errorf("code = %v, details = %v: want the exec domain's INVALID_REQUEST", st.Code(), st.Details())
		}
	})
	if calls.Load() != 0 {
		t.Fatalf("the callback ran %d times for requests that were refused before it", calls.Load())
	}
}

// Requests are strict: a request field this plugin does not know is refused (UNSUPPORTED), because a field a newer
// host sends can change what a command may do. Responses and frames are lenient.
func TestRequestsAreStrictAndAFieldThePluginDoesNotKnowIsRefused(t *testing.T) {
	var calls atomic.Int32
	f := newFixture(t, exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		calls.Add(1)
		return podContainers(), nil
	}))
	unknown := protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1)

	valid := func() *execv2.OptionsRequest {
		wire, err := resourcev2.ToProtoResourceReference(f.ref("pod-1").Reference)
		if err != nil {
			t.Fatal(err)
		}
		route, _ := resourcev2.ToProtoAccessRoute(f.connections.Route())
		return &execv2.OptionsRequest{Target: &execv2.TargetRef{Reference: wire, Route: route}}
	}
	if _, err := f.server.Raw().Options(f.rawCtx(t), valid()); err != nil {
		t.Fatalf("the control request was refused: %v", err)
	}
	before := calls.Load()

	placements := map[string]func(r *execv2.OptionsRequest){
		"the request":                  func(r *execv2.OptionsRequest) { r.ProtoReflect().SetUnknown(unknown) },
		"the target":                   func(r *execv2.OptionsRequest) { r.Target.ProtoReflect().SetUnknown(unknown) },
		"the reference":                func(r *execv2.OptionsRequest) { r.Target.Reference.ProtoReflect().SetUnknown(unknown) },
		"the address of the reference": func(r *execv2.OptionsRequest) { r.Target.Reference.Address.ProtoReflect().SetUnknown(unknown) },
		"the route":                    func(r *execv2.OptionsRequest) { r.Target.Route.ProtoReflect().SetUnknown(unknown) },
		// The two places inside the element of a repeated message: a request is strict there too.
		"an entry of the scope of the address": func(r *execv2.OptionsRequest) {
			entry := &resourcepb.ScopeEntry{Dimension: "namespace", Value: "default"}
			entry.ProtoReflect().SetUnknown(unknown)
			r.Target.Reference.Address.Scope = []*resourcepb.ScopeEntry{entry}
		},
		"an entry of the selection": func(r *execv2.OptionsRequest) {
			entry := &execv2.NameValue{Name: "container", Value: "app"}
			entry.ProtoReflect().SetUnknown(unknown)
			r.Target.Selection = []*execv2.NameValue{entry}
		},
	}
	for where, place := range placements {
		t.Run("unknown field in "+where, func(t *testing.T) {
			req := valid()
			place(req)
			_, err := f.server.Raw().Options(f.rawCtx(t), req)
			st, _ := status.FromError(err)
			if st.Code() != codes.Unimplemented {
				t.Fatalf("code = %v (%v), want Unimplemented", st.Code(), err)
			}
			if info := execErrorInfo(st); info == nil || info.GetReason() != "UNSUPPORTED" {
				t.Errorf("the refusal is not the typed UNSUPPORTED of the exec domain: %v", st.Details())
			}
		})
	}
	t.Run("unknown field in a Describe request", func(t *testing.T) {
		req := &execv2.DescribeRequest{}
		req.ProtoReflect().SetUnknown(unknown)
		_, err := f.server.Raw().Describe(f.ctx(t), req)
		if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
			t.Errorf("code = %v (%v)", st.Code(), err)
		}
	})
	if calls.Load() != before {
		t.Errorf("the author's callback ran for a request carrying an unknown field")
	}
}

// execErrorInfo returns the exec-domain ErrorInfo of a status, or nil.
func execErrorInfo(st *status.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == execErrorDomain {
			return info
		}
	}
	return nil
}

// What an author's callback returns is classified; only the class, the reason, the facts and the bounded public
// message leave the plugin. The local cause does not.
func TestAnErrorOfTheCallbackIsClassifiedAndItsCauseNeverLeaves(t *testing.T) {
	var next atomic.Pointer[error]
	set := func(err error) { next.Store(&err) }
	f := newFixture(t,
		exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
			return nil, *next.Load()
		}),
		exectest.FakeClassify(func(err error) (*exec.Error, bool) {
			if strings.Contains(err.Error(), "the pod is gone") {
				return &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonTargetGone}, true
			}
			return nil, false
		}),
	)
	tests := []struct {
		name   string
		err    error
		class  exec.ErrorClass
		reason exec.ErrorReason
		facts  exec.ErrorFacts
		msg    string
	}{
		{
			"the author's own typed error",
			&exec.Error{
				Class: exec.ErrorClassForbidden, Reason: exec.ErrorReasonTargetDenied, Message: "you may not exec in this pod",
				Facts: exec.ErrorFacts{SelectionName: "container", SelectionValue: "app", RetryAfter: 3 * time.Second},
				Err:   errors.New(secretCause),
			},
			exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied, exec.ErrorFacts{SelectionName: "container", SelectionValue: "app", RetryAfter: 3 * time.Second}, "you may not exec in this pod",
		},
		{"an error the target's Classify recognises", fmt.Errorf("%s: %s", "the pod is gone", secretCause), exec.ErrorClassNotFound, exec.ErrorReasonTargetGone, exec.ErrorFacts{}, ""},
		{"an error nobody classifies is internal and says nothing", errors.New(secretCause), exec.ErrorClassInternal, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
		{"an internal error cannot carry a message", &exec.Error{Class: exec.ErrorClassInternal, Message: "details", Err: errors.New(secretCause)}, exec.ErrorClassInternal, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
		{"a resource-lane error the author returns is mapped", resourcev2.NewError("NOT_FOUND", secretCause), exec.ErrorClassNotFound, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
		{"a reason that does not pair with its class is a broken contract", &exec.Error{Class: exec.ErrorClassNotFound, Reason: exec.ErrorReasonTargetDenied}, exec.ErrorClassInternal, exec.ErrorReasonUnspecified, exec.ErrorFacts{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			set(tc.err)
			_, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
			got := asExecError(t, err)
			if got.Class != tc.class || got.Reason != tc.reason || got.Facts != tc.facts || got.Message != tc.msg {
				t.Errorf("got %+v, want class %s reason %s facts %+v message %q", got, tc.class, tc.reason, tc.facts, tc.msg)
			}
			if strings.Contains(err.Error(), secretCause) || got.Err != nil {
				t.Errorf("the local cause crossed the wire: %v", err)
			}
			// And on the raw wire: the status, its details and its metadata hold no part of the cause.
			_, rawErr := f.server.Raw().Options(f.rawCtx(t), mustOptionsRequest(t, f, "pod-1"))
			st, _ := status.FromError(rawErr)
			encoded, marshalErr := proto.Marshal(st.Proto())
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(encoded), secretCause) || strings.Contains(st.Message(), secretCause) {
				t.Errorf("the local cause is in the status: %q", encoded)
			}
		})
	}
}

func mustOptionsRequest(t *testing.T, f *fixture, id string) *execv2.OptionsRequest {
	t.Helper()
	ref := f.ref(id)
	reference, err := resourcev2.ToProtoResourceReference(ref.Reference)
	if err != nil {
		t.Fatal(err)
	}
	route, err := resourcev2.ToProtoAccessRoute(ref.Route)
	if err != nil {
		t.Fatal(err)
	}
	return &execv2.OptionsRequest{Target: &execv2.TargetRef{Reference: reference, Route: route}}
}

func TestACallbackThatBreaksTheContractIsAnInternalErrorNotAnAnswer(t *testing.T) {
	bad := [][]exec.Option{
		{{Name: "Container"}}, // not a closed code
		{{Name: "c", Choices: []exec.Choice{{Value: "a"}, {Value: "a"}}}},
		{{Name: "c", Choices: []exec.Choice{{Value: "a", Detail: "It crashed."}}}},
		{{Name: "c", Choices: []exec.Choice{{Value: "a", IsDefault: true}, {Value: "b", IsDefault: true}}}},
		{{Name: "c", Choices: []exec.Choice{{Value: ""}, {Value: ""}}}}, // an empty value is a value, and a value is listed once
	}
	for i, options := range bad {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
			f := newFixture(t, exectest.FakeStaticOptions(options...))
			result, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
			if result != nil {
				t.Fatalf("an answer that breaks the contract reached the host: %+v", result)
			}
			if got := asExecError(t, err); got.Class != exec.ErrorClassInternal {
				t.Errorf("class = %s", got.Class)
			}
		})
	}
}

// The contract bounds a choice value by its length only: an empty value is a value, and the host selects it by
// sending it back.
func TestAnEmptyChoiceValueIsAnAnswer(t *testing.T) {
	f := newFixture(t, exectest.FakeStaticOptions(exec.Option{Name: "container", Choices: []exec.Choice{{Value: ""}, {Value: "app"}}}))
	result, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
	if err != nil {
		t.Fatalf("an empty choice value broke the call: %v", err)
	}
	if len(result.Options) != 1 || len(result.Options[0].Choices) != 2 || result.Options[0].Choices[0].Value != "" {
		t.Errorf("answer = %+v, want the empty value and \"app\"", result.Options)
	}
}

func TestAPanickingCallbackIsAnInternalErrorAndThePluginKeepsServing(t *testing.T) {
	var panics atomic.Bool
	panics.Store(true)
	f := newFixture(t, exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		if panics.Load() {
			panic("the author's bug " + secretCause)
		}
		return podContainers(), nil
	}))
	_, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
	if got := asExecError(t, err); got.Class != exec.ErrorClassInternal || strings.Contains(err.Error(), secretCause) {
		t.Fatalf("a panic reached the host as %v", err)
	}
	panics.Store(false)
	if _, err := f.client.Options(f.ctx(t), f.ref("pod-1")); err != nil {
		t.Fatalf("the plugin stopped serving after a panic: %v", err)
	}
}

// awaitReached waits until the call reaches the point the test is waiting for (reached is closed there). A call that
// failed before it got there never closes it, so its error is reported at once, instead of a hang that only the
// safety timeout would end.
func awaitReached(t *testing.T, reached <-chan struct{}, done <-chan error, what string) {
	t.Helper()
	select {
	case <-reached:
	case err := <-done:
		t.Fatalf("the call ended before %s: %v", what, err)
	case <-time.After(safety):
		t.Fatalf("the call never reached the point where %s", what)
	}
}

func TestACallbackSeesTheCallersCancellation(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan error, 1)
	f := newFixture(t, exectest.FakeOptions(func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		close(entered)
		<-pc.Context.Done()
		released <- pc.Context.Err()
		return nil, pc.Context.Err()
	}))
	ctx, cancel := context.WithCancel(f.ctx(t))
	done := make(chan error, 1)
	go func() {
		_, err := f.client.Options(ctx, f.ref("pod-1"))
		done <- err
	}()
	awaitReached(t, entered, done, "the callback ran")
	cancel()
	select {
	case err := <-released:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the callback's context ended with %v", err)
		}
	case <-time.After(safety):
		t.Fatal("the callback never saw the cancellation")
	}
	err := <-done
	if got := asExecError(t, err); got.Class != exec.ErrorClassCancelled || !errors.Is(err, context.Canceled) {
		t.Errorf("the caller got %v, want a cancelled error wrapping context.Canceled", err)
	}
}

// The lease ends when the connection is retired; the callback sees it, and the caller, still waiting, is told the
// connection was retired rather than that its own call was cancelled.
func TestRetiringTheConnectionEndsTheCallbackAndSaysSo(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, exectest.FakeOptions(func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		close(entered)
		<-pc.Context.Done()
		return nil, pc.Context.Err()
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
		done <- err
	}()
	awaitReached(t, entered, done, "the callback ran")
	f.connections.Retire()
	err := <-done
	got := asExecError(t, err)
	if got.Class != exec.ErrorClassConnection || got.Reason != exec.ErrorReasonConnectionRetired {
		t.Errorf("got %+v, want CONNECTION/CONNECTION_RETIRED", got)
	}
	// A call after the retirement is refused by the lease itself.
	_, err = f.client.Options(f.ctx(t), f.ref("pod-1"))
	if resourceErr := asResourceError(t, err); resourceErr.Code != "CONNECTION_ERROR" {
		t.Errorf("code = %s", resourceErr.Code)
	}
}

// A callback that honours the cancellation by returning without an error ends the lease just as well: the lease
// reports its own cancellation after the callback, and the caller, still waiting, is told the connection was
// retired rather than getting a bare gRPC Canceled.
func TestRetiringTheConnectionUnderACallbackThatReturnsNilSaysSo(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, exectest.FakeOptions(func(pc *types.PluginContext, _ resourcev2.ResourceReference) ([]exec.Option, error) {
		close(entered)
		<-pc.Context.Done()
		return podContainers(), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.client.Options(f.ctx(t), f.ref("pod-1"))
		done <- err
	}()
	awaitReached(t, entered, done, "the callback ran")
	f.connections.Retire()
	var err error
	select {
	case err = <-done:
	case <-time.After(safety):
		t.Fatal("the call did not end after the retirement")
	}
	if err == nil {
		t.Fatal("a call whose lease was retired under it succeeded")
	}
	got := asExecError(t, err)
	if got.Class != exec.ErrorClassConnection || got.Reason != exec.ErrorReasonConnectionRetired {
		t.Errorf("got %+v, want CONNECTION/CONNECTION_RETIRED", got)
	}
}

// catalogHook is a connection registry whose catalog checks can be observed, held and failed. hook runs at every check
// with the context of the lease the check belongs to and the number of the check (the first precedes the author's
// callback, the second one follows it); the check fails with the error hook returns.
type catalogHook struct {
	*exectestkit.Connections
	calls atomic.Int32
	hook  func(ctx context.Context, call int32) error
}

func (h *catalogHook) Catalog(ctx context.Context, req resourcev2.CatalogRequest) (resourcev2.Catalog, error) {
	if err := h.hook(ctx, h.calls.Add(1)); err != nil {
		return resourcev2.Catalog{}, err
	}
	return h.Connections.Catalog(ctx, req)
}

// holdCheck is a catalogHook.hook that holds the check number at of a lease (1 precedes the author's callback, 2 follows
// it): it says so on checking, waits until the lease has ended under it (the connection retired, or the caller gone)
// and answers as check does, the way a provider's catalog read answers once its context is over. Every other check
// passes.
func holdCheck(at int32, checking chan<- struct{}, check func(ctx context.Context) error) func(context.Context, int32) error {
	return func(ctx context.Context, call int32) error {
		if call != at {
			return nil
		}
		close(checking)
		select {
		case <-ctx.Done():
		case <-time.After(safety):
		}
		return check(ctx)
	}
}

// endedCheckAnswers are the ways a catalog check answers once its lease has ended under it: it passes and the lease
// reports its own cancellation, it fails with the bare context error, or it fails the way the resource lane's
// controller does, with a classified error that wraps the context error.
func endedCheckAnswers() map[string]func(ctx context.Context) error {
	return map[string]func(ctx context.Context) error{
		"the check passes and the lease reports its own cancellation": func(context.Context) error { return nil },
		"the check fails with the bare context error":                 func(ctx context.Context) error { return ctx.Err() },
		"the check fails with the resource lane's cancellation": func(ctx context.Context) error {
			return &resourcev2.ResourceOperationError{Code: "CANCELLED", Err: ctx.Err()}
		},
	}
}

// recordedServer serves the exec capability of target, bound to the given connection registry, on an in-memory
// connection, and keeps the error every unary call was answered with as the plugin's handler returned it: a caller
// that has gone never reads its answer, and a test can. It returns the host's client and the answers.
func recordedServer(t *testing.T, target exec.Target, connections resourcev2.ConnectionContextProvider) (exec.Client, <-chan error) {
	t.Helper()
	capability, err := exec.NewTargetPlugin([]exec.Target{target})
	if err != nil {
		t.Fatal(err)
	}
	capability.BindConnectionContextProvider(connections)
	answers := make(chan error, 8)
	log := logging.NewNop()
	record := grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		answers <- err
		return resp, err
	})
	server := sdk.GRPCServerFactoryWithLogger(log, []grpc.ServerOption{record})
	if err := capability.GRPCServer(nil, server); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	options := append(sdk.GRPCDialOptionsWithLogger(log),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
	)
	conn, err := grpc.NewClient("passthrough:///recorded", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := (&exec.Plugin{}).GRPCClient(context.Background(), nil, conn)
	if err != nil {
		t.Fatal(err)
	}
	return client.(*exec.PluginClient).V2(), answers
}

// ObservedAt is the clock when the callback returned. The lease checks the catalog once more after the callback;
// a check that is slow must not make the choices look newer. A target with no callback is observed at the same point:
// where its callback would have returned.
func TestObservedAtIsTheClockWhenTheCallbackReturnedNotWhenTheLeaseWasChecked(t *testing.T) {
	for _, kind := range optionsTargets(new(atomic.Int32)) {
		t.Run(kind.name, func(t *testing.T) {
			checking := make(chan struct{})
			gate := exectest.NewGate()
			defer gate.Open()
			f := newFixture(t, kind.opts...)
			hooked := &catalogHook{Connections: f.connections}
			hooked.hook = func(_ context.Context, call int32) error {
				// The first check precedes the callback; the second one follows it.
				if call == 2 {
					close(checking)
					<-gate.Opened()
				}
				return nil
			}
			f.server.Plugin.BindConnectionContextProvider(hooked)

			type answer struct {
				result *exec.OptionsResult
				err    error
			}
			done := make(chan answer, 1)
			ctx := f.ctx(t)
			lower := time.Now()
			go func() {
				result, err := f.client.Options(ctx, f.ref("pod-1"))
				done <- answer{result, err}
			}()
			select {
			case <-checking:
			case got := <-done:
				t.Fatalf("the call ended (%+v, %v) before the lease checked the catalog after the callback", got.result, got.err)
			case <-time.After(safety):
				t.Fatal("the lease never checked the catalog after the callback")
			}
			// The callback has returned. Let the clock move on, then release the check.
			before := time.Now().UnixMilli()
			for time.Now().UnixMilli() <= before {
			}
			gate.Open()
			var got answer
			select {
			case got = <-done:
			case <-time.After(safety):
				t.Fatal("the call never ended")
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			if observed := got.result.ObservedAt.UnixMilli(); observed > before {
				t.Errorf("ObservedAt is %d ms after the callback returned (read at %d): it was taken after the lease check", observed-before, before)
			}
			requireObservedDuring(t, got.result.ObservedAt, lower, time.Now())
		})
	}
}

// The lease can end while a catalog check holds it: the connection is retired while the caller is still waiting, and
// the caller is told so (CONNECTION_RETIRED) and not that its own call was cancelled, whichever way the check reports
// that its lease ended. The final check, after the callback returned without an error, is the one the callback cannot
// see; the first check, before the callback, is the same lease ending a step earlier. A target with no callback holds
// the same lease for the same checks.
func TestRetiringTheConnectionDuringACatalogCheckSaysSo(t *testing.T) {
	checks := []struct {
		name string
		at   int32
	}{{"the final check", 2}, {"the first check", 1}}
	for _, kind := range optionsTargets(new(atomic.Int32)) {
		for _, check := range checks {
			for answer, answerWith := range endedCheckAnswers() {
				t.Run(kind.name+", "+check.name+": "+answer, func(t *testing.T) {
					f := newFixture(t, kind.opts...)
					checking := make(chan struct{})
					hooked := &catalogHook{Connections: f.connections}
					hooked.hook = holdCheck(check.at, checking, answerWith)
					f.server.Plugin.BindConnectionContextProvider(hooked)

					ctx := f.ctx(t)
					done := make(chan error, 1)
					go func() {
						_, err := f.client.Options(ctx, f.ref("pod-1"))
						done <- err
					}()
					awaitReached(t, checking, done, "the lease held "+check.name)
					// The check holds the lease and the caller is waiting: retire the connection under it. Retire returns
					// when the check has returned, as the lease ended.
					f.connections.Retire()
					var err error
					select {
					case err = <-done:
					case <-time.After(safety):
						t.Fatal("the call did not end after the retirement")
					}
					if err == nil {
						t.Fatal("a call whose lease was retired under it succeeded")
					}
					got := asExecError(t, err)
					if got.Class != exec.ErrorClassConnection || got.Reason != exec.ErrorReasonConnectionRetired {
						t.Errorf("got %+v, want CONNECTION/CONNECTION_RETIRED", got)
					}
				})
			}
		}
	}
}

// A caller that cancels while a check holds the lease is not told the connection was retired: the cancellation is its
// own, and the plugin answers it as one (Canceled), whichever way the check reports that its lease ended. The caller
// has gone and reads its own context error, so the test reads the answer where the plugin sent it.
func TestACallerThatCancelsDuringTheFinalCatalogCheckIsNotToldTheConnectionWasRetired(t *testing.T) {
	for name, answerWith := range endedCheckAnswers() {
		t.Run(name, func(t *testing.T) {
			target := exectest.NewFakeTarget(exectest.FakeRouting("k8s", "pods"), exectest.FakeStaticOptions(podContainers()...))
			connections := exectestkit.NewConnectionsT(t, target.Info.Provider)
			checking := make(chan struct{})
			hooked := &catalogHook{Connections: connections}
			hooked.hook = holdCheck(2, checking, answerWith)
			client, answers := recordedServer(t, target, hooked)

			ctx, cancel := context.WithTimeout(connections.Context(context.Background()), safety)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Options(ctx, connections.TargetRef(target.Info.Type, "pod-1"))
				done <- err
			}()
			awaitReached(t, checking, done, "the lease held the final check")
			cancel()

			var answer error
			select {
			case answer = <-answers:
			case <-time.After(safety):
				t.Fatal("the plugin never answered the call that was cancelled")
			}
			st, _ := status.FromError(answer)
			if st.Code() != codes.Canceled {
				t.Errorf("the plugin answered the caller that cancelled with %v, want Canceled", answer)
			}
			if info := execErrorInfo(st); info != nil {
				t.Errorf("the answer is the typed exec refusal %s, but the cancellation was the caller's own", info.GetReason())
			}
			err := <-done
			if got := asExecError(t, err); got.Class != exec.ErrorClassCancelled || !errors.Is(err, context.Canceled) {
				t.Errorf("the caller got %v, want a cancelled error wrapping context.Canceled", err)
			}
		})
	}
}

func TestConcurrentCallsAreIndependentAndLeaveNoLeaseBehind(t *testing.T) {
	const callers = 64
	var running, peak atomic.Int32
	entered := make(chan struct{}, callers)
	gate := exectest.NewGate()
	defer gate.Open()
	f := newFixture(t, exectest.FakeOptions(func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) {
		now := running.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		entered <- struct{}{}
		<-gate.Opened()
		running.Add(-1)
		return podContainers(), nil
	}))
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.client.Options(f.ctx(t), f.ref(fmt.Sprintf("pod-%d", i)))
			if err == nil && (len(result.Options) != 1 || len(result.Options[0].Choices) != 3) {
				err = fmt.Errorf("answer %+v", result)
			}
			errs <- err
		}()
	}
	// Every caller is inside its callback at once: the calls are concurrent, none serialises behind another.
	for range callers {
		select {
		case <-entered:
		case err := <-errs:
			t.Fatalf("a call ended before the gate opened: %v", err)
		case <-time.After(safety):
			t.Fatal("the calls did not all reach their callbacks: they are not concurrent")
		}
	}
	if peak.Load() != callers {
		t.Errorf("peak concurrency %d, want %d", peak.Load(), callers)
	}
	gate.Open()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	// End state: no lease is held. Retire waits for the callbacks that hold one: it returns.
	retired := make(chan struct{})
	go func() { f.connections.Retire(); close(retired) }()
	select {
	case <-retired:
	case <-time.After(safety):
		t.Fatal("a lease was left held after every call returned")
	}
}

// Run, RunStream, ListSessions, ReadOutput and EndSession exist in the service and answer Unimplemented with a typed refusal
// (UNSUPPORTED) in this build; Describe lists no feature for them. (Open is served: open_wire_test.go.)
func TestTheMethodsThisBuildDoesNotServeAnswerATypedUnimplemented(t *testing.T) {
	f := newFixture(t)
	raw := f.server.Raw()
	ctx := f.ctx(t)
	calls := map[string]func() error{
		"Run": func() error { _, err := raw.Run(ctx, &execv2.RunRequest{}); return err },
		"RunStream": func() error {
			stream, err := raw.RunStream(ctx, &execv2.RunRequest{})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		},
		"ListSessions": func() error { _, err := raw.ListSessions(ctx, &execv2.ListSessionsRequest{}); return err },
		"ReadOutput":   func() error { _, err := raw.ReadOutput(ctx, &execv2.ReadOutputRequest{}); return err },
		"EndSession":   func() error { _, err := raw.EndSession(ctx, &execv2.EndSessionRequest{}); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.Unimplemented {
				t.Fatalf("%s: %v", name, err)
			}
			var info *errdetails.ErrorInfo
			for _, d := range st.Details() {
				if i, ok := d.(*errdetails.ErrorInfo); ok {
					info = i
				}
			}
			if info == nil || info.GetDomain() != execErrorDomain || info.GetReason() != "UNSUPPORTED" {
				t.Errorf("%s is Unimplemented without the typed refusal: %v", name, st.Details())
			}
		})
	}
}
