package exec

import (
	"errors"
	"maps"
	"slices"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execv2 "github.com/omniviewdev/plugin-sdk/proto/exec/v2"
)

// This file is the strict half of the request wire for the plugin: it decides whether an Open, a Start or a Run may
// reach an author's code. The checks of a request run in a fixed order, so that a request that breaks two rules is
// always refused by the same one. Each function says its order; the shape of it is the same everywhere:
//
//  1. the message itself: nil, a list longer than any request may carry (ClassInvalidRequest: it is not read), a field
//     this build does not know (ClassUnsupported);
//  2. the ids, the target reference and the registered target (absent: ClassUnsupported);
//  3. the bounds of the request alone, which every target shares (the validators of transport_v2_request.go);
//  4. what this target and this build serve, in the order the functions list.
//
// A refused request returns the zero request: nothing of it escapes. An admitted request is deep-copied, so that it
// never aliases the wire message, and is returned as it was sent: startRequest.request and runRequest.request fill the
// defaults.
//
// A request carries its environment, labels and selection as lists of NameValue, and the admitted request holds
// them as Go maps: a name that appears twice is refused (ClassInvalidRequest), never resolved.

// The terminal size a Start without one gets, when it asks for a terminal.
const (
	defaultStartCols uint16 = 80
	defaultStartRows uint16 = 24
)

// integrationRequest is the integration a Start asked for, as admission returns it.
type integrationRequest struct {
	// Mode is the mode asked for, never IntegrationOff: a request for no integration is a nil *integrationRequest.
	Mode IntegrationMode
	// Consent is the host's assertion that the user agreed to send bootstrap code; always true in an admitted request.
	Consent bool
}

// startRequest is a Start as admission returns it: decoded, bounded and deep-copied.
type startRequest struct {
	// Target is the canonical reference and the route; Selection holds the choices made from Options.
	Target    TargetRef
	Selection map[string]string
	Argv      []string
	TTY       bool
	Size      Size
	Env       map[string]string
	Cwd       string
	Labels    map[string]string
	// DetachTimeout is the lease of a detached session; zero means the target's default.
	DetachTimeout time.Duration
	// ReplayBytes is the replay ring of the session; zero means the target's default.
	ReplayBytes int
	// Features are the opt-in features the session asks for, each at most once: marks and modes.
	Features    []execv2.Feature
	Integration *integrationRequest
}

// attachRequest is an Attach as admission returns it.
type attachRequest struct {
	// FromOffset is the first output offset the host does not have.
	FromOffset uint64
}

// openRequest is the first frame of an Open as admission returns it: exactly one of Start and Attach is set.
type openRequest struct {
	SessionID string
	Start     *startRequest
	Attach    *attachRequest
}

// outputLimits are the output caps of a run as admission returns them. Zero means the target's default.
type outputLimits struct {
	MaxStdoutBytes int
	MaxStderrBytes int
	// Policy is the wire value: UNSPECIFIED is the target's default.
	Policy execv2.TruncatePolicy
}

// runRequest is a RunRequest as admission returns it: decoded, bounded and deep-copied.
type runRequest struct {
	RequestID string
	Target    TargetRef
	Selection map[string]string
	Argv      []string
	Stdin     []byte
	Env       map[string]string
	Cwd       string
	Timeout   time.Duration
	Output    outputLimits
	Labels    map[string]string
}

// featureSet is the set of features an SDK build serves, what Describe lists.
type featureSet map[execv2.Feature]struct{}

// knownFeature reports whether f is a feature of the contract: a named wire value other than UNSPECIFIED.
func knownFeature(f execv2.Feature) bool {
	_, named := execv2.Feature_name[int32(f)]
	return named && f != execv2.Feature_FEATURE_UNSPECIFIED
}

// newFeatureSet returns the set of fs. A value that is not a feature of the contract (unspecified, or a number of a
// newer contract) is never listed, as Describe never lists it.
func newFeatureSet(fs []execv2.Feature) featureSet {
	s := make(featureSet, len(fs))
	for _, f := range fs {
		if knownFeature(f) {
			s[f] = struct{}{}
		}
	}
	return s
}

// has reports whether the build serves f. A nil set serves nothing.
func (s featureSet) has(f execv2.Feature) bool {
	_, ok := s[f]
	return ok
}

// ownedStrings returns a copy of s that the caller owns, or nil when s is empty.
func ownedStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return slices.Clone(s)
}

// ownedBytes returns a copy of b that the caller owns, or nil when b is empty.
func ownedBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}

// lookupRequestTarget returns the target registered for the resource a reference names; no registry or no target
// for the provider and type is ClassUnsupported.
func lookupRequestTarget(reg *targetRegistry, ref TargetRef) (*registeredTarget, *Error) {
	address := ref.Reference.Address
	if reg != nil {
		if t, ok := reg.lookup(address.Provider, address.Type); ok {
			return t, nil
		}
	}
	return nil, unsupportedRequest("no exec target for %s/%s", address.Provider, address.Type)
}

// admitFeatures decodes the features a Start asks for. A valid list holds each requestable feature at most once, so
// the third element of any list is already a violation: reading three elements decides the whole list, however long
// it is. The result is nil for no features.
func admitFeatures(wire []execv2.Feature) ([]execv2.Feature, *Error) {
	var head [maxRequestedFeatures + 1]execv2.Feature
	n := min(len(wire), len(head))
	copy(head[:n], wire[:n])
	if refusal := validateRequestFeatures(head[:n]); refusal != nil {
		return nil, refusal
	}
	if n == 0 {
		return nil, nil
	}
	return slices.Clone(head[:n]), nil
}

// admitIntegration decodes the integration a Start asks for. No message, a message with no mode and a message with
// the mode NONE are no integration whatever the consent flag says (nil): NONE asks for nothing to be sent. Consent is
// checked before the mode is known.
func admitIntegration(p *execv2.IntegrationRequest) (*integrationRequest, *Error) {
	wire := p.GetMode()
	mode := enumFromProto(integrationModePairs, wire) // IntegrationOff for NONE and for a mode this build does not know
	requested := wire != execv2.IntegrationMode_INTEGRATION_MODE_UNSPECIFIED && wire != execv2.IntegrationMode_INTEGRATION_MODE_NONE
	if refusal := validateRequestIntegration(requested, mode != IntegrationOff, p.GetConsent()); refusal != nil {
		return nil, refusal
	}
	if !requested {
		return nil, nil
	}
	return &integrationRequest{Mode: mode, Consent: true}, nil
}

// admitStart is the admission of a Start against the registered target t, which must be the target registered for
// p's reference (admitOpenRequest guarantees it) and whose info is effective, and against the features the build
// serves. It returns the Start as it was sent, deep-copied.
//
// Order of the checks, the first violation decides: the message (nil, a list too long, unknown fields); the target
// reference; the request's own bounds (argv, env, cwd, labels, size, features, then integration: consent first, then
// a mode this build does not know); then the target and the build: the target serves sessions, the build serves
// sessions, an argv or a default, a terminal, a size with a terminal, the size limit, env and cwd honored by the
// target, env and cwd served by the build, the lease and the replay ring (parts of FEATURE_SESSION_MANAGEMENT: the
// build serves it, then the target's limits), the features served, and the integration mode listed by the target and
// served.
func admitStart(p *execv2.Start, t *registeredTarget, served featureSet) (startRequest, *Error) {
	if p == nil {
		return startRequest{}, invalidRequest("start is required")
	}
	if refusal := rejectUnknownFields(p); refusal != nil {
		return startRequest{}, refusal
	}
	ref, selection, refusal := admitTargetRef(p.GetTarget(), true)
	if refusal != nil {
		return startRequest{}, refusal
	}
	return admitStartBody(p, ref, selection, t, served)
}

// admitStartBody admits the parts of a Start that follow its target reference, which the caller admitted already
// (and, for an Open, looked the target up with). It is the second half of admitStart's order.
func admitStartBody(p *execv2.Start, ref TargetRef, selection map[string]string, t *registeredTarget, served featureSet) (startRequest, *Error) {
	if t == nil {
		return startRequest{}, unsupportedRequest("no exec target")
	}
	if _, refusal := validateRequestArgv(p.GetArgv(), defaultArgvBytes, false); refusal != nil {
		return startRequest{}, refusal
	}
	env, refusal := nameValueMap(p.GetEnv(), "env", maxEnvEntries)
	if refusal != nil {
		return startRequest{}, refusal
	}
	if _, refusal = validateRequestEnv(env, defaultEnvBytes); refusal != nil {
		return startRequest{}, refusal
	}
	if refusal = validateRequestCwd(p.GetCwd()); refusal != nil {
		return startRequest{}, refusal
	}
	labels, refusal := nameValueMap(p.GetLabels(), "labels", maxLabelEntries)
	if refusal != nil {
		return startRequest{}, refusal
	}
	if refusal = validateRequestLabels(labels); refusal != nil {
		return startRequest{}, refusal
	}
	size, refusal := requestSizeFromProto(p.GetSize())
	if refusal != nil {
		return startRequest{}, refusal
	}
	features, refusal := admitFeatures(p.GetFeatures())
	if refusal != nil {
		return startRequest{}, refusal
	}
	integration, refusal := admitIntegration(p.GetIntegration())
	if refusal != nil {
		return startRequest{}, refusal
	}
	if refusal = checkStartTarget(p, size, features, integration, t, served); refusal != nil {
		return startRequest{}, refusal
	}
	return startRequest{
		Target:        ref,
		Selection:     selection,
		Argv:          ownedStrings(p.GetArgv()),
		TTY:           p.GetTty(),
		Size:          size,
		Env:           env,
		Cwd:           p.GetCwd(),
		Labels:        labels,
		DetachTimeout: time.Duration(p.GetDetachTimeoutMs()) * time.Millisecond,
		ReplayBytes:   clampInt(p.GetReplayBytes()),
		Features:      features,
		Integration:   integration,
	}, nil
}

// checkStartTarget is the part of admitStart that depends on the target and the build; see admitStart for the order.
func checkStartTarget(p *execv2.Start, size Size, features []execv2.Feature, integration *integrationRequest, t *registeredTarget, served featureSet) *Error {
	info := &t.info
	caps := &info.Capabilities
	switch {
	case info.Session.IsZero():
		return unsupportedRequest("the target serves no sessions")
	case !served.has(execv2.Feature_FEATURE_SESSION):
		return unsupportedRequest("this build does not serve sessions")
	case len(p.GetArgv()) == 0 && len(info.DefaultArgv) == 0:
		return invalidRequest("argv is required: the target has no default")
	case p.GetTty() && !caps.TTY:
		return NewError(ClassUnsupported, ReasonTTYUnsupported, errors.New("the target cannot give a command a terminal"))
	case !size.IsZero() && !p.GetTty():
		return invalidRequest("a size needs a terminal")
	case int(size.Cols) > info.Session.MaxDimension || int(size.Rows) > info.Session.MaxDimension:
		return invalidRequest("a size dimension is above the target's limit of %d", info.Session.MaxDimension)
	case len(p.GetEnv()) > 0 && !caps.Env:
		return unsupportedRequest("the target does not honor env")
	case p.GetCwd() != "" && !caps.Cwd:
		return unsupportedRequest("the target does not honor cwd")
	case len(p.GetEnv()) > 0 && !served.has(execv2.Feature_FEATURE_ENV):
		return unsupportedRequest("this build does not serve env")
	case p.GetCwd() != "" && !served.has(execv2.Feature_FEATURE_CWD):
		return unsupportedRequest("this build does not serve cwd")
	case (p.GetDetachTimeoutMs() != 0 || p.GetReplayBytes() != 0) && !served.has(execv2.Feature_FEATURE_SESSION_MANAGEMENT):
		return unsupportedRequest("this build does not serve session management: a lease and a replay ring cannot be asked for")
	case time.Duration(p.GetDetachTimeoutMs())*time.Millisecond > info.Session.MaxDetachTimeout:
		return invalidRequest("the detach timeout is above the target's limit of %s", info.Session.MaxDetachTimeout)
	case clampInt(p.GetReplayBytes()) > info.Session.MaxReplayBytes:
		return invalidRequest("the replay ring is above the target's limit of %d bytes", info.Session.MaxReplayBytes)
	}
	for _, f := range features {
		if !served.has(f) {
			return unsupportedRequest("this build does not serve %s", f)
		}
	}
	if integration != nil && (!slices.Contains(caps.Integration, integration.Mode) || !served.has(execv2.Feature_FEATURE_INTEGRATION)) {
		return unsupportedRequest("the target does not accept, or this build does not serve, this integration mode")
	}
	return nil
}

// admitOpenRequest is the admission of the first frame of an Open. Order of the checks, the first violation decides:
// the message (nil, a list too long, unknown fields anywhere in the tree), the session id, exactly one of a start and
// an attach, and for a start the target reference, the registered target (absent: ClassUnsupported) and then
// admitStart's checks. An attach names no target, so nothing of the registry is asked: the engine finds the session.
// The build is asked: an attach is a part of FEATURE_SESSION_MANAGEMENT, which needs FEATURE_SESSION.
func admitOpenRequest(p *execv2.OpenRequest, reg *targetRegistry, served featureSet) (openRequest, *registeredTarget, *Error) {
	if p == nil {
		return openRequest{}, nil, invalidRequest("open request is required")
	}
	if refusal := rejectUnknownFields(p); refusal != nil {
		return openRequest{}, nil, refusal
	}
	if refusal := validateSessionID(p.GetSessionId()); refusal != nil {
		return openRequest{}, nil, refusal
	}
	start, attach := p.GetStart(), p.GetAttach()
	switch {
	case start == nil && attach == nil:
		return openRequest{}, nil, invalidRequest("an open request is a start or an attach")
	case attach != nil:
		switch {
		case !served.has(execv2.Feature_FEATURE_SESSION):
			return openRequest{}, nil, unsupportedRequest("this build does not serve sessions")
		case !served.has(execv2.Feature_FEATURE_SESSION_MANAGEMENT):
			return openRequest{}, nil, unsupportedRequest("this build does not serve session management: no session outlives its stream")
		}
		return openRequest{SessionID: p.GetSessionId(), Attach: &attachRequest{FromOffset: attach.GetFromOffset()}}, nil, nil
	}
	ref, selection, refusal := admitTargetRef(start.GetTarget(), true)
	if refusal != nil {
		return openRequest{}, nil, refusal
	}
	t, refusal := lookupRequestTarget(reg, ref)
	if refusal != nil {
		return openRequest{}, nil, refusal
	}
	body, refusal := admitStartBody(start, ref, selection, t, served)
	if refusal != nil {
		return openRequest{}, nil, refusal
	}
	return openRequest{SessionID: p.GetSessionId(), Start: &body}, t, nil
}

// admitRunRequest is the admission of a RunRequest. It returns the request as it was sent, deep-copied, and the
// registered target it runs in. Order of the checks, the first violation decides: the message (nil, a list too long,
// unknown fields); the request id; the target reference; the registered target (absent: ClassUnsupported); the
// request's own bounds (argv required, env, cwd, labels, the output policy: a value this build does not know is
// ClassUnsupported); then the target and the build: the target serves runs, the build serves runs, the argv total,
// stdin (ClassLimit), the env total, env and cwd honored by the target, env and cwd served by the build, the timeout
// and the output caps. The timeout and the caps are never clamped.
func admitRunRequest(p *execv2.RunRequest, reg *targetRegistry, served featureSet) (runRequest, *registeredTarget, *Error) {
	if p == nil {
		return runRequest{}, nil, invalidRequest("run request is required")
	}
	if refusal := rejectUnknownFields(p); refusal != nil {
		return runRequest{}, nil, refusal
	}
	if refusal := validateRequestID(p.GetRequestId()); refusal != nil {
		return runRequest{}, nil, refusal
	}
	ref, selection, refusal := admitTargetRef(p.GetTarget(), true)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	t, refusal := lookupRequestTarget(reg, ref)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	argvTotal, refusal := validateRequestArgv(p.GetArgv(), maxArgvBytesCap, true)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	env, refusal := nameValueMap(p.GetEnv(), "env", maxEnvEntries)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	envTotal, refusal := validateRequestEnv(env, maxEnvBytesCap)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	if refusal = validateRequestCwd(p.GetCwd()); refusal != nil {
		return runRequest{}, nil, refusal
	}
	labels, refusal := nameValueMap(p.GetLabels(), "labels", maxLabelEntries)
	if refusal != nil {
		return runRequest{}, nil, refusal
	}
	if refusal = validateRequestLabels(labels); refusal != nil {
		return runRequest{}, nil, refusal
	}
	policy := p.GetOutput().GetPolicy()
	if _, named := execv2.TruncatePolicy_name[int32(policy)]; !named {
		return runRequest{}, nil, unsupportedRequest("the output policy is not a value this build knows")
	}
	if refusal = checkRunTarget(p, argvTotal, envTotal, t, served); refusal != nil {
		return runRequest{}, nil, refusal
	}
	return runRequest{
		RequestID: p.GetRequestId(),
		Target:    ref,
		Selection: selection,
		Argv:      ownedStrings(p.GetArgv()),
		Stdin:     ownedBytes(p.GetStdin()),
		Env:       env,
		Cwd:       p.GetCwd(),
		Timeout:   time.Duration(p.GetTimeoutMs()) * time.Millisecond,
		Output: outputLimits{
			MaxStdoutBytes: clampInt(p.GetOutput().GetMaxStdoutBytes()),
			MaxStderrBytes: clampInt(p.GetOutput().GetMaxStderrBytes()),
			Policy:         policy,
		},
		Labels: labels,
	}, t, nil
}

// checkRunTarget is the part of admitRunRequest that depends on the target and the build; see admitRunRequest for
// the order. argvTotal and envTotal are the sizes the request's own checks measured.
func checkRunTarget(p *execv2.RunRequest, argvTotal, envTotal int, t *registeredTarget, served featureSet) *Error {
	info := &t.info
	caps := &info.Capabilities
	run := &info.Run
	switch {
	case run.IsZero():
		return unsupportedRequest("the target serves no runs")
	case !served.has(execv2.Feature_FEATURE_RUN):
		return unsupportedRequest("this build does not serve runs")
	case argvTotal > run.MaxArgvBytes:
		return invalidRequest("argv is more than the target's limit of %d bytes", run.MaxArgvBytes)
	case len(p.GetStdin()) > run.MaxStdinBytes:
		return NewError(ClassLimit, ReasonInputTooLarge, errors.New("stdin is more than the target's limit"))
	case envTotal > run.MaxEnvBytes:
		return invalidRequest("env is more than the target's limit of %d bytes", run.MaxEnvBytes)
	case len(p.GetEnv()) > 0 && !caps.Env:
		return unsupportedRequest("the target does not honor env")
	case p.GetCwd() != "" && !caps.Cwd:
		return unsupportedRequest("the target does not honor cwd")
	case len(p.GetEnv()) > 0 && !served.has(execv2.Feature_FEATURE_ENV):
		return unsupportedRequest("this build does not serve env")
	case p.GetCwd() != "" && !served.has(execv2.Feature_FEATURE_CWD):
		return unsupportedRequest("this build does not serve cwd")
	case time.Duration(p.GetTimeoutMs())*time.Millisecond > run.MaxTimeout:
		return invalidRequest("the timeout is above the target's limit of %s", run.MaxTimeout)
	case clampInt(p.GetOutput().GetMaxStdoutBytes()) > run.MaxOutputBytes,
		clampInt(p.GetOutput().GetMaxStderrBytes()) > run.MaxOutputBytes:
		return invalidRequest("an output cap is above the target's limit of %d bytes", run.MaxOutputBytes)
	}
	return nil
}

// cloneRequestReference returns a copy of a reference that shares no storage with r.
func cloneRequestReference(r resourcev2.ResourceReference) resourcev2.ResourceReference {
	r.Address.Scope = slices.Clone(r.Address.Scope)
	if r.Incarnation != nil {
		incarnation := *r.Incarnation
		r.Incarnation = &incarnation
	}
	return r
}

// ownedStringMap returns a copy of m that the caller owns, or nil when m is empty.
func ownedStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return maps.Clone(m)
}

// requestArgv returns the argv a command runs: the request's own, or the target's default when it sent none. The
// result is a copy.
func requestArgv(argv []string, t *registeredTarget) []string {
	if len(argv) == 0 && t != nil {
		argv = t.info.DefaultArgv
	}
	return ownedStrings(argv)
}

// request returns the validated Request the author's code receives for an admitted Start on the registered target t:
// the target's default argv when the Start has none, a terminal of 80 x 24 when it asks for one without a size, and
// no size without a terminal. Everything is copied.
func (s startRequest) request(t *registeredTarget) Request {
	var size Size
	if s.TTY {
		size = s.Size
		if size.IsZero() {
			size = Size{Cols: defaultStartCols, Rows: defaultStartRows}
		}
	}
	return Request{
		Ref:       cloneRequestReference(s.Target.Reference),
		Selection: ownedStringMap(s.Selection),
		Argv:      requestArgv(s.Argv, t),
		TTY:       s.TTY,
		Size:      size,
		Env:       ownedStringMap(s.Env),
		Cwd:       s.Cwd,
		Labels:    ownedStringMap(s.Labels),
	}
}

// request returns the validated Request the author's code receives for an admitted runRequest on the registered
// target t: a command without a terminal and without a size. Everything is copied.
func (r runRequest) request(t *registeredTarget) Request {
	return Request{
		Ref:       cloneRequestReference(r.Target.Reference),
		Selection: ownedStringMap(r.Selection),
		Argv:      requestArgv(r.Argv, t),
		Env:       ownedStringMap(r.Env),
		Cwd:       r.Cwd,
		Labels:    ownedStringMap(r.Labels),
	}
}
