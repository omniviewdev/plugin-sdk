package networker

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
)

const resourceDataWireField protowire.Number = 6

// preflightResource bounds the enclosing frame before allocating a protobuf Value tree.
func preflightResource(parent proto.Message, field protowire.Number, connection Connection) error {
	c, ok := resourceConnection(connection)
	if !ok {
		return validateFrame(parent)
	}
	size, err := resourceConnectionWireSize(c)
	if err != nil {
		return err
	}
	if proto.Size(
		parent,
	)+protowire.SizeTag(
		field,
	)+protowire.SizeBytes(
		size,
	) > resourcev2.MaxMessageBytes {
		return resourcev2.NewError("RESOURCE_LIMIT", "Port forward frame exceeds wire limit.")
	}
	return nil
}

func resourceConnectionWireSize(c PortForwardResourceConnection) (int, error) {
	body := c.ResourceData
	c.ResourceData = nil
	wire, err := c.ToProto()
	if err != nil {
		return 0, err
	}
	size := proto.Size(wire)
	if body != nil {
		n, err := resourcev2.ValueWireSize(*body)
		if err != nil {
			return 0, err
		}
		size += protowire.SizeTag(resourceDataWireField) + protowire.SizeBytes(n)
	}
	return size, nil
}

func preflightSessions(sessions []*PortForwardSession, resourceResponse bool) error {
	size := 0
	field := protowire.Number(1)
	if resourceResponse {
		size = 2
		field = 2
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		metadata := *session
		bodyBytes := 0
		if c, ok := resourceConnection(session.Connection); ok {
			if c.ResourceData != nil {
				n, err := resourcev2.ValueWireSize(*c.ResourceData)
				if err != nil {
					return err
				}
				bodyBytes = protowire.SizeTag(resourceDataWireField) + protowire.SizeBytes(n)
			}
			c.ResourceData = nil
			metadata.Connection = c
		}
		wire, err := metadata.ToProto()
		if err != nil {
			return err
		}
		sessionBytes := proto.Size(wire)
		if c := wire.GetResourceConnection(); c != nil {
			old := proto.Size(c)
			sessionBytes += protowire.SizeBytes(old+bodyBytes) - protowire.SizeBytes(old)
		}
		size += protowire.SizeTag(field) + protowire.SizeBytes(sessionBytes)
		if size > resourcev2.MaxMessageBytes {
			return resourcev2.NewError("RESOURCE_LIMIT", "Port forward response exceeds wire limit.")
		}
	}
	return nil
}

func strictNetworkJSON(data []byte, target any) error {
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return resourcev2.NewError("INVALID_REQUEST", "Expected JSON object.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return resourcev2.NewError("INVALID_REQUEST", err.Error())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return resourcev2.NewError("INVALID_REQUEST", "Trailing JSON input.")
	}
	return nil
}

// UnmarshalJSON rejects unknown fields and invalid resource references, routes,
// or lossless values before replacing the receiver.
func (c *PortForwardResourceConnection) UnmarshalJSON(data []byte) error {
	type plain PortForwardResourceConnection
	var out plain
	if err := strictNetworkJSON(data, &out); err != nil {
		return err
	}
	if err := validateResourceConnection(PortForwardResourceConnection(out)); err != nil {
		return err
	}
	*c = PortForwardResourceConnection(out)
	return nil
}

func validateResourceConnection(c PortForwardResourceConnection) error {
	metadata := c
	metadata.ResourceData = nil
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	if c.Reference != nil {
		if _, err := resourcev2.ToProtoResourceReference(*c.Reference); err != nil {
			return err
		}
		if c.PluginID != "" && c.PluginID != c.Reference.Address.Provider {
			return resourcev2.NewError("INVALID_REQUEST", "Resource provider conflicts with reference.")
		}
		if c.ResourceKey != "" && c.ResourceKey != c.Reference.Address.Type {
			return resourcev2.NewError("INVALID_REQUEST", "Resource type conflicts with reference.")
		}
	}
	if c.Route != nil {
		if _, err := resourcev2.ToProtoAccessRoute(*c.Route); err != nil {
			return err
		}
		if c.ConnectionID != "" && c.ConnectionID != c.Route.Connection {
			return resourcev2.NewError("INVALID_REQUEST", "Resource connection conflicts with route.")
		}
	}
	if c.ResourceData != nil {
		return resourcev2.ValidateValue(*c.ResourceData)
	}
	return nil
}

func validateOptionMetadata(o PortForwardSessionOptions) error {
	metadata := o
	metadata.Connection = nil
	if err := resourcev2.ValidateMetadata(metadata); err != nil {
		return err
	}
	switch c := o.Connection.(type) {
	case PortForwardResourceConnection:
		if o.ConnectionType != PortForwardConnectionTypeResource {
			return resourcev2.NewError("INVALID_REQUEST", "Resource connection requires RESOURCE type.")
		}
		return validateResourceConnection(c)
	case *PortForwardResourceConnection:
		if c == nil {
			return resourcev2.NewError("INVALID_REQUEST", "Resource connection must not be nil.")
		}
		if o.ConnectionType != PortForwardConnectionTypeResource {
			return resourcev2.NewError("INVALID_REQUEST", "Resource connection requires RESOURCE type.")
		}
		return validateResourceConnection(*c)
	default:
		if o.ConnectionType == PortForwardConnectionTypeResource {
			return resourcev2.NewError("INVALID_REQUEST", "RESOURCE type requires resource connection.")
		}
	}
	return nil
}

func validateResourceContext(ctx *types.PluginContext, o PortForwardSessionOptions) error {
	if err := validateOptionMetadata(o); err != nil {
		return err
	}
	c, ok := resourceConnection(o.Connection)
	if !ok {
		return resourcev2.NewError("INVALID_REQUEST", "Resource connection required.")
	}
	if ctx != nil && ctx.Connection != nil && c.Route != nil && c.Route.Connection != ctx.Connection.ID {
		return resourcev2.NewError("INVALID_REQUEST", "Resource route conflicts with trusted connection.")
	}
	return nil
}

func resourceConnection(c Connection) (PortForwardResourceConnection, bool) {
	switch c := c.(type) {
	case PortForwardResourceConnection:
		return c, true
	case *PortForwardResourceConnection:
		if c != nil {
			return *c, true
		}
	}
	return PortForwardResourceConnection{}, false
}

func validateFrame(message proto.Message) error {
	if proto.Size(message) > resourcev2.MaxMessageBytes {
		return resourcev2.NewError("RESOURCE_LIMIT", "Port forward frame exceeds wire limit.")
	}
	return nil
}

func cloneValue(v resourcev2.Value) resourcev2.Value {
	if v.List != nil {
		v.List = slices.Clone(v.List)
		for i := range v.List {
			v.List[i] = cloneValue(v.List[i])
		}
	}
	if v.Map != nil {
		cp := make(map[string]resourcev2.Value, len(v.Map))
		for k := range v.Map {
			cp[k] = cloneValue(v.Map[k])
		}
		v.Map = cp
	}
	return v
}

func cloneResourceConnection(c PortForwardResourceConnection) PortForwardResourceConnection {
	if c.ResourceData != nil {
		v := cloneValue(*c.ResourceData)
		c.ResourceData = &v
	}
	if c.Reference != nil {
		v := *c.Reference
		v.Address.Scope = slices.Clone(v.Address.Scope)
		if v.Incarnation != nil {
			token := *v.Incarnation
			v.Incarnation = &token
		}
		c.Reference = &v
	}
	if c.Route != nil {
		v := *c.Route
		c.Route = &v
	}
	return c
}

func validateResourceSession(s PortForwardSession) error {
	if _, ok := resourceConnection(s.Connection); !ok {
		return nil
	}
	if s.ConnectionType != PortForwardConnectionTypeResource || !s.Protocol.Valid() {
		return resourcev2.NewError("INVALID_REQUEST", "Invalid resource session type or protocol.")
	}
	switch s.State {
	case SessionStateActive, SessionStatePaused, SessionStateStopped, SessionStateFailed:
	default:
		return resourcev2.NewError("INVALID_REQUEST", "Invalid resource session state.")
	}
	return nil
}
