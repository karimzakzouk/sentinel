package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/settings"
)

// Fixed Secret names the chart mounts as optional. The console fills them
// write-only so people never put LLM or Slack keys in Helm values. Forge
// credentials stay out of the UI on purpose (Git write blast radius).
const (
	slackSecretName = "sentinel-slack"
	slackSecretKey  = "webhook-url"
	llmSecretName   = "sentinel-fixer-llm"
	llmSecretKey    = "key"
)

type secretStatus struct {
	Slack bool `json:"slack"`
	LLM   bool `json:"llm"`
}

func (c *Console) getSecretStatus(w http.ResponseWriter, r *http.Request, _ *session, k kubernetes.Interface, _ dynamic.Interface) {
	ns := c.settingsNS()
	writeJSON(w, secretStatus{
		Slack: secretHasKey(r.Context(), k, ns, slackSecretName, slackSecretKey),
		LLM:   secretHasKey(r.Context(), k, ns, llmSecretName, llmSecretKey),
	})
}

func (c *Console) putSlackSecret(w http.ResponseWriter, r *http.Request, _ *session, k kubernetes.Interface, _ dynamic.Interface) {
	var in struct {
		WebhookURL string `json:"webhookUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil || strings.TrimSpace(in.WebhookURL) == "" {
		http.Error(w, "webhookUrl is required", http.StatusBadRequest)
		return
	}
	url := strings.TrimSpace(in.WebhookURL)
	if !strings.HasPrefix(url, "https://") {
		http.Error(w, "webhookUrl must be an https URL", http.StatusBadRequest)
		return
	}
	ns := c.settingsNS()
	if err := upsertSecret(r.Context(), k, ns, slackSecretName, map[string][]byte{slackSecretKey: []byte(url)}); err != nil {
		kubeError(w, err)
		return
	}
	_ = bounceDeploy(r.Context(), k, ns, "sentinel")
	writeJSON(w, map[string]string{"done": "Slack webhook saved; Sentinel reloads within a minute"})
}

func (c *Console) putLLMSecret(w http.ResponseWriter, r *http.Request, _ *session, k kubernetes.Interface, _ dynamic.Interface) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Key) == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}
	ns := c.settingsNS()
	if err := upsertSecret(r.Context(), k, ns, llmSecretName, map[string][]byte{llmSecretKey: []byte(strings.TrimSpace(in.Key))}); err != nil {
		kubeError(w, err)
		return
	}
	_ = bounceDeploy(r.Context(), k, ns, "sentinel-fixer")
	writeJSON(w, map[string]string{"done": "LLM key saved (never shown again); Fixer reloads within a minute"})
}

func (c *Console) deleteSecret(w http.ResponseWriter, r *http.Request, _ *session, k kubernetes.Interface, _ dynamic.Interface) {
	which := r.PathValue("name")
	var secret, deploy string
	switch which {
	case "slack":
		secret, deploy = slackSecretName, "sentinel"
	case "llm":
		secret, deploy = llmSecretName, "sentinel-fixer"
	default:
		http.Error(w, "slack or llm", http.StatusNotFound)
		return
	}
	ns := c.settingsNS()
	err := k.CoreV1().Secrets(ns).Delete(r.Context(), secret, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		kubeError(w, err)
		return
	}
	_ = bounceDeploy(r.Context(), k, ns, deploy)
	writeJSON(w, map[string]string{"done": "cleared " + which + " secret"})
}

func secretHasKey(ctx context.Context, k kubernetes.Interface, ns, name, key string) bool {
	sec, err := k.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return len(sec.Data[key]) > 0
}

func upsertSecret(ctx context.Context, k kubernetes.Interface, ns, name string, data map[string][]byte) error {
	sec, err := k.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k.CoreV1().Secrets(ns).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
				"app.kubernetes.io/part-of": "sentinel",
				"app.kubernetes.io/name":    name,
			}},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}, metav1.CreateOptions{FieldManager: "sentinel-console"})
		return err
	}
	if err != nil {
		return err
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for k, v := range data {
		sec.Data[k] = v
	}
	_, err = k.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{FieldManager: "sentinel-console"})
	return err
}

// listNamespaces returns cluster namespaces the signed-in person can see,
// for the first-run watch picker. System and Sentinel namespaces are flagged.
func (c *Console) listNamespaces(w http.ResponseWriter, r *http.Request, _ *session, k kubernetes.Interface, _ dynamic.Interface) {
	list, err := k.CoreV1().Namespaces().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		kubeError(w, err)
		return
	}
	type row struct {
		Name     string `json:"name"`
		Watched  bool   `json:"watched"`
		Blocked  bool   `json:"blocked"`
		Reason   string `json:"reason,omitempty"`
	}
	blocked := map[string]string{
		"kube-system": "Kubernetes system", "kube-public": "Kubernetes system", "kube-node-lease": "Kubernetes system",
		c.settingsNS(): "Sentinel", "sentinel-actor": "Sentinel", "sentinel-capture": "Sentinel", "sentinel-node-agent": "Sentinel",
	}
	out := make([]row, 0, len(list.Items))
	for _, ns := range list.Items {
		item := row{Name: ns.Name, Watched: ns.Labels[settings.WatchedLabel] == "true"}
		if why, ok := blocked[ns.Name]; ok || strings.HasPrefix(ns.Name, "sentinel-lab-") {
			item.Blocked = true
			if !ok {
				why = "Sentinel Lab"
			}
			item.Reason = why
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{"namespaces": out})
}

func bounceDeploy(ctx context.Context, k kubernetes.Interface, ns, name string) error {
	stamp, _ := json.Marshal(metav1.Now().UTC().Format(time.RFC3339))
	patch := fmt.Appendf(nil,
		`{"spec":{"template":{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/secret-applied":%s}}}}}`,
		stamp)
	_, err := k.AppsV1().Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{
		FieldManager: "sentinel-console",
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
