package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

func TestActualGRPCQualifiedSourcePinPrecedence(t *testing.T) {
	cases := []sourcePinCase{
		{name: "qualified-only", qualifiedPin: "cat-1", provider: "test", connection: "explicit", accepted: true},
		{
			name:         "qualified-over-wrong-default",
			defaultPin:   "wrong-default",
			qualifiedPin: "cat-1",
			provider:     "test",
			connection:   "explicit",
			accepted:     true,
		},
		{
			name:         "wrong-qualified-no-default-fallback",
			defaultPin:   "cat-1",
			qualifiedPin: "wrong-qualified",
			provider:     "test",
			connection:   "explicit",
		},
		{name: "retired-qualified", qualifiedPin: "retired-catalog", provider: "test", connection: "explicit"},
		{name: "missing-route", qualifiedPin: "cat-1", provider: "test", connection: "other-connection"},
		{name: "wrong-provider", qualifiedPin: "cat-1", provider: "other-provider", connection: "explicit"},
		{name: "missing-all"},
	}
	for _, operation := range []string{"Get", "StartConnectionWatch"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				runQualifiedSourcePinScenario(t, operation, tc)
			})
		}
	}
}

type sourcePinCase struct {
	name, defaultPin, qualifiedPin, provider, connection string
	accepted                                             bool
}

// runQualifiedSourcePinScenario checks both admission and native dispatch counts.
func runQualifiedSourcePinScenario(t *testing.T, operation string, tc sourcePinCase) {
	t.Helper()
	provider := &updateProvider{
		qualifiedProvider: qualifiedProvider{allowed: true},
		trigger:           make(chan struct{}, 1),
	}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	ctx := sourcePinScenarioContext(tc)
	var err error
	if operation == "Get" {
		_, err = client.Get(ctx, testGet())
	} else {
		err = client.StartConnectionWatch(ctx, []resourcev2.ReadTarget{updateTarget()})
	}
	calls := provider.calls.Load() + provider.starts.Load()
	if tc.accepted {
		if err != nil || calls != 1 {
			t.Fatalf("qualified source observation rejected: native=%d err=%v", calls, err)
		}
	} else if resourcev2.ErrorCode(err) != "RESET_REQUIRED" || calls != 0 {
		t.Fatalf("unadmitted source reached native work: native=%d err=%v", calls, err)
	}
}

func sourcePinScenarioContext(tc sourcePinCase) context.Context {
	ctx := context.Background()
	if tc.defaultPin != "" {
		ctx = resourcev2.WithCatalogRevision(ctx, tc.defaultPin)
	}
	if tc.qualifiedPin != "" {
		ctx = resourcev2.WithCatalogRevisionForRoute(
			ctx,
			tc.provider,
			resourcev2.AccessRoute{Connection: tc.connection},
			tc.qualifiedPin,
		)
	}
	return ctx
}
