package catalogadmission

import (
	"context"
	"encoding/binary"
	"math"
	"unicode/utf8"
)

const (
	frameVersion = 1
	frameHeaderBytes = 3
	frameLengthBytes = 2
)

// Marshal encodes the SDK-private version/count/length frame after admission.
// Counts and field lengths must also fit uint16, even when supplied limits are wider.
func Marshal(ctx context.Context, defaultRevision string, limits Limits) ([]byte, *ViolationError) {
	size, err := EncodedSize(ctx, defaultRevision, limits)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	entries := Entries(ctx)
	if len(entries) > math.MaxUint16 {
		return nil, &ViolationError{resourceLimit, "Too many route catalog observations."}
	}
	frame := make([]byte, size)
	frame[0] = 1
	binary.BigEndian.PutUint16(frame[1:3], uint16(len(entries)))
	offset := 3
	for _, entry := range entries {
		for _, field := range [3]string{entry.Provider, entry.Connection, entry.Revision} {
			if len(field) > math.MaxUint16 {
				return nil, &ViolationError{resourceLimit, "Catalog observation exceeds frame field bound."}
			}
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
func Unmarshal(ctx context.Context, frame []byte, defaultRevision string, limits Limits) (context.Context, *ViolationError) {
	if len(frame)+len(defaultRevision)+256 > limits.MetadataBytes {
		return nil, &ViolationError{resourceLimit, "Catalog revision metadata exceeds bound."}
	}
	if len(frame) < frameHeaderBytes || frame[0] != frameVersion {
		return nil, malformedFrame()
	}
	count := int(binary.BigEndian.Uint16(frame[1:frameHeaderBytes]))
	if count > limits.Observations {
		return nil, &ViolationError{resourceLimit, "Too many route catalog observations."}
	}
	if count == 0 {
		return nil, malformedFrame()
	}
	entries := make([]Observation, 0, count)
	offset := frameHeaderBytes
	for range count {
		entry, next, err := decodeObservation(frame, offset, limits.RevisionBytes)
		if err != nil {
			return nil, err
		}
		offset = next
		for _, previous := range entries {
			if previous.Provider == entry.Provider && previous.Connection == entry.Connection {
				return nil, malformedFrame()
			}
		}
		entries = append(entries, entry)
	}
	if offset != len(frame) {
		return nil, malformedFrame()
	}
	return context.WithValue(ctx, routesKey{}, entries), nil
}

func malformedFrame() *ViolationError {
	return &ViolationError{resetRequired, "Malformed qualified catalog revision metadata."}
}

// decodeObservation owns field decoding and advances only across complete UTF-8 fields.
func decodeObservation(frame []byte, offset, maxBytes int) (Observation, int, *ViolationError) {
	fields := [3]string{}
	for i := range fields {
		if len(frame)-offset < frameLengthBytes {
			return Observation{}, offset, malformedFrame()
		}
		size := int(binary.BigEndian.Uint16(frame[offset : offset+frameLengthBytes]))
		offset += frameLengthBytes
		if size == 0 || size > maxBytes || size > len(frame)-offset ||
			!utf8.Valid(frame[offset:offset+size]) {
			return Observation{}, offset, malformedFrame()
		}
		fields[i] = string(frame[offset : offset+size])
		offset += size
	}
	return Observation{Provider: fields[0], Connection: fields[1], Revision: fields[2]}, offset, nil
}
