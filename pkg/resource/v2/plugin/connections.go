package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	resourcepb "github.com/omniviewdev/plugin-sdk/proto/resource/v2"
	"io"
	"time"
)

func (s *server) connection(ctx context.Context, in *resourcepb.ConnectionInput) (*resourcepb.ConnectionOutput, error) {
	if in == nil {
		return nil, invalid("missing connection operation")
	}
	if in.Operation == "close" {
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
	if !ok && in.Operation != "state" {
		return nil, unsupported("connection lifecycle unsupported")
	}
	var value any
	var err error
	isStatus := false
	if in.Operation != "load" && in.Id == "" {
		return nil, invalid("missing connection id")
	}
	switch in.Operation {
	case "state":
		readiness, supported := s.impl.(resourcev2.ConnectionReadinessProvider)
		if !supported {
			return nil, unsupported("registered connection readiness unsupported")
		}
		value, err = readiness.GetConnectionStatus(ctx, in.Id)
		isStatus = true
	case "load":
		if in.Id != "" {
			return nil, invalid("load does not accept connection id")
		}
		value, err = p.LoadConnections(ctx)
	case "get":
		value, err = p.GetConnection(ctx, in.Id)
	case "start":
		value, err = p.StartConnection(ctx, in.Id)
		isStatus = true
	case "stop":
		value, err = p.StopConnection(ctx, in.Id)
	case "check":
		value, err = p.CheckConnection(ctx, in.Id)
		isStatus = true
	default:
		return nil, unsupported("unknown connection operation")
	}
	if err != nil {
		return nil, err
	}
	if err = bindConnectionResult(in.Operation, in.Id, value); err != nil {
		return nil, err
	}
	if err = resourcev2.ValidateMetadata(value); err != nil {
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
	r, err := c.call(ctx, &resourcepb.Request{Operation: &resourcepb.Request_Connection{Connection: &resourcepb.ConnectionInput{Operation: operation, Id: id}}})
	if err != nil {
		return err
	}
	p := r.GetConnection()
	if p == nil {
		return invalid("unexpected connection result")
	}
	if operation == "close" {
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
	if operation == "load" || operation == "close" {
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
		if v.Connection != nil && v.Connection.ID == id {
			if operation == "state" {
				switch v.Status {
				case types.ConnectionStatusDisconnected, types.ConnectionStatusPending, types.ConnectionStatusConnected, types.ConnectionStatusFailed:
				default:
					return invalid("invalid registered connection state")
				}
				if v.Error != "" || v.Details != "" {
					return invalid("registered connection state contains diagnostics")
				}
			}
			return nil
		}
	case *types.ConnectionStatus:
		if v != nil {
			return bindConnectionResult(operation, id, *v)
		}
	}
	return invalid("connection response id mismatch")
}
func (c *Client) LoadConnections(ctx context.Context) ([]types.Connection, error) {
	var out []types.Connection
	err := c.connection(ctx, "load", "", &out, false)
	return out, err
}
func (c *Client) GetConnection(ctx context.Context, id string) (types.Connection, error) {
	var out types.Connection
	err := c.connection(ctx, "get", id, &out, false)
	return out, err
}
func (c *Client) StartConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, "start", id, &out, true)
	return out, err
}
func (c *Client) StopConnection(ctx context.Context, id string) (types.Connection, error) {
	var out types.Connection
	err := c.connection(ctx, "stop", id, &out, false)
	return out, err
}
func (c *Client) CheckConnection(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, "check", id, &out, true)
	return out, err
}

// GetConnectionStatus reports registered lifecycle state, never a health probe.
func (c *Client) GetConnectionStatus(ctx context.Context, id string) (types.ConnectionStatus, error) {
	var out types.ConnectionStatus
	err := c.connection(ctx, "state", id, &out, true)
	return out, err
}

const closeTimeout = 5 * time.Second

func (c *Client) Close() error { return c.closeWithTimeout(closeTimeout) }
func (c *Client) closeWithTimeout(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return c.connection(ctx, "close", "", nil, false)
}
