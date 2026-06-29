package executors

import (
        "context"
        "encoding/json"
        "errors"
        "fmt"
        "sort"
        "strconv"

        appsv1 "k8s.io/api/apps/v1"
        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/types"
        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/internal/schemas"
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
// Previous implementation picked the "second-newest by
// CreationTimestamp" ReplicaSet that was not the current revision. That is
// wrong in two cases:
//  1. K8s retains old ReplicaSets with revisionHistoryLimit (default 10).
//     After several rollouts, "second-newest by CreationTimestamp" is not
//     necessarily the immediately previous revision — it could be a much
//     older one if revisions were pruned non-sequentially.
//  2. During rapid rollouts multiple ReplicaSets can share a CreationTimestamp
//     (second-resolution), making the pick non-deterministic.
// The correct algorithm — matching `kubectl rollout undo` — is to sort all
// ReplicaSets for the deployment by the `deployment.kubernetes.io/revision`
// annotation numerically (descending) and pick the highest revision that is
// not equal to the deployment's current revision. That is the
// immediately-previous revision.

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

        // Collect all ReplicaSets that have a revision annotation and are not the
        // current revision. Skip any without the annotation (orphan RS / manually
        // created) — we can't reason about their order.

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

        // Sort by revision descending — the first element is the immediately
        // previous revision.

        sort.SliceStable(cands, func(i, j int) bool {
                return cands[i].rev > cands[j].rev
        })
        prevRS := cands[0].rs

        // Patch the deployment's template to match the previous ReplicaSet.

        patch := fmt.Sprintf(`{"spec":{"template":%s}}`, mustJSON(prevRS.Spec.Template))
        _, err = e.clientset.AppsV1().Deployments(fp.TargetNamespace).Patch(ctx, dep.Name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
        return err
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
// deployment from srcNs to dstNs (used by the sandbox runner).
// Previous version silently swallowed every error — missing ConfigMap,
// missing Secret, create conflict, anything. The sandbox canary would then
// start with empty env vars and Level 1 verification would still pass (because
// the pod can go Ready with broken config), masking the real failure.
// We now collect every error and return an aggregated error. Already-exists
// (HTTP 409) is treated as success because the sandbox namespace may have
// already been partially populated by a previous run.

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

        var errs []error

        for cmName := range cmNames {
                if cmName == "" {
                        continue
                }
                cm, err := clientset.CoreV1().ConfigMaps(srcNs).Get(ctx, cmName, metav1.GetOptions{})
                if err != nil {
                        errs = append(errs, fmt.Errorf("get configmap %s/%s: %w", srcNs, cmName, err))
                        continue
                }
                newCM := &corev1.ConfigMap{
                        ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: dstNs},
                        Data:       cm.Data,
                        BinaryData: cm.BinaryData,
                }
                _, err = clientset.CoreV1().ConfigMaps(dstNs).Create(ctx, newCM, metav1.CreateOptions{})
                if err != nil && !errors.Is(err, errAlreadyExists) && !isAlreadyExistsErr(err) {
                        errs = append(errs, fmt.Errorf("create configmap %s/%s: %w", dstNs, cmName, err))
                }
        }

        for secretName := range secretNames {
                if secretName == "" {
                        continue
                }
                sec, err := clientset.CoreV1().Secrets(srcNs).Get(ctx, secretName, metav1.GetOptions{})
                if err != nil {
                        errs = append(errs, fmt.Errorf("get secret %s/%s: %w", srcNs, secretName, err))
                        continue
                }
                newSec := &corev1.Secret{
                        ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: dstNs},
                        Data:       sec.Data,
                        Type:       sec.Type,
                }
                _, err = clientset.CoreV1().Secrets(dstNs).Create(ctx, newSec, metav1.CreateOptions{})
                if err != nil && !isAlreadyExistsErr(err) {
                        errs = append(errs, fmt.Errorf("create secret %s/%s: %w", dstNs, secretName, err))
                }
        }

        return errors.Join(errs...)
}

// isAlreadyExistsErr returns true if err is a Kubernetes "AlreadyExists"
// status error. We use a string check to avoid importing
// apierrors (which would pull in another package tree) — K8s errors
// consistently contain the substring "already exists".

func isAlreadyExistsErr(err error) bool {
        if err == nil {
                return false
        }
        msg := err.Error()
        return contains(msg, "already exists")
}

// contains is a tiny helper to avoid importing strings just for one call.

func contains(s, sub string) bool {
        if len(sub) == 0 {
                return true
        }
        for i := 0; i+len(sub) <= len(s); i++ {
                if s[i:i+len(sub)] == sub {
                        return true
                }
        }
        return false
}

// errAlreadyExists is a sentinel kept for future typed-error migration.

var errAlreadyExists = errors.New("already exists")

func mustJSON(v any) string {
        b, _ := json.Marshal(v)
        return string(b)
}

// RollbackByName triggers kubectl rollout undo on a deployment by name.
// Used by chatops /sentinel rollback + /sentinel revert commands.
// This is a thin wrapper around Rollback that constructs a synthetic
// FixProposal so we don't have to refactor the existing method.

func (e *K8sExecutor) RollbackByName(ctx context.Context, namespace, deploymentName string) error {
	fp := &schemas.FixProposal{
		FixType:         schemas.FixRollback,
		TargetService:   deploymentName,
		TargetNamespace: namespace,
	}
	return e.Rollback(ctx, fp)
}
