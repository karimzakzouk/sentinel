package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
)

// fakeGitea holds one file of person/delivery and the pull requests merged.
type fakeGitea struct {
	mu      sync.Mutex
	content string
	commits []string
	merged  []string
}

func (g *fakeGitea) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repos/person/delivery/contents/deployment.yaml":
		_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(g.content)), "sha": "s"})
	case r.Method == http.MethodPut && r.URL.Path == "/api/v1/repos/person/delivery/contents/deployment.yaml":
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		content, _ := base64.StdEncoding.DecodeString(in["content"])
		g.content = string(content)
		g.commits = append(g.commits, in["message"])
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/repos/person/delivery/pulls/"):
		body, _ := io.ReadAll(r.Body)
		g.merged = append(g.merged, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/person/delivery/pulls/"), "/merge")+" "+string(body))
		// The fix the person merges puts the image back.
		g.content = strings.Replace(g.content, "podinfo:does-not-exist", "podinfo:6.17.0", 1)
	default:
		http.NotFound(w, r)
	}
}

// A commit fault is committed as the person who owns the repository, and
// reverted as that person would: Sentinel's fix is merged, the owner resumed
// once the fix is in its source, and nothing is left to put back.
func TestACommitFaultIsRevertedAsAPersonWould(t *testing.T) {
	ctx := context.Background()
	gitea := &fakeGitea{content: "image: ghcr.io/stefanprodan/podinfo:6.17.0 # the release we run\n"}
	srv := httptest.NewServer(http.HandlerFunc(gitea.serve))
	defer srv.Close()

	unstr := func(obj map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: obj} }
	kustomization := unstr(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "git-podinfo", "namespace": "flux-system",
			"annotations": map[string]any{actor.AnnotationPausedSync: "suspended"}},
		"spec": map[string]any{"suspend": true, "sourceRef": map[string]any{"kind": "GitRepository", "name": "delivery"}}})
	repo := unstr(map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "delivery", "namespace": "flux-system"}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		fixesResource: "FixList", actor.KustomizationsResource: "KustomizationList", gitRepositoriesResource: "GitRepositoryList",
	}, kustomization, repo)
	// Sentinel's Fix moves on as the person does: Ready once merged, Applied
	// once the Kustomization is resumed. Reactors run under the fake's lock,
	// so the resume is noted as it passes rather than read back.
	var resumed sync.Map
	dyn.PrependReactor("patch", "kustomizations", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if strings.Contains(string(a.(k8stesting.PatchAction).GetPatch()), `"suspend":false`) {
			resumed.Store("git-podinfo", true)
		}
		return false, nil, nil
	})
	dyn.PrependReactor("list", "fixes", func(k8stesting.Action) (bool, runtime.Object, error) {
		gitea.mu.Lock()
		merged := len(gitea.merged) > 0
		gitea.mu.Unlock()
		phase := v1alpha1.FixPhase("Proposed")
		if merged {
			phase = "Ready"
			if _, ok := resumed.Load("git-podinfo"); ok {
				phase = "Applied"
			}
		}
		fix := v1alpha1.Fix{ObjectMeta: metav1.ObjectMeta{Name: "sentinel-x-fix", Namespace: "sentinel", CreationTimestamp: metav1.NewTime(time.Now())},
			Spec:   v1alpha1.FixSpec{Workload: v1alpha1.TargetRef{Kind: "Deployment", Namespace: "e2e-delivery", Name: "git-podinfo"}},
			Status: v1alpha1.FixStatus{Phase: phase, Number: 7}}
		obj, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&fix)
		list := &unstructured.UnstructuredList{Object: map[string]any{"apiVersion": "v1", "kind": "List"}}
		list.Items = []unstructured.Unstructured{{Object: obj}}
		return true, list, nil
	})

	in := &Injector{Dynamic: dyn, Forge: &Forge{API: srv.URL + "/api/v1", Token: "t"}, Poll: 10 * time.Millisecond, FixWait: time.Second}
	s := &Scenario{ID: "git-bad-image", Title: "A bad image through Git", App: "git",
		Fault: Fault{Kind: FaultCommit, Target: Target{Kind: "Kustomization", Namespace: "flux-system", Name: "git-podinfo"},
			Commit: &Commit{Repository: "person/delivery", Path: "deployment.yaml", Old: "podinfo:6.17.0", New: "podinfo:does-not-exist", Message: "Bump podinfo"}},
		GroundTruth: GroundTruth{FaultClass: "deploy.bad-image", Mitigation: "rollout.undo",
			RootCause: Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "e2e-delivery", Name: "git-podinfo"}}}
	run, err := in.Inject(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitea.content, "podinfo:does-not-exist") || len(gitea.commits) != 1 || gitea.commits[0] != "Bump podinfo" {
		t.Fatalf("after the commit: %q, commits %q", gitea.content, gitea.commits)
	}
	r, _ := dyn.Resource(gitRepositoriesResource).Namespace("flux-system").Get(ctx, "delivery", metav1.GetOptions{})
	if r.GetAnnotations()["reconcile.fluxcd.io/requestedAt"] == "" {
		t.Fatal("the GitRepository was not asked to fetch the commit")
	}

	if err := in.Revert(ctx, run); err != nil {
		t.Fatal(err)
	}
	if len(gitea.merged) != 1 || !strings.HasPrefix(gitea.merged[0], "7 ") || len(gitea.commits) != 1 ||
		!strings.Contains(gitea.content, "podinfo:6.17.0") {
		t.Fatalf("merged %q, commits %q, content %q; want fix 7 merged and nothing left to put back", gitea.merged, gitea.commits, gitea.content)
	}
	if !strings.Contains(run.Reverted, "merged Sentinel's fix #7") || !strings.Contains(run.Reverted, "resumed the Flux Kustomization flux-system/git-podinfo") ||
		run.RevertedAt == nil {
		t.Fatalf("reverted: %q", run.Reverted)
	}
}
