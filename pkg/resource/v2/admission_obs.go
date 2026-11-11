package resourcev2

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/omniviewdev/plugin-sdk/internal/obs"
	logging "github.com/omniviewdev/plugin-sdk/log"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// admissionRejected records a rejected request (span event, Debug log, metric)
// and returns err unchanged so call sites can wrap their return expression.
func admissionRejected(ctx context.Context, operation string, err error) error {
	if err != nil {
		obs.Reject(ctx, nil, operation, ErrorCode(err))
	}
	return err
}

// admissionPageInvalid reports a provider source page invariant violation. The
// reason is a closed, SDK-authored token; provider text is never logged.
func admissionPageInvalid(target ReadTarget, reason string, err error) error {
	if err == nil {
		return nil
	}
	ctx := context.Background()
	logging.Default().Warnw(ctx, "provider source page invalid",
		"reason", reason,
		"source_id", target.Provider+"/"+target.Type+"/"+target.Representation,
		"error_code", ErrorCode(err),
	)
	obs.Reject(ctx, nil, "check_page", ErrorCode(err), "reason", reason)
	return err
}

// guardPanic converts a provider panic into INTERNAL and records it. It logs the
// panic's Go type and stack only: the panic value may carry provider data.
func guardPanic(ctx context.Context, operation string, recovered any) {
	logging.Default().Errorw(ctx, "provider handler panicked",
		"operation", operation,
		"panic_type", panicType(recovered),
		"stack", string(debug.Stack()),
	)
	trace.SpanFromContext(ctx).AddEvent("panic_recovered", trace.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("panic_type", panicType(recovered)),
	))
}

func panicType(recovered any) string { return fmt.Sprintf("%T", recovered) }

// admissionActionResult records rejected or unavailable (nil, nil) action
// descriptor outcomes. It is curried so it can wrap a two-value call directly.
func admissionActionResult(ctx context.Context, operation string) func(*ActionDescriptor, error) (*ActionDescriptor, error) {
	return func(descriptor *ActionDescriptor, err error) (*ActionDescriptor, error) {
		if err != nil {
			return descriptor, admissionRejected(ctx, operation, err)
		}
		if descriptor == nil {
			obs.Reject(ctx, nil, operation, "descriptor_unavailable")
		}
		return descriptor, nil
	}
}
