package executors

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// RemediationController detects and auto-remediates infrastructure issues
// that don't require code changes:
//   - Disk pressure on nodes → delete evicted pods (frees image refs).
//   - Pods in CrashLoopBackOff with high restart count (>20) and no
//     sentinel.io/repo annotation → restart pod (the Deployment recreates
//     it). If the pod has the annotation, Sentinel will investigate it as
//     a code issue, so the controller skips it.
//   - Pods stuck Pending for >10 minutes → log + alert (can't auto-fix
//     scheduling issues without scaling nodes).
//
// The controller is opt-in. It runs as a background goroutine alongside
// the detection layer.
type RemediationController struct {
	clientset      kubernetes.Interface
	skipNamespaces map[string]bool
	actionsTaken   int
}

// NewRemediationController creates a controller that ignores the given
// namespaces (typically kube-system, kube-public, sentinel's own namespace).
func NewRemediationController(clientset kubernetes.Interface, skipNamespaces []string) *RemediationController {
	skip := map[string]bool{}
	for _, ns := range skipNamespaces {
		skip[ns] = true
	}
	return &RemediationController{
		clientset:      clientset,
		skipNamespaces: skip,
	}
}

// Run starts the remediation loop. Polls every pollInterval for issues.
// Blocks until ctx is cancelled. pollInterval <= 0 defaults to 5 minutes.
func (r *RemediationController) Run(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 5 * time.Minute
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	logging.Info("remediation controller started", "pollInterval", pollInterval)

	for {
		select {
		case <-ctx.Done():
			logging.Info("remediation controller stopping")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick runs one remediation pass.
func (r *RemediationController) tick(ctx context.Context) {
	r.checkDiskPressure(ctx)
	r.checkCrashLoop(ctx)
	r.checkPendingPods(ctx)
}

// checkDiskPressure checks for nodes with DiskPressure condition and
// attempts cleanup (deleting evicted pods on the node).
func (r *RemediationController) checkDiskPressure(ctx context.Context) {
	nodes, err := r.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		logging.Error("remediation: failed to list nodes", "err", err)
		return
	}

	for _, node := range nodes.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeDiskPressure && cond.Status == corev1.ConditionTrue {
				logging.Info("remediation: disk pressure detected — attempting cleanup", "node", node.Name)
				r.cleanNodeDisk(ctx, node.Name)
			}
		}
	}
}

// cleanNodeDisk deletes evicted pods on a node. Evicted pods are already
// dead but their specs hold image references that prevent the runtime
// from garbage-collecting the images.
func (r *RemediationController) cleanNodeDisk(ctx context.Context, nodeName string) {
	pods, err := r.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s,status.phase=Failed", nodeName),
	})
	if err != nil {
		logging.Error("remediation: failed to list evicted pods on node", "node", nodeName, "err", err)
		return
	}

	deleted := 0
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" {
			if r.skipNamespaces[pod.Namespace] {
				continue
			}
			if err := r.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err == nil {
				deleted++
			}
		}
	}
	if deleted > 0 {
		logging.Info("remediation: cleaned evicted pods on node", "deleted", deleted, "node", nodeName)
		r.actionsTaken++
	}
}

// checkCrashLoop finds pods in CrashLoopBackOff with restart count >20.
// If the pod doesn't have a sentinel.io/repo annotation, it's likely an
// infra issue (bad config, missing secret) — we restart the pod by
// deleting it (the Deployment recreates it). If it has the annotation,
// Sentinel's main loop will investigate it as a code issue.
func (r *RemediationController) checkCrashLoop(ctx context.Context) {
	pods, err := r.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		logging.Error("remediation: failed to list pods for CrashLoopBackOff check", "err", err)
		return
	}

	for _, pod := range pods.Items {
		if r.skipNamespaces[pod.Namespace] {
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CrashLoopBackOff" {
				continue
			}
			if cs.RestartCount <= 20 {
				continue
			}
			if _, hasRepo := pod.Annotations["sentinel.io/repo"]; hasRepo {
				continue
			}
			logging.Info("remediation: pod in CrashLoopBackOff (high restart count, no sentinel annotation) — restarting",
				"namespace", pod.Namespace, "pod", pod.Name, "restarts", cs.RestartCount)
			if err := r.clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err == nil {
				logging.Info("remediation: restarted pod (Deployment will recreate it)",
					"namespace", pod.Namespace, "pod", pod.Name)
				r.actionsTaken++
			}
		}
	}
}

// checkPendingPods finds pods stuck in Pending state for > 10 minutes and
// logs them with the scheduling failure reason. Auto-fixing scheduling
// issues requires scaling nodes, which is out of scope.
func (r *RemediationController) checkPendingPods(ctx context.Context) {
	pods, err := r.clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "status.phase=Pending",
	})
	if err != nil {
		logging.Error("remediation: failed to list pending pods", "err", err)
		return
	}

	for _, pod := range pods.Items {
		if r.skipNamespaces[pod.Namespace] {
			continue
		}
		if pod.Status.StartTime == nil {
			continue
		}
		pendingDuration := time.Since(pod.Status.StartTime.Time)
		if pendingDuration <= 10*time.Minute {
			continue
		}
		schedulingReason := "unknown"
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
				schedulingReason = cond.Message
				break
			}
		}
		logging.Warn("remediation: pod pending for too long — scheduling issue",
			"namespace", pod.Namespace, "pod", pod.Name,
			"duration", pendingDuration.Round(time.Minute),
			"reason", schedulingReason)
	}
}

// ActionsTaken returns the total number of remediation actions taken.
func (r *RemediationController) ActionsTaken() int {
	return r.actionsTaken
}

// RemediationStatus is the public state of the remediation controller.
type RemediationStatus struct {
	Enabled      bool `json:"enabled"`
	ActionsTaken int  `json:"actions_taken"`
	CheckedNodes int  `json:"checked_nodes"`
	CheckedPods  int  `json:"checked_pods"`
}

// Status returns the current remediation state.
func (r *RemediationController) Status() RemediationStatus {
	return RemediationStatus{
		Enabled:      true,
		ActionsTaken: r.actionsTaken,
	}
}

// isStuckPod reports whether a pod should be auto-restarted by the
// remediation controller. Kept as a pure helper so tests can exercise the
// decision logic without a fake clientset.
func isStuckPod(pod *corev1.Pod, skipNamespaces map[string]bool) bool {
	if pod == nil {
		return false
	}
	if skipNamespaces[pod.Namespace] {
		return false
	}
	if _, hasRepo := pod.Annotations["sentinel.io/repo"]; hasRepo {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CrashLoopBackOff" {
			continue
		}
		if cs.RestartCount > 20 {
			return true
		}
	}
	return false
}
