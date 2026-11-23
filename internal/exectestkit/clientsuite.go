package exectestkit

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/omniviewdev/plugin-sdk/pkg/v1/exec"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// ClientSuiteOptions configures RunClientSuite.
type ClientSuiteOptions struct {
	// Context decorates the context of every call, for a provider that needs the call's plugin context (the
	// connection) or the catalog pin in it. Nil leaves the context as it is.
	Context func(context.Context) context.Context
	// Ref returns the reference to ask Options about for a target kind, or false to skip Options for it.
	Ref func(info *execv2.TargetInfo) (exec.TargetRef, bool)
}

func (o ClientSuiteOptions) context(ctx context.Context) context.Context {
	if o.Context == nil {
		return ctx
	}
	return o.Context(ctx)
}

// RunClientSuite runs the provider-neutral conformance cases against an exec.Client: the SDK's gRPC client over a
// plugin, the host's local terminal provider and the host's legacy adapter each pass the same cases (the provider
// states what it cannot do through Describe, and the cases that need what it does not serve are skipped, never
// failed). newClient is called once per case and must return a client the case owns.
//
// This build runs the cases of what the SDK serves: Describe and Options. The cases that need a session or a run
// are listed by the caller (exectest.Conformance lists them under "engine") and skipped with the step that fills
// them.
func RunClientSuite(t *testing.T, newClient func(t testing.TB) exec.Client, opts ClientSuiteOptions) {
	t.Helper()
	t.Run("describe", func(t *testing.T) {
		t.Run("answers", func(t *testing.T) { caseDescribeAnswers(t, newClient(t), opts) })
		t.Run("is static", func(t *testing.T) { caseDescribeStatic(t, newClient(t), opts) })
	})
	t.Run("options", func(t *testing.T) {
		t.Run("answer within the contract", func(t *testing.T) { caseOptionsAnswer(t, newClient(t), opts) })
	})
}

// caseDescribeAnswers: Describe answers, its targets are distinct and named, and it lists only features this
// build knows.
func caseDescribeAnswers(t *testing.T, client exec.Client, opts ClientSuiteOptions) {
	t.Helper()
	description, err := client.Describe(opts.context(context.Background()))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if description.GetContractVersion() == 0 {
		t.Error("the contract version is zero: a producer states the version of the contract it implements")
	}
	seen := map[[2]string]bool{}
	for _, target := range description.GetTargets() {
		key := [2]string{target.GetProvider(), target.GetType()}
		if target.GetProvider() == "" || target.GetType() == "" {
			t.Errorf("a target has no routing key: %v", target)
		}
		if seen[key] {
			t.Errorf("target %s/%s is described twice", target.GetProvider(), target.GetType())
		}
		seen[key] = true
	}
	for _, feature := range description.GetFeatures() {
		if feature == execv2.Feature_FEATURE_UNSPECIFIED {
			t.Error("Describe lists an unspecified feature")
		}
	}
}

// caseDescribeStatic: Describe is static, so two calls answer the same.
func caseDescribeStatic(t *testing.T, client exec.Client, opts ClientSuiteOptions) {
	t.Helper()
	first, err := client.Describe(opts.context(context.Background()))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	second, err := client.Describe(opts.context(context.Background()))
	if err != nil {
		t.Fatalf("describe again: %v", err)
	}
	if !proto.Equal(first, second) {
		t.Errorf("Describe answered differently the second time:\nfirst  %v\nsecond %v", first, second)
	}
}

// caseOptionsAnswer: for every target kind the options can be asked for, Options answers within the contract (the
// client validates the bounds, so a successful answer is one), and an error is never a refusal of the lane.
func caseOptionsAnswer(t *testing.T, client exec.Client, opts ClientSuiteOptions) {
	t.Helper()
	if opts.Ref == nil {
		t.Skip("no reference to ask Options about")
	}
	description, err := client.Describe(opts.context(context.Background()))
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	asked := 0
	for _, info := range description.GetTargets() {
		ref, ok := opts.Ref(info)
		if !ok {
			continue
		}
		asked++
		result, err := client.Options(opts.context(context.Background()), ref)
		if err != nil {
			var refusal *exec.Error
			if errors.As(err, &refusal) && refusal.Class == exec.ClassUnsupported {
				t.Errorf("target %s/%s is described but Options is refused as unsupported: %v", info.GetProvider(), info.GetType(), err)
				continue
			}
			t.Errorf("Options for %s/%s: %v", info.GetProvider(), info.GetType(), err)
			continue
		}
		if result == nil {
			t.Errorf("Options for %s/%s answered nothing", info.GetProvider(), info.GetType())
		}
	}
	if asked == 0 {
		t.Skip("no target kind to ask Options about")
	}
}
