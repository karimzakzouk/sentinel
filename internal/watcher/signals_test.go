package watcher

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSignalsNameWhatAnErrorIsAbout(t *testing.T) {
	for text, want := range map[string]FactKind{
		"dial tcp: lookup redis-cart on <ip>: no such host":                        SignalDNSFailure,
		"Error: getaddrinfo ENOTFOUND payments":                                    SignalDNSFailure,
		"Get \"https://api\": x509: certificate has expired or is not yet valid":   SignalCertExpired,
		"dial tcp <ip>:<num>: connect: connection refused":                         SignalConnectRefused,
		"StackExchange.Redis.RedisConnectionException: No connection is available": "",
		"GET /healthz 200": "",
	} {
		got, ok := SignalKind(text)
		if ok != (want != "") || got != want {
			t.Errorf("%q: %q, %v; want %q", text, got, ok, want)
		}
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionUnknown},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
	}}}
	if got := NodeProblem(node); got != "node worker: not ready (Unknown), DiskPressure" {
		t.Fatalf("problem = %q", got)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if got := NodeProblem(node); got != "" {
		t.Fatalf("a ready node: %q", got)
	}
}
