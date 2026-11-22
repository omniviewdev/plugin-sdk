package logs

import (
	"maps"

	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

const (
	// ParamReplayMarkers is the session parameter a reader sets to [ReplayMarkersOn]
	// in [LogSessionOptions.Params] to ask for replay markers: the [LogLine.Replay] flag
	// on the lines of each source's replay, and a [StreamEventSourceCaughtUp] event when
	// the last of them has been delivered. Only a reader that asks is ever sent the
	// event: a reader whose SDK predates it decodes the event as a source joining.
	//
	// Parameter names that start with "omniview." are reserved to the SDK.
	ParamReplayMarkers = "omniview.log.replay-markers"
	// ParamReplayMarkersAccepted is how the create reply says the plugin accepted:
	// the key is set to [ReplayMarkersOn] in the reply's Options.Params. A plugin that
	// predates replay markers echoes the reader's own parameters, which never carry this key:
	// the SDK removes it from every create and update request it encodes, so a reader
	// cannot forge it, even by reusing the options of an accepted session.
	// [LogSession.ReplayMarkers] is this key read back.
	ParamReplayMarkersAccepted = "omniview.log.replay-markers.accepted"
	// ReplayMarkersOn is the value of both parameters.
	ReplayMarkersOn = "1"
)

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

// requestOptions encodes options for a request. The acceptance key is a plugin's
// to set in a reply, never a reader's to send: options reused from an accepted
// session must not make a plugin that merely echoes them look as though it
// accepted. The caller's map is not changed.
func requestOptions(o *LogSessionOptions) *logspb.LogSessionOptions {
	p := o.ToProto()
	if p != nil {
		p.Params = stampReplayAcceptance(p.Params, false)
	}
	return p
}

// replayCount follows one attempt's stream against the number of lines its plugin
// counted as replay. A nil *replayCount is a stream that takes no part in replay markers.
type replayCount struct {
	finite    bool  // the stream ends: every line is replay and its end is the boundary
	remaining int64 // lines of the counted replay still to come
}

// empty reports whether the stream has no replay to deliver, so it is caught up
// as soon as it opens.
func (c *replayCount) empty() bool { return c != nil && !c.finite && c.remaining == 0 }

// take records one line read from the stream. It reports whether the line is
// replay and whether it is the last of the counted replay.
func (c *replayCount) take() (replay, last bool) {
	switch {
	case c == nil:
		return false, false
	case c.finite:
		return true, false
	case c.remaining <= 0:
		return false, false
	}
	c.remaining--
	return true, c.remaining == 0
}
