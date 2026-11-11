package plugin

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
)

func TestActualGRPCQualifiedSourcePinPrecedence(t *testing.T) {
	cases := []sourcePinCase{
		{name: "qualified-only", qualifiedCurrent: true, provider: "test", connection: "explicit", accepted: true},
		{
			name:             "qualified-over-wrong-default",
			defaultPin:       "wrong-default",
			qualifiedCurrent: true,
			provider:         "test",
			connection:       "explicit",
			accepted:         true,
		},
		{
			name:           "wrong-qualified-no-default-fallback",
			defaultCurrent: true,
			qualifiedPin:   "wrong-qualified",
			provider:       "test",
			connection:     "explicit",
		},
		{name: "retired-qualified", qualifiedPin: "retired-catalog", provider: "test", connection: "explicit"},
		{name: "missing-route", qualifiedCurrent: true, provider: "test", connection: "other-connection"},
		{name: "wrong-provider", qualifiedCurrent: true, provider: "other-provider", connection: "explicit"},
		{name: "missing-all"},
	}
	for _, operation := range []string{"Get", "OpenSource"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				runQualifiedSourcePinScenario(t, operation, tc)
			})
		}
	}
}

type sourcePinCase struct {
	name, defaultPin, qualifiedPin, provider, connection string
	defaultCurrent, qualifiedCurrent                     bool
	accepted                                             bool
}

// runQualifiedSourcePinScenario checks both admission and native dispatch counts.
func runQualifiedSourcePinScenario(t *testing.T, operation string, tc sourcePinCase) {
	t.Helper()
	provider := &sourcePinProvider{qualifiedProvider: qualifiedProvider{allowed: true}}
	server := grpc.NewServer()
	RegisterServer(server, provider)
	client := NewClient(pipe(t, server))
	catalog, err := client.Catalog(context.Background(), resourcev2.CatalogRequest{Route: testGet().Route})
	if err != nil {
		t.Fatal(err)
	}
	ctx := sourcePinScenarioContext(tc, catalog.Revision)
	if operation == "Get" {
		_, err = client.Get(ctx, testGet())
	} else {
		var stream resourcev2.SourceStream
		stream, err = client.OpenSourceStream(ctx, sourcePinRequest())
		if err == nil {
			defer stream.Close()
			for {
				var frame resourcev2.SourceFrame
				frame, err = stream.Recv()
				if err != nil {
					break
				}
				if frame.Failed != nil {
					err = resourcev2.NewError(frame.Failed.Code, frame.Failed.Message)
					break
				}
				if frame.SnapshotEnd != nil {
					break
				}
			}
		}
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

func sourcePinScenarioContext(tc sourcePinCase, currentRevision string) context.Context {
	ctx := context.Background()
	if tc.defaultCurrent {
		tc.defaultPin = currentRevision
	}
	if tc.qualifiedCurrent {
		tc.qualifiedPin = currentRevision
	}
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
