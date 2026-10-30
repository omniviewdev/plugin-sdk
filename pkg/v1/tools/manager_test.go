package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

func echoHandler(_ *types.PluginContext, arguments json.RawMessage) (*Result, error) {
	return TextResult(string(arguments)), nil
}

func validTool(name string, scope Scope) Tool {
	return Tool{
		Descriptor: Descriptor{
			Name:        name,
			Description: "test tool",
			InputSchema: ObjectSchema(map[string]any{"x": map[string]any{"type": "string"}}),
			Scope:       scope,
		},
		Handler: echoHandler,
	}
}

func TestNewManagerRejectsInvalidTools(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		want string
	}{
		{"bad name", validTool("Bad-Name", ScopePlugin), "name must match"},
		{"nil handler", Tool{Descriptor: validTool("ok", ScopePlugin).Descriptor}, "handler is nil"},
		{"empty scope", validTool("ok", ""), "scope must be"},
		{"schema not object", Tool{
			Descriptor: Descriptor{Name: "ok", Scope: ScopePlugin, InputSchema: json.RawMessage(`{"type":"string"}`)},
			Handler:    echoHandler,
		}, `"type" must be "object"`},
		{"schema not json", Tool{
			Descriptor: Descriptor{Name: "ok", Scope: ScopePlugin, InputSchema: json.RawMessage(`{`)},
			Handler:    echoHandler,
		}, "not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tc.tool}})
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestNewManagerRejectsDuplicateNames(t *testing.T) {
	_, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{
		validTool("same", ScopePlugin),
		validTool("same", ScopeConnection),
	}})
	require.ErrorContains(t, err, "duplicate name")
}

func TestManagerListToolsKeepsRegistrationOrder(t *testing.T) {
	m, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{
		validTool("zeta", ScopePlugin),
		validTool("alpha", ScopeConnection),
	}})
	require.NoError(t, err)
	descriptors, err := m.ListTools(nil)
	require.NoError(t, err)
	require.Equal(t, []string{"zeta", "alpha"}, []string{descriptors[0].Name, descriptors[1].Name})
	require.Equal(t, ScopeConnection, descriptors[1].Scope)
}

func TestManagerCallToolErrors(t *testing.T) {
	m, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{
		validTool("scoped", ScopeConnection),
		validTool("plain", ScopePlugin),
	}})
	require.NoError(t, err)
	m.BindConnectionContextProvider(toolRegistry(t, nil))

	_, err = m.CallTool(types.NewPluginContext(context.Background(), "t", nil, nil, nil), CallInput{Name: "missing"})
	require.ErrorIs(t, err, ErrToolNotFound)

	_, err = m.CallTool(types.NewPluginContext(context.Background(), "t", nil, nil, nil), CallInput{Name: "scoped"})
	require.ErrorIs(t, err, ErrConnectionRequired)

	res, err := m.CallTool(
		types.NewPluginContextWithConnection(context.Background(), "t", nil, nil, &types.Connection{ID: "conn-1"}),
		CallInput{
			Name:      "scoped",
			Arguments: json.RawMessage(`{"x":"1"}`),
			Route:     &resourcev2.AccessRoute{Connection: "conn-1"},
		},
	)
	require.NoError(t, err)
	require.JSONEq(t, `{"x":"1"}`, res.Content[0].Text)

	res, err = m.CallTool(
		types.NewPluginContext(context.Background(), "t", nil, nil, nil),
		CallInput{Name: "plain", Arguments: json.RawMessage(`{}`)},
	)
	require.NoError(t, err)
	require.False(t, res.IsError)
}

func TestResultHelpers(t *testing.T) {
	res, err := JSONResult(map[string]any{"a": 1})
	require.NoError(t, err)
	require.JSONEq(t, `{"a":1}`, string(res.StructuredContent))
	require.Equal(t, "{\n  \"a\": 1\n}", res.Content[0].Text)
	require.False(t, res.IsError)

	errRes := ErrorResult("bad %s", "thing")
	require.True(t, errRes.IsError)
	require.Equal(t, "bad thing", errRes.Content[0].Text)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(ObjectSchema(nil), &schema))
	require.Equal(t, "object", schema["type"])
	require.Equal(t, false, schema["additionalProperties"])
	require.Equal(t, []any{}, schema["required"])
}
