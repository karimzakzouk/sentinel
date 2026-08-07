package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Verdict is what the webhook returns for a single admission request. It's
// a simplified view of admissionv1.AdmissionResponse used internally and by
// tests; Handle translates it to the wire AdmissionResponse.
type Verdict struct {
	Allowed   bool
	Reason    string   // human-readable, shown to kubectl user.
	Warnings  []string // k8s 1.19+ — shown but don't block.
	PolicyIDs []string // policies that fired, for audit trail / metrics.
}

// Policy is a single admission rule.
type Policy interface {
	// ID is the stable identifier used in audit logs and metrics.
	ID() string
	// Evaluate is called for every admission request. It must be fast
	// (<50ms) — the kube-apiserver blocks on the webhook response. Return
	// nil to skip this policy (no opinion).
	Evaluate(ctx context.Context, req *admissionv1.AdmissionRequest) *PolicyResult
}

// PolicyResult is what a Policy returns.
type PolicyResult struct {
	Deny     bool
	Reason   string
	Warnings []string
}

// IncidentMemory is the interface over internal/feedback's vector store. It
// lets us answer "does this object look like something that caused an
// incident before?" The real adapter wraps feedback.VectorStore; this
// interface is owned by the consumer (admission) so feedback can depend on
// admission without an import cycle.
type IncidentMemory interface {
	// SimilarIncidents returns incident IDs whose embedded representation
	// is within `threshold` cosine distance of the object's embedding.
	SimilarIncidents(ctx context.Context, obj Manifest, threshold float64) ([]string, error)
}

// Manifest is the JSON-serialized K8s object being admitted, plus metadata.
type Manifest struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	JSON      []byte
}

// CVEScanner checks container images for known vulnerabilities.
// Real impl would call Trivy/Grype as a subprocess or HTTP API.
type CVEScanner interface {
	ScanImage(ctx context.Context, image string) (CVEScanResult, error)
}

// CVEScanResult is the outcome of an image scan.
type CVEScanResult struct {
	CriticalCount   int
	HighCount       int
	UnfixedCritical int // no fix is available — the scariest category.
}

// Handler is the admission webhook's HTTP-agnostic core. It takes a raw
// AdmissionReview, evaluates every registered Policy, and returns the
// verdict. The HTTP layer lives in main.go; this struct is pure logic so
// it can be unit-tested without a network.
type Handler struct {
	policies []Policy
}

// NewHandler creates a Handler with the default policy set. Pass nil for
// memory or scanner to disable the corresponding learned/CVE policies.
func NewHandler(memory IncidentMemory, scanner CVEScanner) *Handler {
	return &Handler{
		policies: defaultPolicies(memory, scanner),
	}
}

// RegisterPolicy appends a custom policy to the chain. Policies are
// evaluated in registration order.
func (h *Handler) RegisterPolicy(p Policy) {
	h.policies = append(h.policies, p)
}

// Handle processes a single AdmissionReview request. The returned
// AdmissionResponse is suitable for JSON-encoding back to the
// kube-apiserver.
func (h *Handler) Handle(ctx context.Context, review *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	if review == nil || review.Request == nil {
		return &admissionv1.AdmissionResponse{Allowed: false, Result: &metav1.Status{Message: "empty admission request"}}
	}

	req := review.Request
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var deniedBy []string
	var warnings []string
	var policyIDs []string

	for _, p := range h.policies {
		result := p.Evaluate(ctx, req)
		if result == nil {
			continue
		}
		policyIDs = append(policyIDs, p.ID())
		if len(result.Warnings) > 0 {
			warnings = append(warnings, result.Warnings...)
		}
		if result.Deny {
			deniedBy = append(deniedBy, fmt.Sprintf("%s: %s", p.ID(), result.Reason))
		}
	}

	resp := &admissionv1.AdmissionResponse{
		UID:      req.UID,
		Allowed:  len(deniedBy) == 0,
		Warnings: warnings,
	}
	if !resp.Allowed {
		resp.Result = &metav1.Status{
			Status:  metav1.StatusFailure,
			Reason:  metav1.StatusReasonForbidden,
			Message: fmt.Sprintf("denied by Sentinel admission policies: %s", strings.Join(deniedBy, "; ")),
		}
		logging.Warn("admission: DENIED",
			"kind", req.Kind.Kind,
			"namespace", req.Namespace,
			"name", req.Name,
			"operation", req.Operation,
			"policies", deniedBy,
		)
	} else {
		logging.Info("admission: allowed",
			"kind", req.Kind.Kind,
			"namespace", req.Namespace,
			"name", req.Name,
			"operation", req.Operation,
			"policies_evaluated", len(policyIDs),
		)
	}

	return resp
}

// defaultPolicies returns the built-in policy set.
func defaultPolicies(memory IncidentMemory, scanner CVEScanner) []Policy {
	return []Policy{
		&NoPrivilegedPodsPolicy{},
		&RequireResourceLimitsPolicy{},
		&BlockKnownBadImagesPolicy{Scanner: scanner},
		&SimilarToPastIncidentPolicy{Memory: memory, Threshold: 0.85},
	}
}

// --- Built-in policies ---

// NoPrivilegedPodsPolicy blocks privileged: true. This is Kyverno/PSA parity.
type NoPrivilegedPodsPolicy struct{}

// ID implements Policy.
func (p *NoPrivilegedPodsPolicy) ID() string { return "sentinel.no-privileged-pods" }

// Evaluate implements Policy.
func (p *NoPrivilegedPodsPolicy) Evaluate(_ context.Context, req *admissionv1.AdmissionRequest) *PolicyResult {
	podSpec, ok := extractPodSpec(req)
	if !ok {
		return nil
	}
	for i := range podSpec.Containers {
		sc := podSpec.Containers[i].SecurityContext
		if sc != nil && sc.Privileged != nil && *sc.Privileged {
			return &PolicyResult{
				Deny:   true,
				Reason: fmt.Sprintf("container %q is privileged — Sentinel policy forbids privileged pods", podSpec.Containers[i].Name),
			}
		}
	}
	return nil
}

// RequireResourceLimitsPolicy blocks containers without resource limits.
// This is the #1 cause of OOM-neighbor-noise incidents.
type RequireResourceLimitsPolicy struct{}

// ID implements Policy.
func (p *RequireResourceLimitsPolicy) ID() string { return "sentinel.require-resource-limits" }

// Evaluate implements Policy.
func (p *RequireResourceLimitsPolicy) Evaluate(_ context.Context, req *admissionv1.AdmissionRequest) *PolicyResult {
	podSpec, ok := extractPodSpec(req)
	if !ok {
		return nil
	}
	var missing []string
	for i := range podSpec.Containers {
		limits := podSpec.Containers[i].Resources.Limits
		if limits == nil || limits.Cpu().IsZero() || limits.Memory().IsZero() {
			missing = append(missing, podSpec.Containers[i].Name)
		}
	}
	if len(missing) > 0 {
		return &PolicyResult{
			Deny:   true,
			Reason: fmt.Sprintf("containers without resource limits: %s (resource limits prevent OOM-neighbor noise)", strings.Join(missing, ", ")),
		}
	}
	return nil
}

// BlockKnownBadImagesPolicy rejects images with unfixed critical CVEs.
type BlockKnownBadImagesPolicy struct {
	Scanner CVEScanner
}

// ID implements Policy.
func (p *BlockKnownBadImagesPolicy) ID() string { return "sentinel.block-bad-images" }

// Evaluate implements Policy.
func (p *BlockKnownBadImagesPolicy) Evaluate(ctx context.Context, req *admissionv1.AdmissionRequest) *PolicyResult {
	if p.Scanner == nil {
		return nil // scanner not configured — skip.
	}
	podSpec, ok := extractPodSpec(req)
	if !ok {
		return nil
	}
	for i := range podSpec.Containers {
		image := podSpec.Containers[i].Image
		if image == "" {
			continue
		}
		result, err := p.Scanner.ScanImage(ctx, image)
		if err != nil {
			// Scanner failure is a warning, not a deny — we don't want
			// a flaky scanner to block all deploys.
			return &PolicyResult{
				Warnings: []string{fmt.Sprintf("CVE scan failed for %q: %v (deploy allowed, scan will retry)", image, err)},
			}
		}
		if result.UnfixedCritical > 0 {
			return &PolicyResult{
				Deny:   true,
				Reason: fmt.Sprintf("image %q has %d unfixed CRITICAL vulnerabilities — deploy blocked", image, result.UnfixedCritical),
			}
		}
		if result.CriticalCount >= 5 {
			return &PolicyResult{
				Warnings: []string{fmt.Sprintf("image %q has %d critical CVEs (threshold=5) — consider pinning a patched tag", image, result.CriticalCount)},
			}
		}
	}
	return nil
}

// SimilarToPastIncidentPolicy is the unique Sentinel value-add: it checks
// whether the manifest being admitted looks similar to manifests that caused
// past incidents. Kyverno and Gatekeeper cannot do this — they have no
// incident memory.
//
// The policy emits a warning, not a deny. Hard-deny on a learned policy
// would be too aggressive until the similarity model is well-calibrated.
type SimilarToPastIncidentPolicy struct {
	Memory    IncidentMemory
	Threshold float64
}

// ID implements Policy.
func (p *SimilarToPastIncidentPolicy) ID() string { return "sentinel.similar-to-past-incident" }

// Evaluate implements Policy.
func (p *SimilarToPastIncidentPolicy) Evaluate(ctx context.Context, req *admissionv1.AdmissionRequest) *PolicyResult {
	if p.Memory == nil {
		return nil
	}
	manifest := Manifest{
		GVK:       schema.GroupVersionKind{Group: req.Kind.Group, Version: req.Kind.Version, Kind: req.Kind.Kind},
		Namespace: req.Namespace,
		Name:      req.Name,
		JSON:      req.Object.Raw,
	}
	similar, err := p.Memory.SimilarIncidents(ctx, manifest, p.Threshold)
	if err != nil {
		return &PolicyResult{
			Warnings: []string{fmt.Sprintf("incident similarity check failed: %v", err)},
		}
	}
	if len(similar) == 0 {
		return nil
	}
	warnings := []string{
		fmt.Sprintf("⚠️ This manifest is similar to %d past incident(s): %s. Review before proceeding.",
			len(similar), strings.Join(similar, ", ")),
	}
	return &PolicyResult{Warnings: warnings}
}

// extractPodSpec pulls the PodSpec out of a Pod or a PodTemplate (Deployment,
// StatefulSet, DaemonSet, Job, CronJob, ReplicaSet). Returns ok=false for
// other resource kinds.
func extractPodSpec(req *admissionv1.AdmissionRequest) (*corev1.PodSpec, bool) {
	if req.Object.Raw == nil {
		return nil, false
	}
	switch req.Kind.Kind {
	case "Pod":
		var pod corev1.Pod
		if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
			return nil, false
		}
		return &pod.Spec, true
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		// All of these embed a PodTemplateSpec under .spec.template.
		var obj struct {
			Spec struct {
				Template corev1.PodTemplateSpec `json:"template"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
			return nil, false
		}
		return &obj.Spec.Template.Spec, true
	case "CronJob":
		var obj struct {
			Spec struct {
				JobTemplate struct {
					Spec struct {
						Template corev1.PodTemplateSpec `json:"template"`
					} `json:"spec"`
				} `json:"jobTemplate"`
			} `json:"spec"`
		}
		if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
			return nil, false
		}
		return &obj.Spec.JobTemplate.Spec.Template.Spec, true
	}
	return nil, false
}

// Compile-time check that the built-in policies implement Policy.
var (
	_ Policy = (*NoPrivilegedPodsPolicy)(nil)
	_ Policy = (*RequireResourceLimitsPolicy)(nil)
	_ Policy = (*BlockKnownBadImagesPolicy)(nil)
	_ Policy = (*SimilarToPastIncidentPolicy)(nil)
)
