package logs

import (
	"maps"
	"time"
)

const (
	// ParamReplayMarkers is the session parameter a reader sets to [ReplayMarkersOn]
	// in [LogSessionOptions.Params] to ask the plugin to say where each source's
	// replay ends. Only a reader that asks is ever sent [StreamEventSourceCaughtUp]:
	// a reader that does not know it decodes the event as a source joining.
	ParamReplayMarkers = "omniview.log.replay-markers"
	// ParamReplayMarkersAccepted is how the create reply says the plugin accepted:
	// the key is set to [ReplayMarkersOn] in the reply's Options.Params. A plugin
	// that predates markers echoes the reader's own parameters, which never carry
	// this key; the SDK removes it from a request, so a reader cannot forge it.
	// [LogSession.ReplayMarkers] is this key read back.
	ParamReplayMarkersAccepted = "omniview.log.replay-markers.accepted"
	// ReplayMarkersOn is the value of both parameters.
	ReplayMarkersOn = "1"
)

// DefaultReplayIdleTimeout is how long a [Handler.Replay] stream may return
// neither a line nor io.EOF before its source fails; see
// [ManagerConfig.ReplayIdleTimeout].
const DefaultReplayIdleTimeout = 30 * time.Second

// replayRequested reports whether a session's options ask for replay markers.
func replayRequested(params map[string]string) bool {
	return params[ParamReplayMarkers] == ReplayMarkersOn
}

// replayAccepted reports whether a create reply's options say the plugin accepted.
func replayAccepted(params map[string]string) bool {
	return params[ParamReplayMarkersAccepted] == ReplayMarkersOn
}

// stampReplayAcceptance returns params with the acceptance key set to what the
// session decided: present when accepted, absent otherwise, so a reader can neither
// forge it nor lose it when it replaces the session's options. The map is copied
// when it changes, never mutated: handlers share the request's own.
func stampReplayAcceptance(params map[string]string, accepted bool) map[string]string {
	if _, present := params[ParamReplayMarkersAccepted]; !present && !accepted {
		return params
	}
	stamped := maps.Clone(params)
	if stamped == nil {
		stamped = make(map[string]string, 1)
	}
	if accepted {
		stamped[ParamReplayMarkersAccepted] = ReplayMarkersOn
	} else {
		delete(stamped, ParamReplayMarkersAccepted)
	}
	return stamped
}
