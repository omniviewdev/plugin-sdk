package exec_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/omniviewdev/plugin-sdk/internal/exechook"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// A refusal of the API that a command runs in (a 401, a 403) must reach the host as a typed class. client-go flattens the status of
// the upgrade into text, which the SDK cannot read, so the author gets a helper that maps an HTTP status to the closed vocabulary,
// and the SDK's own classification reads the status of an error that carries one.

// httpStatusErr has the method of an error that names an HTTP status as HTTPStatus.
type httpStatusErr struct{ status int }

func (e httpStatusErr) Error() string { return "request failed with " + strconv.Itoa(e.status) }

func (e httpStatusErr) HTTPStatus() int { return e.status }

// statusCodeErr has the method of an error that names an HTTP status as StatusCode.
type statusCodeErr struct{ code int }

func (e statusCodeErr) Error() string { return "response status " + strconv.Itoa(e.code) }

func (e statusCodeErr) StatusCode() int { return e.code }

// bothStatuses has the two methods, which disagree: HTTPStatus is read first.
type bothStatuses struct{}

func (bothStatuses) Error() string { return "both" }

func (bothStatuses) HTTPStatus() int { return http.StatusForbidden }

func (bothStatuses) StatusCode() int { return http.StatusNotFound }

// panickingStatus has a method that panics: the author's bug, which the SDK must not turn into the end of the plugin (it classifies on
// the supervisor's goroutine, where nobody recovers).
type panickingStatus struct{}

func (panickingStatus) Error() string { return "panics" }

func (panickingStatus) HTTPStatus() int { panic("CANARY-panic-in-HTTPStatus") }

// statusCases is the table of ErrorFromHTTPStatus, which the guide prints.
var statusCases = []struct {
	status int
	class  exec.ErrorClass
	reason exec.ErrorReason
}{
	{http.StatusBadRequest, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified},
	{http.StatusUnauthorized, exec.ErrorClassUnauthorized, exec.ErrorReasonUnspecified},
	{http.StatusForbidden, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
	{http.StatusNotFound, exec.ErrorClassNotFound, exec.ErrorReasonUnspecified},
	{http.StatusMethodNotAllowed, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified},
	{http.StatusRequestTimeout, exec.ErrorClassTimeout, exec.ErrorReasonUnspecified},
	{http.StatusConflict, exec.ErrorClassConflict, exec.ErrorReasonUnspecified},
	{http.StatusGone, exec.ErrorClassNotFound, exec.ErrorReasonUnspecified},
	{http.StatusUnprocessableEntity, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified},
	{http.StatusTooManyRequests, exec.ErrorClassUnavailable, exec.ErrorReasonUnspecified},
	{499, exec.ErrorClassInvalidRequest, exec.ErrorReasonUnspecified},
	{http.StatusInternalServerError, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{http.StatusNotImplemented, exec.ErrorClassUnsupported, exec.ErrorReasonUnspecified},
	{http.StatusBadGateway, exec.ErrorClassUnavailable, exec.ErrorReasonUnspecified},
	{http.StatusServiceUnavailable, exec.ErrorClassUnavailable, exec.ErrorReasonUnspecified},
	{http.StatusGatewayTimeout, exec.ErrorClassTimeout, exec.ErrorReasonUnspecified},
	{http.StatusHTTPVersionNotSupported, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{599, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	// What is no error status of HTTP is no class: the SDK does not guess one.
	{http.StatusOK, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{http.StatusFound, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{399, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{600, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{0, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
	{-403, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
}

func TestErrorFromHTTPStatusMapsEachStatusToAClosedClass(t *testing.T) {
	for _, tc := range statusCases {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			got := exec.ErrorFromHTTPStatus(tc.status, errors.New("cause"))
			if got == nil || got.Class != tc.class || got.Reason != tc.reason {
				t.Errorf("ErrorFromHTTPStatus(%d) = %+v, want %v / %v", tc.status, got, tc.class, tc.reason)
			}
		})
	}
}

// The error is always an *Error that may cross the wire: its pair of class and reason is one the contract allows, the cause stays
// local (it is found with errors.Is and never printed), and a nil cause is no problem.
func TestErrorFromHTTPStatusKeepsTheCauseLocalAndIsAlwaysAValidError(t *testing.T) {
	const canary = "CANARY-token-9f3a"
	cause := errors.New("pods is forbidden: " + canary)
	for _, tc := range statusCases {
		got := exec.ErrorFromHTTPStatus(tc.status, cause)
		if !errors.Is(got, cause) {
			t.Errorf("status %d: the local cause was lost", tc.status)
		}
		if strings.Contains(got.Error(), canary) {
			t.Errorf("status %d: Error() prints the local cause: %q", tc.status, got.Error())
		}
		if got.Message != "" {
			t.Errorf("status %d: Message is %q, want none: a status says nothing the host may be shown", tc.status, got.Message)
		}
		// The SDK replaces a pair that the contract does not allow with INTERNAL: a mapping that would be replaced is a bug.
		if (got.Reason != exec.ErrorReasonUnspecified) != (tc.status == http.StatusForbidden) {
			t.Errorf("status %d: reason %v", tc.status, got.Reason)
		}
	}
	if got := exec.ErrorFromHTTPStatus(http.StatusForbidden, nil); got == nil || got.Class != exec.ErrorClassForbidden || got.Err != nil {
		t.Errorf("a nil cause: %+v, want a Forbidden error with no cause", got)
	}
}

// failedOf runs a command whose function returns err and reports the class and reason of the failure the host reads, and the
// failure of a target whose Classify is classify.
func failedOf(t *testing.T, err error, classify exec.ClassifyFunc) (exec.ErrorClass, exec.ErrorReason) {
	t.Helper()
	target := engTarget(func(context.Context, *exec.Proc) (exec.Exit, error) { return exec.Exit{}, err })
	target.Classify = classify
	rig := newEngineRig(t, engineRigConfig{}, target)
	res := awaitDone(t, rig.mustOpen("s1"))
	failure := res.End.GetFailed()
	if failure == nil {
		t.Fatalf("the end is %v, want a failure", res.End)
	}
	return classNamed(t, strings.TrimPrefix(failure.GetClass().String(), "ERROR_CLASS_")), reasonNamed(t, strings.TrimPrefix(failure.GetReason().String(), "ERROR_REASON_"))
}

// classNamed and reasonNamed return the Go value of a wire name without its prefix.
func classNamed(t *testing.T, name string) exec.ErrorClass {
	t.Helper()
	for c := exec.ErrorClassUnspecified; c <= exec.ErrorClassInternal; c++ {
		if c.String() == name {
			return c
		}
	}
	t.Fatalf("no error class is named %q", name)
	return exec.ErrorClassUnspecified
}

func reasonNamed(t *testing.T, name string) exec.ErrorReason {
	t.Helper()
	for r := exec.ErrorReasonUnspecified; r <= exec.ErrorReasonTargetWedged; r++ {
		if r.String() == name {
			return r
		}
	}
	t.Fatalf("no error reason is named %q", name)
	return exec.ErrorReasonUnspecified
}

// The SDK's own classification, which runs when the author's Classify is nil or says nothing, reads the status of an error that
// has an HTTPStatus or a StatusCode method, however deep in the chain, with the table of ErrorFromHTTPStatus, and only after what
// it knew before: it never turns an error it can read into one it cannot.
func TestTheSDKClassifiesAnErrorThatCarriesAnHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		class  exec.ErrorClass
		reason exec.ErrorReason
	}{
		{"HTTPStatus 403", httpStatusErr{403}, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
		{"StatusCode 403", statusCodeErr{403}, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
		{"StatusCode 401", statusCodeErr{401}, exec.ErrorClassUnauthorized, exec.ErrorReasonUnspecified},
		{"HTTPStatus 404", httpStatusErr{404}, exec.ErrorClassNotFound, exec.ErrorReasonUnspecified},
		{"StatusCode 503", statusCodeErr{503}, exec.ErrorClassUnavailable, exec.ErrorReasonUnspecified},
		{"StatusCode 504", statusCodeErr{504}, exec.ErrorClassTimeout, exec.ErrorReasonUnspecified},
		{"wrapped twice", fmt.Errorf("exec: %w", fmt.Errorf("upgrade: %w", statusCodeErr{403})), exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
		{"HTTPStatus is read before StatusCode", bothStatuses{}, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
		{"a status that is no error status is no class", statusCodeErr{200}, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
		{"an error that names no status", errors.New("forbidden"), exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
		{"a status method that panics is no status", panickingStatus{}, exec.ErrorClassInternal, exec.ErrorReasonUnspecified},
		{"the context's own error is read before a status", fmt.Errorf("%w: %w", context.DeadlineExceeded, statusCodeErr{403}), exec.ErrorClassTimeout, exec.ErrorReasonUnspecified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			class, reason := failedOf(t, tc.err, nil)
			if class != tc.class || reason != tc.reason {
				t.Errorf("the failure is %v / %v, want %v / %v", class, reason, tc.class, tc.reason)
			}
		})
	}
}

// The author's own answer comes first: Classify, and an *Error the function returned, win over the status; a Classify that says
// nothing leaves the error to the SDK.
func TestTheAuthorsClassificationIsAskedBeforeTheHTTPStatus(t *testing.T) {
	err := statusCodeErr{403}
	asked := false
	mine := func(error) (*exec.Error, bool) {
		asked = true
		return exec.NewError(exec.ErrorClassUnavailable, exec.ErrorReasonNotRunning, nil), true
	}
	if class, reason := failedOf(t, err, mine); !asked || class != exec.ErrorClassUnavailable || reason != exec.ErrorReasonNotRunning {
		t.Errorf("Classify asked %v and the failure is %v / %v, want the author's Unavailable / NotRunning", asked, class, reason)
	}
	declines := func(error) (*exec.Error, bool) { return nil, false }
	if class, reason := failedOf(t, err, declines); class != exec.ErrorClassForbidden || reason != exec.ErrorReasonTargetDenied {
		t.Errorf("a Classify that declines left %v / %v, want the SDK's Forbidden / TargetDenied", class, reason)
	}
	typed := fmt.Errorf("run: %w", exec.NewError(exec.ErrorClassConflict, exec.ErrorReasonTargetReplaced, err))
	if class, reason := failedOf(t, typed, nil); class != exec.ErrorClassConflict || reason != exec.ErrorReasonTargetReplaced {
		t.Errorf("an *Error that wraps a status gave %v / %v, want the author's own Conflict / TargetReplaced", class, reason)
	}
}

// An Options callback that fails with an error that carries a status is a refusal of its class, with the reason: the picker of a
// host shows "forbidden" and not "internal error" for an RBAC refusal of the API.
func TestAnOptionsCallbackThatFailsWithAnHTTPStatusIsRefusedWithItsClass(t *testing.T) {
	options := func(err error) exec.OptionsFunc {
		return func(*types.PluginContext, resourcev2.ResourceReference) ([]exec.Option, error) { return nil, err }
	}
	for _, tc := range []struct {
		name   string
		err    error
		class  exec.ErrorClass
		reason exec.ErrorReason
	}{
		{"forbidden", statusCodeErr{403}, exec.ErrorClassForbidden, exec.ErrorReasonTargetDenied},
		{"unauthorized", httpStatusErr{401}, exec.ErrorClassUnauthorized, exec.ErrorReasonUnspecified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newOptionsRig(t, exechook.EngineConfig{}, optionsCallbackTarget(options(tc.err)))
			_, err := rig.options(context.Background(), "thing-1")
			var refusal *exec.Error
			if !errors.As(err, &refusal) || refusal.Class != tc.class || refusal.Reason != tc.reason {
				t.Fatalf("Options failed with %v, want a refusal of %v / %v", err, tc.class, tc.reason)
			}
		})
	}
}
