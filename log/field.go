package log

import "time"

// Field is a structured key/value pair attached to log records.
type Field struct {
	Key   string
	Value any
}

func String(key, value string) Field                 { return Field{Key: key, Value: value} }
func Int(key string, value int) Field                { return Field{Key: key, Value: value} }
func Int64(key string, value int64) Field            { return Field{Key: key, Value: value} }
func Bool(key string, value bool) Field              { return Field{Key: key, Value: value} }
func Duration(key string, value time.Duration) Field { return Field{Key: key, Value: value} }
func Error(err error) Field                          { return Field{Key: "error", Value: err} }
func Any(key string, value any) Field                { return Field{Key: key, Value: value} }
