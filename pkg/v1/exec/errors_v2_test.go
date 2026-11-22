package exec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
)

// canary is local-only text: it stands for what an author puts in Error.Err and must never leave the process.
const canary = "CANARY-secret-token-8f3a"

func TestEveryReasonHasItsClassesAndNoOtherPairingIsValid(t *testing.T) {
	for _, p := range errorReasonPairs {
		classes, ok := reasonClasses[p.g]
		if !ok || len(classes) == 0 {
			t.Errorf("reason %s has no class it may be paired with", p.g)
		}
		for _, c := range classes {
			if !validPair(c, p.g) {
				t.Errorf("(%s, %s) is listed and not valid", c, p.g)
			}
		}
	}
	for reason := range reasonClasses {
		if reason.toProto() == 0 {
			t.Errorf("reasonClasses lists %d, which is not a reason", int(reason))
		}
	}
	for _, c := range errorClassPairs {
		if !validPair(c.g, ReasonUnspecified) {
			t.Errorf("an unspecified reason must pair with %s", c.g)
		}
		for _, p := range errorReasonPairs {
			want := false
			for _, allowed := range reasonClasses[p.g] {
				want = want || allowed == c.g
			}
			if got := validPair(c.g, p.g); got != want {
				t.Errorf("validPair(%s, %s) = %v, want %v", c.g, p.g, got, want)
			}
		}
	}
	if validPair(ClassUnspecified, ReasonUnspecified) {
		t.Error("an unspecified class is not a class a producer may send")
	}
}

func TestNormalizedBoundsWhatMayCrossTheWire(t *testing.T) {
	long := strings.Repeat("x", maxFactBytes+1)
	tests := []struct {
		name string
		in   *Error
		want Error // compared without Err
	}{
		{"nil", nil, Error{Class: ClassInternal}},
		{"unspecified class reads as internal", &Error{}, Error{Class: ClassInternal}},
		{
			"valid pair is kept",
			&Error{Class: ClassForbidden, Reason: ReasonTargetDenied, Message: "no"},
			Error{Class: ClassForbidden, Reason: ReasonTargetDenied, Message: "no"},
		},
		{
			"a reason of another class is a broken contract",
			&Error{Class: ClassNotFound, Reason: ReasonTargetDenied, Message: "kept?"},
			Error{Class: ClassInternal},
		},
		{
			"facts that break their bound are dropped, never truncated",
			&Error{Class: ClassNotFound, Facts: ErrorFacts{Program: long, SelectionName: "c\x00", SelectionValue: "app"}},
			Error{Class: ClassNotFound, Facts: ErrorFacts{SelectionValue: "app"}},
		},
		{
			"retry advice is whole milliseconds",
			&Error{Class: ClassUnavailable, Facts: ErrorFacts{RetryAfter: 1500*time.Millisecond + 400*time.Microsecond}},
			Error{Class: ClassUnavailable, Facts: ErrorFacts{RetryAfter: 1500 * time.Millisecond}},
		},
		{
			"retry advice beyond the wire range is dropped",
			&Error{Class: ClassUnavailable, Facts: ErrorFacts{RetryAfter: 100 * 24 * 365 * time.Hour}},
			Error{Class: ClassUnavailable},
		},
		{
			"a message is dropped for an internal error",
			&Error{Class: ClassInternal, Message: "details"},
			Error{Class: ClassInternal},
		},
		{
			"a message that is too long is dropped",
			&Error{Class: ClassForbidden, Message: strings.Repeat("m", maxPublicMessage+1)},
			Error{Class: ClassForbidden},
		},
		{
			"a message that is not plain text is dropped",
			&Error{Class: ClassForbidden, Message: "bad\x1b[31m"},
			Error{Class: ClassForbidden},
		},
		{
			"a message that is not UTF-8 is dropped",
			&Error{Class: ClassForbidden, Message: "bad\xff"},
			Error{Class: ClassForbidden},
		},
		{
			"a message at the bound is kept",
			&Error{Class: ClassForbidden, Message: strings.Repeat("m", maxPublicMessage)},
			Error{Class: ClassForbidden, Message: strings.Repeat("m", maxPublicMessage)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var before Error
			if tc.in != nil {
				before = *tc.in
			}
			got := tc.in.normalized()
			cause := got.Err
			got.Err = nil
			if *got != tc.want {
				t.Errorf("normalized = %+v, want %+v", *got, tc.want)
			}
			if tc.in != nil && *tc.in != before {
				t.Errorf("normalized modified its receiver: %+v, was %+v", *tc.in, before)
			}
			if tc.in != nil && cause != tc.in.Err {
				t.Errorf("the local cause was not kept")
			}
		})
	}
}

func TestStatusRoundTripsClassReasonFactsAndMessage(t *testing.T) {
	cases := []*Error{
		{Class: ClassForbidden, Reason: ReasonTargetDenied, Message: "exec in this Pod is not allowed"},
		{Class: ClassNotFound, Reason: ReasonNoSuchExecutable, Facts: ErrorFacts{Program: "/bin/bash", SelectionName: "container", SelectionValue: "app"}},
		{Class: ClassLimit, Reason: ReasonTooManySessions, Facts: ErrorFacts{RetryAfter: 2 * time.Second}},
		{Class: ClassConnection, Reason: ReasonConnectionRetired},
		{Class: ClassInvalidRequest, Reason: ReasonConsentRequired},
		{Class: ClassInternal},
		{Class: ClassTimeout},
		{Class: ClassCancelled},
	}
	for _, in := range cases {
		t.Run(in.Class.String()+"/"+in.Reason.String(), func(t *testing.T) {
			in.Err = errors.New(canary)
			wire := in.status()
			st, ok := status.FromError(wire)
			if !ok {
				t.Fatalf("not a status: %v", wire)
			}
			if st.Code() != in.Class.grpcCode() {
				t.Errorf("code %v, want %v", st.Code(), in.Class.grpcCode())
			}
			if st.Message() != statusMessage {
				t.Errorf("status message %q: nothing but the fixed text may cross", st.Message())
			}
			got := errorFromStatus(context.Background(), wire)
			typed, ok := got.(*Error)
			if !ok {
				t.Fatalf("decoded to %T, want *Error", got)
			}
			want := *in.normalized()
			want.Err = nil
			if *typed != want {
				t.Errorf("decoded %+v, want %+v", *typed, want)
			}
			// The canary stands for a local cause: it must be in no part of the wire form.
			encoded, err := proto.Marshal(st.Proto())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), canary) || strings.Contains(wire.Error(), canary) || strings.Contains(typed.Error(), canary) {
				t.Fatalf("the local cause crossed the wire: %q", encoded)
			}
		})
	}
}

func TestStatusUsesTheExecDomainAndTheDocumentedMetadata(t *testing.T) {
	wire := (&Error{
		Class:   ClassNotFound,
		Reason:  ReasonNoSuchExecutable,
		Facts:   ErrorFacts{Program: "sh", SelectionName: "container", SelectionValue: "app", RetryAfter: 1500 * time.Millisecond},
		Message: "there is no shell in this image",
	}).status()
	st, _ := status.FromError(wire)
	var info *errdetails.ErrorInfo
	var localized *errdetails.LocalizedMessage
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *errdetails.ErrorInfo:
			info = v
		case *errdetails.LocalizedMessage:
			localized = v
		}
	}
	if info == nil || info.GetDomain() != "omniview.sdk.exec.v2" || info.GetReason() != "NOT_FOUND" {
		t.Fatalf("ErrorInfo = %v", info)
	}
	wantMetadata := map[string]string{
		"reason":          "NO_SUCH_EXECUTABLE",
		"program":         "sh",
		"selection_name":  "container",
		"selection_value": "app",
		"retry_after_ms":  "1500",
	}
	if fmt.Sprint(info.GetMetadata()) != fmt.Sprint(wantMetadata) {
		t.Errorf("metadata = %v, want %v", info.GetMetadata(), wantMetadata)
	}
	if localized == nil || localized.GetLocale() != "en" || localized.GetMessage() != "there is no shell in this image" {
		t.Errorf("LocalizedMessage = %v", localized)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v", st.Code())
	}
}

func TestEveryClassHasAGRPCCodeAndItsReverseIsAClass(t *testing.T) {
	for _, p := range errorClassPairs {
		code := p.g.grpcCode()
		if code == codes.OK {
			t.Errorf("%s maps to OK", p.g)
		}
		if classFromCode(code) == ClassUnspecified {
			t.Errorf("code %v reads as an unspecified class", code)
		}
	}
	if ClassUnspecified.grpcCode() != codes.Internal {
		t.Errorf("an unknown class is an internal error")
	}
}

func TestErrorFromStatusClassifiesWhatCarriesNoExecDetail(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"the plugin does not serve the lane", status.Error(codes.Unimplemented, "unknown service omniview.sdk.exec.v2.Exec"), ClassUnsupported},
		{"the plugin is gone", status.Error(codes.Unavailable, "connection refused"), ClassConnection},
		{"a deadline of the call", status.Error(codes.DeadlineExceeded, "deadline"), ClassTimeout},
		{"a rejection of the call", status.Error(codes.InvalidArgument, "bad"), ClassInvalidRequest},
		{"an internal failure", status.Error(codes.Internal, "boom"), ClassInternal},
		{"an error that is not a status", errors.New("plain"), ClassInternal},
		{"an unknown code", status.Error(codes.Code(99), "x"), ClassInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := errorFromStatus(ctx, tc.err)
			typed, ok := got.(*Error)
			if !ok || typed.Class != tc.want {
				t.Fatalf("got %#v, want class %s", got, tc.want)
			}
			if strings.Contains(typed.Error(), "boom") || strings.Contains(typed.Error(), "unknown service") {
				t.Errorf("the server's status text reached the error: %q", typed.Error())
			}
		})
	}
	if errorFromStatus(ctx, nil) != nil {
		t.Error("a nil error decodes to a non-nil error")
	}
}

func TestErrorFromStatusReadsThePeersContextAsTheCallers(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := errorFromStatus(canceled, status.Error(codes.Canceled, "x"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Class != ClassCancelled || !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled call decoded to %#v", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	err = errorFromStatus(expired, status.Error(codes.DeadlineExceeded, "x"))
	if !errors.As(err, &typed) || typed.Class != ClassTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a call past its deadline decoded to %#v", err)
	}
}

// A bare status keeps the class of its own code. The caller's context error is wrapped only when it says the same thing
// the status does: a Canceled status stays ClassCancelled and a DeadlineExceeded status stays ClassTimeout, whichever
// way the context ended after the call returned, and the error then wraps no context error that would contradict it.
func TestErrorFromStatusKeepsTheClassOfABareStatusWhateverTheContextSays(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	tests := []struct {
		name  string
		ctx   context.Context
		code  codes.Code
		class ErrorClass
		cause error // wrapped, or nil when the context does not say what the status says
	}{
		{"a Canceled status, the caller cancelled", canceled, codes.Canceled, ClassCancelled, context.Canceled},
		{"a Canceled status, the caller's deadline passed", expired, codes.Canceled, ClassCancelled, nil},
		{"a DeadlineExceeded status, the caller's deadline passed", expired, codes.DeadlineExceeded, ClassTimeout, context.DeadlineExceeded},
		{"a DeadlineExceeded status, the caller cancelled", canceled, codes.DeadlineExceeded, ClassTimeout, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errorFromStatus(tt.ctx, status.Error(tt.code, "x"))
			var typed *Error
			if !errors.As(err, &typed) || typed.Class != tt.class {
				t.Fatalf("decoded to %#v, want %s", err, tt.class)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Errorf("%v does not wrap %v", err, tt.cause)
			}
			if tt.cause == nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				t.Errorf("%v wraps a context error that contradicts its class", err)
			}
		})
	}
}

// The call returned a status, and it is decoded first: the caller's context is read only when the status itself says
// that the context ended the call (Canceled or DeadlineExceeded, with no exec detail). A refusal the plugin sent keeps
// its class and reason when the caller's deadline passed, or the caller gave up, a moment after the call returned: the
// host acts on it (RESET_REQUIRED asks for a reset) and no later context error replaces it.
func TestErrorFromStatusDecodesTheStatusBeforeItReadsTheContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	callers := []struct {
		name         string
		ctx          context.Context
		code         codes.Code // the status of a call that this context ended
		resourceCode string     // the resource lane's own name for it
		class        ErrorClass
		cause        error
	}{
		{"cancelled", canceled, codes.Canceled, "CANCELLED", ClassCancelled, context.Canceled},
		{"past its deadline", expired, codes.DeadlineExceeded, "TIMEOUT", ClassTimeout, context.DeadlineExceeded},
	}
	for _, caller := range callers {
		t.Run("the caller is "+caller.name, func(t *testing.T) {
			notTheContext := func(t *testing.T, err error) {
				t.Helper()
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("a refusal the call returned was replaced by the caller's context error: %v", err)
				}
			}
			t.Run("a typed refusal of the plugin", func(t *testing.T) {
				err := errorFromStatus(caller.ctx, (&Error{Class: ClassConflict, Reason: ReasonSessionExists}).status())
				var typed *Error
				if !errors.As(err, &typed) || typed.Class != ClassConflict || typed.Reason != ReasonSessionExists {
					t.Fatalf("decoded to %#v, want the plugin's CONFLICT/SESSION_EXISTS", err)
				}
				notTheContext(t, err)
			})
			t.Run("a typed refusal that is a cancellation or a timeout of the plugin's own", func(t *testing.T) {
				for _, class := range []ErrorClass{ClassCancelled, ClassTimeout} {
					err := errorFromStatus(caller.ctx, (&Error{Class: class}).status())
					var typed *Error
					if !errors.As(err, &typed) || typed.Class != class {
						t.Fatalf("a typed %s of the plugin decoded to %#v", class, err)
					}
					notTheContext(t, err)
				}
			})
			t.Run("a refusal of the resource lane", func(t *testing.T) {
				err := errorFromStatus(caller.ctx, resourceplugin.ResourceRPCError(resourcev2.NewError("RESET_REQUIRED", "pin")))
				var resourceErr *resourcev2.ResourceOperationError
				if !errors.As(err, &resourceErr) || resourceErr.Code != "RESET_REQUIRED" {
					t.Fatalf("decoded to %#v, want the resource lane's RESET_REQUIRED", err)
				}
				notTheContext(t, err)
			})
			t.Run("a failure of the connection", func(t *testing.T) {
				err := errorFromStatus(caller.ctx, status.Error(codes.Unavailable, "connection reset"))
				var typed *Error
				if !errors.As(err, &typed) || typed.Class != ClassConnection {
					t.Fatalf("decoded to %#v, want ClassConnection", err)
				}
				notTheContext(t, err)
			})

			// The resource lane's own TIMEOUT and CANCELLED refusals carry the DeadlineExceeded and Canceled codes with a
			// resource detail: they are the answer of the call, and keep their resource code however the caller's context
			// fared after the call returned.
			t.Run("the resource lane's own account of it keeps its resource code", func(t *testing.T) {
				err := errorFromStatus(caller.ctx, resourceplugin.ResourceRPCError(resourcev2.NewError(caller.resourceCode, "x")))
				var resourceErr *resourcev2.ResourceOperationError
				if !errors.As(err, &resourceErr) || resourceErr.Code != caller.resourceCode {
					t.Fatalf("decoded to %#v, want the resource lane's %s", err, caller.resourceCode)
				}
				var typed *Error
				if errors.As(err, &typed) {
					t.Errorf("a resource-layer refusal became an exec *Error: %#v", typed)
				}
			})

			// What does say the context ended the call reads as the context's own error: the status of the cancelled or
			// expired call, and a plain context error.
			for _, ended := range []struct {
				name string
				err  error
			}{
				{"the status of the call that ended", status.Error(caller.code, "x")},
				{"a plain context error", fmt.Errorf("rpc: %w", caller.cause)},
			} {
				t.Run(ended.name+" is the caller's context", func(t *testing.T) {
					err := errorFromStatus(caller.ctx, ended.err)
					var typed *Error
					if !errors.As(err, &typed) || typed.Class != caller.class || !errors.Is(err, caller.cause) {
						t.Fatalf("decoded to %#v, want %s wrapping %v", err, caller.class, caller.cause)
					}
				})
			}
		})
	}
	t.Run("an error that is not a status and not a context error stays internal", func(t *testing.T) {
		err := errorFromStatus(canceled, errors.New("plain"))
		var typed *Error
		if !errors.As(err, &typed) || typed.Class != ClassInternal || errors.Is(err, context.Canceled) {
			t.Errorf("decoded to %#v, want ClassInternal", err)
		}
	})
}

func TestErrorFromStatusKeepsTheResourceLayersOwnRefusals(t *testing.T) {
	wire := resourceplugin.ResourceRPCError(resourcev2.NewError("RESET_REQUIRED", "pin"))
	got := errorFromStatus(context.Background(), wire)
	var resourceErr *resourcev2.ResourceOperationError
	if !errors.As(got, &resourceErr) || resourceErr.Code != "RESET_REQUIRED" {
		t.Fatalf("decoded to %#v, want the resource lane's RESET_REQUIRED", got)
	}
	var execErr *Error
	if errors.As(got, &execErr) {
		t.Error("a resource-layer refusal must not be an exec *Error: its vocabulary has no equivalent")
	}
}

func TestErrorFromStatusIsStrictAboutWhatThePeerSent(t *testing.T) {
	build := func(info *errdetails.ErrorInfo, more ...*errdetails.LocalizedMessage) error {
		st := status.New(codes.Aborted, "x")
		withInfo, err := st.WithDetails(info)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range more {
			withInfo, err = withInfo.WithDetails(m)
			if err != nil {
				t.Fatal(err)
			}
		}
		return withInfo.Err()
	}
	t.Run("an unknown class reads as internal", func(t *testing.T) {
		got := errorFromStatus(context.Background(), build(&errdetails.ErrorInfo{Domain: errorDomain, Reason: "TELEPORTED"}))
		if typed, ok := got.(*Error); !ok || typed.Class != ClassInternal {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("a reason that does not pair with the class is dropped", func(t *testing.T) {
		got := errorFromStatus(context.Background(), build(&errdetails.ErrorInfo{
			Domain: errorDomain, Reason: "FORBIDDEN", Metadata: map[string]string{"reason": "TARGET_GONE"},
		}))
		if typed, ok := got.(*Error); !ok || typed.Class != ClassForbidden || typed.Reason != ReasonUnspecified {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("facts beyond their bound are dropped", func(t *testing.T) {
		got := errorFromStatus(context.Background(), build(&errdetails.ErrorInfo{
			Domain: errorDomain, Reason: "NOT_FOUND",
			Metadata: map[string]string{"program": strings.Repeat("p", 300), "selection_value": "ok", "retry_after_ms": "not a number"},
		}))
		typed, ok := got.(*Error)
		if !ok || typed.Facts != (ErrorFacts{SelectionValue: "ok"}) {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("a message is read only for a reviewed class and within its bound", func(t *testing.T) {
		internal := errorFromStatus(context.Background(), build(
			&errdetails.ErrorInfo{Domain: errorDomain, Reason: "INTERNAL"},
			&errdetails.LocalizedMessage{Locale: "en", Message: "secret detail"}))
		if typed, ok := internal.(*Error); !ok || typed.Message != "" {
			t.Errorf("an internal error carried a message: %#v", internal)
		}
		long := errorFromStatus(context.Background(), build(
			&errdetails.ErrorInfo{Domain: errorDomain, Reason: "FORBIDDEN"},
			&errdetails.LocalizedMessage{Locale: "en", Message: strings.Repeat("m", maxPublicMessage+1)}))
		if typed, ok := long.(*Error); !ok || typed.Message != "" {
			t.Errorf("an over-long message was carried: %#v", long)
		}
		other := errorFromStatus(context.Background(), build(
			&errdetails.ErrorInfo{Domain: errorDomain, Reason: "FORBIDDEN"},
			&errdetails.LocalizedMessage{Locale: "fr", Message: "non"}))
		if typed, ok := other.(*Error); !ok || typed.Message != "" {
			t.Errorf("a message in another locale was carried: %#v", other)
		}
	})
	t.Run("an ErrorInfo of another domain is not ours", func(t *testing.T) {
		got := errorFromStatus(context.Background(), build(&errdetails.ErrorInfo{Domain: "example.com", Reason: "FORBIDDEN"}))
		if typed, ok := got.(*Error); !ok || typed.Class != ClassConflict {
			t.Errorf("a foreign domain was read as ours: %#v", got)
		}
	})
}

func TestErrorDescribesClassAndReasonAndNeverTheCause(t *testing.T) {
	e := &Error{Class: ClassNotFound, Reason: ReasonTargetGone, Message: "the Pod is gone", Err: errors.New(canary)}
	if got, want := e.Error(), "exec: NOT_FOUND/TARGET_GONE: the Pod is gone"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(e.Error(), canary) {
		t.Error("Error() printed the local cause")
	}
	if !errors.Is(e, e.Err) || e.Unwrap() != e.Err {
		t.Error("the local cause is not reachable through errors.Is")
	}
	if got := (&Error{Class: ClassTimeout}).Error(); got != "exec: TIMEOUT" {
		t.Errorf("Error() = %q", got)
	}
	var nilErr *Error
	if nilErr.Error() == "" || nilErr.Unwrap() != nil {
		t.Errorf("a nil *Error must be safe: %q", nilErr.Error())
	}
	if got := NewError(ClassConflict, ReasonSessionExists, errors.New("x")); got.Class != ClassConflict || got.Reason != ReasonSessionExists {
		t.Errorf("NewError = %+v", got)
	}
}

type timeoutNetError struct{ timeout bool }

func (e timeoutNetError) Error() string   { return "net" }
func (e timeoutNetError) Timeout() bool   { return e.timeout }
func (e timeoutNetError) Temporary() bool { return false }

var _ net.Error = timeoutNetError{}

func TestClassifyErrorOrder(t *testing.T) {
	cause := errors.New(canary)
	if classifyError(nil, nil) != nil {
		t.Error("nil classifies to non-nil")
	}

	t.Run("the author's own *Error wins, even wrapped, even over Classify", func(t *testing.T) {
		author := NewError(ClassForbidden, ReasonTargetDenied, cause)
		classify := func(error) (*Error, bool) { return &Error{Class: ClassNotFound}, true }
		got := classifyError(classify, fmt.Errorf("wrapped: %w", author))
		if got.Class != ClassForbidden || got.Reason != ReasonTargetDenied {
			t.Errorf("got %+v", got)
		}
		if !errors.Is(got.Err, cause) {
			t.Error("the local cause was lost")
		}
	})
	t.Run("the target's Classify is next, and receives the original error", func(t *testing.T) {
		var seen error
		classify := func(err error) (*Error, bool) {
			seen = err
			return &Error{Class: ClassNotFound, Reason: ReasonTargetGone}, true
		}
		got := classifyError(classify, cause)
		if got.Class != ClassNotFound || got.Reason != ReasonTargetGone || seen != cause || !errors.Is(got.Err, cause) {
			t.Errorf("got %+v seen %v", got, seen)
		}
	})
	t.Run("Classify declining leaves it to the SDK", func(t *testing.T) {
		got := classifyError(func(error) (*Error, bool) { return nil, false }, context.Canceled)
		if got.Class != ClassCancelled {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("Classify returning an invalid pair is a broken contract", func(t *testing.T) {
		got := classifyError(func(error) (*Error, bool) { return &Error{Class: ClassNotFound, Reason: ReasonTargetDenied}, true }, cause)
		if got.Class != ClassInternal || got.Reason != ReasonUnspecified {
			t.Errorf("got %+v", got)
		}
	})
	sdk := []struct {
		name string
		err  error
		want ErrorClass
	}{
		{"cancellation", fmt.Errorf("x: %w", context.Canceled), ClassCancelled},
		{"deadline", context.DeadlineExceeded, ClassTimeout},
		{"resource: not found", resourcev2.NewError("NOT_FOUND", "x"), ClassNotFound},
		{"resource: reset required has no exec class", resourcev2.NewError("RESET_REQUIRED", "x"), ClassInternal},
		{"resource: connection error", resourcev2.NewError("CONNECTION_ERROR", "x"), ClassConnection},
		{"resource: limit", resourcev2.NewError("RESOURCE_LIMIT", "x"), ClassLimit},
		{"resource: permission denied", resourcev2.NewError("PERMISSION_DENIED", "x"), ClassForbidden},
		{"grpc: permission denied", status.Error(codes.PermissionDenied, "x"), ClassForbidden},
		{"grpc: unavailable", status.Error(codes.Unavailable, "x"), ClassConnection},
		{"network timeout", timeoutNetError{timeout: true}, ClassTimeout},
		{"network failure", timeoutNetError{}, ClassConnection},
		{"anything else", cause, ClassInternal},
	}
	for _, tc := range sdk {
		t.Run("sdk: "+tc.name, func(t *testing.T) {
			got := classifyError(nil, tc.err)
			if got.Class != tc.want || got.Reason != ReasonUnspecified {
				t.Errorf("got %+v, want class %s", got, tc.want)
			}
			if !errors.Is(got.Err, tc.err) && got.Err != tc.err {
				t.Errorf("the local cause was not kept: %v", got.Err)
			}
		})
	}
}

func TestEveryResourceCodeMapsToAClass(t *testing.T) {
	for _, code := range []string{
		"NOT_FOUND", "ALREADY_EXISTS", "PERMISSION_DENIED", "INVALID_INPUT", "CONFLICT", "INTERNAL", "UNAVAILABLE", "TIMEOUT",
		"FILTER_UNKNOWN_FIELD", "FILTER_INVALID_OPERATOR", "FORBIDDEN", "UNAUTHORIZED", "CONNECTION_ERROR", "CERTIFICATE_ERROR",
		"INVALID_REQUEST", "UNSUPPORTED", "CANCELLED", "RESOURCE_LIMIT", "CONTINUATION_EXPIRED", "RESET_REQUIRED",
		"PARTIAL_RESULT", "PROVIDER_ERROR", "GRANT_DENIED", "CONNECTION_NOT_STARTED", "OWNER_UNAVAILABLE", "DELEGATION_NOT_ACTIVE",
	} {
		if !resourcev2.IsErrorCode(code) {
			t.Errorf("%s is not a resource error code any more: update the mapping", code)
		}
		if classFromResourceCode(code) == ClassUnspecified {
			t.Errorf("%s maps to an unspecified class", code)
		}
	}
	if classFromResourceCode("NO_SUCH_CODE") != ClassInternal {
		t.Error("an unknown resource code must read as internal")
	}
}

func TestValidTextRejectsEveryControlCharacterAndKeepsOrdinaryUnicode(t *testing.T) {
	for _, r := range []rune{0x00, 0x09, 0x0a, 0x1f, 0x7f, 0x80, 0x85, 0x9f} {
		if s := "denied" + string(r) + "next"; validText(s) {
			t.Errorf("validText(%+q) = true, want false: U+%04X is a control character", s, r)
		}
	}
	for _, s := range []string{"", "plain", "with space", "h\u00e9llo", "\u00a0no-break", "\u00a1\u00ff", "\u65e5\u672c\u8a9e", "\U0001f600"} {
		if !validText(s) {
			t.Errorf("validText(%+q) = false, want true", s)
		}
	}
	if validText("a\xffb") {
		t.Error("text that is not UTF-8 must be refused")
	}
	if got := (&Error{Class: ClassNotFound, Message: "gone\u0085"}).normalized().Message; got != "" {
		t.Errorf("a message with a C1 control survived normalization: %+q", got)
	}
}
