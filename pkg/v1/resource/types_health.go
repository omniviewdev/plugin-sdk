package resource

import (
	"context"
	"encoding/json"
	"time"
)

// ============================================================================
// Health Types (doc 21 — proto/v1/resource/health.proto)
// ============================================================================

// HealthStatus represents the normalized health of a resource.
type HealthStatus string

const (
	HealthHealthy   HealthStatus = "healthy"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnhealthy HealthStatus = "unhealthy"
	HealthPending   HealthStatus = "pending"
	HealthUnknown   HealthStatus = "unknown"
)

// EventSeverity classifies diagnostic events.
type EventSeverity string

const (
	SeverityNormal  EventSeverity = "normal"
	SeverityWarning EventSeverity = "warning"
	SeverityError   EventSeverity = "error"
)

// HealthCondition represents a single condition (Ready, Scheduled, etc.).
type HealthCondition struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastProbeTime      *time.Time `json:"lastProbeTime,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// ResourceHealth is a normalized health assessment for a resource.
type ResourceHealth struct {
	Status     HealthStatus      `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
	Conditions []HealthCondition `json:"conditions,omitempty"`
}

// ResourceEvent is a diagnostic event associated with a resource.
type ResourceEvent struct {
	Type      EventSeverity `json:"type"`
	Reason    string        `json:"reason"`
	Message   string        `json:"message"`
	Source    string        `json:"source,omitempty"`
	Count     int32         `json:"count,omitempty"`
	FirstSeen time.Time     `json:"firstSeen"`
	LastSeen  time.Time     `json:"lastSeen"`
}

// ============================================================================
// Optional Resourcer Interfaces — Health
// ============================================================================

// HealthAssessor can be implemented by a Resourcer to assess resource health
// from raw resource data.
type HealthAssessor[ClientT any] interface {
	AssessHealth(ctx context.Context, client *ClientT, meta ResourceMeta, data json.RawMessage) (*ResourceHealth, error)
}

// EventRetriever can be implemented by a Resourcer to retrieve diagnostic
// events for a resource instance.
type EventRetriever[ClientT any] interface {
	GetEvents(ctx context.Context, client *ClientT, meta ResourceMeta, id string, namespace string, limit int32) ([]ResourceEvent, error)
}
