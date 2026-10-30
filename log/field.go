package log

import "time"

// Field is a structured key/value pair attached to log records.
type Field struct {
	Key   string
	Value any
}

// String attaches a string value without formatting it.
func String(key, value string) Field { return Field{Key: key, Value: value} }

// Int attaches a native-width integer value.
func Int(key string, value int) Field { return Field{Key: key, Value: value} }

// Int64 attaches a signed 64-bit integer value.
func Int64(key string, value int64) Field { return Field{Key: key, Value: value} }

// Bool attaches a boolean value.
func Bool(key string, value bool) Field { return Field{Key: key, Value: value} }

// Duration attaches a duration without converting it to a string.
func Duration(key string, value time.Duration) Field { return Field{Key: key, Value: value} }

const errorFieldKey = "error"

// Error attaches err under the reserved error field key without converting it.
func Error(err error) Field { return Field{Key: errorFieldKey, Value: err} }

// Any attaches value without copying or converting it.
func Any(key string, value any) Field { return Field{Key: key, Value: value} }
