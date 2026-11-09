//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/cli"
)

// gitea is the test Gitea's API, as the person who owns person/delivery.
type gitea struct {
	t     *testing.T
	base  string
	token string
}

func giteaAsPerson(t *testing.T) *gitea {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	secret, err := clientAs(t, "").CoreV1().Secrets("gitea").Get(ctx, "gitea-person", metav1.GetOptions{})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	const port = "13001"
	pf := exec.CommandContext(ctx, "kubectl", "--context", flags("").Context, "-n", "gitea", "port-forward", "svc/gitea-http", port+":3000")
	if err := pf.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = pf.Wait()
	})
	g := &gitea{t: t, base: "http://127.0.0.1:" + port + "/api/v1", token: string(secret.Data["token"])}
	eventually(t, time.Minute, "Gitea's API through port-forward", func() (bool, error) {
		return g.call(http.MethodGet, "/repos/person/delivery", nil, nil) == nil, nil
	})
	return g
}

func (g *gitea) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, g.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// file reads a file of person/delivery's main branch.
func (g *gitea) file(path string) (string, string) {
	g.t.Helper()
	var f struct{ Content, SHA string }
	if err := g.call(http.MethodGet, "/repos/person/delivery/contents/"+path+"?ref=main", nil, &f); err != nil {
		g.t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(f.Content)
	if err != nil {
		g.t.Fatal(err)
	}
	return string(content), f.SHA
}

// commit writes a file to person/delivery's main branch as the person.
func (g *gitea) commit(path, content, message string) {
	g.t.Helper()
	_, sha := g.file(path)
	if err := g.call(http.MethodPut, "/repos/person/delivery/contents/"+path, map[string]string{
		"content": base64.StdEncoding.EncodeToString([]byte(content)), "sha": sha, "message": message, "branch": "main",
	}, nil); err != nil {
		g.t.Fatal(err)
	}
}

// A bad image committed to the repository Flux deploys from breaks podinfo.
// Sentinel suspends the Kustomization and rolls back, and the Fixer opens a
// pull request that changes the one line that set the image. A person
// merges it and resumes the Kustomization as Sentinel says, and Sentinel
// checks that the fix held. hack/e2e-up.sh with E2E_FLUX=1 and E2E_GITEA=1
// runs Gitea and the Fixer.
func TestTheFixerProposesTheFixInGit(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("kustomize.toolkit.fluxcd.io/v1"); err != nil {
		t.Skip("Flux is not installed: run hack/e2e-up.sh with E2E_FLUX=1")
	}
	if _, err := client.AppsV1().Deployments(sentinelNS).Get(ctx, "sentinel-fixer", metav1.GetOptions{}); err != nil {
		t.Skip("the Fixer is not installed: run hack/e2e-up.sh with E2E_FLUX=1 E2E_GITEA=1")
	}
	g := giteaAsPerson(t)
	original, _ := g.file("deployment.yaml")
	t.Cleanup(func() {
		if now, _ := g.file("deployment.yaml"); now != original {
			g.commit("deployment.yaml", original, "Put podinfo back as it was")
		}
	})

	admin := dynamicAs(t, "")
	repos, ks := admin.Resource(gitRepositories).Namespace("flux-system"), admin.Resource(kustomizations).Namespace("flux-system")
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
		"metadata": map[string]any{"name": "e2e-fixer", "namespace": "flux-system"},
		"spec": map[string]any{"url": "http://gitea-http.gitea.svc:3000/person/delivery.git", "interval": "1m",
			"ref": map[string]any{"branch": "main"}},
	}}
	k := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "e2e-fixer", "namespace": "flux-system"},
		"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "e2e-fixer"},
			"path": "./", "targetNamespace": gitopsNS, "namePrefix": "fixer-", "prune": true, "interval": "1m"},
	}}
	if _, err := repos.Create(ctx, repo, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Delete(ctx, "e2e-fixer", metav1.DeleteOptions{}) })
	if _, err := ks.Create(ctx, k, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ks.Delete(ctx, "e2e-fixer", metav1.DeleteOptions{}) })
	var image string
	eventually(t, 5*time.Minute, "podinfo deployed from Gitea and available", func() (bool, error) {
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "fixer-podinfo", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		image = d.Spec.Template.Spec.Containers[0].Image
		return d.Status.AvailableReplicas > 0 && d.Status.AvailableReplicas == d.Status.Replicas, nil
	})

	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-fixer", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "flux.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-fixer", metav1.DeleteOptions{})
	})
	reconcile := func(r interface {
		Patch(context.Context, string, types.PatchType, []byte, metav1.PatchOptions, ...string) (*unstructured.Unstructured, error)
	}) {
		t.Helper()
		patch := `{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + time.Now().Format(time.RFC3339Nano) + `"}}}`
		if _, err := r.Patch(ctx, "e2e-fixer", types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	injected := time.Now()
	g.commit("deployment.yaml", strings.Replace(original, "podinfo:6.17.0", "podinfo:does-not-exist", 1), "Bump podinfo")
	reconcile(repos)
	reconcile(ks)
	waitForLogEntry(t, client, injected, 5*time.Minute, "a request to roll back through Flux", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "flux.rollback" && e["target"] == "Deployment.apps/"+gitopsNS+"/fixer-podinfo"
	})

	fixes := admin.Resource(schema.GroupVersionResource{Group: v1alpha1.Group, Version: "v1alpha1", Resource: "fixes"}).Namespace(sentinelNS)
	fixOf := func() (*v1alpha1.Fix, error) {
		list, err := fixes.List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, u := range list.Items {
			var f v1alpha1.Fix
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &f); err != nil {
				return nil, err
			}
			if f.Spec.Workload.Name == "fixer-podinfo" && f.CreationTimestamp.After(injected.Add(-time.Second)) {
				return &f, nil
			}
		}
		return nil, nil
	}
	phase := func(want v1alpha1.FixPhase, within time.Duration) *v1alpha1.Fix {
		t.Helper()
		var fix *v1alpha1.Fix
		eventually(t, within, "the fix "+string(want), func() (bool, error) {
			f, err := fixOf()
			if err != nil || f == nil {
				return false, err
			}
			if p := f.Status.Phase; p == "Abstained" || p == "Failed" || p == "Declined" {
				return false, fmt.Errorf("the fix is %s: %s", p, f.Status.Message)
			}
			fix = f
			return f.Status.Phase == want, nil
		})
		return fix
	}
	proposed := phase("Proposed", 5*time.Minute)
	var files []struct {
		Filename  string `json:"filename"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	}
	if err := g.call(http.MethodGet, fmt.Sprintf("/repos/person/delivery/pulls/%d/files", proposed.Status.Number), nil, &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Filename != "deployment.yaml" || files[0].Additions != 1 || files[0].Deletions != 1 {
		t.Fatalf("the pull request changes %+v; want one line of deployment.yaml", files)
	}

	if err := g.call(http.MethodPost, fmt.Sprintf("/repos/person/delivery/pulls/%d/merge", proposed.Status.Number),
		map[string]string{"Do": "merge"}, nil); err != nil {
		t.Fatal(err)
	}
	reconcile(repos)
	ready := phase("Ready", 5*time.Minute)
	if ready.Status.Resume != "sentinel resume flux flux-system/e2e-fixer" {
		t.Fatalf("resume with %q", ready.Status.Resume)
	}
	var out bytes.Buffer
	resume := cli.NewRoot()
	resume.SetArgs([]string{"--context", flags("").Context, "resume", "flux", "flux-system/e2e-fixer"})
	resume.SetOut(&out)
	if err := resume.ExecuteContext(ctx); err != nil {
		t.Fatalf("sentinel resume: %q, %v", out.String(), err)
	}
	reconcile(ks)
	phase("Applied", 5*time.Minute)
	held := waitForLogEntry(t, client, injected, 5*time.Minute, "the fix checked", func(e map[string]any) bool {
		return e["msg"] == "fix held" || e["msg"] == "fix notheld"
	})
	if held["msg"] != "fix held" {
		t.Fatalf("the fix did not hold: %v", held)
	}
	d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "fixer-podinfo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Spec.Template.Spec.Containers[0].Image; got != image {
		t.Fatalf("podinfo runs %s, want %s", got, image)
	}
}
