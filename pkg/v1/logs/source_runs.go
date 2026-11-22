package logs

import "context"

// sourceRun is the registration of one run of a source: the handle that cancels its
// stream. A source that is disabled and enabled again has a new run while the stream of
// the old one may still be closing, so a run is told apart by identity, never by its
// source's ID: the exit of an old stream must not take the registration of the new run
// with it.
type sourceRun struct{ cancel context.CancelFunc }

// registerLocked registers a new run of the source, with a context that ends with the
// session's or when the run is cancelled. The caller holds sourceMu. It returns false,
// and registers nothing, when the source already has a run.
func (s *sessionState) registerLocked(sourceID string) (context.Context, *sourceRun, bool) {
	if _, exists := s.sourceCtxs[sourceID]; exists {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	run := &sourceRun{cancel: cancel}
	s.sourceCtxs[sourceID] = run
	return ctx, run, true
}

// deregister removes the run's registration, unless the source has a newer run by now.
func (s *sessionState) deregister(sourceID string, run *sourceRun) {
	s.sourceMu.Lock()
	if s.sourceCtxs[sourceID] == run {
		delete(s.sourceCtxs, sourceID)
	}
	s.sourceMu.Unlock()
}
