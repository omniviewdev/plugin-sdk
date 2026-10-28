package resourcev2

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPresentationLinksRequireExplicitCanonicalTypeSelection(t *testing.T) {
	valid := ResourceDefinition{ColumnDefs: []ColumnDefinition{{ID: "owner", ResourceLink: &ResourceLink{IDAccessor: "metadata.ownerReferences[0].name", TypeAccessor: "metadata.ownerReferences[0].kind", TypeMap: map[string]string{"ReplicaSet": "apps/replicasets"}, ScopeAccessors: []ScopeExtractor{{Dimension: "namespace", FieldPath: "metadata.namespace"}}}}}}
	require.NoError(t, ValidateResourceDefinition(valid))
	conflict := valid
	conflict.ColumnDefs = append([]ColumnDefinition{}, valid.ColumnDefs...)
	link := *valid.ColumnDefs[0].ResourceLink
	conflict.ColumnDefs[0].ResourceLink = &link
	link.Type = "apps/replicasets"
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateResourceDefinition(conflict)))
	link.Type = ""
	link.TypeMap = nil
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateResourceDefinition(conflict)))
	link.TypeMap = map[string]string{"ReplicaSet": "apps/replicasets"}
	link.ScopeAccessors = append(link.ScopeAccessors, link.ScopeAccessors[0])
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateResourceDefinition(conflict)))
}
func TestPresentationRejectsDuplicateDescriptors(t *testing.T) {
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateResourceGroups([]ResourceGroup{{ID: "workloads"}, {ID: "workloads"}})))
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateResourceGroups([]ResourceGroup{{ID: "workloads", Resources: []string{"apps/deployments", "apps/deployments"}}})))
	action := ActionDescriptor{ID: "restart", Scope: ActionScopeInstance}
	require.NoError(t, ValidateActionDescriptors([]ActionDescriptor{action}))
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateActionDescriptors([]ActionDescriptor{action, action})))
	action.Scope = "unknown"
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateActionDescriptors([]ActionDescriptor{action})))
}
func TestTypedNilProviderErrorDoesNotPanic(t *testing.T) {
	var typed *ResourceOperationError
	var err error = typed
	require.NotPanics(t, func() { require.Equal(t, "INTERNAL", ErrorCode(err)); require.NotEmpty(t, err.Error()) })
}
