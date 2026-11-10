package capture

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InjectWebhook mutates Pods in opted-in namespaces so they trust the capture
// CA and send HTTPS through the MITM. Transparent TPROXY without HTTPS_PROXY
// stays out of scope.
type InjectWebhook struct {
	ProxyURL string
	Log      *slog.Logger
}

func (h *InjectWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil || review.Request == nil {
		http.Error(w, "AdmissionReview required", http.StatusBadRequest)
		return
	}
	resp := h.admit(review.Request)
	review.Response = resp
	review.Request = nil
	out, err := json.Marshal(review)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (h *InjectWebhook) admit(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	resp := &admissionv1.AdmissionResponse{UID: req.UID, Allowed: true}
	if req.Kind.Kind != "Pod" || req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return resp
	}
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		resp.Allowed = false
		resp.Result = &metav1.Status{Message: "pod decode: " + err.Error()}
		return resp
	}
	if pod.Annotations[InjectAnnotation] == InjectValue {
		return resp
	}
	before, err := json.Marshal(pod)
	if err != nil {
		resp.Allowed = false
		resp.Result = &metav1.Status{Message: err.Error()}
		return resp
	}
	InjectMITM(&pod.Spec, h.ProxyURL)
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[InjectAnnotation] = InjectValue
	after, err := json.Marshal(pod)
	if err != nil {
		resp.Allowed = false
		resp.Result = &metav1.Status{Message: err.Error()}
		return resp
	}
	patch, err := jsonPatch(before, after)
	if err != nil || len(patch) == 0 {
		return resp
	}
	pt := admissionv1.PatchTypeJSONPatch
	resp.PatchType = &pt
	resp.Patch = patch
	if h.Log != nil {
		h.Log.Info("injected capture MITM into pod", "namespace", req.Namespace, "name", pod.Name)
	}
	return resp
}

func jsonPatch(before, after []byte) ([]byte, error) {
	var a, b map[string]any
	if err := json.Unmarshal(before, &a); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return nil, err
	}
	var ops []map[string]any
	if _, ok := a["metadata"].(map[string]any); !ok {
		return nil, fmt.Errorf("pod metadata missing")
	}
	// Full-object replace of spec and annotations is enough and stays small.
	ops = append(ops,
		map[string]any{"op": "add", "path": "/metadata/annotations", "value": b["metadata"].(map[string]any)["annotations"]},
		map[string]any{"op": "replace", "path": "/spec", "value": b["spec"]},
	)
	// If annotations already existed, "add" can fail; use replace when present.
	if meta, _ := a["metadata"].(map[string]any); meta != nil {
		if _, ok := meta["annotations"]; ok {
			ops[0]["op"] = "replace"
		}
	}
	return json.Marshal(ops)
}
