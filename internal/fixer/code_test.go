package fixer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	appsv1 "k8s.io/api/apps/v1"
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
	"github.com/karimzakzouk/sentinel/internal/codefix"
	"github.com/karimzakzouk/sentinel/internal/forge"
	"github.com/karimzakzouk/sentinel/internal/verify"
)

// gitRun runs git in a working copy, as a person committing would.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=A person", "-c", "user.email=person@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// imageRegistry serves the labels a pipeline puts on the images it builds:
// each image of the shop tagged with the short commit it was built from.
type imageRegistry struct {
	mu      sync.Mutex
	commits map[string]string // tag -> full commit
}

func (g *imageRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rest := strings.TrimPrefix(r.URL.Path, "/v2/sentinel/shop/")
	switch {
	case strings.HasPrefix(rest, "manifests/"):
		tag := strings.TrimPrefix(rest, "manifests/")
		if g.commits[tag] == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]string{"digest": "sha256:" + tag}})
	case strings.HasPrefix(rest, "blobs/sha256:"):
		commit := g.commits[strings.TrimPrefix(rest, "blobs/sha256:")]
		_ = json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"Labels": map[string]string{
			"org.opencontainers.image.source": sourceURL, "org.opencontainers.image.revision": commit}}})
	default:
		http.NotFound(w, r)
	}
}

var sourceURL string

// A crash in a new image's code gets, beside the pull request that pins the
// image back, one to the image's source that puts the crashing function
// back as it was; once the pipeline builds that commit, the Fixer finds its
// image.
func TestACrashInCodeGetsAFixInItsSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// The deploy repository, at the failing image.
	srv := newForge(t, root)
	registry := &imageRegistry{commits: map[string]string{}}
	reg := httptest.NewTLSServer(registry)
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "https://")
	// The shop's source: a good commit, then one that drops a nil check.
	work := filepath.Join(root, "work")
	if _, err := git.PlainInit(work, false); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := writeFile(filepath.Join(work, name), content); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/shop\n\ngo 1.27\n")
	write("cart/store.go", "package cart\n\nfunc (s *Store) Get(id string) *Cart {\n\tc := s.carts[id]\n\tif c == nil {\n\t\treturn &Cart{}\n\t}\n\treturn c\n}\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "Store carts")
	good := gitRun(t, work, "rev-parse", "HEAD")
	write("cart/store.go", "package cart\n\nfunc (s *Store) Get(id string) *Cart {\n\tc := s.carts[id]\n\tc.Seen++\n\treturn c\n}\n")
	write("README.md", "Counts how often a cart is seen.\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "Count views")
	badRev := gitRun(t, work, "rev-parse", "HEAD")
	bareShop := filepath.Join(root, "sentinel", "shop.git")
	gitRun(t, root, "clone", "--bare", "--quiet", work, bareShop)
	gitRun(t, bareShop, "config", "http.receivepack", "true")
	gitRun(t, bareShop, "symbolic-ref", "HEAD", "refs/heads/master")
	sourceURL = srv.URL + "/sentinel/shop.git"
	registry.commits[good[:7]], registry.commits[badRev[:7]] = good, badRev
	goodImage, badImage := host+"/sentinel/shop:"+good[:7], host+"/sentinel/shop:"+badRev[:7]

	deploy := seed(t, map[string]string{"kustomize/kustomization.yaml": "resources: [deployment.yaml]\n", "kustomize/deployment.yaml": deployment(badImage)})
	bareDeploy := filepath.Join(root, "sentinel", "delivery.git")
	gitRun(t, root, "clone", "--bare", "--quiet", deploy, bareDeploy)
	gitRun(t, bareDeploy, "config", "http.receivepack", "true")
	head := gitRun(t, bareDeploy, "rev-parse", "main")

	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "e2e-delivery", UID: types.UID("dep"),
		Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "delivery", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		Spec: appsv1.DeploymentSpec{Template: podTemplate(goodImage)}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "e2e-delivery", Labels: map[string]string{"pod-template-hash": "bad123"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: podTemplate(badImage)}}
	unstr := func(obj map[string]any) *unstructured.Unstructured { return &unstructured.Unstructured{Object: obj} }
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		FixesResource: "FixList", actor.KustomizationsResource: "KustomizationList", GitRepositoriesResource: "GitRepositoryList",
		verify.ExperimentsResource: "ExperimentList"},
		unstr(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
			"metadata": map[string]any{"name": "delivery", "namespace": "flux-system"},
			"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "delivery"}, "path": "./kustomize"}}),
		unstr(map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
			"metadata": map[string]any{"name": "delivery", "namespace": "flux-system"},
			"spec":     map[string]any{"url": srv.URL + "/sentinel/delivery.git", "ref": map[string]any{"branch": "main"}},
			"status":   map[string]any{"artifact": map[string]any{"revision": "main@sha1:" + head}}}))
	fix := unstr(map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Fix",
		"metadata": map[string]any{"name": "sentinel-0123456789ab-fix", "namespace": "sentinel"},
		"spec": map[string]any{"incidentId": "01a1c008-0123-4567-89ab-0123456789ab", "fromRevision": "bad123", "faultClass": "deploy.crash-on-start",
			"crash":    map[string]any{"language": "go", "frames": []any{"example.com/shop/cart.(*Store).Get", "main.main"}},
			"workload": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "e2e-delivery", "name": "podinfo"}}})
	if _, err := dyn.Resource(FixesResource).Namespace("sentinel").Create(ctx, fix, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f := &Fixer{Client: fake.NewClientset(dep, rs), Dynamic: dyn, Namespace: "sentinel", GitUser: "sentinel",
		Forge:    &forge.Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "s3cret"},
		Registry: &codefix.Registry{HTTP: reg.Client()}, LabNamespaces: []string{"sentinel-lab-0"},
		Author: object.Signature{Name: "Sentinel", Email: "sentinel@example.com"}, Log: slog.New(slog.DiscardHandler)}
	step := func() v1alpha1.Fix {
		t.Helper()
		store := cache.NewStore(cache.MetaNamespaceKeyFunc)
		u, _ := dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
		_ = store.Add(u)
		if _, err := f.process(ctx, store, "sentinel/sentinel-0123456789ab-fix", 0); err != nil {
			t.Fatal(err)
		}
		u, _ = dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
		var got v1alpha1.Fix
		_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &got)
		return got
	}
	got := step()
	cf := got.Status.Code
	if got.Status.Phase != PhaseProposed || cf == nil || cf.Number != 2 || cf.Function != "example.com/shop/cart.(Store).Get" || cf.Commit == "" {
		t.Fatalf("phase %s, code %+v", got.Status.Phase, cf)
	}
	if srv.openedCode["head"] != "sentinel/code-fix-0123456789ab" || !strings.Contains(srv.openedCode["title"], "Put Get back as it was before "+badRev[:12]) {
		t.Fatalf("opened %+v", srv.openedCode)
	}
	pushed := gitRun(t, bareShop, "show", "sentinel/code-fix-0123456789ab:cart/store.go")
	if !strings.Contains(pushed, "if c == nil") || strings.Contains(pushed, "c.Seen++") {
		t.Fatalf("pushed:\n%s", pushed)
	}
	if readme := gitRun(t, bareShop, "show", "sentinel/code-fix-0123456789ab:README.md"); readme == "" {
		t.Fatal("the commit's other change was dropped")
	}
	// The pipeline builds the fix's commit; the Fixer finds its image.
	registry.mu.Lock()
	registry.commits[cf.Commit[:7]] = cf.Commit
	registry.mu.Unlock()
	got = step()
	if got.Status.Code.Image != host+"/sentinel/shop:"+cf.Commit[:7] {
		t.Fatalf("code %+v", got.Status.Code)
	}
	// The Lab races it; once it has a verdict, the pull request says it.
	race := func(image string) *unstructured.Unstructured {
		t.Helper()
		exp := unstr(map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Experiment",
			"metadata": map[string]any{"name": verify.CodeExperimentName("01a1c008-0123-4567-89ab-0123456789ab", image), "namespace": "sentinel-lab-0"},
			"status":   map[string]any{"phase": "Running"}})
		if _, err := dyn.Resource(verify.ExperimentsResource).Namespace("sentinel-lab-0").Create(ctx, exp, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		return exp
	}
	exp := race(got.Status.Code.Image)
	if got = step(); got.Status.Code.Verdict != "" || len(srv.comments) != 0 {
		t.Fatalf("a verdict before the Lab had one: %+v", got.Status.Code)
	}
	// The Lab finds the restored function does not fix it: the Fixer
	// tries the next fixer's change, the whole commit reverted, on the
	// same pull request.
	exp.Object["status"] = map[string]any{"phase": "Failed", "message": "the image does not fix it: it failed in 3 of 3 runs", "runs": int64(3)}
	if _, err := dyn.Resource(verify.ExperimentsResource).Namespace("sentinel-lab-0").Update(ctx, exp, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got = step()
	cf = got.Status.Code
	if cf.Attempt != 2 || cf.Fixer != "revert" || cf.Image != "" || cf.Verdict != "" || len(cf.Tried) != 1 || cf.Tried[0].Verdict != "Failed" ||
		cf.Tried[0].Fixer != "restore" || cf.Number != 2 {
		t.Fatalf("after the Lab failed the restore: %+v", cf)
	}
	if len(srv.comments) != 2 || !strings.Contains(srv.comments[0], "it does not fix the crash") || !strings.Contains(srv.comments[1], "now tries **revert**") {
		t.Fatalf("comments %q", srv.comments)
	}
	if pushed := gitRun(t, bareShop, "show", "sentinel/code-fix-0123456789ab:cart/store.go"); !strings.Contains(pushed, "if c == nil") {
		t.Fatalf("the revert pushed:\n%s", pushed)
	}
	if out, err := exec.Command("git", "-C", bareShop, "cat-file", "-e", "sentinel/code-fix-0123456789ab:README.md").CombinedOutput(); err == nil {
		t.Fatalf("the revert kept the README the commit added: %s", out)
	}
	registry.mu.Lock()
	registry.commits[cf.Commit[:7]] = cf.Commit
	registry.mu.Unlock()
	if got = step(); got.Status.Code.Image != host+"/sentinel/shop:"+cf.Commit[:7] {
		t.Fatalf("the revert's image was not found: %+v", got.Status.Code)
	}
	exp = race(got.Status.Code.Image)
	exp.Object["status"] = map[string]any{"phase": "Passed", "message": "the image fixes it: it worked in 15 of 15 runs", "runs": int64(15)}
	if _, err := dyn.Resource(verify.ExperimentsResource).Namespace("sentinel-lab-0").Update(ctx, exp, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	got = step()
	if got.Status.Code.Verdict != "Passed" || len(srv.comments) != 3 || !strings.Contains(srv.comments[2], "it fixes the crash") {
		t.Fatalf("code %+v, comments %q", got.Status.Code, srv.comments)
	}
	// A person closes it anyway: the next fixer, a model here, gets a new
	// pull request, told what was tried.
	model := &replies{text: "FILE: cart/store.go\nFUNCTION: Store.Get\n```go\nfunc (s *Store) Get(id string) *Cart {\n\tif c := s.carts[id]; c != nil {\n\t\treturn c\n\t}\n\treturn &Cart{}\n}\n```\n"}
	f.LLM = model
	srv.mu.Lock()
	srv.closed = map[string]bool{"2": true}
	srv.mu.Unlock()
	got = step()
	cf = got.Status.Code
	if cf.Number != 3 || cf.Fixer != "llm" || cf.State != "open" || len(cf.Tried) != 2 || cf.Tried[1].Verdict != "Declined" ||
		srv.openedCode["head"] != "sentinel/code-fix-0123456789ab-2" {
		t.Fatalf("after a person closed it: %+v, opened %+v", cf, srv.openedCode)
	}
	if !strings.Contains(model.asked, "revert") || !strings.Contains(model.asked, "Declined") || !strings.Contains(srv.openedCode["body"], "language model") {
		t.Fatalf("the model was asked:\n%s\nthe pull request says:\n%s", model.asked, srv.openedCode["body"])
	}
	if pushed := gitRun(t, bareShop, "show", "sentinel/code-fix-0123456789ab-2:cart/store.go"); !strings.Contains(pushed, "if c := s.carts[id]; c != nil") {
		t.Fatalf("pushed:\n%s", pushed)
	}
}

// replies is a model that always says the same.
type replies struct {
	text, asked string
}

func (r *replies) Complete(_ context.Context, _, user string) (string, error) {
	r.asked = user
	return r.text, nil
}

func writeFile(name, content string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	return os.WriteFile(name, []byte(content), 0o600)
}

// Where no Lab is installed, a fix to the code says it will not be raced.
func TestAFixToTheCodeSaysWhenNoLabWillRaceIt(t *testing.T) {
	fix := &v1alpha1.Fix{Spec: v1alpha1.FixSpec{IncidentID: "inc-1", Workload: v1alpha1.TargetRef{Namespace: "shop", Name: "cart"}}}
	p := &codefix.Change{Fixer: "restore", Function: "main.load", Why: "it changed", Edits: []codefix.Edit{{Path: "catalog.go", Line: 17}}}
	src := codefix.Built{Repository: "http://forge/person/shop-src", Revision: "c2fe9e2bcce8aa"}
	if body := codeBody(fix, p, "shop:95e2311", "shop:c2fe9e2", src, true); !strings.Contains(body, "races it against the failing one in its Lab") {
		t.Fatalf("with a Lab:\n%s", body)
	}
	if body := codeBody(fix, p, "shop:95e2311", "shop:c2fe9e2", src, false); !strings.Contains(body, "no Lab is installed") ||
		strings.Contains(body, "races it") {
		t.Fatalf("without a Lab:\n%s", body)
	}
}

// A forge that refuses the verdict's comment, as for a token that may not
// comment, does not keep the verdict from the fix's status; one that fails
// for its own reasons is asked again.
func TestTheLabsVerdictIsKeptWhenTheForgeRefusesItsComment(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status  int
		verdict string
	}{{http.StatusForbidden, "NotSure"}, {http.StatusBadGateway, ""}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message": "token does not have the required scope"}`, tc.status)
		}))
		exp := &unstructured.Unstructured{Object: map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Experiment",
			"metadata": map[string]any{"name": verify.CodeExperimentName("inc-1", "registry.test/shop:cfb0fc6"), "namespace": "sentinel-lab-0"},
			"status":   map[string]any{"phase": "NotSure", "message": "the Lab stays off"}}}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
			map[schema.GroupVersionResource]string{verify.ExperimentsResource: "ExperimentList"}, exp)
		f := &Fixer{Dynamic: dyn, Forge: &forge.Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "t"},
			LabNamespaces: []string{"sentinel-lab-0"}, Log: slog.New(slog.DiscardHandler)}
		fix := &v1alpha1.Fix{Spec: v1alpha1.FixSpec{IncidentID: "inc-1"}, Status: v1alpha1.FixStatus{Code: &v1alpha1.CodeFix{
			Repository: srv.URL + "/person/shop-src", Number: 1, Image: "registry.test/shop:cfb0fc6"}}}
		var said []string
		f.Forge.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			var c map[string]string
			_ = json.Unmarshal(body, &c)
			said = append(said, c["body"])
			r.Body = io.NopCloser(bytes.NewReader(body))
			return http.DefaultTransport.RoundTrip(r)
		})}
		changed := f.reportVerdict(ctx, fix)
		if len(said) != 1 || !strings.Contains(said[0], "did not race") {
			t.Errorf("%d: said %q", tc.status, said)
		}
		if cf := fix.Status.Code; changed != (tc.verdict != "") || cf.Verdict != tc.verdict ||
			(tc.verdict != "" && !strings.Contains(cf.Message, "the forge refused the comment")) {
			t.Errorf("%d: changed %v, code %+v", tc.status, changed, cf)
		}
		srv.Close()
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A change moved into Tried counts once: a model asked once, whose change
// the Lab failed, is asked again while its attempts last.
func TestAModelsFailedChangeCountsOnce(t *testing.T) {
	cf := &v1alpha1.CodeFix{Tried: []v1alpha1.CodeAttempt{{Fixer: "llm", Verdict: "Failed"}}}
	if n := llmAttempts(cf); n != 1 {
		t.Fatalf("%d model attempts counted, want 1", n)
	}
	cf.Fixer = "llm"
	if n := llmAttempts(cf); n != 2 {
		t.Fatalf("with a current model change, %d counted, want 2", n)
	}
}

// A crash nothing was rolled back from, a bug two commits older than the
// image that runs, gets a fix to its code measured from the commit before
// the one that last changed the crashing function; the newer commits stay.
func TestAnOldBugIsFixedFromTheCommitBeforeIt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	srv := newForge(t, root)
	registry := &imageRegistry{commits: map[string]string{}}
	reg := httptest.NewTLSServer(registry)
	t.Cleanup(reg.Close)
	host := strings.TrimPrefix(reg.URL, "https://")
	work := filepath.Join(root, "work")
	if _, err := git.PlainInit(work, false); err != nil {
		t.Fatal(err)
	}
	commit := func(name, content, message string) string {
		t.Helper()
		if err := writeFile(filepath.Join(work, name), content); err != nil {
			t.Fatal(err)
		}
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", message)
		return gitRun(t, work, "rev-parse", "HEAD")
	}
	base := commit("catalog.py", "def load():\n    items = {}\n    for i in D:\n        items[i] = i\n    return items\n", "Load the catalog")
	commit("catalog.py", "def load():\n    items = None\n    for i in D:\n        items[i] = i\n    return items\n", "Start the catalog empty")
	commit("README.md", "pyshop\n", "Say what it is")
	running := commit("app.py", "import catalog\nCATALOG = catalog.load()\n", "Load at start")
	bare := filepath.Join(root, "sentinel", "shop.git")
	gitRun(t, root, "clone", "--bare", "--quiet", work, bare)
	gitRun(t, bare, "config", "http.receivepack", "true")
	gitRun(t, bare, "symbolic-ref", "HEAD", "refs/heads/master")
	sourceURL = srv.URL + "/sentinel/shop.git"
	registry.commits[running[:7]] = running
	image := host + "/sentinel/shop:" + running[:7]
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "pyshop", Namespace: "e2e-delivery"}, Spec: appsv1.DeploymentSpec{Template: podTemplate(image)}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{FixesResource: "FixList"})
	if _, err := dyn.Resource(FixesResource).Namespace("sentinel").Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1alpha1.GroupVersion.String(), "kind": "Fix",
		"metadata": map[string]any{"name": "sentinel-0123456789ab-fix", "namespace": "sentinel"},
		"spec": map[string]any{"incidentId": "01a1c008-0123-4567-89ab-0123456789ab", "fromRevision": "cur", "faultClass": "runtime.crash", "codeOnly": true,
			"crash": map[string]any{"language": "python", "frames": []any{"catalog.load"},
				"stack": []any{map[string]any{"function": "catalog.load", "file": "/app/catalog.py", "line": int64(4)}}},
			"workload": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "namespace": "e2e-delivery", "name": "pyshop"}}}},
		metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f := &Fixer{Client: fake.NewClientset(dep), Dynamic: dyn, Namespace: "sentinel", GitUser: "sentinel",
		Forge:    &forge.Client{Web: srv.URL, API: srv.URL + "/api/v1", Token: "s3cret"},
		Registry: &codefix.Registry{HTTP: reg.Client()}, LabNamespaces: []string{"sentinel-lab-0"},
		Author: object.Signature{Name: "Sentinel", Email: "sentinel@example.com"}, Log: slog.New(slog.DiscardHandler)}
	store := cache.NewStore(cache.MetaNamespaceKeyFunc)
	u, _ := dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
	_ = store.Add(u)
	if _, err := f.process(ctx, store, "sentinel/sentinel-0123456789ab-fix", 0); err != nil {
		t.Fatal(err)
	}
	u, _ = dyn.Resource(FixesResource).Namespace("sentinel").Get(ctx, "sentinel-0123456789ab-fix", metav1.GetOptions{})
	var got v1alpha1.Fix
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &got)
	cf := got.Status.Code
	if got.Status.Phase != PhaseProposed || cf == nil || cf.Base != base || cf.Fixer != "restore" || cf.Number != 2 {
		t.Fatalf("phase %s %q, code %+v", got.Status.Phase, got.Status.Message, cf)
	}
	pushed := gitRun(t, bare, "show", "sentinel/code-fix-0123456789ab:catalog.py")
	if !strings.Contains(pushed, "items = {}") {
		t.Fatalf("pushed:\n%s", pushed)
	}
	if app := gitRun(t, bare, "show", "sentinel/code-fix-0123456789ab:app.py"); !strings.Contains(app, "catalog.load()") {
		t.Fatal("the newer commits were undone")
	}
	if srv.opened != nil {
		t.Fatalf("a pull request to the deploy repository was opened: %+v", srv.opened)
	}
}
