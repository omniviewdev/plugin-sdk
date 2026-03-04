package logs

import "context"

// OutputSink receives log lines and stream events from the Manager.
// Tests inject RecordingOutput; production uses ChannelSink.
type OutputSink interface {
	OnLine(line LogLine)
	OnEvent(sessionID string, event LogStreamEvent)
}

// ChannelSink writes StreamOutput to a channel with context-aware sends.
// This is the production implementation used by Stream(). If the context
// is cancelled (e.g., gRPC stream closed), sends are dropped instead of
// blocking forever on a full channel.
type ChannelSink struct {
	ctx context.Context
	out chan StreamOutput
}

// NewChannelSink creates a ChannelSink wrapping the given output channel.
// The context controls the lifetime — sends are dropped after cancellation.
func NewChannelSink(ctx context.Context, out chan StreamOutput) *ChannelSink {
	return &ChannelSink{ctx: ctx, out: out}
}

func (s *ChannelSink) OnLine(line LogLine) {
	select {
	case s.out <- StreamOutput{
		SessionID: line.SessionID,
		Line:      &line,
	}:
	case <-s.ctx.Done():
	}
}

func (s *ChannelSink) OnEvent(sessionID string, event LogStreamEvent) {
	select {
	case s.out <- StreamOutput{
		SessionID: sessionID,
		Event:     &event,
	}:
	case <-s.ctx.Done():
	}
}
