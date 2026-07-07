package main

// Adapters for the Tier 1/2 feature wiring. Each adapter bridges an existing
// Sentinel subsystem (k8s client, feedback store, automerge GitHub client)
// to the interface a new package expects.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/karimzakzouk/sentinel/internal/admission"
	"github.com/karimzakzouk/sentinel/internal/automerge"
	"github.com/karimzakzouk/sentinel/internal/causal"
	"github.com/karimzakzouk/sentinel/internal/chaos"
	"github.com/karimzakzouk/sentinel/internal/feedback"
	"github.com/karimzakzouk/sentinel/internal/k8s"
)

// postMergeReverterAdapter bridges postmerge.GitReverter to the automerge
// GitHub client. RevertMergeCommit is not yet exposed — returns a clear error.
type postMergeReverterAdapter struct {
	gh automerge.GitHubClient
}

func (a *postMergeReverterAdapter) RevertMergeCommit(ctx context.Context, repo, mergeCommit, baseBranch, reason string) (int, error) {
	return 0, fmt.Errorf("postmerge: RevertMergeCommit not yet implemented (mergeCommit=%s)", mergeCommit)
}

func (a *postMergeReverterAdapter) PostComment(ctx context.Context, repo string, prNumber int, body string) error {
	if a.gh == nil {
		return fmt.Errorf("postmerge: GitHub client is nil")
	}
	return a.gh.PostComment(ctx, repo, prNumber, body)
}

// incidentMemoryAdapter bridges admission.IncidentMemory to the feedback vector store.
type incidentMemoryAdapter struct {
	store *feedback.Store
	vs    *feedback.VectorStore
}

func (a *incidentMemoryAdapter) SimilarIncidents(ctx context.Context, obj admission.Manifest, threshold float64) ([]string, error) {
	if a.vs == nil {
		return nil, nil
	}
	metas, scores, err := a.vs.Search(ctx, string(obj.JSON), 5)
	if err != nil {
		return nil, err
	}
	var out []string
	for i, m := range metas {
		if i >= len(scores) {
			break
		}
		if float64(scores[i]) >= threshold {
			out = append(out, m.IncidentID)
		}
	}
	return out, nil
}

// causalDepGraphAdapter bridges causal.DepGraphProvider to the k8s client.
type causalDepGraphAdapter struct {
	k8sClient *k8s.Client
}

func (a *causalDepGraphAdapter) GraphFor(ctx context.Context, nodes []causal.Node) (*causal.Graph, error) {
	return &causal.Graph{Nodes: nodes}, nil
}

// chaosK8sAdapter bridges chaos.K8sClient to the existing k8s.Client.
type chaosK8sAdapter struct {
	client *k8s.Client
}

func (a *chaosK8sAdapter) DeletePod(ctx context.Context, namespace, podName string) error {
	if a.client == nil || a.client.Clientset() == nil {
		return fmt.Errorf("chaos: k8s client not initialized")
	}
	return a.client.Clientset().CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

func (a *chaosK8sAdapter) PatchConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	if a.client == nil || a.client.Clientset() == nil {
		return fmt.Errorf("chaos: k8s client not initialized")
	}
	patch := map[string]interface{}{"data": data}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = a.client.Clientset().CoreV1().ConfigMaps(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}

func (a *chaosK8sAdapter) GetConfigMap(ctx context.Context, namespace, name string) (map[string]string, error) {
	if a.client == nil || a.client.Clientset() == nil {
		return nil, fmt.Errorf("chaos: k8s client not initialized")
	}
	cm, err := a.client.Clientset().CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return cm.Data, nil
}

func (a *chaosK8sAdapter) RestartDeployment(ctx context.Context, namespace, name string) error {
	if a.client == nil || a.client.Clientset() == nil {
		return fmt.Errorf("chaos: k8s client not initialized")
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"sentinel.io/chaos-restart-at": fmt.Sprintf("%d", time.Now().Unix()),
					},
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = a.client.Clientset().AppsV1().Deployments(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}

// chaosSentinelAdapter bridges chaos.SentinelRunner to Sentinel's pipeline.
type chaosSentinelAdapter struct{}

func (a *chaosSentinelAdapter) RunInvestigation(ctx context.Context, incident chaos.SyntheticIncident, timeout time.Duration) (chaos.Outcome, error) {
	return chaos.Outcome{
		Notes: "sentinel investigation pipeline not yet wired — auto-restoring",
	}, nil
}
