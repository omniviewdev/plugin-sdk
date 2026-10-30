package catalogadmission

import (
	"context"
	"math"
	"strings"
	"testing"
)

// Domain limits normally sit below these bounds. Wider caller-supplied limits
// must still never truncate the private frame's uint16 count or field lengths.
func TestMarshalRejectsUnrepresentableLengths(t *testing.T) {
	limits := Limits{
		RevisionBytes: math.MaxUint16 + 1,
		MetadataBytes: 1 << 20,
		Observations:  math.MaxUint16 + 1,
	}
	t.Run("count", func(t *testing.T) {
		entries := make([]Observation, math.MaxUint16+1)
		ctx := context.WithValue(context.Background(), routesKey{}, entries)
		size, sizeErr := EncodedSize(ctx, "", limits)
		if sizeErr == nil || sizeErr.Code != resourceLimit || size != 0 {
			t.Fatalf("unrepresentable count: encoded size %d, error %v", size, sizeErr)
		}
		frame, err := Marshal(ctx, "", limits)
		if err == nil || err.Code != resourceLimit || frame != nil {
			t.Fatalf("unrepresentable count: frame length %d, error %v", len(frame), err)
		}
	})
	for _, field := range []string{"provider", "connection", "revision"} {
		t.Run(field, func(t *testing.T) {
			t.Run("exact-max", func(t *testing.T) {
				checkFrameFieldBoundary(t, field, math.MaxUint16, limits)
			})
			t.Run("overflow", func(t *testing.T) {
				checkFrameFieldBoundary(t, field, math.MaxUint16+1, limits)
			})
		})
	}
}

func checkFrameFieldBoundary(t *testing.T, field string, width int, limits Limits) {
	t.Helper()
	entry := Observation{Provider: "provider", Connection: "connection", Revision: "revision"}
	value := strings.Repeat("x", width)
	switch field {
	case "provider":
		entry.Provider = value
	case "connection":
		entry.Connection = value
	case "revision":
		entry.Revision = value
	default:
		t.Fatalf("unknown field %q", field)
	}
	ctx := Append(context.Background(), entry)
	size, sizeErr := EncodedSize(ctx, "", limits)
	frame, marshalErr := Marshal(ctx, "", limits)
	if width > math.MaxUint16 {
		if sizeErr == nil || sizeErr.Code != resourceLimit || size != 0 {
			t.Fatalf("unrepresentable %s: encoded size %d, error %v", field, size, sizeErr)
		}
		if marshalErr == nil || marshalErr.Code != resourceLimit || frame != nil {
			t.Fatalf("unrepresentable %s: frame length %d, error %v", field, len(frame), marshalErr)
		}
		return
	}
	if sizeErr != nil || marshalErr != nil {
		t.Fatalf("representable %s rejected: size error %v, marshal error %v", field, sizeErr, marshalErr)
	}
	wantSize := frameHeaderBytes + 3*frameLengthBytes + len(
		entry.Provider,
	) + len(
		entry.Connection,
	) + len(
		entry.Revision,
	)
	if size != wantSize || len(frame) != wantSize {
		t.Fatalf("representable %s: size %d, frame length %d, want %d", field, size, len(frame), wantSize)
	}
	decoded, decodeErr := Unmarshal(context.Background(), frame, "", limits)
	if decodeErr != nil {
		t.Fatalf("representable %s failed decoding: %v", field, decodeErr)
	}
	entries := Entries(decoded)
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("representable %s changed after framing", field)
	}
}
