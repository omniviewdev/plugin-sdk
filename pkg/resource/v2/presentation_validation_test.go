package resourcev2

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPresentationLinksRequireExplicitCanonicalTypeSelection(t *testing.T) {
	valid := ResourceDefinition{
		ColumnDefs: []ColumnDefinition{
			{
				ID: "owner",
				ResourceLink: &ResourceLink{
					IDAccessor:     "metadata.ownerReferences[0].name",
					TypeAccessor:   "metadata.ownerReferences[0].kind",
					TypeMap:        map[string]string{"ReplicaSet": "apps/replicasets"},
					ScopeAccessors: []ScopeExtractor{{Dimension: "namespace", FieldPath: "metadata.namespace"}},
				},
			},
		},
	}
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
	require.Equal(
		t,
		"INVALID_REQUEST",
		ErrorCode(ValidateResourceGroups([]ResourceGroup{{ID: "workloads"}, {ID: "workloads"}})),
	)
	require.Equal(
		t,
		"INVALID_REQUEST",
		ErrorCode(
			ValidateResourceGroups(
				[]ResourceGroup{{ID: "workloads", Resources: []string{"apps/deployments", "apps/deployments"}}},
			),
		),
	)
	action := ActionDescriptor{ID: "restart", Scope: ActionScopeInstance}
	require.NoError(t, ValidateActionDescriptors([]ActionDescriptor{action}))
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateActionDescriptors([]ActionDescriptor{action, action})))
	action.Scope = "unknown"
	require.Equal(t, "INVALID_REQUEST", ErrorCode(ValidateActionDescriptors([]ActionDescriptor{action})))
}

func TestActionSchemaRequiredNames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required []string
		wantCode string
	}{
		{name: "nil"},
		{name: "empty", required: []string{}},
		{name: "singleton", required: []string{"undeclared"}},
		{name: "empty property name", required: []string{""}},
		{name: "distinct undeclared names", required: []string{"first", "second"}},
		{name: "duplicate", required: []string{"first", "first"}, wantCode: "INVALID_REQUEST"},
		{name: "nonadjacent duplicate", required: []string{"first", "second", "first"}, wantCode: "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			property := SchemaProperty{Type: SchemaObject, Required: tc.required}
			for _, location := range []struct {
				name   string
				schema *Schema
			}{
				{name: "root", schema: &Schema{Required: tc.required}},
				{name: "nested", schema: &Schema{Properties: map[string]SchemaProperty{"outer": property}}},
				{
					name: "deeply nested",
					schema: &Schema{Properties: map[string]SchemaProperty{
						"outer": {Type: SchemaObject, Properties: map[string]SchemaProperty{"inner": property}},
					}},
				},
			} {
				for _, action := range []ActionDescriptor{
					{ID: "params", Scope: ActionScopeInstance, ParamsSchema: location.schema},
					{ID: "output", Scope: ActionScopeInstance, OutputSchema: location.schema},
				} {
					t.Run(location.name+"/"+action.ID, func(t *testing.T) {
						require.Equal(t, tc.wantCode, ErrorCode(ValidateActionDescriptors([]ActionDescriptor{action})))
					})
				}
			}
		})
	}
}

func TestTypedNilProviderErrorDoesNotPanic(t *testing.T) {
	var typed *ResourceOperationError
	var err error = typed
	require.NotPanics(t, func() { require.Equal(t, "INTERNAL", ErrorCode(err)); require.NotEmpty(t, err.Error()) })
}
