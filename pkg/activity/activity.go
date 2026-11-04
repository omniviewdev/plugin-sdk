// Package activity reports process-owned tasks, observations, and messages to the host.
package activity

import (
	"context"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type Presentation = resourcev2.ActivityPresentation

type Source struct {
	PluginID     string                               `json:"pluginID,omitempty"`
	ConnectionID string                               `json:"connectionID,omitempty"`
	Resource     *resourcev2.ResourceNavigationIntent `json:"resource,omitempty"`
}

type Task struct {
	State         string `json:"state"`
	Cancellation  string `json:"cancellation"`
	UnknownReason string `json:"unknownReason,omitempty"`
}

type Alert struct {
	Key            string `json:"key"`
	State          string `json:"state"`
	Occurrences    uint64 `json:"occurrences"`
	AcknowledgedAt string `json:"acknowledgedAt,omitempty"`
	SnoozedUntil   string `json:"snoozedUntil,omitempty"`
}

type Visibility struct {
	Collapsed      bool `json:"collapsed"`
	ToastDismissed bool `json:"toastDismissed"`
	Read           bool `json:"read"`
}

type Record struct {
	ID           string       `json:"id"`
	Revision     uint64       `json:"revision"`
	Source       Source       `json:"source"`
	Kind         string       `json:"kind"`
	Severity     string       `json:"severity"`
	CreatedAt    string       `json:"createdAt"`
	UpdatedAt    string       `json:"updatedAt"`
	ObservedAt   string       `json:"observedAt,omitempty"`
	ParentID     string       `json:"parentId,omitempty"`
	Presentation Presentation `json:"presentation"`
	Task         *Task        `json:"task,omitempty"`
	Alert        *Alert       `json:"alert,omitempty"`
	Visibility   Visibility   `json:"visibility"`
}

type Snapshot struct {
	Revision uint64   `json:"revision"`
	Records  []Record `json:"records"`
}

type Change struct {
	Revision   uint64   `json:"revision"`
	Record     *Record  `json:"record,omitempty"`
	RemovedIDs []string `json:"removedIds,omitempty"`
}

type Subscription struct {
	ID       string   `json:"id"`
	Snapshot Snapshot `json:"snapshot"`
}

type Spec struct {
	Presentation Presentation `json:"presentation"`
	Source       *Source      `json:"source,omitempty"`
	Severity     string       `json:"severity,omitempty"`
	ParentID     string       `json:"parentId,omitempty"`
}

type BeginTaskRequest = Spec

type ReportTaskRequest struct {
	ID           string       `json:"id"`
	Presentation Presentation `json:"presentation"`
	State        string       `json:"state,omitempty"`
	ObservedAt   string       `json:"observedAt,omitempty"`
}

type FinishTaskRequest struct {
	ID           string        `json:"id"`
	State        string        `json:"state"`
	Presentation *Presentation `json:"presentation,omitempty"`
}

type AlertRequest struct {
	Spec
	Key        string `json:"key"`
	ObservedAt string `json:"observedAt"`
}

type ResolveAlertRequest struct {
	Key        string `json:"key"`
	ObservedAt string `json:"observedAt"`
}

type SetPresentationRequest struct {
	ID             string  `json:"id"`
	Collapsed      *bool   `json:"collapsed,omitempty"`
	ToastDismissed *bool   `json:"toastDismissed,omitempty"`
	Read           *bool   `json:"read,omitempty"`
	SnoozedUntil   *string `json:"snoozedUntil,omitempty"`
	Acknowledged   *bool   `json:"acknowledged,omitempty"`
}

type StartResourceActionRequest struct {
	PluginID     string                   `json:"pluginID"`
	Request      resourcev2.ActionRequest `json:"request"`
	Presentation Presentation             `json:"presentation"`
	ParentID     string                   `json:"parentId,omitempty"`
}

// Reporter reports observations only. The host supplies authenticated process provenance.
type Reporter interface {
	BeginTask(context.Context, Spec) (Record, error)
	ReportTask(context.Context, ReportTaskRequest) (Record, error)
	FinishTask(context.Context, FinishTaskRequest) (Record, error)
	PublishMessage(context.Context, Spec) (Record, error)
	UpsertAlert(context.Context, AlertRequest) (Record, error)
	ResolveAlert(context.Context, ResolveAlertRequest) (Record, error)
}
