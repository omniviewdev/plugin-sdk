package logs

import (
	"testing"
	"time"

	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// LV-001: LogLevel.ToProto() round-trips all 7 values
func TestLogLevelToProtoRoundTrip(t *testing.T) {
	levels := []struct {
		go_   LogLevel
		proto logspb.LogLevel
	}{
		{LogLevelUnspecified, logspb.LogLevel_LOG_LEVEL_UNSPECIFIED},
		{LogLevelTrace, logspb.LogLevel_LOG_LEVEL_TRACE},
		{LogLevelDebug, logspb.LogLevel_LOG_LEVEL_DEBUG},
		{LogLevelInfo, logspb.LogLevel_LOG_LEVEL_INFO},
		{LogLevelWarn, logspb.LogLevel_LOG_LEVEL_WARN},
		{LogLevelError, logspb.LogLevel_LOG_LEVEL_ERROR},
		{LogLevelFatal, logspb.LogLevel_LOG_LEVEL_FATAL},
	}

	for _, tc := range levels {
		got := tc.go_.ToProto()
		if got != tc.proto {
			t.Errorf("LogLevel(%d).ToProto() = %v, want %v", tc.go_, got, tc.proto)
		}
		back := LogLevelFromProto(got)
		if back != tc.go_ {
			t.Errorf("LogLevelFromProto(%v) = %d, want %d", got, back, tc.go_)
		}
	}
}

// LV-002: LogLevel.FromProto() maps unknown values to UNSPECIFIED
func TestLogLevelFromProtoUnknown(t *testing.T) {
	got := LogLevelFromProto(logspb.LogLevel(99))
	if got != LogLevelUnspecified {
		t.Errorf("LogLevelFromProto(99) = %d, want LogLevelUnspecified", got)
	}
}

// LV-003: LogLine.ToProto() includes Level field
func TestLogLineToProtoIncludesLevel(t *testing.T) {
	line := LogLine{
		SessionID: "sess-1",
		SourceID:  "src-1",
		Labels:    map[string]string{"k": "v"},
		Timestamp: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Content:   "hello",
		Origin:    LogLineOriginCurrent,
		Level:     LogLevelError,
	}

	p := line.ToProto()
	if p.GetLevel() != logspb.LogLevel_LOG_LEVEL_ERROR {
		t.Errorf("ToProto().Level = %v, want ERROR", p.GetLevel())
	}
}

// LV-004: LogLine.FromProto() with level=0 → UNSPECIFIED
func TestLogLineFromProtoLevelZero(t *testing.T) {
	p := &logspb.LogLine{
		SessionId: "sess-1",
		SourceId:  "src-1",
		Content:   []byte("hello"),
		Level:     logspb.LogLevel_LOG_LEVEL_UNSPECIFIED,
	}

	line := LogLineFromProto(p)
	if line.Level != LogLevelUnspecified {
		t.Errorf("FromProto level=0 → %d, want LogLevelUnspecified", line.Level)
	}
}

// LV-005: LogLine.ToProto() preserves all existing fields unchanged
func TestLogLineToProtoPreservesFields(t *testing.T) {
	ts := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	line := LogLine{
		SessionID: "sess-abc",
		SourceID:  "pod-1/container-a",
		Labels:    map[string]string{"pod": "pod-1", "container": "a"},
		Timestamp: ts,
		Content:   "log message content",
		Origin:    LogLineOriginPrevious,
		Level:     LogLevelInfo,
	}

	p := line.ToProto()

	if p.GetSessionId() != "sess-abc" {
		t.Errorf("SessionId = %q", p.GetSessionId())
	}
	if p.GetSourceId() != "pod-1/container-a" {
		t.Errorf("SourceId = %q", p.GetSourceId())
	}
	if string(p.GetContent()) != "log message content" {
		t.Errorf("Content = %q", string(p.GetContent()))
	}
	if p.GetOrigin() != logspb.LogLineOrigin_LOG_LINE_ORIGIN_PREVIOUS {
		t.Errorf("Origin = %v", p.GetOrigin())
	}
	if p.GetLevel() != logspb.LogLevel_LOG_LEVEL_INFO {
		t.Errorf("Level = %v", p.GetLevel())
	}

	// Round-trip
	back := LogLineFromProto(p)
	if back.SessionID != line.SessionID {
		t.Errorf("round-trip SessionID = %q", back.SessionID)
	}
	if back.Level != LogLevelInfo {
		t.Errorf("round-trip Level = %d", back.Level)
	}
}

// LV-006: StreamEventSessionReady round-trips
func TestStreamEventSessionReadyRoundTrip(t *testing.T) {
	p := StreamEventSessionReady.ToProto()
	if p != logspb.LogStreamEventType_LOG_STREAM_EVENT_SESSION_READY {
		t.Errorf("ToProto() = %v, want SESSION_READY", p)
	}
	back := LogStreamEventTypeFromProto(p)
	if back != StreamEventSessionReady {
		t.Errorf("FromProto() = %d, want StreamEventSessionReady", back)
	}
}

func BenchmarkLogLineToProto(b *testing.B) {
	line := LogLine{
		SessionID: "sess-bench",
		SourceID:  "pod-1/container-a",
		Labels:    map[string]string{"pod": "pod-1", "container": "a"},
		Timestamp: time.Now(),
		Content:   "2025-01-15T10:30:00.000Z [INFO] this is a benchmark log line with some typical content",
		Origin:    LogLineOriginCurrent,
		Level:     LogLevelInfo,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = line.ToProto()
	}
}

func BenchmarkLogLineFromProto(b *testing.B) {
	line := LogLine{
		SessionID: "sess-bench",
		SourceID:  "pod-1/container-a",
		Labels:    map[string]string{"pod": "pod-1", "container": "a"},
		Timestamp: time.Now(),
		Content:   "2025-01-15T10:30:00.000Z [INFO] this is a benchmark log line with some typical content",
		Origin:    LogLineOriginCurrent,
		Level:     LogLevelInfo,
	}
	p := line.ToProto()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = LogLineFromProto(p)
	}
}
