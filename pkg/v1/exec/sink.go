package exec

import "context"

// OutputSink receives stream output from the Manager.
// Tests inject RecordingOutput; production uses ChannelSink.
type OutputSink interface {
	OnOutput(output StreamOutput)
}

// ChannelSink writes StreamOutput to a channel with context-aware sends.
// This is the production implementation used by Stream(). If the context
// is cancelled (e.g., gRPC stream closed), sends may be dropped instead of
// blocking forever on a full channel.
type ChannelSink struct {
	ctx context.Context
	out chan<- StreamOutput
}

// NewChannelSink creates a ChannelSink wrapping the given output channel.
// The context controls the lifetime — sends may be dropped after cancellation.
// If ctx is nil, context.Background() is used. If out is nil, a buffered
// drop channel is created (output is silently discarded).
func NewChannelSink(ctx context.Context, out chan<- StreamOutput) *ChannelSink {
	if ctx == nil {
		ctx = context.Background()
	}
	if out == nil {
		out = make(chan StreamOutput, 1)
	}
	return &ChannelSink{ctx: ctx, out: out}
}

func (s *ChannelSink) OnOutput(output StreamOutput) {
	if s == nil || s.out == nil {
		return
	}
	if s.ctx == nil {
		select {
		case s.out <- output:
		default:
		}
		return
	}
	select {
	case s.out <- output:
	case <-s.ctx.Done():
	default:
		// Drop output when channel is full to avoid blocking.
	}
}
