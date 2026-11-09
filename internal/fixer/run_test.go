package fixer

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/forge"
)

// forgeServer serves a bare repository over Git's smart HTTP protocol and
// the pull request API, as Gitea does.
type forgeServer struct {
	*httptest.Server
	mu         sync.Mutex
	opened     map[string]string
	openedCode map[string]string
	merged     string
}

func newForge(t *testing.T, root string) *forgeServer {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git binary to serve a repository")
	}
	f := &forgeServer{}
	backend := &cgi.Handler{Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=sentinel"}}
	mux := http.NewServeMux()
	mux.Handle("/sentinel/", backend)
	mux.HandleFunc("POST /api/v1/repos/sentinel/delivery/pulls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&f.opened)
		_, _ = io.WriteString(w, `{"number": 1, "html_url": "`+f.URL+`/sentinel/delivery/pulls/1", "state": "open"}`)
	})
	mux.HandleFunc("POST /api/v1/repos/sentinel/shop/pulls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&f.openedCode)
		_, _ = io.WriteString(w, `{"number": 2, "html_url": "`+f.URL+`/sentinel/shop/pulls/2", "state": "open"}`)
	})
	mux.HandleFunc("GET /api/v1/repos/sentinel/delivery/pulls/1", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.merged == "" {
			_, _ = io.WriteString(w, `{"number": 1, "state": "open"}`)
			return
		}
		_, _ = io.WriteString(w, `{"number": 1, "state": "closed", "merged": true, "merge_commit_sha": "`+f.merged+`"}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func podTemplate(image string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "podinfod", Image: image}}}}
}

// A workload no GitOps owner deploys has no repository to fix, and Helm
// values are not changed yet: the Fixer says so.
func TestSourceOfSaysWhatItCannotFix(t *testing.T) {
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	for labels, want := range map[string]string{
		"":         "no Argo CD Application or Flux Kustomization deploys web",
		"helm/web": "the Flux HelmRelease flux-system/web renders web",
	} {
		meta := metav1.ObjectMeta{Name: "web"}
		if labels != "" {
			meta.Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "web", "helm.toolkit.fluxcd.io/namespace": "flux-system"}
		}
		if _, err := SourceOf(ctx, dyn, meta, "argocd"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SourceOf(%q) = %v, want %q", labels, err, want)
		}
	}
}

// A Fix goes from proposal to ready: the Fixer finds the owner and its
// repository, pushes a one-line change and opens a pull request; once it is
// merged and the owner's source has fetched the merge, the fix is ready.
func TestAFixGoesFromProposalToReady(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := seed(t, map[string]string{
		"kustomize/kustomization.yaml": "resources: [deployment.yaml, service.yaml]\n",
		"kustomize/deployment.yaml":    deployment(bad),
		"kustomize/service.yaml":       service,
	})
	bare := filepath.Join(root, "sentinel", "delivery.git")
	if err := exec.Command("git", "clone", "--bare", "--quiet", dir, bare).Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "--git-dir", bare, "config", "http.receivepack", "true").Run(); err != nil {
		t.Fatal(err)
	}
	srv := newForge(t, root)
	url := srv.URL + "/sentinel/delivery.git"

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "e2e-delivery", UID: types.UID("dep"),
			Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "delivery", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		Spec: appsv1.DeploymentSpec{Template: podTemplate(good)},
	}
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "e2e-delivery", Labels: map[string]string{"pod-template-hash": "bad123"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "podinfo", UID: "dep"}}},
		Spec: appsv1.ReplicaSetSpec{Template: podTemplate(bad)},
	}
	client := fake.NewClientset(dep, rs)

	head, err := exec.Command("git", "--git-dir", bare, "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	unstr := func(obj map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: obj} }
	kustomization := unstr(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "delivery", "namespace": "flux-system"},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "delivery"}, "path": "./kustomize"}})
	gitRepository := unstr(map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "delivery", "namespace": "flux-system"},
		"spec":     map[string]any{"url": url, "ref": map[string]any{"branch": "main"}},
		"status":   map[string]any{"artifact": map[string]any{"revision": "main@sha1:" + strings.TrimSpace(string(head))}}})
	fix := unstr(map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Fix",
		"metadata": map[string]any{"name": "sentinel-0123456789ab-fix", "namespace": "sentinel"},
		"spec": map[string]any{"incidentId": "01a1c008-0123-4567-89ab-0123456789ab", "fromRevision": "bad123",
			"workload": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "e2e-delivery", "name": "podinfo"}}})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		FixesResource: "FixList", actor.KustomizationsResource: "KustomizationList", GitRepositoriesResource: "GitRepositoryList",
	}, kustomization, gitRepository)
	// Created through the client: from the object alone the fake would guess
	// the resource "fixs".
	if _, err := dyn.Resource(FixesResource).Namespace("sentinel").Create(ctx, fix, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	f := &Fixer{Client: client, Dynamic: dyn, Namespace: "sentinel", GitUser: "sentinel",
		Forge:  &forge.Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "s3cret"},
		Author: object.Signature{Name: "Sentinel", Email: "sentinel@example.com"}, Log: slog.New(slog.DiscardHandler)}
	key := "sentinel/sentinel-0123456789ab-fix"
	step := func(want v1alpha1.FixPhase) v1alpha1.Fix {
		t.Helper()
		store := cache.NewStore(cache.MetaNamespaceKeyFunc)
		u, err := dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_ = store.Add(u)
		if _, err := f.process(ctx, store, key, 0); err != nil {
			t.Fatal(err)
		}
		u, _ = dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
		var got v1alpha1.Fix
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &got)
		if got.Status.Phase != want {
			t.Fatalf("phase %s, want %s: %s", got.Status.Phase, want, got.Status.Message)
		}
		return got
	}

	proposed := step(PhaseProposed)
	if proposed.Status.Number != 1 || proposed.Status.Owner != "Kustomization flux-system/delivery" || proposed.Status.Branch != "main" ||
		len(proposed.Status.Change) != 1 || !strings.HasPrefix(proposed.Status.Change[0], "kustomize/deployment.yaml:14: podinfod's image") {
		t.Fatalf("status = %+v", proposed.Status)
	}
	if srv.opened["head"] != "sentinel/fix-0123456789ab" || srv.opened["base"] != "main" || !strings.Contains(srv.opened["body"], "kustomize/deployment.yaml") {
		t.Fatalf("opened %+v", srv.opened)
	}
	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName("sentinel/fix-0123456789ab"), true)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing changes until someone merges it.
	step(PhaseProposed)
	if err := exec.Command("git", "--git-dir", bare, "update-ref", "refs/heads/main", ref.Hash().String()).Run(); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.merged = ref.Hash().String()
	srv.mu.Unlock()
	step(PhaseMerged)
	// Ready only once the owner's source has fetched the merge.
	step(PhaseMerged)
	gitRepository.Object["status"] = map[string]any{"artifact": map[string]any{"revision": "main@sha1:" + ref.Hash().String()}}
	if _, err := dyn.Resource(GitRepositoriesResource).Namespace("flux-system").Update(ctx, gitRepository, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ready := step(PhaseReady)
	if ready.Status.Resume != "sentinel resume flux flux-system/delivery" || !strings.Contains(ready.Status.Message, ready.Status.Resume) {
		t.Fatalf("status = %+v", ready.Status)
	}
	// Applied once a person has resumed the owner and it has applied the fix.
	step(PhaseReady)
	kustomization.Object["status"] = map[string]any{"lastAppliedRevision": "main@sha1:" + ref.Hash().String()}
	if _, err := dyn.Resource(actor.KustomizationsResource).Namespace("flux-system").Update(ctx, kustomization, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	step(PhaseApplied)
}

// The Fixer merges a fix itself only when the controller lets it and the
// namespace's policy still grants fix.merge at autonomous.
func TestAFixMergesItselfOnlyWhereThePolicyGrants(t *testing.T) {
	for _, tc := range []struct {
		name          string
		merge, policy bool
		want          v1alpha1.FixPhase
	}{
		{"granted and earned", true, true, PhaseMerged},
		{"earned but no longer granted", true, false, PhaseProposed},
		{"granted but not earned", false, true, PhaseProposed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var mu sync.Mutex
			merges := 0
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v1/repos/sentinel/delivery/pulls/1", func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if merges == 0 {
					_, _ = io.WriteString(w, `{"number": 1, "state": "open"}`)
					return
				}
				_, _ = io.WriteString(w, `{"number": 1, "state": "closed", "merged": true, "merge_commit_sha": "abc"}`)
			})
			mux.HandleFunc("POST /api/v1/repos/sentinel/delivery/pulls/1/merge", func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				merges++
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			fix := &unstructured.Unstructured{Object: map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Fix",
				"metadata": map[string]any{"name": "sentinel-0123456789ab-fix", "namespace": "sentinel"},
				"spec": map[string]any{"incidentId": "01a1c008-0123-4567-89ab-0123456789ab", "fromRevision": "bad123", "merge": tc.merge,
					"workload": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "shop", "name": "web"}},
				"status": map[string]any{"phase": "Proposed", "repository": srv.URL + "/sentinel/delivery.git", "number": int64(1), "owner": "Kustomization flux-system/web"}}}
			var objects []runtime.Object
			if tc.policy {
				objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "SentinelPolicy",
					"metadata": map[string]any{"name": "fixes", "namespace": "shop"},
					"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "fix.merge", "level": "autonomous"}}}}})
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				FixesResource: "FixList", actor.PoliciesResource: "SentinelPolicyList"}, objects...)
			if _, err := dyn.Resource(FixesResource).Namespace("sentinel").Create(ctx, fix, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			f := &Fixer{Dynamic: dyn, Namespace: "sentinel", Forge: &forge.Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "s3cret"},
				Log: slog.New(slog.DiscardHandler)}
			for range 2 {
				store := cache.NewStore(cache.MetaNamespaceKeyFunc)
				u, _ := dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
				_ = store.Add(u)
				if _, err := f.process(ctx, store, "sentinel/sentinel-0123456789ab-fix", 0); err != nil {
					t.Fatal(err)
				}
			}
			u, _ := dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
			phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
			if v1alpha1.FixPhase(phase) != tc.want || (merges > 0) != (tc.want == PhaseMerged) {
				t.Fatalf("phase %s after %d merges, want %s", phase, merges, tc.want)
			}
		})
	}
}
