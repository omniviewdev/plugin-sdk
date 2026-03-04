package logs

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// LogLineOrigin indicates the origin of a log line.
type LogLineOrigin int

const (
	LogLineOriginCurrent  LogLineOrigin = iota
	LogLineOriginPrevious
	LogLineOriginSystem
)

func (o LogLineOrigin) ToProto() logspb.LogLineOrigin {
	switch o {
	case LogLineOriginCurrent:
		return logspb.LogLineOrigin_LOG_LINE_ORIGIN_CURRENT
	case LogLineOriginPrevious:
		return logspb.LogLineOrigin_LOG_LINE_ORIGIN_PREVIOUS
	case LogLineOriginSystem:
		return logspb.LogLineOrigin_LOG_LINE_ORIGIN_SYSTEM
	}
	return logspb.LogLineOrigin_LOG_LINE_ORIGIN_CURRENT
}

func LogLineOriginFromProto(p logspb.LogLineOrigin) LogLineOrigin {
	switch p {
	case logspb.LogLineOrigin_LOG_LINE_ORIGIN_CURRENT:
		return LogLineOriginCurrent
	case logspb.LogLineOrigin_LOG_LINE_ORIGIN_PREVIOUS:
		return LogLineOriginPrevious
	case logspb.LogLineOrigin_LOG_LINE_ORIGIN_SYSTEM:
		return LogLineOriginSystem
	}
	return LogLineOriginCurrent
}

// LogLevel represents the severity level of a log line.
type LogLevel int

const (
	LogLevelUnspecified LogLevel = iota
	LogLevelTrace
	LogLevelDebug
	LogLevelInfo
	LogLevelWarn
	LogLevelError
	LogLevelFatal
)

func (l LogLevel) ToProto() logspb.LogLevel {
	switch l {
	case LogLevelUnspecified:
		return logspb.LogLevel_LOG_LEVEL_UNSPECIFIED
	case LogLevelTrace:
		return logspb.LogLevel_LOG_LEVEL_TRACE
	case LogLevelDebug:
		return logspb.LogLevel_LOG_LEVEL_DEBUG
	case LogLevelInfo:
		return logspb.LogLevel_LOG_LEVEL_INFO
	case LogLevelWarn:
		return logspb.LogLevel_LOG_LEVEL_WARN
	case LogLevelError:
		return logspb.LogLevel_LOG_LEVEL_ERROR
	case LogLevelFatal:
		return logspb.LogLevel_LOG_LEVEL_FATAL
	}
	return logspb.LogLevel_LOG_LEVEL_UNSPECIFIED
}

func LogLevelFromProto(p logspb.LogLevel) LogLevel {
	switch p {
	case logspb.LogLevel_LOG_LEVEL_UNSPECIFIED:
		return LogLevelUnspecified
	case logspb.LogLevel_LOG_LEVEL_TRACE:
		return LogLevelTrace
	case logspb.LogLevel_LOG_LEVEL_DEBUG:
		return LogLevelDebug
	case logspb.LogLevel_LOG_LEVEL_INFO:
		return LogLevelInfo
	case logspb.LogLevel_LOG_LEVEL_WARN:
		return LogLevelWarn
	case logspb.LogLevel_LOG_LEVEL_ERROR:
		return LogLevelError
	case logspb.LogLevel_LOG_LEVEL_FATAL:
		return LogLevelFatal
	}
	return LogLevelUnspecified
}

// LogLine represents a single log line from any source.
type LogLine struct {
	SessionID string            `json:"session_id"`
	SourceID  string            `json:"source_id"`
	Labels    map[string]string `json:"labels"`
	Timestamp time.Time         `json:"timestamp"`
	Content   string            `json:"content"`
	Origin    LogLineOrigin     `json:"origin"`
	Level     LogLevel          `json:"level"`
}

func (l *LogLine) ToProto() *logspb.LogLine {
	return &logspb.LogLine{
		SessionId: l.SessionID,
		SourceId:  l.SourceID,
		Labels:    l.Labels,
		Timestamp: timestamppb.New(l.Timestamp),
		Content:   []byte(l.Content),
		Origin:    l.Origin.ToProto(),
		Level:     l.Level.ToProto(),
	}
}

func LogLineFromProto(p *logspb.LogLine) LogLine {
	var ts time.Time
	if p.GetTimestamp() != nil {
		ts = p.GetTimestamp().AsTime()
	}
	return LogLine{
		SessionID: p.GetSessionId(),
		SourceID:  p.GetSourceId(),
		Labels:    p.GetLabels(),
		Timestamp: ts,
		Content:   string(p.GetContent()),
		Origin:    LogLineOriginFromProto(p.GetOrigin()),
		Level:     LogLevelFromProto(p.GetLevel()),
	}
}

// LogSource is a generic log-producing entity.
type LogSource struct {
	ID     string            `json:"id"`
	Labels map[string]string `json:"labels"`
}

func (s *LogSource) ToProto() *logspb.LogSource {
	return &logspb.LogSource{
		Id:     s.ID,
		Labels: s.Labels,
	}
}

func LogSourceFromProto(p *logspb.LogSource) LogSource {
	return LogSource{
		ID:     p.GetId(),
		Labels: p.GetLabels(),
	}
}

// SourceEventType represents the type of source lifecycle event.
type SourceEventType int

const (
	SourceAdded SourceEventType = iota
	SourceRemoved
)

// SourceEvent represents a source lifecycle change.
type SourceEvent struct {
	Type   SourceEventType
	Source LogSource
}

// LogStreamEventType represents the type of log stream event.
type LogStreamEventType int

const (
	StreamEventSourceAdded LogStreamEventType = iota
	StreamEventSourceRemoved
	StreamEventStreamError
	StreamEventReconnecting
	StreamEventReconnected
	StreamEventStreamEnded
	StreamEventSessionReady
)

func (t LogStreamEventType) ToProto() logspb.LogStreamEventType {
	switch t {
	case StreamEventSourceAdded:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_SOURCE_ADDED
	case StreamEventSourceRemoved:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_SOURCE_REMOVED
	case StreamEventStreamError:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_STREAM_ERROR
	case StreamEventReconnecting:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_RECONNECTING
	case StreamEventReconnected:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_RECONNECTED
	case StreamEventStreamEnded:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_STREAM_ENDED
	case StreamEventSessionReady:
		return logspb.LogStreamEventType_LOG_STREAM_EVENT_SESSION_READY
	}
	return logspb.LogStreamEventType_LOG_STREAM_EVENT_SOURCE_ADDED
}

func LogStreamEventTypeFromProto(p logspb.LogStreamEventType) LogStreamEventType {
	switch p {
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_SOURCE_ADDED:
		return StreamEventSourceAdded
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_SOURCE_REMOVED:
		return StreamEventSourceRemoved
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_STREAM_ERROR:
		return StreamEventStreamError
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_RECONNECTING:
		return StreamEventReconnecting
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_RECONNECTED:
		return StreamEventReconnected
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_STREAM_ENDED:
		return StreamEventStreamEnded
	case logspb.LogStreamEventType_LOG_STREAM_EVENT_SESSION_READY:
		return StreamEventSessionReady
	}
	return StreamEventSourceAdded
}

// LogStreamEvent represents a lifecycle event during streaming.
type LogStreamEvent struct {
	Type      LogStreamEventType `json:"type"`
	SourceID  string             `json:"source_id"`
	Message   string             `json:"message"`
	Timestamp time.Time          `json:"timestamp"`
}

func (e *LogStreamEvent) ToProto() *logspb.LogStreamEvent {
	return &logspb.LogStreamEvent{
		Type:      e.Type.ToProto(),
		SourceId:  e.SourceID,
		Message:   e.Message,
		Timestamp: timestamppb.New(e.Timestamp),
	}
}

func LogStreamEventFromProto(p *logspb.LogStreamEvent) LogStreamEvent {
	var ts time.Time
	if p.GetTimestamp() != nil {
		ts = p.GetTimestamp().AsTime()
	}
	return LogStreamEvent{
		Type:      LogStreamEventTypeFromProto(p.GetType()),
		SourceID:  p.GetSourceId(),
		Message:   p.GetMessage(),
		Timestamp: ts,
	}
}

// LogStreamCommand represents a command from the client to control the stream.
type LogStreamCommand int

const (
	StreamCommandPause LogStreamCommand = iota
	StreamCommandResume
	StreamCommandClose
)

func (c LogStreamCommand) ToProto() logspb.LogStreamCommand {
	switch c {
	case StreamCommandPause:
		return logspb.LogStreamCommand_LOG_STREAM_COMMAND_PAUSE
	case StreamCommandResume:
		return logspb.LogStreamCommand_LOG_STREAM_COMMAND_RESUME
	case StreamCommandClose:
		return logspb.LogStreamCommand_LOG_STREAM_COMMAND_CLOSE
	}
	return logspb.LogStreamCommand_LOG_STREAM_COMMAND_PAUSE
}

func LogStreamCommandFromProto(p logspb.LogStreamCommand) LogStreamCommand {
	switch p {
	case logspb.LogStreamCommand_LOG_STREAM_COMMAND_PAUSE:
		return StreamCommandPause
	case logspb.LogStreamCommand_LOG_STREAM_COMMAND_RESUME:
		return StreamCommandResume
	case logspb.LogStreamCommand_LOG_STREAM_COMMAND_CLOSE:
		return StreamCommandClose
	}
	return StreamCommandPause
}

// StreamInput is a control message from the client to the stream.
type StreamInput struct {
	SessionID string           `json:"session_id"`
	Command   LogStreamCommand `json:"command"`
}

func (i *StreamInput) ToProto() *logspb.LogStreamInput {
	return &logspb.LogStreamInput{
		SessionId: i.SessionID,
		Command:   i.Command.ToProto(),
	}
}

func StreamInputFromProto(p *logspb.LogStreamInput) StreamInput {
	return StreamInput{
		SessionID: p.GetSessionId(),
		Command:   LogStreamCommandFromProto(p.GetCommand()),
	}
}

// StreamOutput is a log line or event from the stream.
type StreamOutput struct {
	SessionID string
	Line      *LogLine
	Event     *LogStreamEvent
}

// LogStreamRequest is the generic request to open a log stream for one source.
type LogStreamRequest struct {
	SourceID          string
	Labels            map[string]string
	ResourceData      map[string]interface{}
	Target            string
	Follow            bool
	IncludePrevious   bool
	IncludeTimestamps bool
	TailLines         int64
	SinceSeconds      int64
	SinceTime         *time.Time
	LimitBytes        int64
	Params            map[string]string
}
