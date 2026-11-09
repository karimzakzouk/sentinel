package watcher

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Signals that name what an error is about. They are read from the logs an
// incident's evidence holds, to tell its class, and never open an incident
// themselves: one "connection refused" is no rate.
const (
	SignalDNSFailure     FactKind = "dns.failure"
	SignalCertExpired    FactKind = "cert.expiring"
	SignalConnectRefused FactKind = "dependency.connect-error"
)

// FactNodeNotReady is a pod on a node that is not ready, or under memory,
// disk or process pressure, counted once per pod and tick while it holds.
const FactNodeNotReady FactKind = "node.not-ready"

// signalWords are, lowercased, what runtimes and libraries write for each
// signal.
var signalWords = []struct {
	kind  FactKind
	words []string
}{
	{SignalDNSFailure, []string{"no such host", "server misbehaving", "enotfound", "eai_again", "name or service not known",
		"temporary failure in name resolution", "unknownhostexception", "could not resolve host"}},
	{SignalCertExpired, []string{"certificate has expired", "certificate expired", "cert_has_expired", "certificate is not yet valid"}},
	{SignalConnectRefused, []string{"connection refused", "econnrefused", "no route to host", "ehostunreach"}},
}

// SignalKind names the signal a log template or line carries, if any.
func SignalKind(text string) (FactKind, bool) {
	l := strings.ToLower(text)
	for _, s := range signalWords {
		for _, w := range s.words {
			if strings.Contains(l, w) {
				return s.kind, true
			}
		}
	}
	return "", false
}

// NodeProblem says what keeps a node from running pods, or "" when nothing
// does.
func NodeProblem(node *corev1.Node) string {
	var problems []string
	for _, c := range node.Status.Conditions {
		switch {
		case c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue:
			problems = append(problems, "not ready ("+string(c.Status)+")")
		case (c.Type == corev1.NodeMemoryPressure || c.Type == corev1.NodeDiskPressure || c.Type == corev1.NodePIDPressure ||
			c.Type == corev1.NodeNetworkUnavailable) && c.Status == corev1.ConditionTrue:
			problems = append(problems, string(c.Type))
		}
	}
	if len(problems) == 0 {
		return ""
	}
	return fmt.Sprintf("node %s: %s", node.Name, strings.Join(problems, ", "))
}
