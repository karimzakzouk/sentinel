package lab

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type grpcRoute struct {
	Path string
	Code codes.Code
	// Body is a redacted response payload when capture kept one: wire
	// protobuf bytes (possibly base64-encoded in the string), or a JSON
	// stub. Empty means the fake answers with emptypb.
	Body string
}

// grpcRoutesFile lists full RPC path, numeric gRPC status code, and an
// optional base64 response body (same shape as HTTP routesFile).
func grpcRoutesFile(routes []grpcRoute) string {
	var b strings.Builder
	for _, r := range routes {
		if r.Body == "" {
			fmt.Fprintf(&b, "%s %d\n", r.Path, r.Code)
			continue
		}
		fmt.Fprintf(&b, "%s %d %s\n", r.Path, r.Code, base64.StdEncoding.EncodeToString([]byte(r.Body)))
	}
	return b.String()
}

// parseGRPCRoutes reads the grpc-routes file the stub mounts.
func parseGRPCRoutes(data string) []grpcRoute {
	var out []grpcRoute
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		code, err := strconv.Atoi(parts[1])
		if err != nil || code < 0 || code > int(codes.Unauthenticated) {
			continue
		}
		route := grpcRoute{Path: parts[0], Code: codes.Code(code)}
		if len(parts) >= 3 {
			if raw, err := base64.StdEncoding.DecodeString(parts[2]); err == nil {
				route.Body = string(raw)
			}
		}
		out = append(out, route)
	}
	return out
}

// grpcPayloadBytes turns a captured ResponseBody into protobuf message
// bytes for SendMsg. Prefer wire bytes: if the body is standard base64 of
// a non-JSON payload, decode it; if it is JSON (or other text), use those
// bytes as a structured stub. Empty body yields nil (caller sends emptypb).
func grpcPayloadBytes(body string) []byte {
	if body == "" {
		return nil
	}
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[") {
		return []byte(body)
	}
	if raw, err := base64.StdEncoding.DecodeString(trim); err == nil && len(raw) > 0 {
		return raw
	}
	if raw, err := base64.RawStdEncoding.DecodeString(trim); err == nil && len(raw) > 0 {
		return raw
	}
	return []byte(body)
}

// rawCodec lets UnknownServiceHandler send and receive message payloads as
// []byte (the gRPC wire body) while still marshaling proto.Message values
// such as emptypb.Empty. Named "proto" so clients keep the default subtype.
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }

func (rawCodec) Marshal(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []byte:
		return x, nil
	case *[]byte:
		if x == nil {
			return nil, nil
		}
		return *x, nil
	case proto.Message:
		return proto.Marshal(x)
	default:
		return nil, fmt.Errorf("lab-grpc-fake: marshal %T", v)
	}
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	switch x := v.(type) {
	case *[]byte:
		*x = append((*x)[:0], data...)
		return nil
	case proto.Message:
		return proto.Unmarshal(data, x)
	default:
		return fmt.Errorf("lab-grpc-fake: unmarshal %T", v)
	}
}

// ServeFakeGRPC answers recorded gRPC statuses on listen. When a route has
// a response body, that payload is sent as the protobuf message bytes (or
// a JSON/text stub when capture stored one); otherwise the response is
// emptypb.Empty. Non-OK codes keep decision 114's status mapping and send
// no message body.
func ServeFakeGRPC(listen, routesFile string) error {
	if listen == "" {
		return fmt.Errorf("lab-grpc-fake needs --listen")
	}
	raw, err := os.ReadFile(routesFile) //nolint:gosec // path is the Lab stub's mounted ConfigMap
	if err != nil {
		return fmt.Errorf("read grpc routes: %w", err)
	}
	byPath := map[string]grpcRoute{}
	for _, r := range parseGRPCRoutes(string(raw)) {
		byPath[r.Path] = r
	}
	if _, err := strconv.Atoi(listen); err == nil {
		listen = ":" + listen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	s := grpc.NewServer(
		grpc.ForceServerCodec(rawCodec{}),
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			full, ok := grpc.MethodFromServerStream(stream)
			if !ok {
				return status.Error(codes.Internal, "lab fake")
			}
			var junk []byte
			_ = stream.RecvMsg(&junk)
			route, found := byPath[full]
			if !found {
				route.Code = codes.OK
			}
			if route.Code != codes.OK && route.Code != 0 {
				return status.Error(route.Code, "sentinel lab fake")
			}
			if payload := grpcPayloadBytes(route.Body); len(payload) > 0 {
				return stream.SendMsg(payload)
			}
			return stream.SendMsg(&emptypb.Empty{})
		}),
	)
	return s.Serve(ln)
}

// Ensure encoding.Codec is satisfied at compile time.
var _ encoding.Codec = rawCodec{}

// grpcCodeFor maps a captured gRPC name or HTTP-mapped status to a codes.Code.
func grpcCodeFor(name string, httpStatus int) codes.Code {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name != "" {
		for i, n := range grpcCodeNames {
			if n == name {
				return codes.Code(i)
			}
		}
		if name == "CANCELED" {
			return codes.Canceled
		}
	}
	for i, h := range grpcHTTPMap {
		if h == httpStatus {
			return codes.Code(i)
		}
	}
	if httpStatus >= 200 && httpStatus < 300 {
		return codes.OK
	}
	return codes.Unavailable
}

var (
	grpcCodeNames = []string{"OK", "CANCELLED", "UNKNOWN", "INVALID_ARGUMENT", "DEADLINE_EXCEEDED", "NOT_FOUND", "ALREADY_EXISTS",
		"PERMISSION_DENIED", "RESOURCE_EXHAUSTED", "FAILED_PRECONDITION", "ABORTED", "OUT_OF_RANGE", "UNIMPLEMENTED", "INTERNAL",
		"UNAVAILABLE", "DATA_LOSS", "UNAUTHENTICATED"}
	grpcHTTPMap = []int{200, 499, 500, 400, 504, 404, 409, 403, 429, 400, 409, 400, 501, 500, 503, 500, 401}
)
