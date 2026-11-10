package nodeagent

import (
	"path/filepath"
	"testing"
)

func TestParsePodLogPath(t *testing.T) {
	root := "/host/var/log/pods"
	path := filepath.Join(root, "shop_web-abc_deadbeef-uid", "web", "0.log")
	got, ok := ParsePodLogPath(root, path)
	if !ok || got.Namespace != "shop" || got.Pod != "web-abc" || got.UID != "deadbeef-uid" || got.Container != "web" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if _, ok := ParsePodLogPath(root, filepath.Join(root, "not-a-pod", "c", "0.log")); ok {
		t.Fatal("accepted a path without namespace_pod_uid")
	}
	if _, ok := ParsePodLogPath(root, filepath.Join(root, "shop_web_uid", "web", "0.log.20240101-120000")); !ok {
		t.Fatal("rotated logs should parse")
	}
}
