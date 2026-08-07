package admission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fakeMemory implements IncidentMemory. It returns whatever was configured.
type fakeMemory struct {
	similar []string
	err     error
	calls   int
}

func (f *fakeMemory) SimilarIncidents(_ context.Context, _ Manifest, _ float64) ([]string, error) {
	f.calls++
	return f.similar, f.err
}

// fakeScanner implements CVEScanner.
type fakeScanner struct {
	result CVEScanResult
	err    error
	calls  int
}

func (f *fakeScanner) ScanImage(_ context.Context, _ string) (CVEScanResult, error) {
	f.calls++
	return f.result, f.err
}

// podReview builds an AdmissionReview whose Object.Raw is the JSON-encoded
// pod. Pass kind="Pod" for a raw Pod; for workload kinds use
// workloadReview instead.
func podReview(pod *corev1.Pod) *admissionv1.AdmissionReview {
	raw, _ := json.Marshal(pod)
	return &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "req-1",
			Kind:      metav1.GroupVersionKind{Kind: "Pod"},
			Operation: admissionv1.Create,
			Namespace: "prod",
			Name:      pod.Name,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// workloadReview builds an AdmissionReview for a Deployment/StatefulSet/etc.
// The raw JSON must contain a .spec.template PodTemplateSpec.
func workloadReview(kind string, raw []byte) *admissionv1.AdmissionReview {
	return &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:       "req-1",
			Kind:      metav1.GroupVersionKind{Kind: kind},
			Operation: admissionv1.Create,
			Namespace: "prod",
			Name:      "workload",
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// makeQuantity returns a resource.Quantity for the given string (e.g. "100m").
func makeQuantity(s string) resource.Quantity {
	return resource.MustParse(s)
}

// cleanPod returns a Pod that satisfies every static policy: not privileged,
// has resource limits.
func cleanPod() *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "clean",
			Namespace: "prod",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "registry.example.com/app:v1.2.3",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    makeQuantity("500m"),
							corev1.ResourceMemory: makeQuantity("256Mi"),
						},
					},
				},
			},
		},
	}
}

// TestAdmission_NoPrivilegedPods verifies that a Pod with privileged: true
// is denied by the NoPrivilegedPodsPolicy.
func TestAdmission_NoPrivilegedPods(t *testing.T) {
	pod := cleanPod()
	pod.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
		Privileged: boolPtr(true),
	}

	h := NewHandler(nil, nil) // no memory, no scanner — static policies only.
	resp := h.Handle(context.Background(), podReview(pod))

	if resp.Allowed {
		t.Error("expected denial for privileged pod")
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil Result on denial")
	}
	if !strings.Contains(resp.Result.Message, "no-privileged-pods") {
		t.Errorf("expected denial message to mention policy ID, got: %s", resp.Result.Message)
	}
}

// TestAdmission_RequireResourceLimits verifies that a Pod missing resource
// limits is denied by the RequireResourceLimitsPolicy.
func TestAdmission_RequireResourceLimits(t *testing.T) {
	pod := cleanPod()
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{} // no limits

	h := NewHandler(nil, nil)
	resp := h.Handle(context.Background(), podReview(pod))

	if resp.Allowed {
		t.Error("expected denial for pod without resource limits")
	}
	if !strings.Contains(resp.Result.Message, "require-resource-limits") {
		t.Errorf("expected denial message to mention policy ID, got: %s", resp.Result.Message)
	}
}

// TestAdmission_BlockBadImages verifies that a Pod whose image has unfixed
// critical CVEs is denied by the BlockKnownBadImagesPolicy.
func TestAdmission_BlockBadImages(t *testing.T) {
	pod := cleanPod()
	pod.Spec.Containers[0].Image = "registry.example.com/app:vulnerable"

	scanner := &fakeScanner{result: CVEScanResult{CriticalCount: 7, UnfixedCritical: 2}}
	h := NewHandler(nil, scanner)

	resp := h.Handle(context.Background(), podReview(pod))

	if resp.Allowed {
		t.Error("expected denial for image with unfixed critical CVEs")
	}
	if !strings.Contains(resp.Result.Message, "block-bad-images") {
		t.Errorf("expected denial message to mention policy ID, got: %s", resp.Result.Message)
	}
	if !strings.Contains(resp.Result.Message, "unfixed CRITICAL") {
		t.Errorf("expected denial reason to mention unfixed critical CVEs, got: %s", resp.Result.Message)
	}
}

// TestAdmission_BlockBadImages_WarnOnHighCriticalCount verifies that an
// image with many (but all fixed) critical CVEs produces a warning, not a
// denial.
func TestAdmission_BlockBadImages_WarnOnHighCriticalCount(t *testing.T) {
	pod := cleanPod()
	pod.Spec.Containers[0].Image = "registry.example.com/app:many-cves"

	scanner := &fakeScanner{result: CVEScanResult{CriticalCount: 6, UnfixedCritical: 0}}
	h := NewHandler(nil, scanner)

	resp := h.Handle(context.Background(), podReview(pod))

	if !resp.Allowed {
		t.Error("expected allow (warning only) for image with no unfixed CVEs")
	}
	if len(resp.Warnings) == 0 {
		t.Error("expected at least one warning about critical CVE count")
	}
}

// TestAdmission_BlockBadImages_ScanErrorWarns verifies that a scanner
// failure produces a warning rather than blocking the deploy.
func TestAdmission_BlockBadImages_ScanErrorWarns(t *testing.T) {
	pod := cleanPod()
	pod.Spec.Containers[0].Image = "registry.example.com/app:flaky"

	scanner := &fakeScanner{err: errors.New("trivy timeout")}
	h := NewHandler(nil, scanner)

	resp := h.Handle(context.Background(), podReview(pod))

	if !resp.Allowed {
		t.Error("expected allow (warning only) when scanner fails")
	}
	if len(resp.Warnings) == 0 {
		t.Error("expected warning about scan failure")
	}
}

// TestAdmission_SimilarToPastIncident verifies that a manifest similar to
// past incidents produces a warning (NOT a denial — the learned policy is
// conservative).
func TestAdmission_SimilarToPastIncident(t *testing.T) {
	pod := cleanPod()
	memory := &fakeMemory{similar: []string{"INC-47", "INC-89"}}
	h := NewHandler(memory, nil)

	resp := h.Handle(context.Background(), podReview(pod))

	if !resp.Allowed {
		t.Error("expected allow (warning only) for similar-to-past-incident policy")
	}
	var sawSimilarityWarning bool
	for _, w := range resp.Warnings {
		if strings.Contains(w, "INC-47") && strings.Contains(w, "INC-89") {
			sawSimilarityWarning = true
		}
	}
	if !sawSimilarityWarning {
		t.Errorf("expected warning listing INC-47, INC-89; got %v", resp.Warnings)
	}
}

// TestAdmission_SimilarToPastIncident_MemoryErrorWarns verifies that an
// incident-memory error produces a warning rather than denying.
func TestAdmission_SimilarToPastIncident_MemoryErrorWarns(t *testing.T) {
	pod := cleanPod()
	memory := &fakeMemory{err: errors.New("vector store offline")}
	h := NewHandler(memory, nil)

	resp := h.Handle(context.Background(), podReview(pod))

	if !resp.Allowed {
		t.Error("expected allow (warning only) when memory check fails")
	}
	if len(resp.Warnings) == 0 {
		t.Error("expected warning about memory check failure")
	}
}

// TestAdmission_AllowsCleanPod verifies that a Pod with no policy
// violations is allowed.
func TestAdmission_AllowsCleanPod(t *testing.T) {
	pod := cleanPod()
	memory := &fakeMemory{similar: nil} // no similar incidents
	scanner := &fakeScanner{result: CVEScanResult{CriticalCount: 0, UnfixedCritical: 0}}
	h := NewHandler(memory, scanner)

	resp := h.Handle(context.Background(), podReview(pod))

	if !resp.Allowed {
		t.Errorf("expected allow for clean pod; got denial: %+v", resp.Result)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings for clean pod, got %v", resp.Warnings)
	}
	if resp.UID != "req-1" {
		t.Errorf("expected UID echoed back, got %q", resp.UID)
	}
}

// TestAdmission_NilRequest verifies that a nil AdmissionRequest returns a
// denial rather than panicking.
func TestAdmission_NilRequest(t *testing.T) {
	h := NewHandler(nil, nil)
	resp := h.Handle(context.Background(), &admissionv1.AdmissionReview{})
	if resp.Allowed {
		t.Error("expected denial for nil request")
	}
}

// TestAdmission_DeploymentPodTemplate verifies that the PodSpec extraction
// handles Deployment objects (which embed PodTemplateSpec under
// .spec.template).
func TestAdmission_DeploymentPodTemplate(t *testing.T) {
	// Build a Deployment with a privileged container — should be denied.
	depJSON := []byte(`{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"metadata": {"name": "bad", "namespace": "prod"},
		"spec": {
			"template": {
				"spec": {
					"containers": [{
						"name": "app",
						"image": "registry.example.com/app:v1",
						"resources": {"limits": {"cpu": "100m", "memory": "128Mi"}},
						"securityContext": {"privileged": true}
					}]
				}
			}
		}
	}`)

	h := NewHandler(nil, nil)
	resp := h.Handle(context.Background(), workloadReview("Deployment", depJSON))

	if resp.Allowed {
		t.Error("expected denial for privileged container in Deployment")
	}
}

// TestAdmission_CronJobPodTemplate verifies that the PodSpec extraction
// handles CronJob objects (which embed PodTemplateSpec under
// .spec.jobTemplate.spec.template).
func TestAdmission_CronJobPodTemplate(t *testing.T) {
	cronJSON := []byte(`{
		"apiVersion": "batch/v1",
		"kind": "CronJob",
		"metadata": {"name": "bad", "namespace": "prod"},
		"spec": {
			"schedule": "*/5 * * * *",
			"jobTemplate": {
				"spec": {
					"template": {
						"spec": {
							"containers": [{
								"name": "app",
								"image": "registry.example.com/app:v1",
								"resources": {"limits": {"cpu": "100m", "memory": "128Mi"}},
								"securityContext": {"privileged": true}
							}]
						}
					}
				}
			}
		}
	}`)

	h := NewHandler(nil, nil)
	resp := h.Handle(context.Background(), workloadReview("CronJob", cronJSON))

	if resp.Allowed {
		t.Error("expected denial for privileged container in CronJob")
	}
}

// TestAdmission_NonPodKindSkipsPodPolicies verifies that for resource kinds
// without a PodSpec (e.g. ConfigMap), the PodSpec policies return nil and
// the request is allowed (the CVE scanner is also skipped because there
// are no containers to scan).
func TestAdmission_NonPodKindSkipsPodPolicies(t *testing.T) {
	cmJSON := []byte(`{
		"apiVersion": "v1",
		"kind": "ConfigMap",
		"metadata": {"name": "config", "namespace": "prod"},
		"data": {"key": "value"}
	}`)

	h := NewHandler(nil, nil)
	resp := h.Handle(context.Background(), workloadReview("ConfigMap", cmJSON))

	if !resp.Allowed {
		t.Error("expected ConfigMap to be allowed (no PodSpec policies apply)")
	}
}

// TestAdmission_RegisterCustomPolicy verifies that custom policies can be
// registered and are evaluated alongside the defaults.
func TestAdmission_RegisterCustomPolicy(t *testing.T) {
	pod := cleanPod()
	h := NewHandler(nil, nil)
	h.RegisterPolicy(&denyAllPolicy{})

	resp := h.Handle(context.Background(), podReview(pod))
	if resp.Allowed {
		t.Error("expected denial from custom deny-all policy")
	}
	if !strings.Contains(resp.Result.Message, "deny-all") {
		t.Errorf("expected denial message to mention custom policy ID, got: %s", resp.Result.Message)
	}
}

// denyAllPolicy is a test-only Policy that always denies.
type denyAllPolicy struct{}

func (p *denyAllPolicy) ID() string { return "test.deny-all" }
func (p *denyAllPolicy) Evaluate(_ context.Context, _ *admissionv1.AdmissionRequest) *PolicyResult {
	return &PolicyResult{Deny: true, Reason: "deny-all policy always denies"}
}

// TestAdmission_ManifestShape verifies that SimilarToPastIncidentPolicy
// constructs a Manifest with the right GVK, namespace, name, and raw JSON
// from the AdmissionRequest.
func TestAdmission_ManifestShape(t *testing.T) {
	pod := cleanPod()
	pod.Name = "myapp"
	pod.Namespace = "staging"

	var captured Manifest
	memory := &similarMemoryCapturing{capture: &captured}
	h := NewHandler(memory, nil)
	h.Handle(context.Background(), podReview(pod))

	if captured.Name != "myapp" {
		t.Errorf("expected manifest name myapp, got %q", captured.Name)
	}
	if captured.Namespace != "prod" { // AdmissionRequest.Namespace, not pod's
		t.Errorf("expected manifest namespace from request (prod), got %q", captured.Namespace)
	}
	if captured.GVK.Kind != "Pod" {
		t.Errorf("expected GVK Kind Pod, got %q", captured.GVK.Kind)
	}
	if len(captured.JSON) == 0 {
		t.Error("expected non-empty raw JSON")
	}
}

// similarMemoryCapturing captures the Manifest passed to SimilarIncidents.
type similarMemoryCapturing struct {
	capture *Manifest
}

func (m *similarMemoryCapturing) SimilarIncidents(_ context.Context, obj Manifest, _ float64) ([]string, error) {
	*m.capture = obj
	return nil, nil
}

// TestAdmission_PolicyIDsCompiled verifies that the policy IDs the handler
// records match the registered policies.
func TestAdmission_PolicyIDsCompiled(t *testing.T) {
	// Compile-time check that the built-in policies implement Policy.
	var _ Policy = (*NoPrivilegedPodsPolicy)(nil)
	var _ Policy = (*RequireResourceLimitsPolicy)(nil)
	var _ Policy = (*BlockKnownBadImagesPolicy)(nil)
	var _ Policy = (*SimilarToPastIncidentPolicy)(nil)

	// And a sanity check on the IDs.
	policies := defaultPolicies(nil, nil)
	want := map[string]bool{
		"sentinel.no-privileged-pods":       true,
		"sentinel.require-resource-limits":  true,
		"sentinel.block-bad-images":         true,
		"sentinel.similar-to-past-incident": true,
	}
	for _, p := range policies {
		if !want[p.ID()] {
			t.Errorf("unexpected policy ID: %s", p.ID())
		}
	}
}

// TestAdmission_ManifestGVK ensures the Manifest type can hold a
// schema.GroupVersionKind without panicking.
func TestAdmission_ManifestGVK(t *testing.T) {
	m := Manifest{
		GVK:       schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"},
		Namespace: "prod",
		Name:      "app",
		JSON:      []byte("{}"),
	}
	if m.GVK.Kind != "Deployment" {
		t.Errorf("expected Deployment, got %s", m.GVK.Kind)
	}
}
