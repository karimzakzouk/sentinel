// Package main — post-deploy health check (honest merge verification).
//
// After a successful auto-merge the merger calls wiring.watchDeploymentHealth:
// it waits for the new ReplicaSet to roll out, then watches the deployed
// service's pods. If pods restart while supposed to be healthy, get
// OOMKilled, or never become Ready, the merge made things worse (or failed
// to fix them) — the merge is auto-reverted and the PR is annotated.
//
// This closes the run #26 user-api blind spot: the canary sandbox has no
// traffic, so a memory leak that only manifests under load passes sandbox
// verification. The DEPLOYED pods, under real traffic, OOM-loop — and a
// pod that flips Ready between OOM kills was previously counted as
// "recovered" by a Ready-only check.

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

const (
	// deployWatchPoll is the pod-sampling interval.
	deployWatchPoll = 20 * time.Second
	// deployRolloutTimeout bounds how long we wait for the merged commit to
	// actually roll out (Argo CD sync + rollout).
	deployRolloutTimeout = 5 * time.Minute
)

// deployPodSample is one observation of one pod.
type deployPodSample struct {
	restarts  int32
	oomKilled bool
	ready     bool
	seenReady bool
}

// watchDeploymentHealth is the onDeployWatch callback. Runs in its own
// goroutine — must never block the merger's tick loop.
func (w *wiring) watchDeploymentHealth(ctx context.Context, pr *automerge.TrackedPR) {
	cfg := w.Config()
	if !cfg.PostDeployCheckEnabled {
		return
	}
	if w.k8sClient == nil || w.k8sClient.Clientset() == nil {
		logging.Warn("deployhealth: k8s client unavailable — skipping post-deploy check",
			"repo", pr.Repo, "pr", pr.PRNumber)
		return
	}

	watchCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.PostDeployCheckDurationSec+300)*time.Second)
	defer cancel()

	service := pr.Input.Service
	namespace := pr.Input.Namespace
	logging.Info("deployhealth: watching deployment after merge",
		"repo", pr.Repo, "pr", pr.PRNumber, "service", service, "namespace", namespace,
		"duration", time.Duration(cfg.PostDeployCheckDurationSec)*time.Second)

	// Phase A: wait for the rollout of the merged commit to complete. Only
	// after updatedReplicas == replicas are restart deltas attributable to
	// the NEW code (pre-rollout pods still run the old, broken image).
	rolloutDoneAt := time.Now()
	rolloutDeadline := rolloutDoneAt.Add(deployRolloutTimeout)
	rolledOut := false
	for time.Now().Before(rolloutDeadline) {
		select {
		case <-watchCtx.Done():
			return
		case <-ctx.Done():
			return
		case <-time.After(deployWatchPoll):
		}
		deploy, err := w.k8sClient.Clientset().AppsV1().Deployments(namespace).Get(watchCtx, service, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if deploy.Generation <= deploy.Status.ObservedGeneration &&
			deploy.Status.UpdatedReplicas == *deploy.Spec.Replicas &&
			deploy.Status.ReadyReplicas >= 1 {
			rolledOut = true
			rolloutDoneAt = time.Now()
			logging.Info("deployhealth: rollout complete — starting restart/OOM watch",
				"service", service, "namespace", namespace,
				"updated_replicas", deploy.Status.UpdatedReplicas)
			break
		}
	}
	if !rolledOut {
		// Rollout incomplete within the wait window. We cannot attribute
		// pod restarts to the new code — do NOT revert on this alone.
		logging.Warn("deployhealth: rollout did not complete within wait window — skipping regression attribution",
			"repo", pr.Repo, "pr", pr.PRNumber, "service", service)
		return
	}

	// Phase B: watch the pods. Regression = restart delta after first
	// observation, any OOMKill, or sustained all-pods-not-Ready.
	pods := map[string]*deployPodSample{}
	notReadyStreak := 0
	deadline := time.Now().Add(time.Duration(cfg.PostDeployCheckDurationSec) * time.Second)
	// Pods of the NEW ReplicaSet are created between merge and now; a
	// restart on one of them happened under the merged code even if it
	// predates our first 20s sample. The slack covers pods created while
	// the rollout was still converging.
	newPodCutoff := rolloutDoneAt.Add(-2 * time.Minute)

	for time.Now().Before(deadline) {
		select {
		case <-watchCtx.Done():
			return
		case <-ctx.Done():
			return
		case <-time.After(deployWatchPoll):
		}

		podList, err := w.k8sClient.Clientset().CoreV1().Pods(namespace).List(watchCtx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", service),
		})
		if err != nil || len(podList.Items) == 0 {
			// Label selector miss — try the kubernetes.io/name variant.
			podList, err = w.k8sClient.Clientset().CoreV1().Pods(namespace).List(watchCtx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
			})
			if err != nil || len(podList.Items) == 0 {
				continue
			}
		}

		anyReady := false
		for i := range podList.Items {
			pod := &podList.Items[i]
			var restarts int32
			oom := false
			ready := false
			for _, cs := range pod.Status.ContainerStatuses {
				restarts += cs.RestartCount
				if cs.Ready {
					ready = true
				}
				if t := cs.LastTerminationState.Terminated; t != nil {
					if t.Reason == "OOMKilled" || t.ExitCode == 137 {
						oom = true
					}
				}
			}
			if ready {
				anyReady = true
			}

			prev, seen := pods[pod.Name]
			if !seen {
				pods[pod.Name] = &deployPodSample{restarts: restarts, oomKilled: oom, ready: ready, seenReady: ready}
				// A pod that already restarted before our
				// first sample: if it belongs to the new
				// rollout (created after the merge), the
				// merged code crash-looped within the
				// rollout window — run #27's user-api
				// escaped through exactly this sampling gap.
				if restarts > 0 && oom {
					w.revertMergedPR(ctx, pr,
						fmt.Sprintf("deployed pod %s was OOMKilled before monitoring began (restarts=%d) — the fix does not resolve the memory problem", pod.Name, restarts))
					return
				}
				if restarts > 0 && pod.Status.StartTime != nil && pod.Status.StartTime.After(newPodCutoff) {
					w.revertMergedPR(ctx, pr,
						fmt.Sprintf("deployed pod %s (created %s, after the merge rolled out) already restarted %d time(s) before monitoring began — the fix does not hold under real traffic", pod.Name, pod.Status.StartTime.UTC().Format(time.RFC3339), restarts))
					return
				}
				continue
			}
			prev.ready = ready
			if ready {
				prev.seenReady = true
			}

			// OOMKill observed on a rolled-out pod = the deployed code still
			// exceeds its memory limit.
			if oom {
				w.revertMergedPR(ctx, pr,
					fmt.Sprintf("deployed pod %s was OOMKilled after the merge (pod had been Ready) — the fix does not resolve the memory problem", pod.Name))
				return
			}
			// A pod that was Ready at least once and then restarted = the
			// deployed code crash-loops under real traffic.
			if prev.seenReady && restarts > prev.restarts {
				w.revertMergedPR(ctx, pr,
					fmt.Sprintf("deployed pod %s restarted while supposed to be healthy (restarts %d -> %d) — the fix does not hold under real traffic", pod.Name, prev.restarts, restarts))
				return
			}
			prev.restarts = restarts
		}

		if anyReady {
			notReadyStreak = 0
		} else {
			notReadyStreak++
			// 6 consecutive samples (~2 min) with zero Ready pods after a
			// completed rollout = deployment is down.
			if notReadyStreak >= 6 {
				w.revertMergedPR(ctx, pr,
					"no Ready replicas for ~2 minutes after rollout completed — the deployment is down following this merge")
				return
			}
		}
	}

	logging.Info("deployhealth: watch window complete — deployment healthy, no regression",
		"repo", pr.Repo, "pr", pr.PRNumber, "service", service)
}

// revertMergedPR opens (and optionally immediately merges) a revert PR for
// the bad merge, comments on the original PR, and records the outcome.
func (w *wiring) revertMergedPR(ctx context.Context, pr *automerge.TrackedPR, reason string) {
	cfg := w.Config()
	logging.Error("deployhealth: REGRESSION after merge — reverting",
		"repo", pr.Repo, "pr", pr.PRNumber, "reason", reason)

	if cfg.PostDeployRegressionCooldown {
		w.autoMergePolicy.RecordRegression(reason)
	}

	revCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	mergeSHA, err := w.autoMergeGH.GetPRMergeCommitSHA(revCtx, pr.Repo, pr.PRNumber)
	if err != nil || mergeSHA == "" {
		logging.Error("deployhealth: could not fetch merge commit SHA — posting comment only",
			"repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		_ = w.autoMergeGH.PostComment(revCtx, pr.Repo, pr.PRNumber,
			fmt.Sprintf("⚠️ **Post-deploy regression detected: %s**\n\nCould not auto-revert (merge commit unavailable: %v). Revert manually.", reason, err))
		_ = w.store.UpdateAutoMergeStatus(ctx, uuid.MustParse(pr.IncidentID), "regression_no_revert")
		return
	}

	revertPR, err := w.autoMergeGH.RevertMergeCommit(revCtx, pr.Repo, mergeSHA, pr.BaseBranch, reason)
	if err != nil {
		logging.Error("deployhealth: revert PR creation failed",
			"repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		_ = w.autoMergeGH.PostComment(revCtx, pr.Repo, pr.PRNumber,
			fmt.Sprintf("⚠️ **Post-deploy regression detected: %s**\n\nAuto-revert failed: %v. Revert manually.", reason, err))
		_ = w.store.UpdateAutoMergeStatus(ctx, uuid.MustParse(pr.IncidentID), "regression_revert_failed")
		return
	}

	comment := fmt.Sprintf("⚠️ **Sentinel auto-reverted this merge.**\n\nPost-deploy health check detected a regression:\n\n> %s\n\nRevert PR: #%d",
		reason, revertPR)

	if cfg.PostDeployRevertAutoMerge {
		if err := w.autoMergeGH.Merge(revCtx, pr.Repo, revertPR,
			fmt.Sprintf("Auto-merge revert of #%d (post-deploy regression)", pr.PRNumber)); err != nil {
			logging.Error("deployhealth: revert PR merge failed — revert PR left open for review",
				"repo", pr.Repo, "revert_pr", revertPR, "err", err)
			comment += "\n\n(The revert PR was opened but could not be auto-merged — merge it manually.)"
		} else {
			comment += "\n\nThe revert has been merged; the service is returning to its pre-fix state."
		}
	}

	_ = w.autoMergeGH.PostComment(revCtx, pr.Repo, pr.PRNumber, comment)
	_ = w.store.UpdateAutoMergeStatus(ctx, uuid.MustParse(pr.IncidentID), "reverted:postdeploy_regression")
	logging.Info("deployhealth: merge reverted",
		"repo", pr.Repo, "pr", pr.PRNumber, "revert_pr", revertPR, "reason", strings.TrimSpace(reason))
}
