package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

// GRPCServiceName and GRPCDispatchMethod are stable names for the generic
// protobuf-free gRPC protocol adapter. Both request and response use
// google.protobuf.Struct and preserve the exact Command/Response JSON shape.
const (
	GRPCServiceName    = "y.runtime.v1.CommandService"
	GRPCDispatchMethod = "/" + GRPCServiceName + "/Dispatch"
)

// RegisterGRPC registers the command service on any gRPC server. It requires
// no generated bindings, making it practical for host applications to expose
// the SDK protocol without a code-generation dependency.
func RegisterGRPC(registrar grpc.ServiceRegistrar, handler Handler) error {
	if registrar == nil {
		return errors.New("runtime gRPC registrar is nil")
	}
	if handler == nil {
		return errors.New("runtime gRPC handler is nil")
	}
	registrar.RegisterService(&grpc.ServiceDesc{
		ServiceName: GRPCServiceName,
		HandlerType: (*grpcCommandService)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Dispatch", Handler: grpcDispatch(handler)}},
	}, &grpcCommandServiceImpl{})
	return nil
}

type grpcCommandService interface{ isGRPCCommandService() }

type grpcCommandServiceImpl struct{}

func (*grpcCommandServiceImpl) isGRPCCommandService() {}

func grpcDispatch(handler Handler) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(service any, ctx context.Context, decoder func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		request := &structpb.Struct{}
		if err := decoder(request); err != nil {
			return nil, err
		}
		invoke := func(ctx context.Context, request any) (any, error) {
			message, ok := request.(*structpb.Struct)
			if !ok {
				return nil, errors.New("runtime gRPC request has unexpected type")
			}
			command, commandErr := commandFromStruct(message)
			if commandErr != nil {
				response, responseErr := responseStruct(Response{SchemaVersion: ProtocolVersion, Accepted: false, Error: &CommandError{Code: "invalid_command", Message: commandErr.Error()}})
				if responseErr != nil {
					return nil, responseErr
				}
				return response, nil
			}
			response, err := dispatch(ctx, handler, command)
			if err != nil && response.Error == nil {
				response = Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "dispatch_failed", Message: err.Error()}}
			}
			return responseStruct(response)
		}
		if interceptor == nil {
			return invoke(ctx, request)
		}
		return interceptor(ctx, request, &grpc.UnaryServerInfo{Server: service, FullMethod: GRPCDispatchMethod}, invoke)
	}
}

func commandFromStruct(value *structpb.Struct) (Command, error) {
	raw, err := json.Marshal(value.AsMap())
	if err != nil {
		return Command{}, err
	}
	var command Command
	if err := json.Unmarshal(raw, &command); err != nil {
		return Command{}, err
	}
	return command, nil
}

func responseStruct(response Response) (*structpb.Struct, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return structpb.NewStruct(fields)
}
