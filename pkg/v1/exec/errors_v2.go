package exec

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"
	"unicode/utf8"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// errorDomain is the google.rpc.ErrorInfo domain of every exec v2 refusal.
const errorDomain = "omniview.sdk.exec.v2"

// resourceErrorDomain is the ErrorInfo domain of the resource lane. The SDK's own admission and connection-lease
// failures keep it on the exec v2 service too: its vocabulary (RESET_REQUIRED, CONNECTION_NOT_STARTED, ...) is the
// host's existing contract for catalog and connection admission and has no equivalent in the closed exec classes.
const resourceErrorDomain = "omniview.resource.v2"

const (
	metadataReason         = "reason"
	metadataProgram        = "program"
	metadataSelectionName  = "selection_name"
	metadataSelectionValue = "selection_value"
	metadataRetryAfterMS   = "retry_after_ms"

	// statusMessage is the message of every exec v2 status: the class, reason and facts carry the meaning, and
	// nothing the plugin wrote crosses in the status text.
	statusMessage = "exec operation failed"

	// localizedLocale marks the LocalizedMessage detail that carries Error.Message.
	localizedLocale = "en"
)

// ErrorFacts are the bounded facts a refusal carries so that the host can word it without free text. Each string
// is at most 256 bytes of UTF-8 without control characters; a fact that breaks the bound is dropped, never
// truncated. A zero field means the fact does not apply.
type ErrorFacts struct {
	// Program is the program that was not found (ErrorReasonNoSuchExecutable).
	Program string
	// SelectionName is the name of the selection the failure concerns ("container").
	SelectionName string
	// SelectionValue is the value of that selection ("app").
	SelectionValue string
	// RetryAfter is how long to wait when retrying can help. Zero means no advice, not "retry now".
	RetryAfter time.Duration
}

// Error is what an author returns when it knows why a command could not run or a target failed. It carries the
// closed class and reason and the bounded facts, which are what crosses to the host; the host writes the words.
//
// Message is an optional public-safe sentence (at most 512 bytes of UTF-8, no control characters): it crosses only
// for a class other than ErrorClassInternal, and never carries credentials, paths or raw diagnostics. Err is the local
// cause: it is logged by the SDK and never sent. Error() does not print it.
type Error struct {
	Class   ErrorClass
	Reason  ErrorReason
	Facts   ErrorFacts
	Message string
	Err     error
}

// NewError returns an *Error for class and reason whose local cause is err.
func NewError(class ErrorClass, reason ErrorReason, err error) *Error {
	return &Error{Class: class, Reason: reason, Err: err}
}

// Error describes the class and reason, and Message when one is set. It never prints Err.
func (e *Error) Error() string {
	if e == nil {
		return "exec: <nil>"
	}
	text := "exec: " + e.Class.String()
	if e.Reason != ErrorReasonUnspecified {
		text += "/" + e.Reason.String()
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// Unwrap returns the local cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// reasonClasses is the closed set of classes each reason may be paired with (ERROR_REASON_UNSPECIFIED pairs with every
// class). The pairing is part of the contract (proto/exec/v2/exec.proto, enum ErrorReason).
var reasonClasses = map[ErrorReason][]ErrorClass{
	ErrorReasonTargetDenied:      {ErrorClassForbidden},
	ErrorReasonTargetGone:        {ErrorClassNotFound},
	ErrorReasonTargetReplaced:    {ErrorClassConflict},
	ErrorReasonNotRunning:        {ErrorClassUnavailable},
	ErrorReasonNoSuchExecutable:  {ErrorClassNotFound},
	ErrorReasonTTYUnsupported:    {ErrorClassUnsupported},
	ErrorReasonTooManySessions:   {ErrorClassLimit},
	ErrorReasonTooManyRuns:       {ErrorClassLimit},
	ErrorReasonInputTooLarge:     {ErrorClassLimit},
	ErrorReasonConnectionRetired: {ErrorClassConnection},
	ErrorReasonSessionExists:     {ErrorClassConflict},
	ErrorReasonSessionUnknown:    {ErrorClassNotFound},
	ErrorReasonConsentRequired:   {ErrorClassInvalidRequest},
	ErrorReasonInputBacklog:      {ErrorClassLimit},
	ErrorReasonTargetWedged:      {ErrorClassUnavailable},
}

// validPair reports whether class and reason may be sent together.
func validPair(class ErrorClass, reason ErrorReason) bool {
	if class.toProto() == execv2.ErrorClass_ERROR_CLASS_UNSPECIFIED {
		return false
	}
	if reason == ErrorReasonUnspecified {
		return true
	}
	for _, c := range reasonClasses[reason] {
		if c == class {
			return true
		}
	}
	return false
}

// boundedFact returns s when it may cross as a fact (at most 256 bytes of UTF-8 without control characters), and
// "" otherwise.
func boundedFact(s string) string {
	if len(s) > maxFactBytes || !validText(s) {
		return ""
	}
	return s
}

// validText reports whether s is UTF-8 without control characters (NUL, C0, DEL and C1).
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// normalized returns the error as it may cross the wire: an unknown class reads as ErrorClassInternal, a pairing the
// contract does not allow is replaced by ErrorClassInternal (the producer broke the contract), facts and the message are
// dropped when they break their bounds, and Message is dropped for ErrorClassInternal. The receiver is not modified.
func (e *Error) normalized() *Error {
	if e == nil {
		return &Error{Class: ErrorClassInternal}
	}
	out := &Error{Class: e.Class, Reason: e.Reason, Err: e.Err}
	if out.Class.toProto() == execv2.ErrorClass_ERROR_CLASS_UNSPECIFIED {
		out.Class = ErrorClassInternal
	}
	if !validPair(out.Class, out.Reason) {
		out.Class, out.Reason = ErrorClassInternal, ErrorReasonUnspecified
		return out
	}
	out.Facts = ErrorFacts{
		Program:        boundedFact(e.Facts.Program),
		SelectionName:  boundedFact(e.Facts.SelectionName),
		SelectionValue: boundedFact(e.Facts.SelectionValue),
	}
	if e.Facts.RetryAfter > 0 && e.Facts.RetryAfter/time.Millisecond <= time.Duration(^uint32(0)) {
		out.Facts.RetryAfter = e.Facts.RetryAfter.Truncate(time.Millisecond)
	}
	if out.Class != ErrorClassInternal && len(e.Message) <= maxPublicMessage && validText(e.Message) {
		out.Message = e.Message
	}
	return out
}

// grpcCode maps a class to the gRPC status code of a refusal.
func (c ErrorClass) grpcCode() codes.Code {
	switch c {
	case ErrorClassForbidden:
		return codes.PermissionDenied
	case ErrorClassUnauthorized:
		return codes.Unauthenticated
	case ErrorClassNotFound:
		return codes.NotFound
	case ErrorClassConflict:
		return codes.Aborted
	case ErrorClassUnavailable, ErrorClassConnection:
		return codes.Unavailable
	case ErrorClassTimeout:
		return codes.DeadlineExceeded
	case ErrorClassInvalidRequest:
		return codes.InvalidArgument
	case ErrorClassUnsupported:
		return codes.Unimplemented
	case ErrorClassLimit:
		return codes.ResourceExhausted
	case ErrorClassCancelled:
		return codes.Canceled
	case ErrorClassUnspecified, ErrorClassInternal:
		return codes.Internal
	}
	return codes.Internal
}

// status encodes e (normalized) as a gRPC status: the class as the ErrorInfo reason, the reason and the facts as its
// metadata, and Message as a bounded LocalizedMessage. Nothing else of e is sent.
func (e *Error) status() error {
	n := e.normalized()
	metadata := map[string]string{}
	if n.Reason != ErrorReasonUnspecified {
		metadata[metadataReason] = n.Reason.String()
	}
	if n.Facts.Program != "" {
		metadata[metadataProgram] = n.Facts.Program
	}
	if n.Facts.SelectionName != "" {
		metadata[metadataSelectionName] = n.Facts.SelectionName
	}
	if n.Facts.SelectionValue != "" {
		metadata[metadataSelectionValue] = n.Facts.SelectionValue
	}
	if n.Facts.RetryAfter > 0 {
		metadata[metadataRetryAfterMS] = strconv.FormatInt(n.Facts.RetryAfter.Milliseconds(), 10)
	}
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{
		Reason:   n.Class.String(),
		Domain:   errorDomain,
		Metadata: metadata,
	}}
	if n.Message != "" {
		details = append(details, &errdetails.LocalizedMessage{Locale: localizedLocale, Message: n.Message})
	}
	st := status.New(n.Class.grpcCode(), statusMessage)
	detailed, err := st.WithDetails(details...)
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}

// errorFromStatus decodes the error of a call to an exec v2 service. The status the call returned is decoded first, and
// the caller's context is read only when the status itself says the context ended the call. An exec refusal is an
// *Error. A resource-layer refusal (the connection lease, the catalog pin) is the *resourcev2.ResourceOperationError it
// was sent as. Both are the answer of the call and keep their own class and code however the caller's context fared
// after the call returned: the host acts on them (RESET_REQUIRED asks for a reset). Any other failure is classified by
// its gRPC code alone, and never carries the server's status text: Unimplemented (the plugin does not serve the
// service) is ErrorClassUnsupported. A status of Canceled or DeadlineExceeded with no exec or resource detail, or a plain
// context error, from a call whose caller's context is over is that context's own: it reads as cancelled or timed out and
// wraps the context error, so that errors.Is finds it. A status keeps its own class: a Canceled status is never a timeout
// because the caller's deadline passed after the call returned, so the context error is wrapped only when it says the same. A
// context is over when its deadline has passed, and its Err says so a moment later (contextOver).
func errorFromStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctxErr := contextOver(ctx); ctxErr != nil {
				return contextError(ctxErr)
			}
		}
		return &Error{Class: ErrorClassInternal}
	}
	details := st.Details()
	resourceLayer := false
	for _, d := range details {
		info, isInfo := d.(*errdetails.ErrorInfo)
		if !isInfo {
			continue
		}
		switch info.GetDomain() {
		case errorDomain:
			return execErrorFromInfo(info, details)
		case resourceErrorDomain:
			resourceLayer = true
		}
	}
	if resourceLayer {
		// The resource lane's own refusal, TIMEOUT and CANCELLED included (they travel as DeadlineExceeded and Canceled with
		// a resource detail): it is the answer of the call and keeps its resource code however the caller's context fared.
		return resourceplugin.ResourceDomainError(err)
	}
	class := classFromCode(st.Code())
	if code := st.Code(); code == codes.Canceled || code == codes.DeadlineExceeded {
		// The class is the status's own. The caller's context error is wrapped only when it says the same as the status:
		// a Canceled status stays ErrorClassCancelled and a DeadlineExceeded one stays ErrorClassTimeout, whatever ended the caller's
		// context after the call returned.
		if ctxErr := contextOver(ctx); ctxErr != nil && contextError(ctxErr).Class == class {
			return contextError(ctxErr)
		}
	}
	return &Error{Class: class}
}

// contextOver is the error of the caller's context as far as a call that has returned can tell: its Err, or, for a context whose deadline has
// passed and whose timer has not run it out yet, the error of the deadline. A context is over when its deadline passes, and its Err says so when
// the goroutine that runs the timer is scheduled, which on a busy machine is not at once. A transport that reads the clock, as gRPC's does (it
// refuses a call whose deadline has passed, and it reads the cancellation of a stream after the deadline as the deadline), returns a status of
// DeadlineExceeded in that moment, and the error that is made of it must not read the context as alive: a call whose deadline passed before the
// plugin answered is a timeout with the context's error inside, however late the context says it.
func contextOver(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// contextError is the error of the caller's own context as the SDK reports it: ErrorClassCancelled, or ErrorClassTimeout when the
// context passed its deadline. It wraps ctxErr so that errors.Is finds it.
func contextError(ctxErr error) *Error {
	class := ErrorClassCancelled
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		class = ErrorClassTimeout
	}
	return &Error{Class: class, Err: ctxErr}
}

// execErrorFromInfo builds the *Error of an exec ErrorInfo and the details beside it.
func execErrorFromInfo(info *errdetails.ErrorInfo, details []any) *Error {
	out := &Error{Class: classFromName(info.GetReason())}
	metadata := info.GetMetadata()
	out.Reason = reasonFromName(metadata[metadataReason])
	out.Facts = ErrorFacts{
		Program:        boundedFact(metadata[metadataProgram]),
		SelectionName:  boundedFact(metadata[metadataSelectionName]),
		SelectionValue: boundedFact(metadata[metadataSelectionValue]),
	}
	if ms, parseErr := strconv.ParseUint(metadata[metadataRetryAfterMS], 10, 32); parseErr == nil {
		out.Facts.RetryAfter = time.Duration(ms) * time.Millisecond
	}
	if !validPair(out.Class, out.Reason) {
		out.Reason = ErrorReasonUnspecified
	}
	for _, d := range details {
		if localized, ok := d.(*errdetails.LocalizedMessage); ok && localized.GetLocale() == localizedLocale {
			if m := localized.GetMessage(); out.Class != ErrorClassInternal && len(m) <= maxPublicMessage && validText(m) {
				out.Message = m
			}
			break
		}
	}
	return out
}

// classFromName reads a class from its wire name without the prefix; an unknown name reads as ErrorClassInternal.
func classFromName(name string) ErrorClass {
	for _, p := range errorClassPairs {
		if p.g.String() == name {
			return p.g
		}
	}
	return ErrorClassInternal
}

// reasonFromName reads a reason from its wire name without the prefix; an unknown name reads as
// ErrorReasonUnspecified.
func reasonFromName(name string) ErrorReason {
	for _, p := range errorReasonPairs {
		if p.g.String() == name {
			return p.g
		}
	}
	return ErrorReasonUnspecified
}

// classFromCode classifies a status that carries no exec or resource detail by its code alone.
func classFromCode(code codes.Code) ErrorClass {
	switch code {
	case codes.PermissionDenied:
		return ErrorClassForbidden
	case codes.Unauthenticated:
		return ErrorClassUnauthorized
	case codes.NotFound:
		return ErrorClassNotFound
	case codes.AlreadyExists, codes.Aborted:
		return ErrorClassConflict
	case codes.Unavailable:
		return ErrorClassConnection
	case codes.DeadlineExceeded:
		return ErrorClassTimeout
	case codes.InvalidArgument, codes.OutOfRange, codes.FailedPrecondition:
		return ErrorClassInvalidRequest
	case codes.Unimplemented:
		return ErrorClassUnsupported
	case codes.ResourceExhausted:
		return ErrorClassLimit
	case codes.Canceled:
		return ErrorClassCancelled
	case codes.OK, codes.Unknown, codes.Internal, codes.DataLoss:
		return ErrorClassInternal
	}
	return ErrorClassInternal
}

// classFromResourceCode maps a resource-lane error code onto the exec classes.
func classFromResourceCode(code string) ErrorClass {
	switch code {
	case "INVALID_REQUEST", "INVALID_INPUT", "FILTER_UNKNOWN_FIELD", "FILTER_INVALID_OPERATOR":
		return ErrorClassInvalidRequest
	case "UNSUPPORTED":
		return ErrorClassUnsupported
	case "RESOURCE_LIMIT":
		return ErrorClassLimit
	case "NOT_FOUND":
		return ErrorClassNotFound
	case "ALREADY_EXISTS", "CONFLICT":
		return ErrorClassConflict
	case "UNAUTHORIZED":
		return ErrorClassUnauthorized
	case "FORBIDDEN", "PERMISSION_DENIED", "GRANT_DENIED":
		return ErrorClassForbidden
	case "UNAVAILABLE", "OWNER_UNAVAILABLE", "DELEGATION_NOT_ACTIVE":
		return ErrorClassUnavailable
	case "CONNECTION_ERROR", "CERTIFICATE_ERROR", "CONNECTION_NOT_STARTED":
		return ErrorClassConnection
	case "TIMEOUT":
		return ErrorClassTimeout
	case "CANCELLED":
		return ErrorClassCancelled
	default:
		return ErrorClassInternal
	}
}

// classifyError turns an error an author's callback returned into the *Error that may cross the wire. In order: an
// *Error the author built; the target's own Classify; the SDK's classification of context, resource-lane, gRPC and
// network errors, and of an error that carries an HTTP status (HTTPStatus or StatusCode, see ErrorFromHTTPStatus);
// ErrorClassInternal. The local cause is kept in Err and never sent.
func classifyError(classify ClassifyFunc, err error) *Error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		out := typed.normalized()
		if out.Err == nil {
			out.Err = err
		}
		return out
	}
	if classify != nil {
		if classified, ok := classify(err); ok && classified != nil {
			out := classified.normalized()
			if out.Err == nil {
				out.Err = err
			}
			return out
		}
	}
	return sdkError(err)
}

// sdkError is the *Error the SDK makes of an error nobody else classified: what sdkClass knows, and then the status of an error that
// carries an HTTP one, before ErrorClassInternal.
func sdkError(err error) *Error {
	if class := sdkClass(err); class != ErrorClassInternal {
		return &Error{Class: class, Err: err}
	}
	if status, ok := httpStatusOf(err); ok {
		return ErrorFromHTTPStatus(status, err)
	}
	return &Error{Class: ErrorClassInternal, Err: err}
}

// sdkClass is the SDK's own classification of the errors every plugin shares.
func sdkClass(err error) ErrorClass {
	var resourceErr *resourcev2.ResourceOperationError
	switch {
	case errors.Is(err, context.Canceled):
		return ErrorClassCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorClassTimeout
	case errors.As(err, &resourceErr) && resourceErr != nil:
		return classFromResourceCode(resourcev2.ErrorCode(err))
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		return classFromCode(st.Code())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ErrorClassTimeout
		}
		return ErrorClassConnection
	}
	return ErrorClassInternal
}
