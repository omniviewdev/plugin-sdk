package exec

import (
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	execpb "github.com/omniviewdev/plugin-sdk/proto/v1/exec"
)

// sessionOptionMetadata deliberately excludes the tagged body: metadata and
// Value have separate bounds, so reflection bookkeeping cannot shrink Value's
// admitted node budget or convert its scalar types.
type sessionOptionMetadata struct {
	Params         map[string]string
	Labels         map[string]string
	ID             string
	ResourcePlugin string
	ResourceKey    string
	Reference      *resourcev2.ResourceReference
	Route          *resourcev2.AccessRoute
	Command        []string
	TTY            bool
}

func optionError(code, detail string) error { return resourcev2.NewError(code, detail) }

// ValidateSessionOptions bounds and checks ordinary options and optional C01
// inputs without resolving a connection or acquiring resource data.
func ValidateSessionOptions(o SessionOptions) error {
	metadata := sessionOptionMetadata{
		o.Params,
		o.Labels,
		o.ID,
		o.ResourcePlugin,
		o.ResourceKey,
		o.Reference,
		o.Route,
		o.Command,
		o.TTY,
	}
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	if o.Reference != nil {
		if err := resourcev2.ValidateAddress(o.Reference.Address); err != nil {
			return err
		}
		if o.Reference.Incarnation != nil && *o.Reference.Incarnation == "" {
			return optionError("INVALID_REQUEST", "Resource incarnation must not be empty.")
		}
		if o.Route == nil {
			return optionError("INVALID_REQUEST", "An explicit route is required with a resource reference.")
		}
		if o.ResourcePlugin != "" && o.ResourcePlugin != o.Reference.Address.Provider {
			return optionError("INVALID_REQUEST", "Resource provider selector conflicts with reference.")
		}
		if o.ResourceKey != "" && o.ResourceKey != o.Reference.Address.Type {
			return optionError("INVALID_REQUEST", "Resource type selector conflicts with reference.")
		}
	}
	if o.Route != nil && o.Route.Connection == "" {
		return optionError("INVALID_REQUEST", "Resource route requires an explicit connection.")
	}
	if o.ResourceData != nil {
		if o.ResourceData.Kind != "map" {
			return optionError("INVALID_REQUEST", "Exec resource data must be a tagged map.")
		}
		if err := resourcev2.ValidateValue(*o.ResourceData); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeResourceSessionOptions derives only redundant opaque selectors from
// an actual supplied reference. It never invents a reference, route, authority,
// body, native scope, or a Get permission requirement.
func NormalizeResourceSessionOptions(o SessionOptions) (SessionOptions, error) {
	if err := ValidateSessionOptions(o); err != nil {
		return SessionOptions{}, err
	}
	if !o.HasResourceIntent() {
		return SessionOptions{}, optionError(
			"INVALID_REQUEST",
			"Resource session requires an explicit resource target.",
		)
	}
	if o.Reference != nil {
		o.ResourcePlugin = o.Reference.Address.Provider
		o.ResourceKey = o.Reference.Address.Type
	}
	if o.ResourcePlugin == "" || o.ResourceKey == "" {
		return SessionOptions{}, optionError(
			"INVALID_REQUEST",
			"Resource session requires explicit provider and type selectors.",
		)
	}
	return o, nil
}

func ValidateResourceSessionOptions(o SessionOptions) error {
	_, err := NormalizeResourceSessionOptions(o)
	return err
}

func (o *SessionOptions) ToProto() (*execpb.SessionOptions, error) {
	if o == nil {
		return nil, optionError("INVALID_REQUEST", "Session options must not be nil.")
	}
	opts := *o
	var err error
	if opts.HasResourceIntent() {
		opts, err = NormalizeResourceSessionOptions(opts)
	} else {
		err = ValidateSessionOptions(opts)
	}
	if err != nil {
		return nil, err
	}
	out := &execpb.SessionOptions{
		Id:             opts.ID,
		Command:        opts.Command,
		Tty:            opts.TTY,
		Params:         opts.Params,
		Labels:         opts.Labels,
		ResourcePlugin: opts.ResourcePlugin,
		ResourceKey:    opts.ResourceKey,
	}
	if opts.HasResourceIntent() {
		out.ResourceWireVersion = ResourceSessionWireVersion
	}
	if opts.Reference != nil {
		out.Reference, err = resourcev2.ToProtoResourceReference(*opts.Reference)
		if err != nil {
			return nil, err
		}
	}
	if opts.Route != nil {
		out.Route, err = resourcev2.ToProtoAccessRoute(*opts.Route)
		if err != nil {
			return nil, err
		}
	}
	// Preflight the complete encoded frame before allocating a second Value tree.
	frameBytes := proto.Size(out)
	if opts.ResourceData != nil {
		valueBytes, err := resourcev2.ValueWireSize(*opts.ResourceData)
		if err != nil {
			return nil, err
		}
		frameBytes += protowire.SizeTag(9) + protowire.SizeBytes(valueBytes)
	}
	if frameBytes > resourcev2.MaxMessageBytes {
		return nil, optionError("RESOURCE_LIMIT", "Exec resource frame exceeds wire limit.")
	}
	if opts.ResourceData != nil {
		out.ResourceData, err = resourcev2.ToProtoValue(*opts.ResourceData)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// retiredExecOptionField recognizes the old Struct payload even for direct Go
// adapter callers. Unknown resource-option fields are never silently dropped.
func retiredExecOptionField(data []byte) (bool, error) {
	if len(data) > resourcev2.MaxMessageBytes {
		return false, optionError("RESOURCE_LIMIT", "Exec unknown fields exceed wire limit.")
	}
	fields := 0
	for len(data) > 0 {
		fields++
		if fields > resourcev2.MaxWireFields {
			return false, optionError("RESOURCE_LIMIT", "Exec unknown field limit.")
		}
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 || number <= 0 || kind == protowire.StartGroupType || kind == protowire.EndGroupType {
			return false, optionError("INVALID_REQUEST", "Malformed exec option field.")
		}
		if number >= 8 && number <= 12 {
			return true, nil
		}
		data = data[n:]
		n = protowire.ConsumeFieldValue(number, kind, data)
		if n < 0 {
			return false, optionError("INVALID_REQUEST", "Malformed exec option field.")
		}
		data = data[n:]
	}
	return false, nil
}

// NewSessionOptionsFromProto has no Struct or float64 fallback. Resource intent
// selects wire version two before body conversion; old nonresource options keep
// their ordinary terminal path.
func NewSessionOptionsFromProto(p *execpb.SessionOptions) (*SessionOptions, error) {
	if p == nil {
		return nil, optionError("INVALID_REQUEST", "Session options must not be nil.")
	}
	unknown := p.ProtoReflect().GetUnknown()
	retired, err := retiredExecOptionField(unknown)
	if err != nil {
		return nil, err
	}
	if retired {
		return nil, optionError("UNSUPPORTED", "Legacy or unknown resource exec option is unsupported.")
	}
	resourceIntent := p.ResourceData != nil || p.ResourcePlugin != "" || p.ResourceKey != "" || p.Reference != nil ||
		p.Route != nil ||
		p.ResourceWireVersion != 0
	if resourceIntent && p.ResourceWireVersion != ResourceSessionWireVersion {
		return nil, optionError("UNSUPPORTED", "Resource exec wire version is unsupported.")
	}
	if resourceIntent && len(unknown) > 0 {
		return nil, optionError("UNSUPPORTED", "Unknown resource exec option field.")
	}
	opts := SessionOptions{
		ID:             p.Id,
		Command:        p.Command,
		TTY:            p.Tty,
		Params:         p.Params,
		Labels:         p.Labels,
		ResourcePlugin: p.ResourcePlugin,
		ResourceKey:    p.ResourceKey,
	}
	metadata := sessionOptionMetadata{
		Params:         opts.Params,
		Labels:         opts.Labels,
		ID:             opts.ID,
		ResourcePlugin: opts.ResourcePlugin,
		ResourceKey:    opts.ResourceKey,
		Command:        opts.Command,
		TTY:            opts.TTY,
	}
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return nil, err
	}
	if p.Reference != nil {
		reference, err := resourcev2.FromProtoResourceReference(p.Reference)
		if err != nil {
			return nil, err
		}
		opts.Reference = &reference
	}
	if p.Route != nil {
		route, err := resourcev2.FromProtoAccessRoute(p.Route)
		if err != nil {
			return nil, err
		}
		opts.Route = &route
	}
	if p.ResourceData != nil {
		body, err := resourcev2.FromProtoValue(p.ResourceData)
		if err != nil {
			return nil, err
		}
		opts.ResourceData = &body
	}
	if resourceIntent {
		opts, err = NormalizeResourceSessionOptions(opts)
	} else {
		err = ValidateSessionOptions(opts)
	}
	if err != nil {
		return nil, err
	}
	if proto.Size(p) > resourcev2.MaxMessageBytes {
		return nil, optionError("RESOURCE_LIMIT", "Exec frame exceeds wire limit.")
	}
	return &opts, nil
}
