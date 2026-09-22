// Package depanalysis — pluggable dependency graph providers.
//
// This file defines the DepGraphProvider implementations that the causal
// analyzer uses to build service dependency graphs. Each provider extracts
// the graph from a different source:
//
//   - IstioProvider: reads Istio telemetry (destination_rules + virtual_services)
//   - LinkerdProvider: reads Linkerd service-profiles + server annotations
//   - NetworkPolicyProvider: parses K8s NetworkPolicies (egress rules)
//   - AnnotationProvider: reads sentinel.io/depends-on annotations
//   - CompositeProvider: tries each in priority order, returns first non-empty
//
// The composite provider is the production default — it auto-detects which
// mesh/backend is available and falls back gracefully.
package depanalysis

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/karimzakzouk/sentinel/internal/logging"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DepGraphNode is a service in the dependency graph.
type DepGraphNode struct {
	Service   string
	Namespace string
}

// DepGraphEdge is a directed dependency: Source calls Target.
type DepGraphEdge struct {
	Source DepGraphNode
	Target DepGraphNode
	Weight float64
}

// DepGraph is a directed graph of service dependencies.
type DepGraph struct {
	Nodes []DepGraphNode
	Edges []DepGraphEdge
}

// DepGraphProvider builds dependency graphs. Implementations extract the
// graph from different sources (service mesh, NetworkPolicies, annotations).
type DepGraphProvider interface {
	Name() string
	GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error)
}

// --- AnnotationProvider (always available) ---
//
// Reads sentinel.io/depends-on annotations from Deployments. The annotation
// value is a comma-separated list of service names (optionally with namespace
// prefix: "namespace/service").
//
// Example:
//   annotations:
//     sentinel.io/depends-on: "payments-api,inventory-api,staging/db"

// AnnotationProvider builds graphs from sentinel.io/depends-on annotations.
type AnnotationProvider struct {
	clientset kubernetes.Interface
}

// NewAnnotationProvider creates an annotation-based graph provider.
func NewAnnotationProvider(clientset kubernetes.Interface) *AnnotationProvider {
	return &AnnotationProvider{clientset: clientset}
}

// Name returns the provider name.
func (a *AnnotationProvider) Name() string { return "annotation" }

// GraphFor reads sentinel.io/depends-on annotations from all Deployments in
// the namespaces of the anomalous nodes and builds a graph.
func (a *AnnotationProvider) GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	graph := &DepGraph{}
	seenNodes := map[DepGraphNode]bool{}

	// Collect unique namespaces from the anomalous nodes.
	namespaces := map[string]bool{}
	for _, n := range nodes {
		namespaces[n.Namespace] = true
		seenNodes[n] = true
		graph.Nodes = append(graph.Nodes, n)
	}

	for ns := range namespaces {
		deps, err := a.clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			logging.Debug("annotation provider: list deployments failed", "namespace", ns, "error", err)
			continue
		}
		for _, dep := range deps.Items {
			svcName := dep.Labels["app.kubernetes.io/name"]
			if svcName == "" {
				svcName = dep.Labels["app"]
			}
			if svcName == "" {
				continue
			}
			source := DepGraphNode{Service: svcName, Namespace: ns}
			if !seenNodes[source] {
				seenNodes[source] = true
				graph.Nodes = append(graph.Nodes, source)
			}

			dependsOn := dep.Annotations["sentinel.io/depends-on"]
			if dependsOn == "" {
				continue
			}
			for _, target := range strings.Split(dependsOn, ",") {
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				tgtNode := parseServiceRef(target, ns)
				if !seenNodes[tgtNode] {
					seenNodes[tgtNode] = true
					graph.Nodes = append(graph.Nodes, tgtNode)
				}
				graph.Edges = append(graph.Edges, DepGraphEdge{
					Source: source,
					Target: tgtNode,
				})
			}
		}
	}

	return graph, nil
}

// parseServiceRef parses "service" or "namespace/service" into a DepGraphNode.
func parseServiceRef(ref, defaultNs string) DepGraphNode {
	if parts := strings.SplitN(ref, "/", 2); len(parts) == 2 {
		return DepGraphNode{Namespace: parts[0], Service: parts[1]}
	}
	return DepGraphNode{Namespace: defaultNs, Service: ref}
}

// --- NetworkPolicyProvider ---
//
// Parses K8s NetworkPolicies to build an allowed-egress graph. When a
// NetworkPolicy selects pod A and allows egress to pod B (by label selector
// or namespace selector), we infer A depends on B.
//
// Note: NetworkPolicies describe ALLOWED traffic, not actual traffic. This
// is an over-approximation — the graph may include edges that don't exist
// in practice. The causal analyzer's Granger scoring filters these out
// because non-existent dependencies won't show temporal correlation.

// NetworkPolicyProvider builds graphs from K8s NetworkPolicies.
type NetworkPolicyProvider struct {
	clientset kubernetes.Interface
}

// NewNetworkPolicyProvider creates a NetworkPolicy-based graph provider.
func NewNetworkPolicyProvider(clientset kubernetes.Interface) *NetworkPolicyProvider {
	return &NetworkPolicyProvider{clientset: clientset}
}

// Name returns the provider name.
func (n *NetworkPolicyProvider) Name() string { return "networkpolicy" }

// GraphFor reads NetworkPolicies in the namespaces of the anomalous nodes.
func (n *NetworkPolicyProvider) GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	graph := &DepGraph{}
	seenNodes := map[DepGraphNode]bool{}

	namespaces := map[string]bool{}
	for _, node := range nodes {
		namespaces[node.Namespace] = true
		seenNodes[node] = true
		graph.Nodes = append(graph.Nodes, node)
	}

	for ns := range namespaces {
		npList, err := n.clientset.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			logging.Debug("networkpolicy provider: list failed", "namespace", ns, "error", err)
			continue
		}

		// Build a label-indexed map of pods in this namespace for resolving
		// podSelector → service name.
		podIndex, err := buildPodIndex(ctx, n.clientset, ns)
		if err != nil {
			continue
		}

		for _, np := range npList.Items {
			// The source is the pod selected by spec.podSelector.
			sourceSvc := matchServiceFromSelector(podIndex, np.Spec.PodSelector)
			if sourceSvc == "" {
				continue
			}
			source := DepGraphNode{Service: sourceSvc, Namespace: ns}
			if !seenNodes[source] {
				seenNodes[source] = true
				graph.Nodes = append(graph.Nodes, source)
			}

			// Each egress rule with a podSelector defines a dependency.
			for _, rule := range np.Spec.Egress {
				for _, peer := range rule.To {
					if peer.PodSelector == nil && peer.NamespaceSelector == nil {
						// egress to anywhere — skip (no specific dependency)
						continue
					}
					if peer.PodSelector != nil {
						tgtSvc := matchServiceFromSelector(podIndex, *peer.PodSelector)
						if tgtSvc != "" && tgtSvc != sourceSvc {
							tgtNs := ns
							tgt := DepGraphNode{Service: tgtSvc, Namespace: tgtNs}
							if !seenNodes[tgt] {
								seenNodes[tgt] = true
								graph.Nodes = append(graph.Nodes, tgt)
							}
							graph.Edges = append(graph.Edges, DepGraphEdge{
								Source: source,
								Target: tgt,
							})
						}
					}
				}
			}
		}
	}

	return graph, nil
}

// buildPodIndex returns a map of label-key → service name for a namespace.
// Used to resolve NetworkPolicy podSelectors to service names.
func buildPodIndex(ctx context.Context, clientset kubernetes.Interface, ns string) (map[string]string, error) {
	pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	index := map[string]string{}
	for _, pod := range pods.Items {
		svc := pod.Labels["app.kubernetes.io/name"]
		if svc == "" {
			svc = pod.Labels["app"]
		}
		if svc == "" {
			continue
		}
		// Index by each label key=value for selector matching.
		for k, v := range pod.Labels {
			index[k+"="+v] = svc
		}
	}
	return index, nil
}

// matchServiceFromSelector resolves a LabelSelector to a service name using
// the pod index. Returns "" if no match.
func matchServiceFromSelector(podIndex map[string]string, selector metav1.LabelSelector) string {
	if len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0 {
		return ""
	}
	for k, v := range selector.MatchLabels {
		if svc, ok := podIndex[k+"="+v]; ok {
			return svc
		}
	}
	return ""
}

// --- IstioProvider ---
//
// Reads Istio telemetry. Istio exposes service dependencies via:
//   - destination_rules (host → service mapping)
//   - virtual_services (routing rules → call graph)
//   - The Istio telemetry service (if running)
//
// This implementation uses a simplified approach: reads virtual_services in
// the namespace and extracts the route destinations as edges. A full
// implementation would query the Istio telemetry API for observed traffic.
//
// Requires Istio CRDs to be installed. If not present, GraphFor returns an
// empty graph (the composite provider will fall back to the next provider).

// IstioProvider builds graphs from Istio VirtualService resources.
type IstioProvider struct {
	clientset kubernetes.Interface
	// dynamicClient is used for Istio CRDs (virtualservices).
	dynamicClient dynamicClient
}

// dynamicClient is the minimal interface we need from k8s dynamic client.
type dynamicClient interface {
	List(ctx context.Context, namespace, resource, version string) ([]map[string]any, error)
}

// NewIstioProvider creates an Istio-based graph provider.
func NewIstioProvider(clientset kubernetes.Interface, dyn dynamicClient) *IstioProvider {
	return &IstioProvider{clientset: clientset, dynamicClient: dyn}
}

// Name returns the provider name.
func (i *IstioProvider) Name() string { return "istio" }

// GraphFor reads Istio VirtualServices in the namespaces of the anomalous
// nodes and extracts route destinations as edges.
func (i *IstioProvider) GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	graph := &DepGraph{}
	seenNodes := map[DepGraphNode]bool{}

	namespaces := map[string]bool{}
	for _, node := range nodes {
		namespaces[node.Namespace] = true
		seenNodes[node] = true
		graph.Nodes = append(graph.Nodes, node)
	}

	if i.dynamicClient == nil {
		return graph, nil
	}

	for ns := range namespaces {
		// List VirtualServices (Istio CRD: networking.istio.io/v1)
		vss, err := i.dynamicClient.List(ctx, ns, "virtualservices", "v1")
		if err != nil {
			logging.Debug("istio provider: list virtualservices failed", "namespace", ns, "error", err)
			continue
		}

		for _, vs := range vss {
			name, _ := vs["metadata"].(map[string]any)["name"].(string)
			source := DepGraphNode{Service: name, Namespace: ns}
			if !seenNodes[source] {
				seenNodes[source] = true
				graph.Nodes = append(graph.Nodes, source)
			}

			// Extract route destinations from spec.http[].route[].destination
			spec, _ := vs["spec"].(map[string]any)
			http, _ := spec["http"].([]any)
			for _, h := range http {
				hMap, _ := h.(map[string]any)
				routes, _ := hMap["route"].([]any)
				for _, r := range routes {
					rMap, _ := r.(map[string]any)
					dest, _ := rMap["destination"].(map[string]any)
					host, _ := dest["host"].(string)
					if host == "" {
						continue
					}
					tgt := parseServiceRef(host, ns)
					if !seenNodes[tgt] {
						seenNodes[tgt] = true
						graph.Nodes = append(graph.Nodes, tgt)
					}
					graph.Edges = append(graph.Edges, DepGraphEdge{
						Source: source,
						Target: tgt,
					})
				}
			}
		}
	}

	return graph, nil
}

// --- LinkerdProvider ---
//
// Reads Linkerd service-profiles and server annotations. Linkerd exposes
// dependencies via:
//   - service-profiles (service.istio.io spec routes)
//   - The Linkerd viz API (top connections)
//
// This implementation uses a simplified approach: reads service-profiles
// and extracts retryBudget/destinations. A full implementation would query
// the Linkerd viz API for observed traffic.
//
// Requires Linkerd CRDs to be installed.

// LinkerdProvider builds graphs from Linkerd ServiceProfile resources.
type LinkerdProvider struct {
	clientset     kubernetes.Interface
	dynamicClient dynamicClient
}

// NewLinkerdProvider creates a Linkerd-based graph provider.
func NewLinkerdProvider(clientset kubernetes.Interface, dyn dynamicClient) *LinkerdProvider {
	return &LinkerdProvider{clientset: clientset, dynamicClient: dyn}
}

// Name returns the provider name.
func (l *LinkerdProvider) Name() string { return "linkerd" }

// GraphFor reads Linkerd ServiceProfiles in the namespaces of the anomalous
// nodes. ServiceProfiles are named after the target service, so each
// ServiceProfile's name IS the target service.
func (l *LinkerdProvider) GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	graph := &DepGraph{}
	seenNodes := map[DepGraphNode]bool{}

	namespaces := map[string]bool{}
	for _, node := range nodes {
		namespaces[node.Namespace] = true
		seenNodes[node] = true
		graph.Nodes = append(graph.Nodes, node)
	}

	if l.dynamicClient == nil {
		return graph, nil
	}

	for ns := range namespaces {
		sps, err := l.dynamicClient.List(ctx, ns, "serviceprofiles", "v1alpha2")
		if err != nil {
			logging.Debug("linkerd provider: list serviceprofiles failed", "namespace", ns, "error", err)
			continue
		}

		for _, sp := range sps {
			name, _ := sp["metadata"].(map[string]any)["name"].(string)
			if name == "" {
				continue
			}
			// ServiceProfile name is the target service — it defines routes
			// TO that service. We can't infer the source from the profile
			// alone, but we can add the target as a known node.
			tgt := DepGraphNode{Service: name, Namespace: ns}
			if !seenNodes[tgt] {
				seenNodes[tgt] = true
				graph.Nodes = append(graph.Nodes, tgt)
			}
		}
	}

	return graph, nil
}

// --- CompositeProvider ---
//
// Tries each provider in priority order. Returns the first non-empty graph
// (has edges). If all providers return empty graphs, returns the last graph
// (which has at least the anomalous nodes).

// CompositeProvider tries multiple providers in priority order.
type CompositeProvider struct {
	providers []DepGraphProvider
}

// NewCompositeProvider creates a composite provider. Providers are tried in
// the order given. The first provider that returns a graph with edges wins.
func NewCompositeProvider(providers ...DepGraphProvider) *CompositeProvider {
	return &CompositeProvider{providers: providers}
}

// Name returns the names of all providers.
func (c *CompositeProvider) Name() string {
	names := make([]string, len(c.providers))
	for i, p := range c.providers {
		names[i] = p.Name()
	}
	return "composite[" + strings.Join(names, ",") + "]"
}

// GraphFor tries each provider in order. Returns the first graph with edges.
// If all return empty, returns the last graph (at minimum, the anomalous nodes).
func (c *CompositeProvider) GraphFor(ctx context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	var lastGraph *DepGraph
	var lastErr error

	for _, p := range c.providers {
		graph, err := p.GraphFor(ctx, nodes)
		if err != nil {
			logging.Debug("composite: provider failed", "provider", p.Name(), "error", err)
			lastErr = err
			continue
		}
		lastGraph = graph
		if len(graph.Edges) > 0 {
			logging.Info("composite: dependency graph built", "provider", p.Name(), "nodes", len(graph.Nodes), "edges", len(graph.Edges))
			return graph, nil
		}
		logging.Debug("composite: provider returned empty graph", "provider", p.Name())
	}

	if lastGraph == nil {
		lastGraph = &DepGraph{}
		lastGraph.Nodes = append(lastGraph.Nodes, nodes...)
	}
	if lastErr != nil {
		return lastGraph, fmt.Errorf("all providers failed or returned empty (last error: %w)", lastErr)
	}
	logging.Info("composite: no provider produced edges — using anomalous nodes only", "nodes", len(lastGraph.Nodes))
	return lastGraph, nil
}

// --- NoopProvider ---
//
// Always returns an empty graph (just the anomalous nodes). Used as the
// final fallback in the composite chain.

// NoopProvider returns an empty graph (just the input nodes).
type NoopProvider struct{}

// NewNoopProvider creates a noop provider.
func NewNoopProvider() *NoopProvider { return &NoopProvider{} }

// Name returns "noop".
func (n *NoopProvider) Name() string { return "noop" }

// GraphFor returns a graph with just the input nodes and no edges.
func (n *NoopProvider) GraphFor(_ context.Context, nodes []DepGraphNode) (*DepGraph, error) {
	graph := &DepGraph{Nodes: nodes}
	return graph, nil
}

// Compile-time checks.
var _ DepGraphProvider = (*AnnotationProvider)(nil)
var _ DepGraphProvider = (*NetworkPolicyProvider)(nil)
var _ DepGraphProvider = (*IstioProvider)(nil)
var _ DepGraphProvider = (*LinkerdProvider)(nil)
var _ DepGraphProvider = (*CompositeProvider)(nil)
var _ DepGraphProvider = (*NoopProvider)(nil)

// unused but kept for future use
var _ = sync.Mutex{}
var _ = corev1.Pod{}
var _ = networkingv1.NetworkPolicy{}
