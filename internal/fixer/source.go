package fixer

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/actor"
)

// GitRepositoriesResource is Flux's GitRepository.
var GitRepositoriesResource = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}

// Source is where a GitOps owner deploys a workload from: a branch of a
// repository, a path in it, and what the owner adds to the names it
// renders.
type Source struct {
	// Owner names the owner, as "Kind namespace/name".
	Owner                  string
	URL, Branch, Path      string
	NamePrefix, NameSuffix string
	// Resume is the command a person runs to resume the owner.
	Resume string

	revision func(context.Context) (string, error)
	applied  func(context.Context) (bool, string, error)
}

// Revision is the commit the owner's source last fetched.
func (s *Source) Revision(ctx context.Context) (string, error) { return s.revision(ctx) }

// Applied reports whether the owner syncs again, no longer paused, and the
// commit it last applied.
func (s *Source) Applied(ctx context.Context) (bool, string, error) { return s.applied(ctx) }

var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

// SourceOf finds the GitOps owner that deploys a workload, and where it
// deploys it from.
func SourceOf(ctx context.Context, dyn dynamic.Interface, meta metav1.ObjectMeta, argoNamespace string) (*Source, error) {
	if ref := actor.FluxKustomization(meta); ref != "" {
		ns, name, _ := strings.Cut(ref, "/")
		return fluxSource(ctx, dyn, ns, name)
	}
	if ns, name, ok := actor.ArgoApplication(meta, argoNamespace); ok {
		return argoSource(ctx, dyn, ns, name)
	}
	if ref := actor.FluxHelmRelease(meta); ref != "" {
		return nil, fmt.Errorf("the Flux HelmRelease %s renders %s, and the Fixer does not change Helm values yet", ref, meta.Name)
	}
	return nil, fmt.Errorf("no Argo CD Application or Flux Kustomization deploys %s, so no repository the Fixer can find holds the change", meta.Name)
}

func fluxSource(ctx context.Context, dyn dynamic.Interface, ns, name string) (*Source, error) {
	owner := "Kustomization " + ns + "/" + name
	k, err := dyn.Resource(actor.KustomizationsResource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the Flux %s: %w", owner, err)
	}
	kind, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "kind")
	srcName, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "name")
	srcNS, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "namespace")
	if srcNS == "" {
		srcNS = ns
	}
	if kind != "GitRepository" {
		return nil, fmt.Errorf("the Flux %s deploys from a %s; the Fixer proposes changes to Git repositories", owner, kind)
	}
	repos := dyn.Resource(GitRepositoriesResource).Namespace(srcNS)
	repo, err := repos.Get(ctx, srcName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the GitRepository %s/%s: %w", srcNS, srcName, err)
	}
	url, _, _ := unstructured.NestedString(repo.Object, "spec", "url")
	ref, _, _ := unstructured.NestedStringMap(repo.Object, "spec", "ref")
	branch := ref["branch"]
	for _, pinned := range []string{"tag", "semver", "commit", "name"} {
		if ref[pinned] != "" {
			return nil, fmt.Errorf("the GitRepository %s/%s follows a %s, so a pull request to a branch would not change what it deploys", srcNS, srcName, pinned)
		}
	}
	if branch == "" {
		branch = "master"
	}
	s := &Source{Owner: owner, URL: url, Branch: branch, Resume: "sentinel resume flux " + ns + "/" + name}
	s.Path, _, _ = unstructured.NestedString(k.Object, "spec", "path")
	s.NamePrefix, _, _ = unstructured.NestedString(k.Object, "spec", "namePrefix")
	s.NameSuffix, _, _ = unstructured.NestedString(k.Object, "spec", "nameSuffix")
	s.revision = func(ctx context.Context) (string, error) {
		repo, err := repos.Get(ctx, srcName, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		rev, _, _ := unstructured.NestedString(repo.Object, "status", "artifact", "revision")
		_, sha, _ := strings.Cut(rev, ":")
		return sha, nil
	}
	s.applied = func(ctx context.Context) (bool, string, error) {
		k, err := dyn.Resource(actor.KustomizationsResource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		suspended, _, _ := unstructured.NestedBool(k.Object, "spec", "suspend")
		rev, _, _ := unstructured.NestedString(k.Object, "status", "lastAppliedRevision")
		_, sha, _ := strings.Cut(rev, ":")
		return !suspended, sha, nil
	}
	return s, nil
}

func argoSource(ctx context.Context, dyn dynamic.Interface, ns, name string) (*Source, error) {
	owner := "Application " + ns + "/" + name
	apps := dyn.Resource(actor.ApplicationsResource).Namespace(ns)
	app, err := apps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("the Argo CD %s: %w", owner, err)
	}
	if sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources"); len(sources) > 0 {
		return nil, fmt.Errorf("the Argo CD %s has several sources; the Fixer handles one", owner)
	}
	src, _, _ := unstructured.NestedMap(app.Object, "spec", "source")
	if src["chart"] != nil || src["helm"] != nil {
		return nil, fmt.Errorf("the Argo CD %s renders a Helm chart, and the Fixer does not change Helm values yet", owner)
	}
	url, _ := src["repoURL"].(string)
	branch, _ := src["targetRevision"].(string)
	switch {
	case branch == "HEAD":
		branch = ""
	case commitHash.MatchString(branch):
		return nil, fmt.Errorf("the Argo CD %s pins commit %s, so a pull request would not change what it deploys", owner, branch)
	}
	s := &Source{Owner: owner, URL: url, Branch: branch, Resume: "sentinel resume argocd " + ns + "/" + name}
	s.Path, _ = src["path"].(string)
	s.NamePrefix, _, _ = unstructured.NestedString(src, "kustomize", "namePrefix")
	s.NameSuffix, _, _ = unstructured.NestedString(src, "kustomize", "nameSuffix")
	s.revision = func(ctx context.Context) (string, error) {
		app, err := apps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		rev, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
		return rev, nil
	}
	s.applied = func(ctx context.Context) (bool, string, error) {
		app, err := apps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		_, automated, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
		status, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
		rev, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
		if status != "Synced" {
			rev = ""
		}
		return automated, rev, nil
	}
	return s, nil
}

// ImageChange finds the one container whose image differs between the
// revision a Deployment was rolled back from and its current template.
func ImageChange(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment, fromRevision string) (Change, error) {
	list, err := client.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "pod-template-hash=" + fromRevision})
	if err != nil {
		return Change{}, err
	}
	var from *appsv1.ReplicaSet
	for i, rs := range list.Items {
		for _, o := range rs.OwnerReferences {
			if o.UID == dep.UID {
				from = &list.Items[i]
			}
		}
	}
	if from == nil {
		return Change{}, fmt.Errorf("%s has no revision %s any more", dep.Name, fromRevision)
	}
	current := map[string]string{}
	for _, c := range append(dep.Spec.Template.Spec.InitContainers, dep.Spec.Template.Spec.Containers...) {
		current[c.Name] = c.Image
	}
	var changes []Change
	for _, c := range append(from.Spec.Template.Spec.InitContainers, from.Spec.Template.Spec.Containers...) {
		if now, ok := current[c.Name]; ok && now != c.Image {
			changes = append(changes, Change{Container: c.Name, Bad: c.Image, Good: now})
		}
	}
	switch len(changes) {
	case 1:
		return changes[0], nil
	case 0:
		return Change{}, fmt.Errorf("the revision %s was rolled back from runs the same images as now; the Fixer restores images only so far", fromRevision)
	}
	return Change{}, fmt.Errorf("%d containers' images changed in revision %s; the Fixer restores one at a time", len(changes), fromRevision)
}
