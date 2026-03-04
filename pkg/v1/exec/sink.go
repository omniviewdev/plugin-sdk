package exec

import "context"

// OutputSink receives stream output from the Manager.
// Tests inject RecordingOutput; production uses ChannelSink.
type OutputSink interface {
	OnOutput(output StreamOutput)
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

func (s *ChannelSink) OnOutput(output StreamOutput) {
	select {
	case s.out <- output:
	case <-s.ctx.Done():
	}
}
