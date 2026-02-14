package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

const atlasGRPCServiceName = "atlas.v1.AtlasService"

type atlasGRPCService interface {
	GetStats(context.Context, *emptypb.Empty) (*structpb.Struct, error)
	GetSystemInfo(context.Context, *emptypb.Empty) (*structpb.Struct, error)
	ListProcesses(context.Context, *emptypb.Empty) (*structpb.Struct, error)
}

type atlasGRPCHandler struct {
	srv *Server
}

func (s *Server) GRPCServer() *grpc.Server {
	gs := grpc.NewServer(grpc.UnaryInterceptor(s.grpcAuthUnaryInterceptor))
	gs.RegisterService(&atlasGRPCServiceDesc, &atlasGRPCHandler{srv: s})
	return gs
}

func GRPCMux(httpHandler http.Handler, grpcServer *grpc.Server) http.Handler {
	if grpcServer == nil {
		return httpHandler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		httpHandler.ServeHTTP(w, r)
	})
}

func (h *atlasGRPCHandler) GetStats(_ context.Context, _ *emptypb.Empty) (*structpb.Struct, error) {
	st, err := h.srv.stats.Collect()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return toPBStruct(st)
}

func (h *atlasGRPCHandler) GetSystemInfo(_ context.Context, _ *emptypb.Empty) (*structpb.Struct, error) {
	info, err := h.srv.info.Collect()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return toPBStruct(info)
}

func (h *atlasGRPCHandler) ListProcesses(_ context.Context, _ *emptypb.Empty) (*structpb.Struct, error) {
	ps, err := h.srv.process.List()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return toPBStruct(map[string]any{"processes": ps})
}

func (s *Server) grpcAuthUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if s.cfg.AuthStore == nil {
		return nil, status.Error(codes.Internal, "auth store is not configured")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	user := firstMetadata(md, "x-atlas-user")
	pass := firstMetadata(md, "x-atlas-pass")
	if user == "" || pass == "" {
		return nil, status.Error(codes.Unauthenticated, "missing credentials")
	}
	ok, err := s.cfg.AuthStore.Authenticate(user, pass)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	return handler(ctx, req)
}

func firstMetadata(md metadata.MD, key string) string {
	if md == nil {
		return ""
	}
	v := md.Get(strings.ToLower(strings.TrimSpace(key)))
	if len(v) == 0 {
		return ""
	}
	return strings.TrimSpace(v[0])
}

func toPBStruct(v any) (*structpb.Struct, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out, err := structpb.NewStruct(m)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return out, nil
}

var atlasGRPCServiceDesc = grpc.ServiceDesc{
	ServiceName: atlasGRPCServiceName,
	HandlerType: (*atlasGRPCService)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "GetStats",
			Handler:    _Atlas_GetStats_Handler,
		},
		{
			MethodName: "GetSystemInfo",
			Handler:    _Atlas_GetSystemInfo_Handler,
		},
		{
			MethodName: "ListProcesses",
			Handler:    _Atlas_ListProcesses_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "atlas/v1/atlas.proto",
}

func _Atlas_GetStats_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(atlasGRPCService).GetStats(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/" + atlasGRPCServiceName + "/GetStats",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(atlasGRPCService).GetStats(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Atlas_GetSystemInfo_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(atlasGRPCService).GetSystemInfo(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/" + atlasGRPCServiceName + "/GetSystemInfo",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(atlasGRPCService).GetSystemInfo(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _Atlas_ListProcesses_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(emptypb.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(atlasGRPCService).ListProcesses(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/" + atlasGRPCServiceName + "/ListProcesses",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(atlasGRPCService).ListProcesses(ctx, req.(*emptypb.Empty))
	}
	return interceptor(ctx, in, info, handler)
}
