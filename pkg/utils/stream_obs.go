package utils

import (
	"context"
	"errors"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/status"

	logging "github.com/omniviewdev/plugin-sdk/log"

	"github.com/omniviewdev/plugin-sdk/internal/obs"
)

// StreamObs observes one long-lived server stream. The span covers only the
// open/setup phase; closing the stream records a Debug log and releases the
// stream gauge. It never records payloads or error text.
type StreamObs struct {
	log     logging.Logger
	service string
	op      string
	start   time.Time
	span    trace.Span
	release func()
}

// BeginStream starts the "sdk.<service>.open" span and registers the stream
// with the sdk.streams gauge. op must be a vocabulary value accepted by
// obs.StreamOpened. Call OpenDone or OpenFailed exactly once, then Close.
func BeginStream(ctx context.Context, log logging.Logger, service, op string) (context.Context, *StreamObs) {
	if log == nil {
		log = logging.Default()
	}
	ctx, span := obs.Tracer().Start(ctx, "sdk."+service+".open",
		trace.WithAttributes(attribute.String("operation", op)))
	return ctx, &StreamObs{
		log:     log,
		service: service,
		op:      op,
		start:   time.Now(),
		span:    span,
		release: obs.StreamOpened(op),
	}
}

// OpenDone ends the open-phase span successfully.
func (s *StreamObs) OpenDone() { obs.End(s.span, nil, "") }

// OpenFailed ends the open-phase span with a classified failure and logs it.
func (s *StreamObs) OpenFailed(ctx context.Context, err error) {
	obs.End(s.span, err, "")
	s.log.Debugw(ctx, "stream open failed",
		"operation", s.op, "error_code", obs.ErrorCode(err, ""), "duration_ms", time.Since(s.start).Milliseconds())
}

// Event adds a bounded event to the open-phase span; it is a no-op after OpenDone.
func (s *StreamObs) Event(name string, attrs ...attribute.KeyValue) {
	s.span.AddEvent(name, trace.WithAttributes(attrs...))
}

// Close releases the stream gauge and logs the classified end of the stream.
// io.EOF is the normal client close and is reported as a clean end.
func (s *StreamObs) Close(ctx context.Context, err error) {
	s.release()
	grpcCode := "OK"
	code := ""
	if err != nil && !errors.Is(err, io.EOF) {
		code = obs.ErrorCode(err, "")
		grpcCode = status.Code(err).String()
	}
	s.log.Debugw(ctx, "stream closed",
		"operation", s.op, "grpc_code", grpcCode, "error_code", code,
		"duration_ms", time.Since(s.start).Milliseconds())
}
