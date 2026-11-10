package lab

import (
	"testing"

	"google.golang.org/grpc/codes"
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
