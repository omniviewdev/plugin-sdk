package exec

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// recordingRPC is the generated client of a plugin that answers canned messages and keeps the context of the call it
// received, and whether that context had ended when the call arrived, so that a test can read what the client did to it
// without waiting for anything. The other methods of the service are the nil interface's: a test that calls one panics.
type recordingRPC struct {
	execv2.ExecClient

	describe *execv2.DescribeResponse
	options  *execv2.OptionsResponse

	ctx       context.Context // the context of the last call
	errAtCall error           // ctx.Err() at the moment the call reached the plugin
}

func (r *recordingRPC) Describe(ctx context.Context, _ *execv2.DescribeRequest, _ ...grpc.CallOption) (*execv2.DescribeResponse, error) {
	r.ctx, r.errAtCall = ctx, ctx.Err()
	return r.describe, nil
}

func (r *recordingRPC) Options(ctx context.Context, _ *execv2.OptionsRequest, _ ...grpc.CallOption) (*execv2.OptionsResponse, error) {
	r.ctx, r.errAtCall = ctx, ctx.Err()
	return r.options, nil
}

// refusingRPC is the generated client of a plugin that answers every call with the error it was given.
type refusingRPC struct {
	execv2.ExecClient

	err error
}

func (r *refusingRPC) Describe(context.Context, *execv2.DescribeRequest, ...grpc.CallOption) (*execv2.DescribeResponse, error) {
	return nil, r.err
}

func (r *refusingRPC) Options(context.Context, *execv2.OptionsRequest, ...grpc.CallOption) (*execv2.OptionsResponse, error) {
	return nil, r.err
}

// calls are the two unary methods of the client, each as a function of the client and the context of the call.
func clientCalls(t *testing.T) map[string]func(c *clientV2, ctx context.Context) error {
	t.Helper()
	ref := TargetRef{
		Reference: resourcev2.ResourceReference{Address: resourcev2.ResourceAddress{
			Provider: "k8s", Authority: "authority", Type: "pods", Scope: []resourcev2.ScopeEntry{}, ID: "pod-1",
		}},
		Route: resourcev2.AccessRoute{Connection: "conn-1"},
	}
	return map[string]func(c *clientV2, ctx context.Context) error{
		"Describe": func(c *clientV2, ctx context.Context) error { _, err := c.Describe(ctx); return err },
		"Options":  func(c *clientV2, ctx context.Context) error { _, err := c.Options(ctx, ref); return err },
	}
}

// A unary call whose context has no deadline is bounded by the client's call timeout, so that a wedged plugin cannot
// hold the caller for ever; a call whose context has one keeps it, shorter or longer than the timeout. The test reads
// the context that reaches the plugin's end of the call: it waits for nothing and decides nothing by a clock.
func TestTheClientBoundsACallWithNoDeadlineAndKeepsTheCallersOwn(t *testing.T) {
	const timeout = 10 * time.Minute
	for name, call := range clientCalls(t) {
		newClient := func() (*clientV2, *recordingRPC) {
			rpc := &recordingRPC{describe: validDescription(t), options: &execv2.OptionsResponse{}}
			c := newClientV2(rpc, nil)
			c.callTimeout = timeout
			return c, rpc
		}

		t.Run(name+" without a deadline gets the call timeout", func(t *testing.T) {
			c, rpc := newClient()
			before := time.Now()
			if err := call(c, context.Background()); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			deadline, ok := rpc.ctx.Deadline()
			if !ok {
				t.Fatal("the call reached the plugin with no deadline: a wedged plugin would hold the caller for ever")
			}
			if deadline.Before(before.Add(timeout)) || deadline.After(after.Add(timeout)) {
				t.Errorf("deadline %v is not the call timeout (%v) after the start of the call, which was between %v and %v",
					deadline, timeout, before, after)
			}
			if rpc.errAtCall != nil {
				t.Fatalf("the call reached the plugin with a context that was already over: %v", rpc.errAtCall)
			}
			if rpc.ctx.Err() == nil {
				t.Error("the deadline the client added was not released when the call ended")
			}
		})

		t.Run(name+" keeps a deadline of the caller that is shorter than the timeout", func(t *testing.T) {
			c, rpc := newClient()
			want := time.Now().Add(time.Minute)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			if err := call(c, ctx); err != nil {
				t.Fatal(err)
			}
			if got, ok := rpc.ctx.Deadline(); !ok || !got.Equal(want) {
				t.Errorf("deadline = %v, %v: the caller's own deadline %v was changed", got, ok, want)
			}
		})

		t.Run(name+" keeps a deadline of the caller that is longer than the timeout", func(t *testing.T) {
			c, rpc := newClient()
			want := time.Now().Add(time.Hour)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			if err := call(c, ctx); err != nil {
				t.Fatal(err)
			}
			if got, ok := rpc.ctx.Deadline(); !ok || !got.Equal(want) {
				t.Errorf("deadline = %v, %v: the caller's own deadline %v was replaced by the shorter call timeout", got, ok, want)
			}
			if rpc.ctx.Err() != nil {
				t.Errorf("the client ended a context it did not own: %v", rpc.ctx.Err())
			}
		})

		t.Run(name+" still ends with the caller", func(t *testing.T) {
			c, rpc := newClient()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = call(c, ctx)
			if rpc.ctx == nil || rpc.errAtCall == nil {
				t.Error("a call whose caller was already cancelled reached the plugin with a live context")
			}
		})
	}
}

func TestTheClientsDefaultCallTimeoutIsThirtySeconds(t *testing.T) {
	if got := newClientV2(&recordingRPC{}, nil).callTimeout; got != 30*time.Second {
		t.Errorf("the default call timeout is %v", got)
	}
}

// A call that returned a refusal returns it, though the caller's deadline has passed by the time the client decodes the
// answer: the host acts on a refusal (RESET_REQUIRED asks for a reset) and the later deadline does not replace it. The
// caller's context, here already past its deadline, is what the client reads after the call returned.
func TestTheClientReturnsTheRefusalTheCallGotWhenTheCallersDeadlineHasPassed(t *testing.T) {
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	for name, call := range clientCalls(t) {
		t.Run(name+" with a refusal of the plugin", func(t *testing.T) {
			c := newClientV2(&refusingRPC{err: (&Error{Class: ErrorClassUnavailable, Reason: ErrorReasonNotRunning}).status()}, nil)
			err := call(c, expired)
			var typed *Error
			if !errors.As(err, &typed) || typed.Class != ErrorClassUnavailable || typed.Reason != ErrorReasonNotRunning || errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("got %#v, want the plugin's UNAVAILABLE/NOT_RUNNING", err)
			}
		})
		// TIMEOUT and CANCELLED travel as the DeadlineExceeded and Canceled codes with a resource detail: still the resource
		// lane's refusal, not the caller's context error.
		for _, code := range []string{"RESET_REQUIRED", "TIMEOUT", "CANCELLED"} {
			t.Run(name+" with a "+code+" refusal of the resource lane", func(t *testing.T) {
				c := newClientV2(&refusingRPC{err: resourceplugin.ResourceRPCError(resourcev2.NewError(code, "pin"))}, nil)
				err := call(c, expired)
				var resourceErr *resourcev2.ResourceOperationError
				if !errors.As(err, &resourceErr) || resourceErr.Code != code || errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("got %#v, want the resource lane's %s", err, code)
				}
			})
		}
	}
}

// Describe returns the plugin's message, validated and otherwise as it came: what the plugin did not send is absent in
// it, and the host reads an absent field as "not known", never as false. The client does not fill anything in.
func TestDescribeReturnsThePluginsMessageWithItsAbsentFieldsAbsent(t *testing.T) {
	sent := validDescription(t)
	caps := sent.GetTargets()[0].GetCapabilities()
	caps.Tty, caps.Resize, caps.Env, caps.LinkHealth = nil, nil, nil, nil
	caps.Signals, caps.Integration = nil, nil
	caps.CwdFact, caps.HostRestart = execv2.FactMode_FACT_MODE_UNSPECIFIED, execv2.Survival_SURVIVAL_UNSPECIFIED
	want := proto.Clone(sent)

	got, err := newClientV2(&recordingRPC{describe: sent}, nil).Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("Describe changed the message:\n got  %v\n want %v", got, want)
	}
	gotCaps := got.GetTargets()[0].GetCapabilities()
	if gotCaps.Tty != nil || gotCaps.Resize != nil || gotCaps.Env != nil || gotCaps.LinkHealth != nil || gotCaps.Signals != nil || gotCaps.Integration != nil {
		t.Errorf("a field the plugin did not send is present: %v", gotCaps)
	}
	if gotCaps.SeparateStderr == nil || gotCaps.StdinEof == nil {
		t.Errorf("a field the plugin sent is absent: %v", gotCaps)
	}
	if gotCaps.CwdFact != execv2.FactMode_FACT_MODE_UNSPECIFIED || gotCaps.HostRestart != execv2.Survival_SURVIVAL_UNSPECIFIED {
		t.Errorf("an enum the plugin did not say is not unspecified: %v", gotCaps)
	}
}
