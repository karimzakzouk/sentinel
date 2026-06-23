package executors

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// K8sExecutor applies K8s patches (restart, scale, rollback).
type K8sExecutor struct {
	clientset *kubernetes.Clientset
}

// NewK8s creates a K8sExecutor.
func NewK8s(clientset *kubernetes.Clientset) *K8sExecutor {
	return &K8sExecutor{clientset: clientset}
}

// Restart performs a rollout restart on the target Deployment.
func (e *K8sExecutor) Restart(ctx context.Context, fp *schemas.FixProposal) error {
	dep, err := e.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{})
	if err != nil {
		// Try by label
		deps, listErr := e.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
		})
		if listErr != nil || len(deps.Items) == 0 {
			return fmt.Errorf("deployment %s not found in %s: %w", fp.TargetService, fp.TargetNamespace, err)
		}
		dep = &deps.Items[0]
	}

	// Restart by patching the restart annotation
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, metav1.Now().Format("2006-01-02T15:04:05Z"))
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// Scale patches the replica count on the target Deployment.
func (e *K8sExecutor) Scale(ctx context.Context, fp *schemas.FixProposal, newReplicas int32) error {
	dep, err := e.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{})
	if err != nil {
		deps, listErr := e.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
		})
		if listErr != nil || len(deps.Items) == 0 {
			return fmt.Errorf("deployment %s not found: %w", fp.TargetService, err)
		}
		dep = &deps.Items[0]
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, newReplicas)
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// Rollback performs a kubectl rollout undo on the target Deployment.
func (e *K8sExecutor) Rollback(ctx context.Context, fp *schemas.FixProposal) error {
	dep, err := e.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{})
	if err != nil {
		deps, listErr := e.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
		})
		if listErr != nil || len(deps.Items) == 0 {
			return fmt.Errorf("deployment %s not found: %w", fp.TargetService, err)
		}
		dep = &deps.Items[0]
	}

	// Find the previous ReplicaSet
	rss, err := e.clientset.AppsV1().ReplicaSets(fp.TargetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
	})
	if err != nil || len(rss.Items) < 2 {
		return fmt.Errorf("no previous ReplicaSet to roll back to")
	}

	// Pick the second-newest ReplicaSet (the previous deployment)
	var prevRS *appsv1.ReplicaSet
	for i := range rss.Items {
		rs := &rss.Items[i]
		if rs.Annotations["deployment.kubernetes.io/revision"] != dep.Annotations["deployment.kubernetes.io/revision"] {
			if prevRS == nil || rs.CreationTimestamp.After(prevRS.CreationTimestamp.Time) {
				prevRS = rs
			}
		}
	}
	if prevRS == nil {
		return fmt.Errorf("no previous ReplicaSet found")
	}

	// Patch the deployment's template to match the previous ReplicaSet
	patch := fmt.Sprintf(`{"spec":{"template":%s}}`, mustJSON(prevRS.Spec.Template))
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// CopyConfigMapsAndSecrets copies ConfigMaps + Secrets referenced by a
// deployment from srcNs to dstNs (used by the sandbox runner).
func CopyConfigMapsAndSecrets(ctx context.Context, clientset *kubernetes.Clientset, deploy *appsv1.Deployment, srcNs, dstNs string) error {
	cmNames := map[string]bool{}
	secretNames := map[string]bool{}

	for _, container := range deploy.Spec.Template.Spec.Containers {
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil {
				cmNames[envFrom.ConfigMapRef.Name] = true
			}
			if envFrom.SecretRef != nil {
				secretNames[envFrom.SecretRef.Name] = true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil {
				if env.ValueFrom.ConfigMapKeyRef != nil {
					cmNames[env.ValueFrom.ConfigMapKeyRef.Name] = true
				}
				if env.ValueFrom.SecretKeyRef != nil {
					secretNames[env.ValueFrom.SecretKeyRef.Name] = true
				}
			}
		}
	}

	for cmName := range cmNames {
		if cmName == "" {
			continue
		}
		cm, err := clientset.CoreV1().ConfigMaps(srcNs).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		newCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: dstNs},
			Data:       cm.Data,
			BinaryData: cm.BinaryData,
		}
		_, _ = clientset.CoreV1().ConfigMaps(dstNs).Create(ctx, newCM, metav1.CreateOptions{})
	}

	for secretName := range secretNames {
		if secretName == "" {
			continue
		}
		sec, err := clientset.CoreV1().Secrets(srcNs).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		newSec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: dstNs},
			Data:       sec.Data,
			Type:       sec.Type,
		}
		_, _ = clientset.CoreV1().Secrets(dstNs).Create(ctx, newSec, metav1.CreateOptions{})
	}

	return nil
}

func mustJSON(v any) string {
	b, _ := jsonMarshal(v)
	return string(b)
}

func jsonMarshal(v any) ([]byte, error) {
	return jsonMarshalImpl(v)
}
