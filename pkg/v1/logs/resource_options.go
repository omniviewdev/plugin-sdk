package logs

import (
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

// ResourceSessionWireVersion identifies the canonical resource log-session wire contract.
const ResourceSessionWireVersion = resourcev2.WireVersion

// ResourceSessionProvider extends the existing log lifecycle without converting
// resource bodies through JSON maps or Struct. Resource data remains a canonical
// Value; references and routes are optional actual caller bindings. Resource
// creation never falls back to the legacy CreateSession RPC.
// Admission inherits creation cancellation; accepted native callbacks retain its
// values under the native-generation lifetime. A missing serialized plugin
// context does not create a connection or authority; explicit routes still
// require a matching connection supplied through the caller's plugin metadata.
// SessionID correlates acceptance and cleanup only; it confers no authority and
// does not make creation idempotent. Owners must close the chosen ID after a
// failed or lost response. Manager retains the generation until producers drain.
type ResourceSessionProvider interface {
	CreateResourceSession(*types.PluginContext, CreateSessionOptions) (*LogSession, error)
}

func resourceError(code, detail string) error { return resourcev2.NewError(code, detail) }

// HasResourceIntent reports whether options must use the resource-aware creation path.
func (o CreateSessionOptions) HasResourceIntent() bool {
	return o.SessionID != "" || o.ResourceData != nil || o.Reference != nil || o.Route != nil || o.ResourceKey != "" ||
		o.ResourceID != ""
}

// Validate checks canonical metadata and values without requiring a live connection.
func (o CreateSessionOptions) Validate() error {
	if o.SessionID != "" {
		if _, err := uuid.Parse(o.SessionID); err != nil {
			return resourceError("INVALID_REQUEST", "Resource session ID must be a UUID.")
		}
	}
	metadata := struct {
		ResourceKey, ResourceID string
		Reference               *resourcev2.ResourceReference
		Route                   *resourcev2.AccessRoute
		Options                 LogSessionOptions
	}{o.ResourceKey, o.ResourceID, o.Reference, o.Route, o.Options}
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	if o.Reference != nil {
		if err := resourcev2.ValidateAddress(o.Reference.Address); err != nil {
			return err
		}
		if o.Reference.Incarnation != nil && *o.Reference.Incarnation == "" {
			return resourceError("INVALID_REQUEST", "Resource incarnation must not be empty.")
		}
		if o.Route == nil {
			return resourceError("INVALID_REQUEST", "Resource reference requires an explicit route.")
		}
		if o.ResourceKey != "" && o.ResourceKey != o.Reference.Address.Type {
			return resourceError("INVALID_REQUEST", "Resource selector conflicts with reference.")
		}
	}
	if o.Route != nil && o.Route.Connection == "" {
		return resourceError("INVALID_REQUEST", "Resource route requires a connection.")
	}
	if o.ResourceData != nil {
		return resourcev2.ValidateValue(*o.ResourceData)
	}
	return nil
}

func validateRoute(ctx *types.PluginContext, o CreateSessionOptions) error {
	if o.Route != nil && (ctx == nil || ctx.Connection == nil || ctx.Connection.ID != o.Route.Connection) {
		return resourceError("INVALID_REQUEST", "Resource route must match trusted connection.")
	}
	return nil
}

// ToProto validates options and bounds the encoded frame before allocating the resource body.
func (o CreateSessionOptions) ToProto() (*logspb.CreateLogSessionRequest, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	p := &logspb.CreateLogSessionRequest{
		SessionId:   o.SessionID,
		ResourceKey: o.ResourceKey,
		ResourceId:  o.ResourceID,
		Options:     o.Options.ToProto(),
	}
	if o.HasResourceIntent() {
		p.ResourceWireVersion = ResourceSessionWireVersion
	}
	var err error
	if o.Reference != nil {
		p.Reference, err = resourcev2.ToProtoResourceReference(*o.Reference)
		if err != nil {
			return nil, err
		}
	}
	if o.Route != nil {
		p.Route, err = resourcev2.ToProtoAccessRoute(*o.Route)
		if err != nil {
			return nil, err
		}
	}
	size := proto.Size(p)
	if o.ResourceData != nil {
		n, e := resourcev2.ValueWireSize(*o.ResourceData)
		if e != nil {
			return nil, e
		}
		const resourceDataField protowire.Number = 5
		size += protowire.SizeTag(resourceDataField) + protowire.SizeBytes(n)
	}
	if size > resourcev2.MaxMessageBytes {
		return nil, resourceError("RESOURCE_LIMIT", "Logs frame exceeds wire limit.")
	}
	if o.ResourceData != nil {
		p.ResourceData, err = resourcev2.ToProtoValue(*o.ResourceData)
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

// CreateSessionOptionsFromProto rejects unknown fields and unsupported versions before decoding canonical values.
// Optional reference, route, and body presence survives conversion.
func CreateSessionOptionsFromProto(p *logspb.CreateLogSessionRequest) (CreateSessionOptions, error) {
	var o CreateSessionOptions
	if err := validateSessionRequest(p); err != nil {
		return o, err
	}
	o.ResourceKey, o.ResourceID, o.Options = p.ResourceKey, p.ResourceId, LogSessionOptionsFromProto(p.Options)
	o.SessionID = p.SessionId
	if err := o.Validate(); err != nil {
		return o, err
	}
	if p.Options != nil && p.Options.SinceTime != nil {
		if err := p.Options.SinceTime.CheckValid(); err != nil {
			return o, resourceError("INVALID_REQUEST", "Invalid logs since timestamp.")
		}
	}
	if p.Reference != nil {
		v, e := resourcev2.FromProtoResourceReference(p.Reference)
		if e != nil {
			return o, e
		}
		o.Reference = &v
	}
	if p.Route != nil {
		v, e := resourcev2.FromProtoAccessRoute(p.Route)
		if e != nil {
			return o, e
		}
		o.Route = &v
	}
	if err := o.Validate(); err != nil {
		return o, err
	}
	if p.ResourceData != nil {
		v, e := resourcev2.FromProtoValue(p.ResourceData)
		if e != nil {
			return o, e
		}
		o.ResourceData = &v
	}
	return o, o.Validate()
}

func validateSessionRequest(p *logspb.CreateLogSessionRequest) error {
	if p == nil {
		return resourceError("INVALID_REQUEST", "Request must not be nil.")
	}
	if proto.Size(p) > resourcev2.MaxMessageBytes {
		return resourceError("RESOURCE_LIMIT", "Logs frame exceeds wire limit.")
	}
	if len(p.ProtoReflect().GetUnknown()) != 0 {
		return resourceError("UNSUPPORTED", "Unknown or retired logs resource field.")
	}
	intent := p.ResourceKey != "" || p.ResourceId != "" || p.ResourceData != nil ||
		p.Reference != nil || p.Route != nil || p.ResourceWireVersion != 0 || p.SessionId != ""
	if intent && p.ResourceWireVersion != ResourceSessionWireVersion {
		return resourceError("UNSUPPORTED", "Unsupported logs resource wire version.")
	}
	return nil
}

func validateResourceSessionResult(s *LogSession) error {
	if s == nil || s.ID == "" {
		return resourceError("PROVIDER_ERROR", "Logs response requires a session ID.")
	}
	if err := s.ToProto().CreatedAt.CheckValid(); err != nil {
		return resourceError("PROVIDER_ERROR", "Invalid logs session timestamp.")
	}
	return resourcev2.ValidateMetadata(s)
}
