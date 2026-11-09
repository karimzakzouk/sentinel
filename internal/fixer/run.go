package fixer

import (
	"context"
	"fmt"
	"log/slog"
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
	"github.com/karimzakzouk/sentinel/internal/forge"
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

func (f *Fixer) process(ctx context.Context, store cache.Store, key string, tries int) (time.Duration, error) {
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
		owner, repo, err := f.Forge.Repo(fix.Status.Repository)
		if err != nil {
			return 0, f.setStatus(ctx, &fix, PhaseFailed, err.Error())
		}
		pr, err := f.Forge.Get(ctx, owner, repo, int(fix.Status.Number))
		switch {
		case err != nil:
			return 0, err
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
	dep, err := f.Client.AppsV1().Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	change, err := ImageChange(ctx, f.Client, dep, fix.Spec.FromRevision)
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
	fix.Status.Change = []string{fmt.Sprintf("%s:%d: %s's image %s -> %s", edit.Path, edit.Line, change.Container, change.Bad, change.Good)}
	title := fmt.Sprintf("Set %s's image back to %s", change.Container, change.Good)
	why := strings.ToUpper(edit.Why[:1]) + edit.Why[1:]
	message := fmt.Sprintf("%s\n\n%s. Sentinel rolled %s/%s back from it in incident %s.\n", title, why, w.Namespace, w.Name, fix.Spec.IncidentID)
	branch := "sentinel/fix-" + shortID(fix.Spec.IncidentID)
	if _, err := co.Push(ctx, edit, branch, message, f.Author); err != nil {
		return "", "", err
	}
	pr, err := f.Forge.Open(ctx, owner, repo, branch, co.Branch, title, body(fix, src, edit, change))
	if err != nil {
		return "", "", err
	}
	fix.Status.PullRequest, fix.Status.Number = pr.URL, int32(min(pr.Number, 1<<30)) //nolint:gosec // bounded just above
	return PhaseProposed, edit.Why, nil
}

func body(fix *v1alpha1.Fix, src *Source, edit *Edit, change Change) string {
	w := fix.Spec.Workload
	return fmt.Sprintf(`Sentinel rolled **%s/%s** back from a revision whose container %s could not run as %s, and paused the %s so the rollback holds.

This pull request makes the fix last: %s's image goes back to %s, the image the workload ran before.

- **What changes**: %s, line %d.
- **Why there**: %s.

Once this is merged and the %s has fetched it, resume it with `+"`%s`"+`; Sentinel then checks that %s keeps running %s.

Incident %s.
`, w.Namespace, w.Name, change.Container, "`"+change.Bad+"`", src.Owner, change.Container, "`"+change.Good+"`",
		"`"+edit.Path+"`", edit.Line, edit.Why, src.Owner, src.Resume, w.Name, "`"+change.Good+"`", fix.Spec.IncidentID)
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
