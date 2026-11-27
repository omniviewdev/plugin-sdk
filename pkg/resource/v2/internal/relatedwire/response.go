// Package relatedwire shares related-read response encoding between registration and transport.
package relatedwire

import (
	json "encoding/json/v2"
	"io"

	"google.golang.org/protobuf/encoding/protowire"
)

const MetadataBytes = 16 << 10

type Discovery struct {
	Sources []string `json:"sources"`
}

// WriteJSON writes only the JSON payload, charging its enclosing BytesValue to the frame budget.
func WriteJSON(dst io.Writer, value any, limit int) error {
	return json.MarshalWrite(&frameWriter{dst: dst, limit: limit}, value)
}

type frameWriter struct {
	dst         io.Writer
	limit, size int
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.size || 1+protowire.SizeBytes(w.size+len(p)) > w.limit {
		return 0, io.ErrShortBuffer
	}
	n, err := w.dst.Write(p)
	w.size += n
	return n, err
}
