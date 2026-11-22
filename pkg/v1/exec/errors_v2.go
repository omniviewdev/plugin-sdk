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
	// Program is the program that was not found (ReasonNoSuchExecutable).
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
// for a class other than ClassInternal, and never carries credentials, paths or raw diagnostics. Err is the local
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
	if e.Reason != ReasonUnspecified {
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
	ReasonTargetDenied:      {ClassForbidden},
	ReasonTargetGone:        {ClassNotFound},
	ReasonTargetReplaced:    {ClassConflict},
	ReasonNotRunning:        {ClassUnavailable},
	ReasonNoSuchExecutable:  {ClassNotFound},
	ReasonTTYUnsupported:    {ClassUnsupported},
	ReasonTooManySessions:   {ClassLimit},
	ReasonTooManyRuns:       {ClassLimit},
	ReasonInputTooLarge:     {ClassLimit},
	ReasonConnectionRetired: {ClassConnection},
	ReasonSessionExists:     {ClassConflict},
	ReasonSessionUnknown:    {ClassNotFound},
	ReasonConsentRequired:   {ClassInvalidRequest},
	ReasonInputBacklog:      {ClassLimit},
}

// validPair reports whether class and reason may be sent together.
func validPair(class ErrorClass, reason ErrorReason) bool {
	if class.toProto() == execv2.ErrorClass_ERROR_CLASS_UNSPECIFIED {
		return false
	}
	if reason == ReasonUnspecified {
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

// normalized returns the error as it may cross the wire: an unknown class reads as ClassInternal, a pairing the
// contract does not allow is replaced by ClassInternal (the producer broke the contract), facts and the message are
// dropped when they break their bounds, and Message is dropped for ClassInternal. The receiver is not modified.
func (e *Error) normalized() *Error {
	if e == nil {
		return &Error{Class: ClassInternal}
	}
	out := &Error{Class: e.Class, Reason: e.Reason, Err: e.Err}
	if out.Class.toProto() == execv2.ErrorClass_ERROR_CLASS_UNSPECIFIED {
		out.Class = ClassInternal
	}
	if !validPair(out.Class, out.Reason) {
		out.Class, out.Reason = ClassInternal, ReasonUnspecified
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
	if out.Class != ClassInternal && len(e.Message) <= maxPublicMessage && validText(e.Message) {
		out.Message = e.Message
	}
	return out
}

// grpcCode maps a class to the gRPC status code of a refusal.
func (c ErrorClass) grpcCode() codes.Code {
	switch c {
	case ClassForbidden:
		return codes.PermissionDenied
	case ClassUnauthorized:
		return codes.Unauthenticated
	case ClassNotFound:
		return codes.NotFound
	case ClassConflict:
		return codes.Aborted
	case ClassUnavailable, ClassConnection:
		return codes.Unavailable
	case ClassTimeout:
		return codes.DeadlineExceeded
	case ClassInvalidRequest:
		return codes.InvalidArgument
	case ClassUnsupported:
		return codes.Unimplemented
	case ClassLimit:
		return codes.ResourceExhausted
	case ClassCancelled:
		return codes.Canceled
	case ClassUnspecified, ClassInternal:
		return codes.Internal
	}
	return codes.Internal
}

// status encodes e (normalized) as a gRPC status: the class as the ErrorInfo reason, the reason and the facts as its
// metadata, and Message as a bounded LocalizedMessage. Nothing else of e is sent.
func (e *Error) status() error {
	n := e.normalized()
	metadata := map[string]string{}
	if n.Reason != ReasonUnspecified {
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

// unsupportedError is the typed refusal of a method this SDK build does not serve.
func unsupportedError() *Error { return &Error{Class: ClassUnsupported} }

// errorFromStatus decodes the error of a call to an exec v2 service. The status the call returned is decoded first, and
// the caller's context is read only when the status itself says the context ended the call. An exec refusal is an
// *Error. A resource-layer refusal (the connection lease, the catalog pin) is the *resourcev2.ResourceOperationError it
// was sent as. Both are the answer of the call and keep their own class and code however the caller's context fared
// after the call returned: the host acts on them (RESET_REQUIRED asks for a reset). Any other failure is classified by
// its gRPC code alone, and never carries the server's status text: Unimplemented (the plugin does not serve the
// service) is ClassUnsupported. A status of Canceled or DeadlineExceeded with no exec or resource detail, or a plain
// context error, from a call whose caller's context is over is that context's own: it reads as cancelled or timed out and
// wraps the context error, so that errors.Is finds it. A status keeps its own class: a Canceled status is never a timeout
// because the caller's deadline passed after the call returned, so the context error is wrapped only when it says the same.
func errorFromStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return contextError(ctxErr)
			}
		}
		return &Error{Class: ClassInternal}
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
		// a Canceled status stays ClassCancelled and a DeadlineExceeded one stays ClassTimeout, whatever ended the caller's
		// context after the call returned.
		if ctxErr := ctx.Err(); ctxErr != nil && contextError(ctxErr).Class == class {
			return contextError(ctxErr)
		}
	}
	return &Error{Class: class}
}

// contextError is the error of the caller's own context as the SDK reports it: ClassCancelled, or ClassTimeout when the
// context passed its deadline. It wraps ctxErr so that errors.Is finds it.
func contextError(ctxErr error) *Error {
	class := ClassCancelled
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		class = ClassTimeout
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
		out.Reason = ReasonUnspecified
	}
	for _, d := range details {
		if localized, ok := d.(*errdetails.LocalizedMessage); ok && localized.GetLocale() == localizedLocale {
			if m := localized.GetMessage(); out.Class != ClassInternal && len(m) <= maxPublicMessage && validText(m) {
				out.Message = m
			}
			break
		}
	}
	return out
}

// classFromName reads a class from its wire name without the prefix; an unknown name reads as ClassInternal.
func classFromName(name string) ErrorClass {
	for _, p := range errorClassPairs {
		if p.g.String() == name {
			return p.g
		}
	}
	return ClassInternal
}

// reasonFromName reads a reason from its wire name without the prefix; an unknown name reads as
// ReasonUnspecified.
func reasonFromName(name string) ErrorReason {
	for _, p := range errorReasonPairs {
		if p.g.String() == name {
			return p.g
		}
	}
	return ReasonUnspecified
}

// classFromCode classifies a status that carries no exec or resource detail by its code alone.
func classFromCode(code codes.Code) ErrorClass {
	switch code {
	case codes.PermissionDenied:
		return ClassForbidden
	case codes.Unauthenticated:
		return ClassUnauthorized
	case codes.NotFound:
		return ClassNotFound
	case codes.AlreadyExists, codes.Aborted:
		return ClassConflict
	case codes.Unavailable:
		return ClassConnection
	case codes.DeadlineExceeded:
		return ClassTimeout
	case codes.InvalidArgument, codes.OutOfRange, codes.FailedPrecondition:
		return ClassInvalidRequest
	case codes.Unimplemented:
		return ClassUnsupported
	case codes.ResourceExhausted:
		return ClassLimit
	case codes.Canceled:
		return ClassCancelled
	case codes.OK, codes.Unknown, codes.Internal, codes.DataLoss:
		return ClassInternal
	}
	return ClassInternal
}

// classFromResourceCode maps a resource-lane error code onto the exec classes.
func classFromResourceCode(code string) ErrorClass {
	switch code {
	case "INVALID_REQUEST", "INVALID_INPUT", "FILTER_UNKNOWN_FIELD", "FILTER_INVALID_OPERATOR":
		return ClassInvalidRequest
	case "UNSUPPORTED":
		return ClassUnsupported
	case "RESOURCE_LIMIT":
		return ClassLimit
	case "NOT_FOUND":
		return ClassNotFound
	case "ALREADY_EXISTS", "CONFLICT":
		return ClassConflict
	case "UNAUTHORIZED":
		return ClassUnauthorized
	case "FORBIDDEN", "PERMISSION_DENIED", "GRANT_DENIED":
		return ClassForbidden
	case "UNAVAILABLE", "OWNER_UNAVAILABLE", "DELEGATION_NOT_ACTIVE":
		return ClassUnavailable
	case "CONNECTION_ERROR", "CERTIFICATE_ERROR", "CONNECTION_NOT_STARTED":
		return ClassConnection
	case "TIMEOUT":
		return ClassTimeout
	case "CANCELLED":
		return ClassCancelled
	default:
		return ClassInternal
	}
}

// classifyError turns an error an author's callback returned into the *Error that may cross the wire. In order: an
// *Error the author built; the target's own Classify; the SDK's classification of context, resource-lane, gRPC and
// network errors; ClassInternal. The local cause is kept in Err and never sent.
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
	return &Error{Class: sdkClass(err), Err: err}
}

// sdkClass is the SDK's own classification of the errors every plugin shares.
func sdkClass(err error) ErrorClass {
	var resourceErr *resourcev2.ResourceOperationError
	switch {
	case errors.Is(err, context.Canceled):
		return ClassCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTimeout
	case errors.As(err, &resourceErr) && resourceErr != nil:
		return classFromResourceCode(resourcev2.ErrorCode(err))
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		return classFromCode(st.Code())
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return ClassTimeout
		}
		return ClassConnection
	}
	return ClassInternal
}
