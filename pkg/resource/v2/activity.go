package resourcev2

// ActivityPresentation is safe, data-only presentation independent of business output.
type ActivityPresentation struct {
	Title    string            `json:"title"`
	Message  string            `json:"message,omitempty"`
	Details  string            `json:"details,omitempty"`
	Progress *ActivityProgress `json:"progress,omitempty"`
	Metrics  []ActivityMetric  `json:"metrics,omitempty"`
	Actions  []ActivityAction  `json:"actions,omitempty"`
}

type ActivityProgress struct {
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	Current string `json:"current,omitempty"`
	Total   string `json:"total,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Label   string `json:"label,omitempty"`
}

type ActivityMetric struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit,omitempty"`
	Tone  string `json:"tone,omitempty"`
}

type ResourceNavigationIntent struct {
	PluginID       string            `json:"pluginID"`
	TargetID       string            `json:"targetId"`
	Route          AccessRoute       `json:"route"`
	Reference      ResourceReference `json:"reference"`
	Representation string            `json:"representation"`
}

type ResourceActionIntent struct {
	PluginID string        `json:"pluginID"`
	Request  ActionRequest `json:"request"`
}

type ActivityAction struct {
	ID     string                    `json:"id"`
	Kind   string                    `json:"kind"`
	Label  string                    `json:"label"`
	Target *ResourceNavigationIntent `json:"target,omitempty"`
	Intent *ResourceActionIntent     `json:"intent,omitempty"`
	TaskID string                    `json:"taskId,omitempty"`
}
