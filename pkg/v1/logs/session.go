package logs

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// LogSessionStatus represents the status of a log session.
type LogSessionStatus int

const (
	// LogSessionStatusActive indicates that the session is delivering logs.
	LogSessionStatusActive LogSessionStatus = iota
	// LogSessionStatusPaused indicates that log delivery is suspended.
	LogSessionStatusPaused
	// LogSessionStatusClosed indicates that the session has ended.
	LogSessionStatusClosed
	// LogSessionStatusError indicates that the session encountered a failure.
	LogSessionStatusError
	// LogSessionStatusConnecting indicates that native source acquisition is pending.
	LogSessionStatusConnecting
	// LogSessionStatusInitializing indicates that session setup is in progress.
	LogSessionStatusInitializing
)

// ToProto converts the status to its wire value, defaulting unknown values to active.
func (s LogSessionStatus) ToProto() logspb.LogSessionStatus {
	switch s {
	case LogSessionStatusActive:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_ACTIVE
	case LogSessionStatusPaused:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_PAUSED
	case LogSessionStatusClosed:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_CLOSED
	case LogSessionStatusError:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_ERROR
	case LogSessionStatusConnecting:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_CONNECTING
	case LogSessionStatusInitializing:
		return logspb.LogSessionStatus_LOG_SESSION_STATUS_INITIALIZING
	}
	return logspb.LogSessionStatus_LOG_SESSION_STATUS_ACTIVE
}

// LogSessionStatusFromProto decodes a wire status, defaulting unknown values to active.
func LogSessionStatusFromProto(p logspb.LogSessionStatus) LogSessionStatus {
	switch p {
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_ACTIVE:
		return LogSessionStatusActive
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_PAUSED:
		return LogSessionStatusPaused
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_CLOSED:
		return LogSessionStatusClosed
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_ERROR:
		return LogSessionStatusError
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_CONNECTING:
		return LogSessionStatusConnecting
	case logspb.LogSessionStatus_LOG_SESSION_STATUS_INITIALIZING:
		return LogSessionStatusInitializing
	}
	return LogSessionStatusActive
}

// LogSession represents an active log viewing session.
type LogSession struct {
	ID            string            `json:"id"`
	PluginID      string            `json:"plugin_id"`
	ConnectionID  string            `json:"connection_id"`
	ResourceKey   string            `json:"resource_key"`
	ResourceID    string            `json:"resource_id"`
	Options       LogSessionOptions `json:"options"`
	Status        LogSessionStatus  `json:"status"`
	ActiveSources []LogSource       `json:"active_sources"`
	CreatedAt     time.Time         `json:"created_at"`
	// ReplayMarkers is true when the plugin accepted the session's request for replay
	// markers ([ParamReplayMarkers]): it flags the lines of each source's replay
	// ([LogLine.Replay]) and sends [StreamEventSourceCaughtUp] when a source's replay
	// is delivered. False means no caught-up event will ever come: the reader did not
	// ask, the plugin's SDK predates replay markers, or its handler has no
	// [Handler.ReplayCount]. It is known from the create reply and fixed for the
	// session, so a reader tells "never" from "not yet" without a timeout. The wire
	// carries it as [ParamReplayMarkersAccepted] in Options.Params, the one place a
	// create reply can say it without a wire change; [LogSessionFromProto] reads it.
	ReplayMarkers bool `json:"replay_markers,omitempty"`
}

// ToProto encodes the session snapshot and its active sources.
func (s *LogSession) ToProto() *logspb.LogSession {
	sources := make([]*logspb.LogSource, 0, len(s.ActiveSources))
	for _, src := range s.ActiveSources {
		sources = append(sources, src.ToProto())
	}
	return &logspb.LogSession{
		Id:            s.ID,
		PluginId:      s.PluginID,
		ConnectionId:  s.ConnectionID,
		ResourceKey:   s.ResourceKey,
		ResourceId:    s.ResourceID,
		Options:       s.Options.ToProto(),
		Status:        s.Status.ToProto(),
		ActiveSources: sources,
		CreatedAt:     timestamppb.New(s.CreatedAt),
	}
}

// LogSessionFromProto decodes a session snapshot without opening native sources.
func LogSessionFromProto(p *logspb.LogSession) *LogSession {
	sources := make([]LogSource, 0, len(p.GetActiveSources()))
	for _, src := range p.GetActiveSources() {
		sources = append(sources, LogSourceFromProto(src))
	}
	options := LogSessionOptionsFromProto(p.GetOptions())
	return &LogSession{
		ID:            p.GetId(),
		PluginID:      p.GetPluginId(),
		ConnectionID:  p.GetConnectionId(),
		ResourceKey:   p.GetResourceKey(),
		ResourceID:    p.GetResourceId(),
		Options:       options,
		Status:        LogSessionStatusFromProto(p.GetStatus()),
		ActiveSources: sources,
		CreatedAt:     p.GetCreatedAt().AsTime(),
		ReplayMarkers: replayAccepted(options.Params),
	}
}

// LogSessionOptions contains options for creating or updating a log session.
type LogSessionOptions struct {
	Reference           *resourcev2.ResourceReference `json:"-"`
	Route               *resourcev2.AccessRoute       `json:"-"`
	Target              string                        `json:"target"`
	Follow              bool                          `json:"follow"`
	IncludePrevious     bool                          `json:"include_previous"`
	IncludeTimestamps   bool                          `json:"include_timestamps"`
	TailLines           int64                         `json:"tail_lines"`
	SinceSeconds        int64                         `json:"since_seconds"`
	SinceTime           *time.Time                    `json:"since_time,omitempty"`
	LimitBytes          int64                         `json:"limit_bytes"`
	IncludeSourceEvents bool                          `json:"include_source_events"`
	Params              map[string]string             `json:"params"`
}

// ToProto encodes stream options, sharing Params and omitting Reference and Route.
// A nil receiver produces nil; resource admission metadata travels separately.
func (o *LogSessionOptions) ToProto() *logspb.LogSessionOptions {
	if o == nil {
		return nil
	}
	p := &logspb.LogSessionOptions{
		Target:              o.Target,
		Follow:              o.Follow,
		IncludePrevious:     o.IncludePrevious,
		IncludeTimestamps:   o.IncludeTimestamps,
		TailLines:           o.TailLines,
		SinceSeconds:        o.SinceSeconds,
		LimitBytes:          o.LimitBytes,
		IncludeSourceEvents: o.IncludeSourceEvents,
		Params:              o.Params,
	}
	if o.SinceTime != nil {
		p.SinceTime = timestamppb.New(*o.SinceTime)
	}
	return p
}

// LogSessionOptionsFromProto decodes stream options and shares the wire Params map.
// Nil input produces zero options without resource admission metadata.
func LogSessionOptionsFromProto(p *logspb.LogSessionOptions) LogSessionOptions {
	if p == nil {
		return LogSessionOptions{}
	}
	opts := LogSessionOptions{
		Target:              p.GetTarget(),
		Follow:              p.GetFollow(),
		IncludePrevious:     p.GetIncludePrevious(),
		IncludeTimestamps:   p.GetIncludeTimestamps(),
		TailLines:           p.GetTailLines(),
		SinceSeconds:        p.GetSinceSeconds(),
		LimitBytes:          p.GetLimitBytes(),
		IncludeSourceEvents: p.GetIncludeSourceEvents(),
		Params:              p.GetParams(),
	}
	if p.GetSinceTime() != nil {
		t := p.GetSinceTime().AsTime()
		opts.SinceTime = &t
	}
	return opts
}

// CreateSessionOptions contains everything needed to create a log session.
type CreateSessionOptions struct {
	// SessionID optionally binds resource-session acceptance to a host-generated UUID.
	SessionID    string                        `json:"session_id,omitempty"`
	ResourceKey  string                        `json:"resource_key"`
	ResourceID   string                        `json:"resource_id"`
	ResourceData *resourcev2.Value             `json:"resource_data"`
	Reference    *resourcev2.ResourceReference `json:"reference,omitempty"`
	Route        *resourcev2.AccessRoute       `json:"route,omitempty"`
	Options      LogSessionOptions             `json:"options"`
}
