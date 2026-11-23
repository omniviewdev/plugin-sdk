package session

import "strconv"

// Cause says why a session ended. It is lane-neutral: each lane maps it to its own
// vocabulary, such as the reason a log stream or an exec run reports to its client.
type Cause uint8

const (
	// CauseNone means the session has not ended. It is never a valid argument to
	// [Entry.End].
	CauseNone Cause = iota
	// CauseFinished means the session's own work ended by itself.
	CauseFinished
	// CauseRequested means the session's owner asked for the end: a close, a kill.
	CauseRequested
	// CauseShutdown means the registry was closed: the plugin is shutting down.
	CauseShutdown
	// CauseRetired means the connection the session belongs to was retired.
	CauseRetired
	// CauseExpired means a lease or an idle bound the lane set ran out.
	CauseExpired
	// CauseTimeout means a time bound on the work, such as a run's timeout, passed.
	CauseTimeout
	// CauseCancelled means the context that owned the session ended.
	CauseCancelled
)

var causeNames = [...]string{
	CauseNone:      "none",
	CauseFinished:  "finished",
	CauseRequested: "requested",
	CauseShutdown:  "shutdown",
	CauseRetired:   "retired",
	CauseExpired:   "expired",
	CauseTimeout:   "timeout",
	CauseCancelled: "cancelled",
}

// String returns the cause as a lowercase word: "none", "finished", "requested", "shutdown",
// "retired", "expired", "timeout" or "cancelled". A value that is not a cause is
// "cause(N)".
func (c Cause) String() string {
	if int(c) < len(causeNames) {
		return causeNames[c]
	}
	return "cause(" + strconv.Itoa(int(c)) + ")"
}
