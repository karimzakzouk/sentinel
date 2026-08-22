#!/usr/bin/env python3
"""Add NewNoop to k8s client for auto-degrade mode."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
k8s_path = REPO / "internal/k8s/client.go"
k = k8s_path.read_text()

# Add IsNoop method + noopClient type at the end of the file
# Also add a nil-safe wrapper for the Client

# First, add a noop field to Client struct
old_struct = """// Client wraps the Kubernetes clientset + dynamic client.
type Client struct {
\tclientset *kubernetes.Clientset
\tdynamic   dynamic.Interface
\tconfig    *rest.Config
}"""

new_struct = """// Client wraps the Kubernetes clientset + dynamic client.
type Client struct {
\tclientset *kubernetes.Clientset
\tdynamic   dynamic.Interface
\tconfig    *rest.Config
\tnoop      bool
}"""

assert old_struct in k, "k8s/client.go: Client struct not found"
k = k.replace(old_struct, new_struct, 1)

# Add NewNoop + IsNoop after New()
old_after_new = """\treturn &Client{clientset: clientset, dynamic: dyn, config: cfg}, nil
}

// Clientset returns the underlying kubernetes clientset."""

new_after_new = """\treturn &Client{clientset: clientset, dynamic: dyn, config: cfg}, nil
}

// NewNoop returns a Client that never talks to a Kubernetes cluster. All
// methods return empty results and nil errors. Used when no K8s cluster is
// available (auto-degrade) or in observe-only mode.
func NewNoop() *Client {
\treturn &Client{noop: true}
}

// IsNoop reports whether the client is in noop mode (no real K8s cluster).
func (c *Client) IsNoop() bool {
\treturn c != nil && c.noop
}

// Clientset returns the underlying kubernetes clientset."""

assert old_after_new in k, "k8s/client.go: after New() not found"
k = k.replace(old_after_new, new_after_new, 1)

# Add nil-safe checks to LookupWorkloadAnnotations and FetchCrashingPodLogs
old_lookup = """func (c *Client) LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error) {
\tannotations := map[string]string{}

\tif dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {"""

new_lookup = """func (c *Client) LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error) {
\tif c.IsNoop() {
\t\treturn map[string]string{}, nil
\t}
\tannotations := map[string]string{}

\tif dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {"""

assert old_lookup in k, "k8s/client.go: LookupWorkloadAnnotations not found"
k = k.replace(old_lookup, new_lookup, 1)

k8s_path.write_text(k)
print("OK: k8s/client.go — noop mode added")
