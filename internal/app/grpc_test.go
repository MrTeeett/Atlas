package app

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGRPCRequiresAuth(t *testing.T) {
	t.Parallel()

	conn, cleanup := dialGRPCForTest(t)
	defer cleanup()

	var out structpb.Struct
	err := conn.Invoke(context.Background(), "/atlas.v1.AtlasService/GetStats", &emptypb.Empty{}, &out)
	if err == nil {
		t.Fatalf("expected unauthenticated error")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v (%v)", status.Code(err), err)
	}
}

func TestGRPCGetStats(t *testing.T) {
	t.Parallel()

	conn, cleanup := dialGRPCForTest(t)
	defer cleanup()

	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-atlas-user", "admin",
		"x-atlas-pass", "ok",
	))

	var out structpb.Struct
	if err := conn.Invoke(ctx, "/atlas.v1.AtlasService/GetStats", &emptypb.Empty{}, &out); err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if _, ok := out.Fields["cpu_cores"]; !ok {
		t.Fatalf("expected cpu_cores in response, got keys=%v", keys(out.Fields))
	}
}

func dialGRPCForTest(t *testing.T) (*grpc.ClientConn, func()) {
	t.Helper()

	srv, err := New(Config{
		RootDir:    "/",
		BasePath:   "/x",
		AuthStore:  &testStore{passByUser: map[string]string{"admin": "ok"}},
		Secret:     []byte("0123456789abcdef0123456789abcdef"),
		FWDBPath:   "/tmp/fw.db",
		ConfigPath: "/tmp/atlas.json",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	gs := srv.GRPCServer()
	lis := bufconn.Listen(1024 * 1024)
	go func() {
		_ = gs.Serve(lis)
	}()

	ctx := context.Background()
	conn, err := grpc.DialContext(
		ctx,
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		gs.Stop()
		_ = lis.Close()
		t.Fatalf("DialContext: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		gs.Stop()
		_ = lis.Close()
	}
	return conn, cleanup
}

func keys(m map[string]*structpb.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
