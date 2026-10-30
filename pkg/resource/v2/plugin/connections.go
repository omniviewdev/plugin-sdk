package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
)

const (
	connectionCloseOperation = "close"
	connectionStateOperation = "state"
	connectionLoadOperation  = "load"
)

func (s *server) connection(ctx context.Context, in *resourcepb.ConnectionInput) (*resourcepb.ConnectionOutput, error) {
	if in == nil {
		return nil, invalid("missing connection operation")
	}
	if in.Operation == connectionCloseOperation {
		p, ok := s.impl.(resourcev2.Closer)
		if !ok {
			return nil, unsupported("close unsupported")
		}
		if in.Id != "" {
			return nil, invalid("close does not accept connection id")
		}
		return &resourcepb.ConnectionOutput{}, p.Close()
	}
	p, ok := s.impl.(resourcev2.ConnectionLifecycle)
	if !ok && in.Operation != connectionStateOperation {
		return nil, unsupported("connection lifecycle unsupported")
	}
	value, isStatus, err := invokeConnection(ctx, s.impl, p, in)
	if err != nil {
		return nil, err
	}
	return encodeConnectionResult(in, value, isStatus)
}

func invokeConnection(
	ctx context.Context,
	impl resourcev2.Provider,
	p resourcev2.ConnectionLifecycle,
	in *resourcepb.ConnectionInput,
) (any, bool, error) {
	if in.Operation != connectionLoadOperation && in.Id == "" {
		return nil, false, invalid("missing connection id")
	}
	switch in.Operation {
	case connectionStateOperation:
		readiness, supported := impl.(resourcev2.ConnectionReadinessProvider)
		if !supported {
			return nil, false, unsupported("registered connection readiness unsupported")
		}
		value, err := readiness.GetConnectionStatus(ctx, in.Id)
		return value, true, err
	case connectionLoadOperation:
		if in.Id != "" {
			return nil, false, invalid("load does not accept connection id")
		}
		value, err := p.LoadConnections(ctx)
		return value, false, err
	case "get":
		value, err := p.GetConnection(ctx, in.Id)
		return value, false, err
	case "start":
		value, err := p.StartConnection(ctx, in.Id)
		return value, true, err
	case "stop":
		value, err := p.StopConnection(ctx, in.Id)
		return value, false, err
	case "check":
		value, err := p.CheckConnection(ctx, in.Id)
		return value, true, err
	default:
		return nil, false, unsupported("unknown connection operation")
	}
}

func encodeConnectionResult(
	in *resourcepb.ConnectionInput,
	value any,
	isStatus bool,
) (*resourcepb.ConnectionOutput, error) {
	if err := bindConnectionResult(in.Operation, in.Id, value); err != nil {
		return nil, err
	}
	if err := resourcev2.ValidateMetadata(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("invalid connection JSON")
	}
	if err = resourcev2.ValidateJSON(data); err != nil {
		return nil, err
	}
	out := &resourcepb.ConnectionOutput{}
	if isStatus {
		out.StatusJson = data
	} else {
		out.ConnectionJson = data
	}
	return out, nil
}

func decodeConnection(data []byte, out any) error {
	if err := resourcev2.ValidateJSON(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return invalid("invalid connection JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return invalid("trailing connection JSON")
	}
	return resourcev2.ValidateMetadata(out)
}

func (c *Client) connection(ctx context.Context, operation, id string, out any, isStatus bool) error {
	r, err := c.call(
		ctx,
		&resourcepb.Request{
			Operation: &resourcepb.Request_Connection{
				Connection: &resourcepb.ConnectionInput{Operation: operation, Id: id},
			},
		},
	)
	if err != nil {
		return err
	}
	p := r.GetConnection()
	if p == nil {
		return invalid("unexpected connection result")
	}
	if operation == connectionCloseOperation {
		if len(p.ConnectionJson) != 0 || len(p.StatusJson) != 0 {
			return invalid("unexpected close payload")
		}
		return nil
	}
	if isStatus {
		if len(p.ConnectionJson) != 0 {
			return invalid("unexpected connection payload")
		}
		err = decodeConnection(p.StatusJson, out)
	} else {
		if len(p.StatusJson) != 0 {
			return invalid("unexpected status payload")
		}
		err = decodeConnection(p.ConnectionJson, out)
	}
	if err != nil {
		return err
	}
	return bindConnectionResult(operation, id, out)
}

func bindConnectionResult(operation, id string, result any) error {
	if operation == connectionLoadOperation || operation == connectionCloseOperation {
		return nil
	}
	switch v := result.(type) {
	case types.Connection:
		if v.ID == id {
			return nil
		}
	case *types.Connection:
		if v != nil && v.ID == id {
			return nil
		}
	case types.ConnectionStatus:
		return bindConnectionStatus(operation, id, v)
	case *types.ConnectionStatus:
		if v != nil {
			return bindConnectionResult(operation, id, *v)
		}
	}
	return invalid("connection response id mismatch")
}

func bindConnectionStatus(operation, id string, status types.ConnectionStatus) error {
	if status.Connection == nil || status.Connection.ID != id {
		return invalid("connection response id mismatch")
	}
	if operation != connectionStateOperation {
		return nil
	}
	switch status.Status {
	case types.ConnectionStatusDisconnected,
		types.ConnectionStatusPending,
		types.ConnectionStatusConnected,
		types.ConnectionStatusFailed:
	case types.ConnectionStatusUnknown,
		types.ConnectionStatusError,
		types.ConnectionStatusUnauthorized,
		types.ConnectionStatusForbidden,
		types.ConnectionStatusBadRequest,
		types.ConnectionStatusNotFound,
		types.ConnectionStatusTimeout,
		types.ConnectionStatusUnavailable,
		types.ConnectionStatusRequestEntityTooLarge:
		return invalid("invalid registered connection state")
	default:
		return invalid("invalid registered connection state")
	}
	if status.Error != "" || status.Details != "" {
		return invalid("registered connection state contains diagnostics")
	}
	return nil
}

// LoadConnections discovers configured profiles without starting native clients.
func (c *Client) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	var out []types.Connection
	err := c.connection(ctx, connectionLoadOperation, "", &out, false)
	return out, err
}

// GetConnection returns one configured profile without starting it.
func (c *Client) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	var out types.Connection
	err := c.connection(ctx, "get", id, &out, false)
	return out, err
}

// StartConnection starts the selected configured generation.
func (c *Client) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, "start", id, &out, true)
	return out, err
}

// StopConnection retires the selected generation and returns its profile.
func (c *Client) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	var out types.Connection
	err := c.connection(ctx, "stop", id, &out, false)
	return out, err
}

// CheckConnection performs the provider's optional health check.
func (c *Client) CheckConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, "check", id, &out, true)
	return out, err
}

// GetConnectionStatus reports registered lifecycle state, never a health probe.
func (c *Client) GetConnectionStatus(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, connectionStateOperation, id, &out, true)
	return out, err
}

const closeTimeout = 5 * time.Second

// Close retires remote provider resources within the transport close timeout.
func (c *Client) Close() error { return c.closeWithTimeout(closeTimeout) }

func (c *Client) closeWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.connection(ctx, connectionCloseOperation, "", nil, false)
}
