package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

type schemaActionProvider struct {
	streamProvider
	descriptor resourcev2.ActionDescriptor
	called     bool
}

func (p *schemaActionProvider) GetActions(
	context.Context,
	resourcev2.AccessRoute,
	string,
) ([]resourcev2.ActionDescriptor, error) {
	return []resourcev2.ActionDescriptor{p.descriptor}, nil
}

func (p *schemaActionProvider) Action(context.Context, resourcev2.ActionRequest) (resourcev2.Value, error) {
	p.called = true
	return resourcev2.Value{Kind: "string", String: "invalid object output"}, nil
}

func TestActionTransportRejectsDeclaredSchemaViolations(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "streaming"}[streaming], func(t *testing.T) {
			p := &schemaActionProvider{
				descriptor: resourcev2.ActionDescriptor{
					ID:           "test",
					Scope:        resourcev2.ActionScopeInstance,
					Streaming:    streaming,
					OutputSchema: (&resourcev2.Schema{}).Ptr(),
				},
			}
			server := grpc.NewServer()
			RegisterServer(server, p)
			client := NewClient(pipe(t, server))
			input := actionInput()
			ctx := acceptedCatalogContext(context.Background(), t, client, input.Route)
			var err error
			if streaming {
				err = client.StreamAction(ctx, input, make(chan resourcev2.ActionEvent, 8))
			} else {
				_, err = client.Action(ctx, input)
			}
			if resourcev2.ErrorCode(err) != "INVALID_REQUEST" {
				t.Fatalf("invalid output succeeded: %v", err)
			}
		})
	}
}

func TestActionTransportValidatesInputBeforeNativeWork(t *testing.T) {
	p := &schemaActionProvider{
		descriptor: resourcev2.ActionDescriptor{
			ID:           "test",
			Scope:        resourcev2.ActionScopeInstance,
			ParamsSchema: (&resourcev2.Schema{Properties: map[string]resourcev2.SchemaProperty{"required": {Type: resourcev2.SchemaString}}, Required: []string{"required"}}).Ptr(),
		},
	}
	server := grpc.NewServer()
	RegisterServer(server, p)
	client := NewClient(pipe(t, server))
	input := actionInput()
	_, err := client.Action(acceptedCatalogContext(context.Background(), t, client, input.Route), input)
	if err == nil || p.called {
		t.Fatalf("invalid input reached native work: called=%v err=%v", p.called, err)
	}
}
