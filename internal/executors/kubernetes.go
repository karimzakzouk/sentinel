package executors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/errs"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// K8sExecutor applies K8s-only patches (restart, scale, rollback). Used
// when FixType is FixRestart / FixScale / FixRollback instead of
// FixCodeFix.
type K8sExecutor struct {
	clientset kubernetes.Interface
}

// NewK8s creates a K8sExecutor.
func NewK8s(clientset kubernetes.Interface) *K8sExecutor {
	return &K8sExecutor{clientset: clientset}
}

// Restart performs a rollout restart on the target Deployment by patching
// the kubectl.kubernetes.io/restartedAt annotation on the pod template.
func (e *K8sExecutor) Restart(ctx context.Context, fp *schemas.FixProposal) error {
	dep, err := e.findDeployment(ctx, fp)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		metav1.Now().Format("2006-01-02T15:04:05Z"))
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("restart %s/%s: %w", fp.TargetNamespace, dep.Name, err)
	}
	return nil
}

// Scale patches the replica count on the target Deployment.
func (e *K8sExecutor) Scale(ctx context.Context, fp *schemas.FixProposal, newReplicas int32) error {
	dep, err := e.findDeployment(ctx, fp)
	if err != nil {
		return err
	}
	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, newReplicas)
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("scale %s/%s: %w", fp.TargetNamespace, dep.Name, err)
	}
	return nil
}

// Rollback performs a kubectl rollout undo on the target Deployment by
// sorting all ReplicaSets by the deployment.kubernetes.io/revision
// annotation numerically (descending) and patching the Deployment's
// template to match the highest revision that is NOT the current revision.
// This matches `kubectl rollout undo` semantics.
func (e *K8sExecutor) Rollback(ctx context.Context, fp *schemas.FixProposal) error {
	dep, err := e.findDeployment(ctx, fp)
	if err != nil {
		return err
	}

	rss, err := e.clientset.AppsV1().ReplicaSets(fp.TargetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
	})
	if err != nil {
		return fmt.Errorf("list replicasets for %s: %w", fp.TargetService, err)
	}
	if len(rss.Items) < 2 {
		return fmt.Errorf("no previous ReplicaSet to roll back to (found %d)", len(rss.Items))
	}

	currentRev := parseRevision(dep.Annotations["deployment.kubernetes.io/revision"])

	type cand struct {
		rs  *appsv1.ReplicaSet
		rev int64
	}
	cands := make([]cand, 0, len(rss.Items))
	for i := range rss.Items {
		rs := &rss.Items[i]
		revStr := rs.Annotations["deployment.kubernetes.io/revision"]
		if revStr == "" {
			continue
		}
		rev := parseRevision(revStr)
		if rev == currentRev {
			continue
		}
		cands = append(cands, cand{rs: rs, rev: rev})
	}
	if len(cands) == 0 {
		return fmt.Errorf("no previous ReplicaSet found (all %d RS are current or have no revision)", len(rss.Items))
	}

	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].rev > cands[j].rev
	})
	prevRS := cands[0].rs

	patch := fmt.Sprintf(`{"spec":{"template":%s}}`, mustJSON(prevRS.Spec.Template))
	_, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("rollback %s/%s: %w", fp.TargetNamespace, dep.Name, err)
	}
	return nil
}

// RollbackByName triggers kubectl rollout undo on a deployment by name.
// Used by chatops /sentinel rollback + /sentinel revert commands.
func (e *K8sExecutor) RollbackByName(ctx context.Context, namespace, deploymentName string) error {
	fp := &schemas.FixProposal{
		FixType:         schemas.FixRollback,
		TargetService:   deploymentName,
		TargetNamespace: namespace,
	}
	return e.Rollback(ctx, fp)
}

// findDeployment fetches the named deployment, falling back to a label
// selector on app.kubernetes.io/name if the named lookup fails.
func (e *K8sExecutor) findDeployment(ctx context.Context, fp *schemas.FixProposal) (*appsv1.Deployment, error) {
	dep, err := e.clientset.AppsV1().Deployments(fp.TargetNamespace).Get(ctx, fp.TargetService, metav1.GetOptions{})
	if err == nil {
		return dep, nil
	}
	deps, listErr := e.clientset.AppsV1().Deployments(fp.TargetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", fp.TargetService),
	})
	if listErr != nil || len(deps.Items) == 0 {
		return nil, fmt.Errorf("deployment %s not found in %s: %w", fp.TargetService, fp.TargetNamespace, err)
	}
	return &deps.Items[0], nil
}

// parseRevision parses the deployment.kubernetes.io/revision annotation.
// Returns 0 if the value is missing or unparseable (treated as the oldest
// possible revision so it sorts first in the candidate list).
func parseRevision(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// CopyConfigMapsAndSecrets copies ConfigMaps + Secrets referenced by a
// deployment from srcNs to dstNs (used by the sandbox runner). Collects
// every error and returns an aggregated error. Already-exists (HTTP 409)
// is treated as success because the sandbox namespace may have been
// partially populated by a previous run.
func CopyConfigMapsAndSecrets(ctx context.Context, clientset kubernetes.Interface, deploy *appsv1.Deployment, srcNs, dstNs string) error {
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

	var allErrs []error

	for cmName := range cmNames {
		if cmName == "" {
			continue
		}
		cm, err := clientset.CoreV1().ConfigMaps(srcNs).Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("get configmap %s/%s: %w", srcNs, cmName, err))
			continue
		}
		newCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: dstNs},
			Data:       cm.Data,
			BinaryData: cm.BinaryData,
		}
		_, err = clientset.CoreV1().ConfigMaps(dstNs).Create(ctx, newCM, metav1.CreateOptions{})
		if err != nil && !errs.IsAlreadyExists(err) {
			allErrs = append(allErrs, fmt.Errorf("create configmap %s/%s: %w", dstNs, cmName, err))
		}
	}

	for secretName := range secretNames {
		if secretName == "" {
			continue
		}
		sec, err := clientset.CoreV1().Secrets(srcNs).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("get secret %s/%s: %w", srcNs, secretName, err))
			continue
		}
		newSec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: dstNs},
			Data:       sec.Data,
			Type:       sec.Type,
		}
		_, err = clientset.CoreV1().Secrets(dstNs).Create(ctx, newSec, metav1.CreateOptions{})
		if err != nil && !errs.IsAlreadyExists(err) {
			allErrs = append(allErrs, fmt.Errorf("create secret %s/%s: %w", dstNs, secretName, err))
		}
	}

	return errors.Join(allErrs...)
}

// mustJSON marshals v to JSON, panicking only on truly impossible errors
// (recursive struct, etc.). Used to serialize the ReplicaSet template for
// the rollback patch.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// buildRestartPatch constructs the JSON merge patch used by Restart. Kept
// as a separate helper so tests can verify the patch shape without
// constructing a FixProposal.
func buildRestartPatch(ts string) string {
	return fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, ts)
}

// buildScalePatch constructs the JSON merge patch used by Scale.
func buildScalePatch(replicas int32) string {
	return fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
}

// _ keeps strings imported (used indirectly via errs.IsAlreadyExists).
var _ = strings.TrimSpace
