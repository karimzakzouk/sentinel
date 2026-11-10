package fixer

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
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

// ContainerChange finds the one value that differs between the revision a
// Deployment was rolled back from and its current template, among those the
// Fixer restores: a container's image, command and arguments, and its
// environment variables' literal values.
func ContainerChange(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment, fromRevision string) (Change, error) {
	c, _, err := ChangeFor(ctx, client, dep, v1alpha1.FixSpec{FromRevision: fromRevision})
	return c, err
}

// ChangeFor finds the value a fix sets: of the values the revision the
// Deployment was rolled back from changed, the one the incident's fault
// class blames. The others it returns as kept, for the fix to leave as that
// revision set them. A memory or CPU limit gets the value Sentinel learned,
// not the old one.
func ChangeFor(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment, spec v1alpha1.FixSpec) (Change, []Change, error) {
	changes, err := revisionChanges(ctx, client, dep, spec.FromRevision)
	if err != nil {
		return Change{}, nil, err
	}
	if len(changes) == 0 {
		return Change{}, nil, fmt.Errorf("the revision %s was rolled back from differs from the current one in nothing the Fixer restores "+
			"(images, commands, arguments, environment values, memory and CPU limits)", spec.FromRevision)
	}
	var blamed, kept []Change
	for _, c := range changes {
		if blames(spec.FaultClass, c.field()) {
			blamed = append(blamed, c)
		} else {
			kept = append(kept, c)
		}
	}
	switch {
	case len(blamed) == 0:
		return Change{}, nil, fmt.Errorf("revision %s changed %s, none of which a %s fault is blamed on", spec.FromRevision, listed(changes), spec.FaultClass)
	case len(blamed) > 1 && spec.FaultClass == "":
		return Change{}, nil, fmt.Errorf("%d values changed in revision %s (%s); the Fixer restores one at a time", len(blamed), spec.FromRevision, listed(blamed))
	case len(blamed) > 1:
		return Change{}, nil, fmt.Errorf("%d values changed in revision %s that a %s fault could be blamed on (%s); the Fixer restores one at a time",
			len(blamed), spec.FromRevision, spec.FaultClass, listed(blamed))
	}
	c := blamed[0]
	if c.Bad == valueFromSet || c.Good == valueFromSet {
		return Change{}, nil, fmt.Errorf("%s's %s comes from a ConfigMap, Secret or field in one revision; the Fixer restores literal values only", c.Container, what(c.Field))
	}
	if learned, ok := learnedLimit(spec, c); c.field() == fieldMemoryLimit || c.field() == fieldCPULimit {
		if !ok && strings.HasPrefix(spec.LabVerdict, "it does not fix") {
			return Change{}, nil, fmt.Errorf("the Lab raced the limit learned from %s's healthy use, and %s; putting the old limit back can be as wrong as the new one",
				c.Container, spec.LabVerdict)
		}
		if !ok {
			return Change{}, nil, fmt.Errorf("revision %s changed %s's %s; putting the old limit back can be as wrong as the new one, "+
				"and Sentinel has not measured enough of what %s uses to learn one", spec.FromRevision, c.Container, what(c.field()), c.Container)
		}
		if !lessQuantity(c.Good, learned) {
			c.Good = learned
		}
	}
	return c, kept, nil
}

// learnedLimit is the limit Sentinel learned for a memory or CPU change.
func learnedLimit(spec v1alpha1.FixSpec, c Change) (string, bool) {
	switch c.field() {
	case fieldMemoryLimit:
		v, ok := spec.MemoryLimits[c.Container]
		return v, ok
	case fieldCPULimit:
		v, ok := spec.CPULimits[c.Container]
		return v, ok
	}
	return "", false
}

// blames reports whether a fault of a class is blamed on a field: a bad
// image on the image, a memory or CPU limit on the limit, a crash or an
// error on what the container runs. A class it does not know blames every
// field.
func blames(class, field string) bool {
	switch class {
	case "deploy.bad-image":
		return field == fieldImage
	case "resource.memory-limit":
		return field == fieldMemoryLimit
	case "resource.cpu-limit":
		return field == fieldCPULimit
	case "deploy.crash-on-start", "config.error", "deploy.new-errors", "runtime.crash":
		return field != fieldMemoryLimit && field != fieldCPULimit
	}
	return true
}

func listed(cs []Change) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Container+"'s "+what(c.field()))
	}
	return strings.Join(out, ", ")
}

// lessQuantity reports whether quantity a is set and smaller than b.
func lessQuantity(a any, b string) bool {
	as, ok := a.(string)
	if !ok {
		return false
	}
	aq, aerr := resource.ParseQuantity(as)
	bq, berr := resource.ParseQuantity(b)
	return aerr == nil && berr == nil && aq.Cmp(bq) < 0
}

// revisionChanges lists the values the Fixer restores that differ between
// the revision a Deployment was rolled back from and its current template.
func revisionChanges(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment, fromRevision string) ([]Change, error) {
	list, err := client.AppsV1().ReplicaSets(dep.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "pod-template-hash=" + fromRevision})
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%s has no revision %s any more", dep.Name, fromRevision)
	}
	current := map[string]corev1.Container{}
	for _, c := range append(dep.Spec.Template.Spec.InitContainers, dep.Spec.Template.Spec.Containers...) {
		current[c.Name] = c
	}
	var changes []Change
	for _, bad := range append(from.Spec.Template.Spec.InitContainers, from.Spec.Template.Spec.Containers...) {
		good, ok := current[bad.Name]
		if !ok {
			continue
		}
		for _, field := range containerFields(bad, good) {
			c := Change{Container: bad.Name, Field: field, Bad: fieldValue(bad, field), Good: fieldValue(good, field)}
			// Two values taken from elsewhere differ by where they come from.
			if !c.same(c.Bad, c.Good) || c.Bad == valueFromSet && !reflect.DeepEqual(envVar(bad, field), envVar(good, field)) {
				changes = append(changes, c)
			}
		}
	}
	return changes, nil
}

// containerFields lists the fields a fix may restore in two versions of a
// container: image, command, arguments, memory and CPU limits, and every
// environment variable either sets.
func containerFields(a, b corev1.Container) []string {
	fields := []string{fieldImage, fieldCommand, fieldArgs, fieldMemoryLimit, fieldCPULimit}
	seen := map[string]bool{}
	for _, e := range append(a.Env, b.Env...) {
		if !seen[e.Name] {
			seen[e.Name] = true
			fields = append(fields, envPrefix+e.Name)
		}
	}
	return fields
}

func envVar(c corev1.Container, field string) *corev1.EnvVar {
	for i := range c.Env {
		if envPrefix+c.Env[i].Name == field {
			return &c.Env[i]
		}
	}
	return nil
}

// fieldValue reads a field of a container as valueIn reads a rendered one.
func fieldValue(c corev1.Container, field string) any {
	switch {
	case field == fieldImage:
		return c.Image
	case field == fieldCommand && len(c.Command) > 0:
		return c.Command
	case field == fieldArgs && len(c.Args) > 0:
		return c.Args
	case strings.HasPrefix(field, envPrefix):
		for _, e := range c.Env {
			if e.Name != strings.TrimPrefix(field, envPrefix) {
				continue
			}
			if e.ValueFrom != nil {
				return valueFromSet
			}
			return e.Value
		}
	case field == fieldMemoryLimit:
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			return q.String()
		}
	case field == fieldCPULimit:
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			return q.String()
		}
	}
	return nil
}
