package resourcev2

import (
	"testing"

	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

func TestRelationshipAdapterChecksTargetCountBeforeConversion(t *testing.T) {
	// Neither source nor individual targets are materializable. Cardinality must
	// win before their conversion or allocating the handwritten target slice.
	input := &resourcepb.RelationshipResolutionInput{Targets: make([]*resourcepb.ReadTarget, MaxRelationshipTargets+1)}
	if _, err := FromProtoRelationshipResolutionRequest(input); ErrorCode(err) != "RESOURCE_LIMIT" {
		t.Fatalf("adapter missed preallocation target guard: %v", err)
	}
}
