package fixer

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/codefix"
	"github.com/karimzakzouk/sentinel/internal/forge"
	"github.com/karimzakzouk/sentinel/internal/policy"
)

// FixesResource is the Fix custom resource.
var FixesResource = v1alpha1.GroupVersion.WithResource("fixes")

// Phases of a fix.
const (
	PhaseProposed  v1alpha1.FixPhase = "Proposed"
	PhaseMerged    v1alpha1.FixPhase = "Merged"
	PhaseReady     v1alpha1.FixPhase = "Ready"
	PhaseApplied   v1alpha1.FixPhase = "Applied"
	PhaseDeclined  v1alpha1.FixPhase = "Declined"
	PhaseAbstained v1alpha1.FixPhase = "Abstained"
	PhaseFailed    v1alpha1.FixPhase = "Failed"
)

// retries is how many times a fix is tried again after an error from the
// forge or the network before it fails.
const retries = 3

// historyDepth is how far back the Fixer looks for a merge in the history
// of the commit an owner's source has fetched.
const historyDepth = 200

// Fixer turns Fix requests in its namespace into pull requests to the
// repositories GitOps owners deploy from, follows them until they are
// merged and the owner's source holds the merge, and says why when it
// will not propose one.
type Fixer struct {
	Client          kubernetes.Interface
	Dynamic         dynamic.Interface
	Namespace       string
	Forge           *forge.Client
	GitUser         string
	Author          object.Signature
	ArgoCDNamespace string
	// Registry, when set, reads images' labels, which turns on fixes to a
	// failing image's code; LabNamespaces are where the Lab races the
	// images built from them.
	Registry      *codefix.Registry
	LabNamespaces []string
	// CodeFixers are the fixers tried, in order, for a crash in code:
	// restore, template, revert and llm; empty is all four, in that order.
	CodeFixers []string
	// LLM, when set, is the language model the llm fixer asks, at most
	// LLMAttempts times a fix, after the other fixers have nothing left.
	LLM         codefix.Completer
	LLMAttempts int
	// Sources maps image repositories (registry/name) to the repositories
	// they are built from, for images whose labels do not say.
	Sources map[string]string
	// Poll is how often it reads an open pull request, and a merged fix's
	// source.
	Poll    time.Duration
	Log     *slog.Logger
	OnReady func()
}

// Run watches Fixes and works on each, until ctx ends.
func (f *Fixer) Run(ctx context.Context) error {
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())
	defer queue.ShutDown()
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(f.Dynamic, 0, f.Namespace, nil)
	inf := factory.ForResource(FixesResource).Informer()
	enqueue := func(obj any) {
		if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
			queue.Add(key)
		}
	}
	if _, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{AddFunc: enqueue, UpdateFunc: func(_, obj any) { enqueue(obj) }}); err != nil {
		return err
	}
	factory.Start(ctx.Done())
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !cache.WaitForCacheSync(wait.Done(), inf.HasSynced) {
		return fmt.Errorf("fixes in %s did not sync within a minute: the Fixer may lack permission there", f.Namespace)
	}
	if f.OnReady != nil {
		f.OnReady()
	}
	f.Log.Info("fixer started", "namespace", f.Namespace, "forge", f.Forge.Web)
	go func() {
		<-ctx.Done()
		queue.ShutDown()
	}()
	for {
		key, quit := queue.Get()
		if quit {
			return nil
		}
		after, err := f.process(ctx, inf.GetStore(), key, queue.NumRequeues(key))
		switch {
		case err != nil:
			f.Log.Error("could not work on a fix", "fix", key, "error", err)
			queue.AddRateLimited(key)
		case after > 0:
			queue.Forget(key)
			queue.AddAfter(key, after)
		default:
			queue.Forget(key)
		}
		queue.Done(key)
	}
}

func (f *Fixer) process(ctx context.Context, store cache.Store, key string, tries int) (after time.Duration, err error) {
	obj, exists, err := store.GetByKey(key)
	if err != nil || !exists {
		return 0, err
	}
	var fix v1alpha1.Fix
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(*unstructured.Unstructured).Object, &fix); err != nil {
		return 0, err
	}
	poll := f.Poll
	if poll <= 0 {
		poll = 30 * time.Second
	}
	if f.awaitBuild(ctx, &fix, time.Now()) || f.reportVerdict(ctx, &fix) || f.codeClosed(ctx, &fix) {
		return poll, f.setStatus(ctx, &fix, fix.Status.Phase, fix.Status.Message)
	}
	// A code fix's build is looked for whatever phase the fix that pins
	// the image back has reached, Applied included.
	defer func() {
		cf := fix.Status.Code
		building := cf != nil && cf.Commit != "" && cf.Image == "" && !strings.HasPrefix(cf.Message, "no image")
		judging := cf != nil && cf.Image != "" && cf.Verdict == "" && len(f.LabNamespaces) > 0
		if err == nil && (building || judging) && (after == 0 || after > poll) {
			after = poll
		}
	}()
	switch fix.Status.Phase {
	case "", "Pending":
		phase, msg, err := f.propose(ctx, &fix)
		if err != nil {
			if tries < retries {
				return 0, err
			}
			phase, msg = PhaseFailed, err.Error()
		}
		f.Log.Info("fix "+strings.ToLower(string(phase)), "fix", key, "incident", fix.Spec.IncidentID, "pullRequest", fix.Status.PullRequest, "message", msg)
		return poll, f.setStatus(ctx, &fix, phase, msg)
	case PhaseProposed:
		if fix.Spec.CodeOnly {
			// Only the fix to the code is followed, above.
			return poll, nil
		}
		owner, repo, err := f.Forge.Repo(fix.Status.Repository)
		if err != nil {
			return 0, f.setStatus(ctx, &fix, PhaseFailed, err.Error())
		}
		pr, err := f.Forge.Get(ctx, owner, repo, int(fix.Status.Number))
		switch {
		case err != nil:
			return 0, err
		case !pr.Merged && pr.State == "open" && fix.Spec.Merge:
			granted, err := f.mergeGranted(ctx, fix.Spec.Workload.Namespace)
			switch {
			case err != nil:
				return 0, err
			case !granted:
				f.Log.Info("not merging a fix: the namespace's policy no longer grants fix.merge", "fix", key)
				return poll, nil
			}
			if err := f.Forge.Merge(ctx, owner, repo, int(fix.Status.Number)); err != nil {
				return 0, err
			}
			f.Log.Info("merged a fix as policy grants", "fix", key, "pullRequest", fix.Status.PullRequest)
			return time.Second, nil
		case pr.Merged:
			fix.Status.MergeCommit = pr.MergeCommit
			f.Log.Info("fix merged", "fix", key, "incident", fix.Spec.IncidentID, "commit", pr.MergeCommit)
			return poll, f.setStatus(ctx, &fix, PhaseMerged, "merged as "+pr.MergeCommit+"; waiting for "+fix.Status.Owner+"'s source to fetch it")
		case pr.State == "closed":
			f.Log.Info("fix declined", "fix", key, "incident", fix.Spec.IncidentID)
			return 0, f.setStatus(ctx, &fix, PhaseDeclined, "the pull request was closed without merging; "+fix.Status.Owner+" stays paused")
		}
		return poll, nil
	case PhaseMerged, PhaseReady:
		dep, err := f.Client.AppsV1().Deployments(fix.Spec.Workload.Namespace).Get(ctx, fix.Spec.Workload.Name, metav1.GetOptions{})
		if err != nil {
			return 0, err
		}
		src, err := SourceOf(ctx, f.Dynamic, dep.ObjectMeta, f.ArgoCDNamespace)
		if err != nil {
			return 0, err
		}
		if fix.Status.Phase == PhaseMerged {
			head, err := src.Revision(ctx)
			if err != nil || head == "" {
				return poll, err
			}
			if has, err := f.holds(ctx, &fix, head); err != nil || !has {
				return poll, err
			}
			f.Log.Info("fix ready", "fix", key, "incident", fix.Spec.IncidentID, "revision", head)
			return poll, f.setStatus(ctx, &fix, PhaseReady, fix.Status.Owner+"'s source has fetched the fix at "+head+
				"; resume it with `"+fix.Status.Resume+"`")
		}
		resumed, applied, err := src.Applied(ctx)
		if err != nil || !resumed || applied == "" {
			return poll, err
		}
		if has, err := f.holds(ctx, &fix, applied); err != nil || !has {
			return poll, err
		}
		f.Log.Info("fix applied", "fix", key, "incident", fix.Spec.IncidentID, "revision", applied)
		return 0, f.setStatus(ctx, &fix, PhaseApplied, fix.Status.Owner+" is resumed and has applied the fix at "+applied)
	}
	return 0, nil
}

// mergeGranted reports whether the namespace's policies grant fix.merge at
// autonomous. The controller decides whether a fix has earned it; the
// Fixer, which holds the forge's credential, checks the grant itself.
func (f *Fixer) mergeGranted(ctx context.Context, namespace string) (bool, error) {
	list, err := f.Dynamic.Resource(actor.PoliciesResource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	var policies []v1alpha1.SentinelPolicy
	for _, u := range list.Items {
		var p v1alpha1.SentinelPolicy
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &p); err == nil {
			policies = append(policies, p)
		}
	}
	return policy.Autonomy(policies, "fix.merge") == v1alpha1.AutonomyAutonomous, nil
}

// holds reports whether a commit of the fix's branch is the merge or comes
// after it.
func (f *Fixer) holds(ctx context.Context, fix *v1alpha1.Fix, commit string) (bool, error) {
	return Contains(ctx, fix.Status.Repository, fix.Status.Branch, commit, fix.Status.MergeCommit, historyDepth, f.auth())
}

func (f *Fixer) auth() *githttp.BasicAuth {
	return &githttp.BasicAuth{Username: f.GitUser, Password: f.Forge.Token}
}

// propose turns a Fix into a pull request. A refusal is a phase, Abstained,
// with the reason; an error is worth trying again.
func (f *Fixer) propose(ctx context.Context, fix *v1alpha1.Fix) (v1alpha1.FixPhase, string, error) {
	w := fix.Spec.Workload
	if w.Kind != "Deployment" {
		return PhaseAbstained, fmt.Sprintf("the Fixer restores Deployments only so far, not a %s", w.Kind), nil
	}
	if fix.Spec.CodeOnly {
		return f.proposeCode(ctx, fix)
	}
	dep, err := f.Client.AppsV1().Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	change, kept, err := ChangeFor(ctx, f.Client, dep, fix.Spec)
	if err != nil {
		return PhaseAbstained, err.Error(), nil
	}
	src, err := SourceOf(ctx, f.Dynamic, dep.ObjectMeta, f.ArgoCDNamespace)
	if err != nil {
		return PhaseAbstained, err.Error(), nil
	}
	fix.Status.Owner, fix.Status.Repository, fix.Status.Resume = src.Owner, src.URL, src.Resume
	owner, repo, err := f.Forge.Repo(src.URL)
	if err != nil {
		return PhaseAbstained, err.Error() + ", the only forge the Fixer may write to", nil
	}
	co, err := Clone(ctx, src.URL, src.Branch, f.auth())
	if err != nil {
		return "", "", err
	}
	fix.Status.Branch = co.Branch
	files, err := co.Files()
	if err != nil {
		return PhaseAbstained, err.Error(), nil
	}
	edit, err := Locate(files, src.Path, Target{Kind: "Deployment", Name: dep.Name, NamePrefix: src.NamePrefix, NameSuffix: src.NameSuffix}, change)
	if err != nil {
		return PhaseAbstained, err.Error(), nil
	}
	field := what(change.field())
	fix.Status.Change = []string{fmt.Sprintf("%s:%d: %s's %s %s -> %s", edit.Path, edit.Line, change.Container, field, shown(change.Bad), shown(change.Good))}
	edits := []codefix.Edit{{Path: edit.Path, After: edit.After}}
	if change.field() == fieldMemoryLimit {
		// Heap settings sized for the old limit must fit in the new one.
		for _, fc := range runtimeFlags(dep.Spec.Template.Spec, change.Container, fmt.Sprint(change.Good)) {
			last := edits[len(edits)-1]
			if err := files.WriteFile(path.Join("/", last.Path), last.After); err != nil {
				break
			}
			e, err := Locate(files, src.Path, Target{Kind: "Deployment", Name: dep.Name, NamePrefix: src.NamePrefix, NameSuffix: src.NameSuffix}, fc)
			if err != nil {
				f.Log.Info("a heap setting that no longer fits the new limit stays as it is", "fix", fix.Name, "setting", fc.Field, "reason", err)
				continue
			}
			edits = append(edits, codefix.Edit{Path: e.Path, After: e.After})
			fix.Status.Change = append(fix.Status.Change, fmt.Sprintf("%s:%d: %s's %s %s -> %s, to fit the new limit", e.Path, e.Line, fc.Container,
				what(fc.Field), shown(fc.Bad), shown(fc.Good)))
		}
	}
	title := fmt.Sprintf("Set %s's %s back to %s", change.Container, field, shown(change.Good))
	switch {
	case change.field() == fieldMemoryLimit:
		title = fmt.Sprintf("Set %s's memory limit to %s", change.Container, shown(change.Good))
	case change.Good == nil:
		title = fmt.Sprintf("Remove %s's %s", change.Container, field)
	}
	why := strings.ToUpper(edit.Why[:1]) + edit.Why[1:]
	message := fmt.Sprintf("%s\n\n%s. Sentinel rolled %s/%s back from it in incident %s.\n", title, why, w.Namespace, w.Name, fix.Spec.IncidentID)
	branch := "sentinel/fix-" + shortID(fix.Spec.IncidentID)
	if _, err := co.PushChange(ctx, &codefix.Change{Edits: edits}, branch, message, f.Author); err != nil {
		return "", "", err
	}
	pr, err := f.Forge.Open(ctx, owner, repo, branch, co.Branch, title, body(fix, src, edit, change, kept))
	if err != nil {
		return "", "", err
	}
	fix.Status.PullRequest, fix.Status.Number = pr.URL, int32(min(pr.Number, 1<<30)) //nolint:gosec // bounded just above
	fix.Status.Code = f.codeFix(ctx, fix, change)
	return PhaseProposed, edit.Why, nil
}

func body(fix *v1alpha1.Fix, src *Source, edit *Edit, change Change, kept []Change) string {
	w := fix.Spec.Workload
	field := what(change.field())
	code := func(v any) string { return "`" + shown(v) + "`" }
	back := fmt.Sprintf("%s's %s goes back to %s, as the workload ran before", change.Container, field, code(change.Good))
	switch {
	case change.field() == fieldMemoryLimit:
		back = fmt.Sprintf("%s's memory limit becomes %s, learned from what it used while healthy: putting back the old limit "+
			"can be as wrong as the new one, so this sets the least that held with room to spare", change.Container, code(change.Good))
	case change.Good == nil:
		back = fmt.Sprintf("%s's %s goes, as the workload ran before it was set", change.Container, field)
	}
	also := ""
	if change.field() == fieldMemoryLimit && fix.Spec.LabVerdict != "" {
		also += "\n- **The Lab**: it raced the workload's failing revision with this limit in a sealed copy: " + fix.Spec.LabVerdict + "."
	}
	if len(kept) > 0 {
		also += fmt.Sprintf("\n- **Left as it is**: the same revision also changed %s; a %s fault is not blamed on that, so it stays.",
			listed(kept), fix.Spec.FaultClass)
	}
	return fmt.Sprintf(`Sentinel rolled **%s/%s** back from the revision that set container %s's %s to %s, and paused the %s so the rollback holds.

This pull request makes the fix last: %s.

- **What changes**: %s, line %d.
- **Why there**: %s.%s

Once this is merged and the %s has fetched it, resume it with `+"`%s`"+`; Sentinel then checks that %s keeps running the revision it rolled back to.

Incident %s.
`, w.Namespace, w.Name, change.Container, field, code(change.Bad), src.Owner, back,
		"`"+edit.Path+"`", edit.Line, edit.Why, also, src.Owner, src.Resume, w.Name, fix.Spec.IncidentID)
}

func shortID(incident string) string {
	id := strings.ReplaceAll(incident, "-", "")
	return id[max(0, len(id)-12):]
}

func (f *Fixer) setStatus(ctx context.Context, fix *v1alpha1.Fix, phase v1alpha1.FixPhase, msg string) error {
	fix.Status.Phase, fix.Status.Message, fix.Status.ObservedGeneration = phase, msg, fix.Generation
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(fix)
	if err != nil {
		return err
	}
	_, err = f.Dynamic.Resource(FixesResource).Namespace(fix.Namespace).UpdateStatus(ctx, &unstructured.Unstructured{Object: obj},
		metav1.UpdateOptions{FieldManager: "sentinel-fixer"})
	return err
}
