package logtest

import (
	"slices"
	"sync"
	"time"

	logs "github.com/omniviewdev/plugin-sdk/pkg/v1/logs"
)

// RecordingOutput is a thread-safe recorder for all output from a log manager.
// It captures log lines and stream events, and provides waiters for test assertions.
type RecordingOutput struct {
	mu       sync.Mutex
	lines    []logs.LogLine
	events   []logs.LogStreamEvent
	statuses []logs.LogSessionStatus
	notify   chan struct{} // broadcast channel, replaced on each wait
}

// NewRecordingOutput creates a new RecordingOutput.
func NewRecordingOutput() *RecordingOutput {
	return &RecordingOutput{
		notify: make(chan struct{}),
	}
}

// RecordLine records a log line. Thread-safe.
func (r *RecordingOutput) RecordLine(line logs.LogLine) {
	r.mu.Lock()
	r.lines = append(r.lines, line)
	r.mu.Unlock()
	r.broadcast()
}

// RecordEvent records a stream event. Thread-safe.
func (r *RecordingOutput) RecordEvent(event logs.LogStreamEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	r.broadcast()
}

// RecordStatus records a session status. Thread-safe.
func (r *RecordingOutput) RecordStatus(status logs.LogSessionStatus) {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
	r.broadcast()
}

// Lines returns a copy of all recorded lines.
func (r *RecordingOutput) Lines() []logs.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

// Events returns a copy of all recorded events.
func (r *RecordingOutput) Events() []logs.LogStreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// Statuses returns a copy of all recorded statuses.
func (r *RecordingOutput) Statuses() []logs.LogSessionStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.statuses)
}

// WaitForLines waits until at least count lines have been recorded, or times out.
func (r *RecordingOutput) WaitForLines(count int, timeout time.Duration) []logs.LogLine {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		if len(r.lines) >= count {
			cp := slices.Clone(r.lines)
			r.mu.Unlock()
			return cp
		}
		ch := r.getNotifyChanLocked()
		r.mu.Unlock()

		select {
		case <-ch:
		case <-deadline:
			return r.Lines()
		}
	}
}

// WaitForEvent waits until an event of the given type is recorded, or times out.
func (r *RecordingOutput) WaitForEvent(eventType logs.LogStreamEventType, timeout time.Duration) *logs.LogStreamEvent {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		for i := range r.events {
			if r.events[i].Type == eventType {
				evt := r.events[i]
				r.mu.Unlock()
				return &evt
			}
		}
		ch := r.getNotifyChanLocked()
		r.mu.Unlock()

		select {
		case <-ch:
		case <-deadline:
			return nil
		}
	}
}

// WaitForStatus waits until a session with the given status is recorded, or times out.
func (r *RecordingOutput) WaitForStatus(status logs.LogSessionStatus, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		r.mu.Lock()
		for _, s := range r.statuses {
			if s == status {
				r.mu.Unlock()
				return true
			}
		}
		ch := r.getNotifyChanLocked()
		r.mu.Unlock()

		select {
		case <-ch:
		case <-deadline:
			return false
		}
	}
}

func (r *RecordingOutput) broadcast() {
	r.mu.Lock()
	ch := r.notify
	r.notify = make(chan struct{})
	r.mu.Unlock()
	close(ch)
}

func (r *RecordingOutput) getNotifyChanLocked() chan struct{} {
	return r.notify
}

// OnLine satisfies the logs.OutputSink interface.
func (r *RecordingOutput) OnLine(line logs.LogLine) {
	r.RecordLine(line)
}

// OnEvent satisfies the logs.OutputSink interface.
func (r *RecordingOutput) OnEvent(_ string, event logs.LogStreamEvent) {
	r.RecordEvent(event)
}
