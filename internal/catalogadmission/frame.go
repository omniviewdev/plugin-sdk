package catalogadmission

import (
	"context"
	"encoding/binary"
	"unicode/utf8"
)

// Marshal encodes the SDK-private version/count/length frame after admission.
func Marshal(ctx context.Context, defaultRevision string, limits Limits) ([]byte, *Violation) {
	size, err := EncodedSize(ctx, defaultRevision, limits)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	entries := Entries(ctx)
	frame := make([]byte, size)
	frame[0] = 1
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(entries)))
	offset := 3
	for _, entry := range entries {
		for _, field := range [3]string{entry.Provider, entry.Connection, entry.Revision} {
			binary.BigEndian.PutUint16(frame[offset:offset+2], uint16(len(field)))
			offset += 2
			copy(frame[offset:], field)
			offset += len(field)
		}
	}
	return frame, nil
}

// Unmarshal admits a complete frame without inheriting previous observations.
// Repeated entries preserve duplicate evidence until rejection; trailing bytes fail.
func Unmarshal(ctx context.Context, frame []byte, defaultRevision string, limits Limits) (context.Context, *Violation) {
	invalid := func() (context.Context, *Violation) {
		return nil, &Violation{"RESET_REQUIRED", "Malformed qualified catalog revision metadata."}
	}
	if len(frame)+len(defaultRevision)+256 > limits.MetadataBytes {
		return nil, &Violation{"RESOURCE_LIMIT", "Catalog revision metadata exceeds bound."}
	}
	if len(frame) < 3 || frame[0] != 1 {
		return invalid()
	}
	count := int(binary.BigEndian.Uint16(frame[1:3]))
	if count > limits.Observations {
		return nil, &Violation{"RESOURCE_LIMIT", "Too many route catalog observations."}
	}
	if count == 0 {
		return invalid()
	}
	entries := make([]Observation, 0, count)
	offset := 3
	for range count {
		fields := [3]string{}
		for j := range fields {
			if len(frame)-offset < 2 {
				return invalid()
			}
			size := int(binary.BigEndian.Uint16(frame[offset : offset+2]))
			offset += 2
			if size == 0 || size > limits.RevisionBytes || size > len(frame)-offset || !utf8.Valid(frame[offset:offset+size]) {
				return invalid()
			}
			fields[j] = string(frame[offset : offset+size])
			offset += size
		}
		entry := Observation{Provider: fields[0], Connection: fields[1], Revision: fields[2]}
		for _, previous := range entries {
			if previous.Provider == entry.Provider && previous.Connection == entry.Connection {
				return invalid()
			}
		}
		entries = append(entries, entry)
	}
	if offset != len(frame) {
		return invalid()
	}
	return context.WithValue(ctx, routesKey{}, entries), nil
}
