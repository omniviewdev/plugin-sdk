package log

import "time"

// Record is the normalized representation of a log entry.
type Record struct {
	Timestamp time.Time
	Level     Level
	Logger    string
	Message   string
	Fields    []Field
}

func (r Record) ToMap() map[string]any {
	out := make(map[string]any, len(r.Fields)+4)
	out["timestamp"] = r.Timestamp
	out["level"] = r.Level.String()
	out["logger"] = r.Logger
	out["message"] = r.Message
	for _, f := range r.Fields {
		out[f.Key] = f.Value
	}
	return out
}
