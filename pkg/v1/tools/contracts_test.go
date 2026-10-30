package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	logging "github.com/omniviewdev/plugin-sdk/log"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	toolspb "github.com/omniviewdev/plugin-sdk/proto/v1/tools"
)

func TestAnnotationsPreserveAbsentAndFalse(t *testing.T) {
	for _, annotations := range []Annotations{{}, {ReadOnly: boolPointer(false), Destructive: boolPointer(false), Idempotent: boolPointer(false), OpenWorld: boolPointer(false)}} {
		wire, err := proto.Marshal(descriptorToProto(Descriptor{Annotations: annotations}))
		require.NoError(t, err)
		message := &toolspb.ToolDescriptor{}
		require.NoError(t, proto.Unmarshal(wire, message))
		got := descriptorFromProto(message).Annotations
		require.Equal(t, annotations, got)
		raw, err := json.Marshal(annotations)
		require.NoError(t, err)
		var decoded Annotations
		require.NoError(t, json.Unmarshal(raw, &decoded))
		require.Equal(t, annotations, decoded)
	}
	require.Equal(t, EffectiveAnnotations{false, true, false, true}, (Annotations{}).Resolve())
	require.False(t, (Annotations{Destructive: boolPointer(false)}).Resolve().Destructive)
}

func TestSchemaDeclarationsRejectMalformedAndRemote(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","required":"x"}`,
		`{"type":"object","properties":{"x":{"type":"integer","minimum":"1"}}}`,
		`{"type":"object","properties":{"x":{"type":"integer","type":"string"}}}`,
		`{"type":"object","$ref":"https://example.com/schema"}`,
		`{"type":"object","$schema":"http://json-schema.org/draft-07/schema#"}`,
		`{"type":"object","properties":{"x":{"format":"unknown"}}}`,
		`{"type":"object","properties":{"x":{"format":"date-time"}}}`,
	} {
		t.Run(raw, func(t *testing.T) { _, err := compileSchema([]byte(raw)); require.Error(t, err) })
	}
	schema, err := compileSchema(
		[]byte(
			`{"type":"object","$defs":{"n":{"type":"integer","minimum":9007199254740993,"maximum":9007199254740993}},"properties":{"n":{"$ref":"#/$defs/n"}},"required":["n"]}`,
		),
	)
	require.NoError(t, err)
	require.NoError(t, validateJSON(schema, []byte(`{"n":9007199254740993}`)))
	require.Error(t, validateJSON(schema, []byte(`{"n":9007199254740992}`)))
	require.Error(t, validateJSON(schema, []byte(`{"n":9007199254740994}`)))
	require.Error(t, validateJSON(schema, []byte(`{"n":9007199254740993,"nested":{"x":1,"x":2}}`)))
}

func TestValidationPrecedesHandlerAndFencesBadOutputs(t *testing.T) {
	calls := 0
	tool := validTool("checked", ScopePlugin)
	tool.InputSchema = json.RawMessage(
		`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`,
	)
	tool.OutputSchema = json.RawMessage(`{"type":"object","required":["ok"],"properties":{"ok":{"const":true}}}`)
	tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) {
		calls++
		return &Result{Content: []Content{{Text: "secret output"}}, StructuredContent: []byte(`{"ok":false}`)}, nil
	}
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	for _, input := range []string{`{}`, `{"n":"1"}`, `{"n":1,"n":2}`, `{"n":1,"extra":{"x":1,"x":2}}`} {
		_, callErr := manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(input)})
		require.Error(t, callErr)
	}
	require.Zero(t, calls)
	result, err := manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{"n":1}`)})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.True(t, result.IsError)
	require.Empty(t, result.StructuredContent)
	require.NotContains(t, result.Content[0].Text, "secret")
}

func TestHandlerSecretsNeverCrossWire(t *testing.T) {
	for _, handler := range []Handler{
		func(*types.PluginContext, json.RawMessage) (*Result, error) {
			return nil, errors.New("secret bearer token")
		},
		func(*types.PluginContext, json.RawMessage) (*Result, error) {
			return &Result{IsError: true, ErrorCode: "secret code", Content: []Content{{Text: "secret bearer token"}}, StructuredContent: []byte(`{"secret":"token"}`)}, nil
		},
	} {
		tool := validTool("secret", ScopePlugin)
		tool.Handler = handler
		manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.NoError(t, err)
		client := newWireClient(t, manager)
		result, err := client.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
		require.NoError(t, err)
		require.Equal(t, ErrorExecutionFailed, result.ErrorCode)
		require.Empty(t, result.StructuredContent)
		require.Equal(t, "Tool execution failed.", result.Content[0].Text)
	}
}

func TestUnsupportedContractRejectedBeforeSideEffects(t *testing.T) {
	calls := 0
	tool := validTool("side_effect", ScopePlugin)
	tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) { calls++; return TextResult("ok"), nil }
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	client := newWireClient(t, manager)
	for _, version := range []uint32{0, 1, 3} {
		_, callErr := client.client.CallTool(
			context.Background(),
			&toolspb.CallToolRequest{ContractVersion: version, Name: tool.Name, Arguments: []byte(`{}`)},
		)
		require.Equal(t, codes.FailedPrecondition, status.Code(callErr))
	}
	require.Zero(t, calls)
	_, err = client.client.ListTools(context.Background(), &toolspb.ListToolsRequest{ContractVersion: 1})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

type oldToolsPeer struct{ calls int }

func (*oldToolsPeer) ListTools(
	context.Context,
	*toolspb.ListToolsRequest,
	...grpc.CallOption,
) (*toolspb.ListToolsResponse, error) {
	return &toolspb.ListToolsResponse{
		Tools: []*toolspb.ToolDescriptor{descriptorToProto(validTool("side_effect", ScopePlugin).Descriptor)},
	}, nil
}

func (p *oldToolsPeer) CallTool(
	context.Context,
	*toolspb.CallToolRequest,
	...grpc.CallOption,
) (*toolspb.CallToolResponse, error) {
	p.calls++
	return &toolspb.CallToolResponse{}, nil
}

func TestClientRefusesOldPeerBeforeDispatch(t *testing.T) {
	peer := &oldToolsPeer{}
	client := &PluginClient{client: peer}
	_, err := client.CallTool(nil, CallInput{Name: "side_effect", Arguments: []byte(`{}`)})
	require.Error(t, err)
	require.Zero(t, peer.calls)
}

func TestInvalidArgumentsPrecedeNativeAdmission(t *testing.T) {
	tool := validTool("native", ScopeConnection)
	calls := 0
	tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) { calls++; return TextResult("ok"), nil }
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	// Missing registry and route would fail admission; malformed arguments win first.
	_, err = manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{"x":1}`)})
	require.ErrorContains(t, err, "Tool arguments do not match")
	require.Zero(t, calls)
}

func TestHandlerCancellationPreservesTransportSemantics(t *testing.T) {
	for _, failure := range []error{fmt.Errorf("secret: %w", context.Canceled), fmt.Errorf("secret: %w", context.DeadlineExceeded), status.Error(codes.Canceled, "secret"), status.Error(codes.DeadlineExceeded, "secret")} {
		tool := validTool("cancel", ScopePlugin)
		tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) { return TextResult("secret"), failure }
		manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.NoError(t, err)
		result, err := manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
		require.Nil(t, result)
		expected := codes.Canceled
		if errors.Is(failure, context.DeadlineExceeded) || status.Code(failure) == codes.DeadlineExceeded {
			expected = codes.DeadlineExceeded
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.ErrorIs(t, err, context.Canceled)
		}
		client := newWireClient(t, manager)
		result, err = client.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
		require.Nil(t, result)
		require.Equal(t, expected, status.Code(err))
		require.NotContains(t, err.Error(), "secret")
	}
}

func TestHandlerReviewedErrorsKeepSafeClassification(t *testing.T) {
	for _, code := range []string{ErrorUnavailable, ErrorPermissionDenied} {
		tool := validTool("classified", ScopePlugin)
		tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) {
			return nil, resourcev2.NewError(code, "secret diagnostic")
		}
		manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.NoError(t, err)
		result, err := newWireClient(t, manager).CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
		require.NoError(t, err)
		require.True(t, result.IsError)
		require.Equal(t, code, result.ErrorCode)
		require.NotContains(t, result.Content[0].Text, "secret")
	}
}

type outputToolsPeer struct {
	descriptor Descriptor
	raw        []byte
	calls      int
}

func (p *outputToolsPeer) ListTools(
	context.Context,
	*toolspb.ListToolsRequest,
	...grpc.CallOption,
) (*toolspb.ListToolsResponse, error) {
	return &toolspb.ListToolsResponse{
		ContractVersion: ContractVersion,
		Tools:           []*toolspb.ToolDescriptor{descriptorToProto(p.descriptor)},
	}, nil
}

func (p *outputToolsPeer) CallTool(
	context.Context,
	*toolspb.CallToolRequest,
	...grpc.CallOption,
) (*toolspb.CallToolResponse, error) {
	p.calls++
	return &toolspb.CallToolResponse{StructuredContent: p.raw}, nil
}

func TestStructuredOutputRequiresObjectAtBothBoundaries(t *testing.T) {
	for _, raw := range []string{`[]`, `1`, `"secret"`, `null`, `true`} {
		for _, declared := range []bool{false, true} {
			tool := validTool("object_result", ScopePlugin)
			if declared {
				tool.OutputSchema = []byte(`{"type":"object"}`)
			}
			tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) {
				return &Result{StructuredContent: []byte(raw)}, nil
			}
			manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
			require.NoError(t, err)
			result, err := manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Empty(t, result.StructuredContent)
			client := &PluginClient{client: &outputToolsPeer{descriptor: tool.Descriptor, raw: []byte(raw)}}
			result, err = client.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Empty(t, result.StructuredContent)
		}
	}
	for _, schema := range []string{`{"type":"array"}`, `{"type":"string"}`, `true`, `{}`} {
		tool := validTool("bad_output_schema", ScopePlugin)
		tool.OutputSchema = []byte(schema)
		_, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.Error(t, err)
		client := &PluginClient{client: &outputToolsPeer{descriptor: tool.Descriptor}}
		_, err = client.ListTools(nil)
		require.Error(t, err)
	}
}

func TestExtremeExponentRejectedBeforeValidationOrHandler(t *testing.T) {
	tool := validTool("numeric_budget", ScopePlugin)
	tool.InputSchema = []byte(`{"type":"object","properties":{"n":{"type":"number","minimum":0}}}`)
	calls := 0
	tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) { calls++; return TextResult("ok"), nil }
	manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
	require.NoError(t, err)
	for _, number := range []string{"1e1000001", "1e-1000001", "1e999999999999999999999999", "1" + strings.Repeat("0", 4096)} {
		input := CallInput{Name: tool.Name, Arguments: []byte(`{"n":` + number + `}`)}
		require.NotPanics(t, func() { _, err = manager.CallTool(nil, input); require.Error(t, err) })
		peer := &outputToolsPeer{descriptor: tool.Descriptor}
		client := &PluginClient{client: peer}
		require.NotPanics(t, func() { _, err = client.CallTool(nil, input); require.Error(t, err) })
		require.Zero(t, peer.calls)
	}
	require.Zero(t, calls)
	schema, err := compileSchema(tool.InputSchema)
	require.NoError(t, err)
	for _, number := range []string{"9007199254740993", "1e4096", "1e-4096", "-0"} {
		require.NoError(t, validateJSON(schema, []byte(`{"n":`+number+`}`)))
	}
	tool.InputSchema = []byte(`{"type":"object","properties":{"n":{"minimum":1e1000001}}}`)
	require.NotPanics(
		t,
		func() { _, err = NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}}); require.Error(t, err) },
	)
}

func doublingSchema(levels int) json.RawMessage {
	definitions := map[string]any{"s0": false}
	for index := 1; index <= levels; index++ {
		ref := map[string]any{"$ref": fmt.Sprintf("#/$defs/s%d", index-1)}
		definitions[fmt.Sprintf("s%d", index)] = map[string]any{"allOf": []any{ref, ref}}
	}
	raw, err := json.Marshal(
		map[string]any{
			"type":  "object",
			"$defs": definitions,
			"allOf": []any{map[string]any{"$ref": fmt.Sprintf("#/$defs/s%d", levels)}},
		},
	)
	if err != nil {
		panic(err)
	}
	return raw
}

func TestReferenceExpansionRejectedBeforeCompilerAndDispatch(t *testing.T) {
	for _, raw := range []json.RawMessage{
		doublingSchema(30),
		[]byte(`{"type":"object","$defs":{"loop":{"$ref":"#/$defs/loop"}},"properties":{"child":{"$ref":"#/$defs/loop"}}}`),
		[]byte(`{"type":"object","$dynamicAnchor":"node","properties":{"child":{"$dynamicRef":"#node"}}}`),
	} {
		tool := validTool("schema_budget", ScopePlugin)
		tool.InputSchema = raw
		calls := 0
		tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) { calls++; return TextResult("ok"), nil }
		_, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.Error(t, err)
		peer := &outputToolsPeer{descriptor: tool.Descriptor}
		_, err = (&PluginClient{client: peer}).CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
		require.Error(t, err)
		require.Zero(t, peer.calls)
		require.Zero(t, calls)
	}
	// Small static DAGs and anchor refs remain valid; their assertions still run.
	small, err := compileSchema(doublingSchema(3))
	require.NoError(t, err)
	require.Error(t, validateJSON(small, []byte(`{}`)))
	anchored, err := compileSchema(
		[]byte(
			`{"type":"object","$defs":{"n":{"$anchor":"number","type":"integer"}},"properties":{"n":{"$ref":"#number"}}}`,
		),
	)
	require.NoError(t, err)
	require.NoError(t, validateJSON(anchored, []byte(`{"n":9007199254740993}`)))
	require.Error(t, validateJSON(anchored, []byte(`{"n":"wrong"}`)))
}

func TestValidationWorkBudgetCombinesSchemaAndInstance(t *testing.T) {
	schema, err := compileSchema(
		[]byte(
			`{"type":"object","properties":{"items":{"type":"array","uniqueItems":true,"items":{"type":"integer"}}}}`,
		),
	)
	require.NoError(t, err)
	items := make([]int, 1000)
	for index := range items {
		items[index] = index
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	require.NoError(t, err)
	require.ErrorContains(t, validateJSON(schema, raw), "work budget")
	require.NoError(t, validateJSON(schema, []byte(`{"items":[1,2,3]}`)))
	require.Error(t, validateJSON(schema, []byte(`{"items":[1,1]}`)))
}

func TestExtremeOutputNumberFailsSafelyAtBothBoundaries(t *testing.T) {
	for _, declared := range []bool{false, true} {
		tool := validTool("numeric_output", ScopePlugin)
		if declared {
			tool.OutputSchema = []byte(`{"type":"object","properties":{"n":{"minimum":0}}}`)
		}
		raw := []byte(`{"n":1e1000001}`)
		tool.Handler = func(*types.PluginContext, json.RawMessage) (*Result, error) {
			return &Result{StructuredContent: raw}, nil
		}
		manager, err := NewManager(logging.NewNop(), PluginOpts{Tools: []Tool{tool}})
		require.NoError(t, err)
		require.NotPanics(t, func() {
			result, err := manager.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Empty(t, result.StructuredContent)
		})
		client := &PluginClient{client: &outputToolsPeer{descriptor: tool.Descriptor, raw: raw}}
		require.NotPanics(t, func() {
			result, err := client.CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{}`)})
			require.NoError(t, err)
			require.True(t, result.IsError)
			require.Empty(t, result.StructuredContent)
		})
	}
}

func TestLegacyApplicatorsCannotBypassSchemaBudget(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","dependencies":{"x":{"$ref":"#"}}}`,
		`{"type":"object","dependencies":{"x":{"$ref":"https://example.com/remote"}}}`,
		`{"type":"object","properties":{"x":{"dependencies":{"y":{"$dynamicRef":"#node"}}}}}`,
		`{"type":"object","$recursiveRef":"#"}`,
		`{"type":"object","$defs":{"x":{"$recursiveAnchor":true}}}`,
		`{"type":"object","definitions":{"hidden":{"$schema":"http://json-schema.org/draft-07/schema#","dependencies":{"x":{"$ref":"#"}}}}}`,
		`{"type":"object","additionalItems":{"$recursiveRef":"#"}}`,
		`{"type":"object","properties":{"list":{"items":[{"$recursiveRef":"#"}]}}}`,
	} {
		_, err := compileSchema([]byte(raw))
		require.Error(t, err)
		tool := validTool("legacy_rejected", ScopePlugin)
		tool.InputSchema = []byte(raw)
		peer := &outputToolsPeer{descriptor: tool.Descriptor}
		_, err = (&PluginClient{client: peer}).CallTool(nil, CallInput{Name: tool.Name, Arguments: []byte(`{"x":1}`)})
		require.Error(t, err)
		require.Zero(t, peer.calls)
	}
	// Modern dependentSchemas remains a budgeted assertion, not a dropped alias.
	modern, err := compileSchema([]byte(`{"type":"object","dependentSchemas":{"x":{"required":["y"]}}}`))
	require.NoError(t, err)
	require.Error(t, validateJSON(modern, []byte(`{"x":1}`)))
	require.NoError(t, validateJSON(modern, []byte(`{"x":1,"y":2}`)))
}

func TestSchemaCountsRejectOverflowBeforeDependencyCompilation(t *testing.T) {
	for _, keyword := range []string{"minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties", "minContains", "maxContains"} {
		for _, number := range []string{"18446744073709551616", "9223372036854775808", "1e100", "-1", "0.5"} {
			raw := []byte(`{"type":"object","properties":{"value":{"` + keyword + `":` + number + `}}}`)
			_, err := compileSchema(raw)
			require.ErrorContains(t, err, "schema count")
			tool := validTool("count_rejected", ScopePlugin)
			tool.InputSchema = raw
			peer := &outputToolsPeer{descriptor: tool.Descriptor}
			_, err = (&PluginClient{client: peer}).CallTool(
				nil,
				CallInput{Name: tool.Name, Arguments: []byte(`{"value":""}`)},
			)
			require.Error(t, err)
			require.Zero(t, peer.calls)
		}
	}
	maximum := strconv.FormatUint(uint64(^uint(0) >> 1), 10)
	require.NoError(t, admitCount(json.Number(maximum)))
	require.NoError(t, admitCount(json.Number("1e1")))
	schema, err := compileSchema(
		[]byte(`{"type":"object","properties":{"value":{"type":"string","minLength":2,"maxLength":3}}}`),
	)
	require.NoError(t, err)
	require.Error(t, validateJSON(schema, []byte(`{"value":"a"}`)))
	require.NoError(t, validateJSON(schema, []byte(`{"value":"ab"}`)))
	require.Error(t, validateJSON(schema, []byte(`{"value":"abcd"}`)))
	// Keywords inside const data are data, not declaration counts/applicators.
	_, err = compileSchema(
		[]byte(`{"type":"object","const":{"dependencies":{},"minLength":18446744073709551616}}`),
	)
	require.NoError(t, err)
}
