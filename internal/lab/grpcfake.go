package lab

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type grpcRoute struct {
	Path string
	Code codes.Code
}

// grpcRoutesFile lists full RPC path and numeric gRPC status code.
func grpcRoutesFile(routes []grpcRoute) string {
	var b strings.Builder
	for _, r := range routes {
		fmt.Fprintf(&b, "%s %d\n", r.Path, r.Code)
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
		if len(parts) != 2 {
			continue
		}
		code, err := strconv.Atoi(parts[1])
		if err != nil || code < 0 || code > int(codes.Unauthenticated) {
			continue
		}
		out = append(out, grpcRoute{Path: parts[0], Code: codes.Code(code)})
	}
	return out
}

// ServeFakeGRPC answers recorded gRPC statuses with empty messages on listen.
func ServeFakeGRPC(listen, routesFile string) error {
	if listen == "" {
		return fmt.Errorf("lab-grpc-fake needs --listen")
	}
	raw, err := os.ReadFile(routesFile) //nolint:gosec // path is the Lab stub's mounted ConfigMap
	if err != nil {
		return fmt.Errorf("read grpc routes: %w", err)
	}
	byPath := map[string]codes.Code{}
	for _, r := range parseGRPCRoutes(string(raw)) {
		byPath[r.Path] = r.Code
	}
	if _, err := strconv.Atoi(listen); err == nil {
		listen = ":" + listen
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	s := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		full, ok := grpc.MethodFromServerStream(stream)
		if !ok {
			return status.Error(codes.Internal, "lab fake")
		}
		var junk []byte
		_ = stream.RecvMsg(&junk)
		code := byPath[full]
		if code == codes.OK || code == 0 {
			return stream.SendMsg(&emptypb.Empty{})
		}
		return status.Error(code, "sentinel lab fake")
	}))
	return s.Serve(ln)
}

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
