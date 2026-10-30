package logs

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	resourcev2 "github.com/omniviewdev/plugin-sdk/pkg/resource/v2"
	"github.com/omniviewdev/plugin-sdk/pkg/types"
	logspb "github.com/omniviewdev/plugin-sdk/proto/v1/logs"
)

type responseLogServer struct {
	logspb.UnimplementedLogPluginServer
	response *logspb.ResourceLogSessionResponse
}

func (s responseLogServer) CreateResourceSession(
	context.Context,
	*logspb.CreateLogSessionRequest,
) (*logspb.ResourceLogSessionResponse, error) {
	return s.response, nil
}

type malformedLogResponseCodec struct{ data []byte }

func (c malformedLogResponseCodec) Name() string { return "proto" }
func (c malformedLogResponseCodec) Marshal(message any) ([]byte, error) {
	if _, ok := message.(*logspb.ResourceLogSessionResponse); ok {
		return c.data, nil
	}
	return proto.Marshal(message.(proto.Message))
}

func (c malformedLogResponseCodec) Unmarshal(data []byte, message any) error {
	return proto.Unmarshal(data, message.(proto.Message))
}

func TestResourceLogsMalformedResponseBeforeDecode(t *testing.T) {
	for _, data := range [][]byte{{0x08, 2, 0x08, 2}, {0x08, 1}, {0x08, 2, 0x12, 0}, {0x08, 2, 0x12, 2, 0x0a, 0}} {
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer(grpc.ForceServerCodec(malformedLogResponseCodec{data}))
		logspb.RegisterLogPluginServer(server, responseLogServer{})
		go server.Serve(listener)
		connection, err := grpc.NewClient(
			"passthrough:///invalid-logs-response",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		)
		if err != nil {
			t.Fatal(err)
		}
		client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
		_, err = client.CreateSession(
			types.NewPluginContext(context.Background(), "logs", nil, nil, nil),
			CreateSessionOptions{ResourceKey: "pod"},
		)
		connection.Close()
		server.Stop()
		listener.Close()
		if err == nil {
			t.Fatalf("malformed response admitted: %x", data)
		}
	}
}

func TestResourceLogsRejectsInvalidResponseIdentityAndTimestamp(t *testing.T) {
	for _, session := range []*logspb.LogSession{
		{CreatedAt: timestamppb.Now()},
		{Id: "session", CreatedAt: &timestamppb.Timestamp{Seconds: 253402300800}},
		{Id: "session"},
	} {
		connection := logsConnection(t, func(s *grpc.Server) {
			logspb.RegisterLogPluginServer(
				s,
				responseLogServer{
					response: &logspb.ResourceLogSessionResponse{
						ResourceWireVersion: ResourceSessionWireVersion,
						Session:             session,
					},
				},
			)
		})
		client := &PluginClient{client: logspb.NewLogPluginClient(connection)}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.CreateSession(
			types.NewPluginContext(ctx, "logs", nil, nil, nil),
			CreateSessionOptions{ResourceKey: "pod"},
		)
		cancel()
		if err == nil || resourcev2.ErrorCode(err) == "UNSUPPORTED" {
			t.Fatalf("invalid session admitted: %v", err)
		}
	}
}
