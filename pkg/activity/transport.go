package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	pb "github.com/omniviewdev/plugin-sdk/proto/v1/activity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const Version uint32 = 1

var ErrUnavailable = errors.New("activity reporter is unavailable")

// Binding owns exactly one callback connection for the plugin process lifetime.
// It does not use delegated host connection grants or caller-supplied identity.
type Binding struct {
	mu    sync.RWMutex
	conn  *grpc.ClientConn
	bound bool
}

func (b *Binding) Bind(conn *grpc.ClientConn) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bound || conn == nil {
		return ErrUnavailable
	}
	b.conn = conn
	b.bound = true
	return nil
}

func (b *Binding) Close() error {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	b.bound = true
	b.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (b *Binding) call(ctx context.Context, operation string, request any) (Record, error) {
	if b == nil {
		return Record{}, ErrUnavailable
	}
	b.mu.RLock()
	conn := b.conn
	b.mu.RUnlock()
	if conn == nil {
		return Record{}, ErrUnavailable
	}
	if err := ValidateRequest(operation, request); err != nil {
		return Record{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return Record{}, err
	}
	response, err := pb.NewActivityReporterClient(conn).Report(ctx, &pb.ReportRequest{
		Version:   Version,
		Operation: operation,
		Payload:   payload,
	})
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := decode(response.Record, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (b *Binding) BeginTask(ctx context.Context, v Spec) (Record, error) {
	return b.call(ctx, "begin-task", v)
}

func (b *Binding) ReportTask(ctx context.Context, v ReportTaskRequest) (Record, error) {
	return b.call(ctx, "report-task", v)
}

func (b *Binding) FinishTask(ctx context.Context, v FinishTaskRequest) (Record, error) {
	return b.call(ctx, "finish-task", v)
}

func (b *Binding) PublishMessage(ctx context.Context, v Spec) (Record, error) {
	return b.call(ctx, "publish-message", v)
}

func (b *Binding) UpsertAlert(ctx context.Context, v AlertRequest) (Record, error) {
	return b.call(ctx, "upsert-alert", v)
}

func (b *Binding) ResolveAlert(ctx context.Context, v ResolveAlertRequest) (Record, error) {
	return b.call(ctx, "resolve-alert", v)
}

type reporterServer struct {
	pb.UnimplementedActivityReporterServer
	reporter Reporter
}

func RegisterServer(reg grpc.ServiceRegistrar, reporter Reporter) {
	pb.RegisterActivityReporterServer(reg, &reporterServer{reporter: reporter})
}

func decode(data []byte, out any) error {
	if len(data) > 262144 {
		return errors.New("activity payload exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing activity payload")
	}
	return nil
}

func (s *reporterServer) Report(ctx context.Context, req *pb.ReportRequest) (*pb.ReportResponse, error) {
	if req.Version != Version {
		return nil, status.Error(codes.FailedPrecondition, "Unsupported activity version.")
	}
	var record Record
	var err error
	invoke := func(v any, call func() (Record, error)) {
		if err = decode(req.Payload, v); err != nil {
			return
		}
		if err = ValidateRequest(req.Operation, indirect(v)); err != nil {
			return
		}
		record, err = call()
	}
	switch req.Operation {
	case "begin-task":
		var v Spec
		invoke(&v, func() (Record, error) {
			return s.reporter.BeginTask(ctx, v)
		})
	case "publish-message":
		var v Spec
		invoke(&v, func() (Record, error) {
			return s.reporter.PublishMessage(ctx, v)
		})
	case "report-task":
		var v ReportTaskRequest
		invoke(&v, func() (Record, error) {
			return s.reporter.ReportTask(ctx, v)
		})
	case "finish-task":
		var v FinishTaskRequest
		invoke(&v, func() (Record, error) {
			return s.reporter.FinishTask(ctx, v)
		})
	case "upsert-alert":
		var v AlertRequest
		invoke(&v, func() (Record, error) {
			return s.reporter.UpsertAlert(ctx, v)
		})
	case "resolve-alert":
		var v ResolveAlertRequest
		invoke(&v, func() (Record, error) {
			return s.reporter.ResolveAlert(ctx, v)
		})
	default:
		return nil, status.Error(codes.InvalidArgument, "Unknown activity operation.")
	}
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Error(codes.InvalidArgument, "Activity report rejected.")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, status.Error(codes.Internal, "Activity response unavailable.")
	}
	return &pb.ReportResponse{Record: data}, nil
}

func indirect(v any) any {
	switch v := v.(type) {
	case *Spec:
		return *v
	case *ReportTaskRequest:
		return *v
	case *FinishTaskRequest:
		return *v
	case *AlertRequest:
		return *v
	case *ResolveAlertRequest:
		return *v
	}
	return v
}

// ValidateRequest validates data shape; the host still checks ownership and transitions.
func ValidateRequest(operation string, request any) error {
	bad := func() error {
		return resourcev2.NewError("INVALID_REQUEST", "Invalid activity report.")
	}
	presentation := func(p Presentation) error {
		return resourcev2.ValidateActivityPresentation(p)
	}
	spec := func(v Spec) error {
		if v.Severity != "" && v.Severity != "info" && v.Severity != "success" && v.Severity != "warning" && v.Severity != "error" {
			return bad()
		}
		return presentation(v.Presentation)
	}
	switch v := request.(type) {
	case Spec:
		if operation != "begin-task" && operation != "publish-message" {
			return bad()
		}
		return spec(v)
	case ReportTaskRequest:
		if operation != "report-task" || v.ID == "" || (v.State != "" && v.State != "running" && v.State != "paused") {
			return bad()
		}
		return presentation(v.Presentation)
	case FinishTaskRequest:
		if operation != "finish-task" || v.ID == "" || (v.State != "succeeded" && v.State != "failed" && v.State != "cancelled" && v.State != "unknown") {
			return bad()
		}
		if v.Presentation != nil {
			return presentation(*v.Presentation)
		}
		return nil
	case AlertRequest:
		if operation != "upsert-alert" || v.Key == "" || v.ObservedAt == "" {
			return bad()
		}
		return spec(v.Spec)
	case ResolveAlertRequest:
		if operation != "resolve-alert" || v.Key == "" || v.ObservedAt == "" {
			return bad()
		}
		return nil
	default:
		return bad()
	}
}
