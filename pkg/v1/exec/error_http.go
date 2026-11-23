package exec

import (
	"errors"
	"net/http"
)

// ErrorFromHTTPStatus returns the *Error that an HTTP status stands for, so that a refusal of the API a command runs in reaches the host as
// a typed class and not as INTERNAL. cause is kept as the local cause, which the SDK logs and never sends; it may be nil.
//
// The table is closed, and it infers no more than a status says:
//
//	status                     class           reason
//	401                        UNAUTHORIZED
//	403                        FORBIDDEN       TARGET_DENIED
//	404, 410                   NOT_FOUND
//	409                        CONFLICT
//	408, 504                   TIMEOUT
//	429, 502, 503              UNAVAILABLE
//	501                        UNSUPPORTED
//	any other 4xx              INVALID_REQUEST
//	any other 5xx              INTERNAL
//	anything outside 400-599   INTERNAL
//
// A 404 says the API did not find what was asked for, and not whether that is the target, the executable or the session, so it has no
// reason: add ErrorReasonTargetGone (or ErrorReasonNoSuchExecutable and ErrorFacts.Program) on the result when you know which it is.
//
// The SDK applies this table by itself to an error in the chain that has an HTTPStatus() int or a StatusCode() int method, when the
// target's Classify says nothing. An error that carries its status only in its text (client-go flattens the 401 or 403 of the upgrade
// into "unable to upgrade connection: pods \"x\" is forbidden: ...") has none: extract the status in your own Classify and call this.
func ErrorFromHTTPStatus(status int, cause error) *Error {
	class, reason := httpStatusClass(status)
	return &Error{Class: class, Reason: reason, Err: cause}
}

// httpStatusClass is the table of ErrorFromHTTPStatus.
func httpStatusClass(status int) (ErrorClass, ErrorReason) {
	switch status {
	case http.StatusUnauthorized:
		return ErrorClassUnauthorized, ErrorReasonUnspecified
	case http.StatusForbidden:
		return ErrorClassForbidden, ErrorReasonTargetDenied
	case http.StatusNotFound, http.StatusGone:
		return ErrorClassNotFound, ErrorReasonUnspecified
	case http.StatusConflict:
		return ErrorClassConflict, ErrorReasonUnspecified
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return ErrorClassTimeout, ErrorReasonUnspecified
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable:
		return ErrorClassUnavailable, ErrorReasonUnspecified
	case http.StatusNotImplemented:
		return ErrorClassUnsupported, ErrorReasonUnspecified
	}
	if status >= 400 && status < 500 {
		return ErrorClassInvalidRequest, ErrorReasonUnspecified
	}
	return ErrorClassInternal, ErrorReasonUnspecified
}

// httpStatusError is the shape of an error that names its HTTP status as HTTPStatus.
type httpStatusError interface{ HTTPStatus() int }

// statusCodeError is the shape of an error that names its HTTP status as StatusCode.
type statusCodeError interface{ StatusCode() int }

// httpStatusOf returns the HTTP status of the first error in the chain that names one (HTTPStatus is read before StatusCode). A method
// that panics is the author's bug, and the SDK classifies on the supervisor's goroutine, where a panic would end the plugin: it is read as
// no status.
func httpStatusOf(err error) (status int, ok bool) {
	defer func() {
		if recover() != nil {
			status, ok = 0, false
		}
	}()
	if named := new(httpStatusError); errors.As(err, named) {
		return (*named).HTTPStatus(), true
	}
	if named := new(statusCodeError); errors.As(err, named) {
		return (*named).StatusCode(), true
	}
	return 0, false
}
