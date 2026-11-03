package logs

import "context"

// OutputSink receives log lines and stream events from the Manager.
// Tests inject RecordingOutput; callers may inject a custom sink instead of
// the Manager's default session-cancellable output channel.
type OutputSink interface {
	OnLine(line LogLine)
	OnEvent(sessionID string, event LogStreamEvent)
}

// ChannelSink writes StreamOutput to a channel with context-aware sends.
// Callers may inject it when a shared context should control delivery.
// If that context is canceled, sends stop blocking on a full channel.
type ChannelSink struct {
	ctx context.Context
	out chan StreamOutput
}

// NewChannelSink creates a ChannelSink wrapping the given output channel.
// The context controls the lifetime — sends are dropped after cancellation.
func NewChannelSink(ctx context.Context, out chan StreamOutput) *ChannelSink {
	return &ChannelSink{ctx: ctx, out: out}
}

// OnLine sends a line or returns when the sink context is canceled.
func (s *ChannelSink) OnLine(line LogLine) {
	select {
	case s.out <- StreamOutput{
		SessionID: line.SessionID,
		Line:      &line,
	}:
	case <-s.ctx.Done():
	}
}

// OnEvent sends a session event or returns when the sink context is canceled.
func (s *ChannelSink) OnEvent(sessionID string, event LogStreamEvent) {
	select {
	case s.out <- StreamOutput{
		SessionID: sessionID,
		Event:     &event,
	}:
	case <-s.ctx.Done():
	}
}
