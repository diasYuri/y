package runtime

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRegisterGRPCDispatchesCommand(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	if err := RegisterGRPC(server, HandlerFunc(func(_ context.Context, command Command) (Response, error) {
		if command.Kind != CommandAbort {
			t.Fatalf("command = %#v", command)
		}
		return Response{Accepted: true}, nil
	})); err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	defer server.Stop()
	connection, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	command, err := NewCommand(CommandAbort, nil)
	if err != nil {
		t.Fatal(err)
	}
	command.ID, command.RunID = "cmd", "run"
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	request, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	response := &structpb.Struct{}
	if err := connection.Invoke(context.Background(), GRPCDispatchMethod, request, response); err != nil {
		t.Fatal(err)
	}
	if accepted, _ := response.AsMap()["accepted"].(bool); !accepted {
		t.Fatalf("response = %#v", response.AsMap())
	}
}
