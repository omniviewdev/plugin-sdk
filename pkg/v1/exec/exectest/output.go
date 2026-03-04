package exectest

import (
	"sync"
	"time"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
)

// RecordingOutput implements exec.OutputSink by recording all output.
// Thread-safe, with WaitFor* helpers for test assertions.
type RecordingOutput struct {
	mu      sync.Mutex
	outputs []exec.StreamOutput
	notify  chan struct{}
}

var _ exec.OutputSink = (*RecordingOutput)(nil)

// NewRecordingOutput creates a new RecordingOutput.
func NewRecordingOutput() *RecordingOutput {
	return &RecordingOutput{
		notify: make(chan struct{}),
	}
}

func (r *RecordingOutput) OnOutput(output exec.StreamOutput) {
	r.mu.Lock()
	r.outputs = append(r.outputs, output)
	r.broadcast()
	r.mu.Unlock()
}

// broadcast wakes up all waiters by closing the notify channel and replacing it.
func (r *RecordingOutput) broadcast() {
	close(r.notify)
	r.notify = make(chan struct{})
}

// Outputs returns a copy of all recorded outputs.
func (r *RecordingOutput) Outputs() []exec.StreamOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]exec.StreamOutput, len(r.outputs))
	copy(cp, r.outputs)
	return cp
}

// Count returns the number of recorded outputs.
func (r *RecordingOutput) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outputs)
}

// WaitForOutputs blocks until at least count outputs have been recorded,
// or the timeout expires.
func (r *RecordingOutput) WaitForOutputs(count int, timeout time.Duration) []exec.StreamOutput {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		if len(r.outputs) >= count {
			cp := make([]exec.StreamOutput, len(r.outputs))
			copy(cp, r.outputs)
			r.mu.Unlock()
			return cp
		}
		ch := r.notify
		r.mu.Unlock()

		select {
		case <-ch:
			continue
		case <-deadline:
			return r.Outputs()
		}
	}
}

// WaitForSignal blocks until an output with the given signal is recorded,
// or the timeout expires.
func (r *RecordingOutput) WaitForSignal(signal exec.StreamSignal, timeout time.Duration) *exec.StreamOutput {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		for i := range r.outputs {
			if r.outputs[i].Signal == signal {
				out := r.outputs[i]
				r.mu.Unlock()
				return &out
			}
		}
		ch := r.notify
		r.mu.Unlock()

		select {
		case <-ch:
			continue
		case <-deadline:
			return nil
		}
	}
}

// DataOutputs returns only the outputs that have non-zero data and no signal.
func (r *RecordingOutput) DataOutputs() []exec.StreamOutput {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []exec.StreamOutput
	for _, o := range r.outputs {
		if len(o.Data) > 0 && o.Signal == exec.StreamSignalNone {
			result = append(result, o)
		}
	}
	return result
}

// AllData concatenates all data from non-signal outputs.
func (r *RecordingOutput) AllData() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var buf []byte
	for _, o := range r.outputs {
		if o.Signal == exec.StreamSignalNone {
			buf = append(buf, o.Data...)
		}
	}
	return buf
}
