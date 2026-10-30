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

// ToMap copies fields into a map, then overwrites reserved keys with record metadata.
// Field values are shared with the record.
func (r Record) ToMap() map[string]any {
	const metadataFieldCount = 4
	out := make(map[string]any, len(r.Fields)+metadataFieldCount)
	for _, f := range r.Fields {
		out[f.Key] = f.Value
	}
	// Write canonical metadata AFTER fields so they cannot be overwritten.
	out["timestamp"] = r.Timestamp
	out["level"] = r.Level.String()
	out["logger"] = r.Logger
	out["message"] = r.Message
	return out
}
