package resourcev2

import (
	"reflect"
	"testing"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func overDepthProtoValue() *resourcepb.Value {
	p := &resourcepb.Value{Selection: &resourcepb.Value_BoolValue{BoolValue: true}}
	for range maxDomainDepth + 1 {
		p = &resourcepb.Value{Selection: &resourcepb.Value_ListValue{ListValue: &resourcepb.ListValue{Values: []*resourcepb.Value{p}}}}
	}
	return p
}

func TestMapDecoderEntryAndChildErrorPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []*resourcepb.ValueEntry
		code    string
	}{
		{"child limit", []*resourcepb.ValueEntry{{Key: "", Value: overDepthProtoValue()}}, "RESOURCE_LIMIT"},
		{"duplicate before child limit", []*resourcepb.ValueEntry{{Key: "", Value: overDepthProtoValue()}, {Key: ""}}, "INVALID_REQUEST"},
		{"nil before child limit", []*resourcepb.ValueEntry{{Key: "", Value: overDepthProtoValue()}, nil}, "INVALID_REQUEST"},
		{"first child invalid", []*resourcepb.ValueEntry{{Key: "", Value: &resourcepb.Value{}}, {Key: "next", Value: overDepthProtoValue()}}, "INVALID_REQUEST"},
		{"first child limit", []*resourcepb.ValueEntry{{Key: "", Value: overDepthProtoValue()}, {Key: "next", Value: &resourcepb.Value{}}}, "RESOURCE_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &resourcepb.Value{Selection: &resourcepb.Value_MapValue{MapValue: &resourcepb.MapValue{Entries: tc.entries}}}
			got, err := FromProtoValue(p)
			if ErrorCode(err) != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
			if !reflect.DeepEqual(got, Value{}) {
				t.Fatalf("failed decode returned nonzero value: %#v", got)
			}
		})
	}
}

func TestMapDecoderEntryFailureReplacesChildFailure(t *testing.T) {
	d := &adapterDecoder{}
	got := d.mapValue(&resourcepb.Value_MapValue{MapValue: &resourcepb.MapValue{
		Entries: []*resourcepb.ValueEntry{{Key: "", Value: overDepthProtoValue()}, nil},
	}}, 0)
	if ErrorCode(d.err) != "INVALID_REQUEST" {
		t.Fatalf("missing entry did not override child failure: %v", d.err)
	}
	if !reflect.DeepEqual(got, Value{}) {
		t.Fatalf("failed decode returned nonzero value: %#v", got)
	}
}

func TestMetadataDecoderRejectsMalformedLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []*resourcepb.LabelEntry
	}{
		{"duplicate", []*resourcepb.LabelEntry{{Key: ""}, {Key: ""}}},
		{"nil", []*resourcepb.LabelEntry{{Key: ""}, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &resourcepb.ResourceMetadata{Labels: tc.labels, CreatedAt: &resourcepb.TimestampValue{Nanos: 1}}
			got, err := fromProto(p, (*adapterDecoder).metadata, validateMetadata)
			if ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("expected INVALID_REQUEST, got %v", err)
			}
			if !reflect.DeepEqual(got, ResourceMetadata{}) {
				t.Fatalf("failed decode returned nonzero metadata: %#v", got)
			}
		})
	}
}

func TestMapDecoderStorageIsDetached(t *testing.T) {
	child := &resourcepb.Value{Selection: &resourcepb.Value_StringValue{StringValue: "original"}}
	entry := &resourcepb.ValueEntry{Key: "", Value: child}
	p := &resourcepb.Value{Selection: &resourcepb.Value_MapValue{MapValue: &resourcepb.MapValue{Entries: []*resourcepb.ValueEntry{entry}}}}
	got, err := FromProtoValue(p)
	if err != nil {
		t.Fatal(err)
	}
	entry.Key = "changed"
	child.Selection = &resourcepb.Value_StringValue{StringValue: "changed"}
	if got.Map[""].String != "original" || len(got.Map) != 1 {
		t.Fatalf("wire mutation changed decoded map: %#v", got)
	}
	got.Map[""] = Value{Kind: "string", String: "domain"}
	if child.GetStringValue() != "changed" {
		t.Fatal("domain mutation changed wire value")
	}
}

func TestMetadataDecoderStorageAndPresence(t *testing.T) {
	entry := &resourcepb.LabelEntry{Key: "", Value: "original"}
	p := &resourcepb.ResourceMetadata{Labels: []*resourcepb.LabelEntry{entry}, CreatedAt: &resourcepb.TimestampValue{Seconds: 1}}
	got, err := fromProto(p, (*adapterDecoder).metadata, validateMetadata)
	if err != nil {
		t.Fatal(err)
	}
	entry.Key, entry.Value = "changed", "changed"
	p.CreatedAt.Seconds = 2
	if got.Labels[""] != "original" || len(got.Labels) != 1 || got.CreatedAt == nil || got.CreatedAt.Unix() != 1 {
		t.Fatalf("wire mutation changed decoded metadata: %#v", got)
	}
	got.Labels[""] = "domain"
	if entry.Value != "changed" {
		t.Fatal("domain mutation changed wire label")
	}
	for _, absent := range []*resourcepb.ResourceMetadata{nil, {}} {
		d := &adapterDecoder{}
		metadata := d.metadata(absent)
		if d.err != nil || metadata.Labels == nil || len(metadata.Labels) != 0 || metadata.CreatedAt != nil {
			t.Fatalf("absent metadata presence changed: %#v, %v", metadata, d.err)
		}
	}
}
