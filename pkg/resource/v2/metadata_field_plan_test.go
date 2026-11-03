package resourcev2

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Keep the pre-plan traversal as an independent field-selection oracle.
func legacyMetadataStructFields(b *metadataBudget, v reflect.Value, depth int) error {
	for i := range v.NumField() {
		field := v.Type().Field(i)
		if field.PkgPath != "" || field.Tag.Get("json") == "-" {
			continue
		}
		if b.jsonOutput && strings.Contains(field.Tag.Get("json"), ",omitempty") && emptyJSONField(v.Field(i)) {
			continue
		}
		if err := b.visit(v.Field(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func TestMetadataFieldPlanBudgetParity(t *testing.T) {
	empty := ""
	fixtures := []any{
		SchemaBinding{}, SchemaBinding{Revision: &empty},
		Coverage{Kind: "partial", Paths: []string{"/a", "/b"}, Reason: "bounded"},
		ActionEvent{Data: &Value{Kind: "null", List: []Value{{Kind: "null"}}}},
		ResourceHealth{}, ConnectionMetadataSnapshot{},
	}
	for _, fixture := range fixtures {
		v := reflect.ValueOf(fixture)
		for _, jsonOutput := range []bool{false, true} {
			for _, depthLimit := range []int{0, 1, 2, maxDomainDepth} {
				for _, nodeLimit := range []int{1, 2, 3, 10, maxDomainNodes} {
					for _, bytes := range []int{0, MaxMessageBytes - 1, MaxMessageBytes} {
						got := metadataBudget{depthLimit: depthLimit, nodeLimit: nodeLimit, bytes: bytes, jsonOutput: jsonOutput}
						want := got
						want.nodes = 1
						gotErr := got.visit(v, 0)
						wantErr := legacyMetadataStructFields(&want, v, 0)
						if bytes == 0 {
							err := metadataAdmission(fixture, depthLimit, nodeLimit, jsonOutput)
							if fmt.Sprint(err) != fmt.Sprint(wantErr) {
								t.Fatalf("%v admission: got %v; want %v", v.Type(), err, wantErr)
							}
						}
						if got != want || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
							t.Fatalf("%v json=%v depth=%d nodes=%d bytes=%d: got %+v / %v; want %+v / %v", v.Type(), jsonOutput, depthLimit, nodeLimit, bytes, got, gotErr, want, wantErr)
						}
					}
				}
			}
		}
	}
}

func TestMetadataCallerTypeVisibilityAndOmit(t *testing.T) {
	type metadata struct {
		Visible string  `json:"visible"`
		Omitted string  `json:"omitted,omitempty"`
		Secret  float64 `json:"-"`
		hidden  float64
	}
	v := metadata{Visible: "x", Secret: math.NaN(), hidden: math.NaN()}
	for _, jsonOutput := range []bool{false, true} {
		b := metadataBudget{depthLimit: 1, nodeLimit: 3, jsonOutput: jsonOutput}
		if err := b.visit(reflect.ValueOf(v), 0); err != nil {
			t.Fatal(err)
		}
		wantNodes, wantBytes := 3, 1
		if jsonOutput {
			wantNodes, wantBytes = 2, 3
		}
		if b.nodes != wantNodes || b.bytes != wantBytes {
			t.Fatalf("json=%v: got %+v", jsonOutput, b)
		}
		if err := metadataAdmission(v, 1, wantNodes-1, jsonOutput); ErrorCode(err) != "RESOURCE_LIMIT" {
			t.Fatalf("node boundary json=%v: %v", jsonOutput, err)
		}
		if err := metadataAdmission(v, 0, wantNodes, jsonOutput); ErrorCode(err) != "RESOURCE_LIMIT" {
			t.Fatalf("depth boundary json=%v: %v", jsonOutput, err)
		}
	}
	v.Visible = string([]byte{0xff})
	if err := ValidateMetadata(v); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("visible invalid UTF-8 admitted: %v", err)
	}
}

func TestMetadataFieldPlanTimestampAndInactiveValue(t *testing.T) {
	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	v := ConnectionMetadataEntry{ObservedAt: &invalidTime}
	if err := ValidateMetadata(v); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("invalid nested timestamp admitted: %v", err)
	}
	if err := ValidateMetadata(ActionEvent{Data: &Value{Kind: "null", String: string([]byte{0xff})}}); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("inactive Value payload admitted: %v", err)
	}
}

func BenchmarkMetadataFieldPlanLookup(b *testing.B) {
	v := reflect.ValueOf(ResourceType{Type: "example", DisplayName: "Example"})
	for _, planned := range []bool{false, true} {
		b.Run(fmt.Sprintf("planned=%v", planned), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				budget := metadataBudget{depthLimit: maxDomainDepth, nodeLimit: maxDomainNodes, jsonOutput: true}
				var err error
				if planned {
					err = budget.structFields(v, 0)
				} else {
					err = legacyMetadataStructFields(&budget, v, 0)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
