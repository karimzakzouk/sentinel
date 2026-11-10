package lab

import (
	"context"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/karimzakzouk/sentinel/internal/capture"
)

func TestGRPCRoutesAndCodeMapping(t *testing.T) {
	routes := parseGRPCRoutes(grpcRoutesFile([]grpcRoute{{Path: "/a.B/C", Code: codes.Unavailable}}))
	if len(routes) != 1 || routes[0].Code != codes.Unavailable {
		t.Fatalf("routes %+v", routes)
	}
	if grpcCodeFor("UNAVAILABLE", 503) != codes.Unavailable {
		t.Fatal("name mapping")
	}
	if grpcCodeFor("", 200) != codes.OK {
		t.Fatal("http mapping")
	}
}

func TestGRPCRoutesFileRoundTripsResponseBodies(t *testing.T) {
	in := []grpcRoute{
		{Path: "/svc.A/Empty", Code: codes.OK},
		{Path: "/svc.A/JSON", Code: codes.OK, Body: `{"ok":true}`},
		{Path: "/svc.A/Wire", Code: codes.OK, Body: string([]byte{0x08, 0x2a})},
	}
	got := parseGRPCRoutes(grpcRoutesFile(in))
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Body != "" || got[1].Body != `{"ok":true}` || got[2].Body != string([]byte{0x08, 0x2a}) {
		t.Fatalf("bodies %+v", got)
	}
}

func TestGRPCPayloadBytesPrefersBase64WireAndJSONStub(t *testing.T) {
	wire, _ := proto.Marshal(wrapperspb.Int32(42))
	b64 := base64.StdEncoding.EncodeToString(wire)
	if got := grpcPayloadBytes(b64); string(got) != string(wire) {
		t.Fatalf("base64 wire: %q", got)
	}
	if got := grpcPayloadBytes(`{"n":1}`); string(got) != `{"n":1}` {
		t.Fatalf("json stub: %q", got)
	}
	if got := grpcPayloadBytes(""); got != nil {
		t.Fatalf("empty: %q", got)
	}
}

func TestFakeRoutesCarryGRPCResponseBodies(t *testing.T) {
	_, grpcs, _, _ := fakeRoutes("currencyservice", []capture.Call{
		{Host: "currencyservice", Method: "gRPC", Path: "/hipstershop.CurrencyService/Convert", Port: 7000, Status: 200, ResponseBody: `{"units":1}`},
		{Host: "currencyservice", Method: "gRPC", Path: "/hipstershop.CurrencyService/GetSupportedCurrencies", Port: 7000, Status: 200},
	})
	if len(grpcs) != 2 {
		t.Fatalf("grpcs %+v", grpcs)
	}
	byPath := map[string]grpcRoute{}
	for _, r := range grpcs {
		byPath[r.Path] = r
	}
	if byPath["/hipstershop.CurrencyService/Convert"].Body != `{"units":1}` {
		t.Fatalf("body-bearing %+v", byPath["/hipstershop.CurrencyService/Convert"])
	}
	if byPath["/hipstershop.CurrencyService/GetSupportedCurrencies"].Body != "" {
		t.Fatalf("empty %+v", byPath["/hipstershop.CurrencyService/GetSupportedCurrencies"])
	}
}

func TestServeFakeGRPCReturnsCapturedBodyOrEmpty(t *testing.T) {
	wire, err := proto.Marshal(wrapperspb.String("typed"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "grpc-routes")
	routes := grpcRoutesFile([]grpcRoute{
		{Path: "/demo.Svc/WithBody", Code: codes.OK, Body: string(wire)},
		{Path: "/demo.Svc/Empty", Code: codes.OK},
		{Path: "/demo.Svc/Down", Code: codes.Unavailable},
		{Path: "/demo.Svc/JSON", Code: codes.OK, Body: `{"ok":true}`},
	})
	if err := os.WriteFile(path, []byte(routes), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	errc := make(chan error, 1)
	go func() { errc <- ServeFakeGRPC(addr, path) }()
	t.Cleanup(func() {
		// Dial once more so Serve can exit after the test process tears down
		// the listener via process exit; force-close by connecting then cancel.
		conn, _ := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if conn != nil {
			_ = conn.Close()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Wait until the server accepts.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var probe []byte
		err := conn.Invoke(ctx, "/demo.Svc/Empty", []byte{}, &probe)
		if err == nil || status.Code(err) != codes.Unavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	var got []byte
	if err := conn.Invoke(ctx, "/demo.Svc/WithBody", []byte{}, &got); err != nil {
		t.Fatalf("WithBody: %v", err)
	}
	if string(got) != string(wire) {
		t.Fatalf("WithBody payload %q want %q", got, wire)
	}

	got = nil
	if err := conn.Invoke(ctx, "/demo.Svc/Empty", []byte{}, &got); err != nil {
		t.Fatalf("Empty: %v", err)
	}
	empty, _ := proto.Marshal(&emptypb.Empty{})
	if string(got) != string(empty) && len(got) != 0 {
		t.Fatalf("Empty payload %q", got)
	}

	got = nil
	if err := conn.Invoke(ctx, "/demo.Svc/JSON", []byte{}, &got); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("JSON payload %q", got)
	}

	err = conn.Invoke(ctx, "/demo.Svc/Down", []byte{}, &got)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Down: %v", err)
	}
}
