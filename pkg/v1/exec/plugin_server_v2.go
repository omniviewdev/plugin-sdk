package exec

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	logging "github.com/omniviewdev/plugin-sdk/log"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	resourceplugin "github.com/omniviewdev/plugin-sdk/pkg/resource/v2/plugin"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// serverV2 serves the exec v2 service of a plugin: the targets it registered.
//
// This build serves Describe and Options. Open, Run, RunStream, ListSessions, ReadOutput and EndSession answer
// Unimplemented with a typed ClassUnsupported refusal, and Describe lists no feature for them (servedFeatures), so
// a host that asks first never calls them. The server holds the engine that will run the commands of those RPCs (engine.go);
// nothing calls it from the wire yet, and it is driven in process by the tests (engine_hook.go).
type serverV2 struct {
	log logging.Logger
	reg *targetRegistry
	// connections returns the connection registry the plugin's resource provider owns, or nil when none is bound.
	// It is read at each call: the SDK binds it after the capability is registered.
	connections func() resourcev2.ConnectionContextProvider
	now         func() time.Time
	engine      *engine
}

func newServerV2(log logging.Logger, reg *targetRegistry, connections func() resourcev2.ConnectionContextProvider) *serverV2 {
	if log == nil {
		log = logging.NewNop()
	}
	s := &serverV2{log: log, reg: reg, connections: connections, now: time.Now}
	s.engine = newEngine(engineConfig{log: log, targets: reg, connections: connections})
	return s
}

// setLogger names the server's logger, and the engine's. The SDK does it when it registers the capability, before
// anything serves.
func (s *serverV2) setLogger(log logging.Logger) {
	if log == nil {
		log = logging.NewNop()
	}
	s.log = log
	s.engine.setLogger(log)
}

// refuse logs the local cause of a refusal and returns its wire status. The status carries the class, reason and
// facts only; the cause never leaves the process.
func (s *serverV2) refuse(ctx context.Context, method string, e *Error) error {
	n := e.normalized()
	if n.Err != nil || n.Class == ClassInternal {
		level := s.log.Debugw
		if n.Class == ClassInternal {
			level = s.log.Warnw
		}
		cause := ""
		if n.Err != nil {
			cause = n.Err.Error()
		}
		level(ctx, "exec v2 refusal", "method", method, "class", n.Class.String(), "reason", n.Reason.String(), "cause", cause)
	}
	return n.status()
}

// Describe answers with the registered targets and the features this build serves.
func (s *serverV2) Describe(ctx context.Context, req *execv2.DescribeRequest) (*execv2.DescribeResponse, error) {
	if refusal := rejectUnknownFields(req); refusal != nil {
		return nil, s.refuse(ctx, "Describe", refusal)
	}
	return s.reg.description(), nil
}

// Options lists the choices of one resource. The request is strict, the target must be registered, and the request is
// admitted like every resource utility call, for every target: the session context of the call is read, and the
// connection is leased with the caller's route and the catalog pin checked. The author's callback, when the target has
// one, runs inside the lease. A target with no callback answers an empty list, once admitted.
func (s *serverV2) Options(ctx context.Context, req *execv2.OptionsRequest) (*execv2.OptionsResponse, error) {
	if refusal := rejectUnknownFields(req); refusal != nil {
		return nil, s.refuse(ctx, "Options", refusal)
	}
	ref, _, refusal := admitTargetRef(req.GetTarget(), false)
	if refusal != nil {
		return nil, s.refuse(ctx, "Options", refusal)
	}
	address := ref.Reference.Address
	target, ok := s.reg.lookup(address.Provider, address.Type)
	if !ok {
		return nil, s.refuse(ctx, "Options", unsupportedRequest("no exec target for %s/%s", address.Provider, address.Type))
	}
	ctx, err := resourceplugin.IncomingSessionContext(ctx)
	if err != nil {
		return nil, resourceplugin.ResourceRPCError(err)
	}
	pc := types.PluginContextFromContext(ctx)
	if pc == nil {
		pc = &types.PluginContext{Context: ctx}
	}

	var (
		options     []Option
		callbackErr error
		// leaseOver is the lease context's error when the callback returned: a lease that is over then was cancelled
		// under the callback.
		leaseOver  error
		observedAt time.Time
	)
	leaseErr := resourcev2.WithUtilityConnection(pc, s.connections(), &ref.Reference, &ref.Route, func(bound *types.PluginContext) error {
		// A target with no callback has nothing to ask. It is admitted all the same, so that a request that cannot be
		// admitted (a stale pin, another connection's route, no session) is refused for every target alike, and it is
		// answered at the same point: where its callback would have returned.
		if fn := target.target.Options; fn != nil {
			options, callbackErr = s.callOptions(ctx, fn, bound, ref)
		}
		// Read right after the callback returns, before the lease checks the catalog once more and releases: the
		// choices are as old as the callback's return, however long that check takes.
		observedAt = s.now()
		if bound.Context != nil {
			leaseOver = bound.Context.Err()
		}
		return callbackErr
	})
	switch {
	case leaseErr == nil:
	case callbackErr != nil && errors.Is(leaseErr, callbackErr):
		refusal := classifyError(target.target.Classify, callbackErr)
		if leaseOver != nil && ctx.Err() == nil {
			// The caller is still waiting and the lease was cancelled under the callback: the connection was retired.
			refusal = &Error{Class: ClassConnection, Reason: ReasonConnectionRetired, Err: callbackErr}
		}
		return nil, s.refuse(ctx, "Options", refusal)
	case callbackErr == nil && ctx.Err() == nil && errors.Is(leaseErr, context.Canceled):
		// The lease ended with a cancellation, not the callback's error and not the caller's: the caller is still
		// waiting, so the connection was retired. That is so when the retirement came under a callback that returned
		// without an error, and when it came during a catalog check around the callback (the final one runs after the
		// callback returned, where the lease context cannot be read any more): the lease reports its own
		// cancellation, bare or wrapped by the resource lane's classification.
		//
		// A bare context.Canceled that reaches this check while the caller is alive is read as the lease having been
		// cancelled under it. A catalog provider that returns context.Canceled from a downstream operation of its own would
		// be misread as a retirement: that is a provider bug (it must return its own error for such a failure), and it
		// cannot be told apart from here, because the lease's context ends on return, so the lease itself cannot say
		// whether it was cancelled before the provider returned or by it.
		return nil, s.refuse(ctx, "Options", &Error{Class: ClassConnection, Reason: ReasonConnectionRetired, Err: leaseErr})
	default:
		// The connection lease or the catalog pin refused: keep the resource lane's own classification, the host's
		// existing contract for it (RESET_REQUIRED, CONNECTION_ERROR, ...). A cancellation that is the caller's own
		// stays the Canceled status the resource lane makes of it.
		return nil, resourceplugin.ResourceRPCError(leaseErr)
	}
	if err := validateOptions(options); err != nil {
		return nil, s.refuse(ctx, "Options", &Error{Class: ClassInternal, Err: fmt.Errorf("options callback broke the contract: %w", err)})
	}
	// A choice declares the mode of a session, and a mode is at most what its kind can do: what a choice declares above
	// the kind's is served as the kind's value, and the author is told which field in the log, as for a Started.
	options, widened := clampChoiceCapabilities(target.info.Capabilities, options)
	for _, v := range widened {
		s.log.Warnw(ctx, "exec options callback declared a choice wider than its kind: the SDK serves the kind's value for the field",
			"target", target.target.key(), "option", v.option, "choice", v.choice, "violation", v.text)
	}
	return &execv2.OptionsResponse{Options: optionsToProto(options), ObservedAtUnixMs: observedAt.UnixMilli()}, nil
}

// callOptions runs the author's callback; a panic is the author's bug and must not take the plugin down.
func (s *serverV2) callOptions(ctx context.Context, fn OptionsFunc, pc *types.PluginContext, ref TargetRef) (options []Option, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.log.Errorw(ctx, "exec options callback panicked", "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			options, err = nil, &Error{Class: ClassInternal, Err: fmt.Errorf("options callback panicked: %v", recovered)}
		}
	}()
	return fn(pc, ref.Reference)
}

// Open is not served by this build.
func (s *serverV2) Open(execv2.Exec_OpenServer) error {
	return unsupportedError().status()
}

// Run is not served by this build.
func (s *serverV2) Run(context.Context, *execv2.RunRequest) (*execv2.RunResult, error) {
	return nil, unsupportedError().status()
}

// RunStream is not served by this build.
func (s *serverV2) RunStream(*execv2.RunRequest, execv2.Exec_RunStreamServer) error {
	return unsupportedError().status()
}

// ListSessions is not served by this build.
func (s *serverV2) ListSessions(context.Context, *execv2.ListSessionsRequest) (*execv2.ListSessionsResponse, error) {
	return nil, unsupportedError().status()
}

// ReadOutput is not served by this build.
func (s *serverV2) ReadOutput(context.Context, *execv2.ReadOutputRequest) (*execv2.ReadOutputResponse, error) {
	return nil, unsupportedError().status()
}

// EndSession is not served by this build.
func (s *serverV2) EndSession(context.Context, *execv2.EndSessionRequest) (*execv2.EndSessionResponse, error) {
	return nil, unsupportedError().status()
}

var _ execv2.ExecServer = (*serverV2)(nil)
