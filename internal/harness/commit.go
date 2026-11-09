package harness

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/resume"
)

var (
	gitRepositoriesResource = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	fixesResource           = v1alpha1.GroupVersion.WithResource("fixes")
)

// commit injects a commit fault: the person who owns the repository
// replaces the scenario's old text with the new in one file, and asks the
// owner to fetch it and apply it now.
func (in *Injector) commit(ctx context.Context, s *Scenario, run *Run) error {
	if in.Forge == nil {
		return errors.New("a commit fault needs the forge: give the harness --forge-api and --forge-token-secret")
	}
	c := s.Fault.Commit
	branch := c.Branch
	if branch == "" {
		branch = "main"
	}
	content, sha, err := in.Forge.file(ctx, c.Repository, branch, c.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", c.Path, err)
	}
	if !strings.Contains(content, c.Old) {
		return fmt.Errorf("%s in %s does not contain %q", c.Path, c.Repository, c.Old)
	}
	if run.Before, err = json.Marshal(committed{Repository: c.Repository, Branch: branch, Path: c.Path, Content: content}); err != nil {
		return err
	}
	message := c.Message
	if message == "" {
		message = s.Title
	}
	run.InjectedAt = in.now()
	if _, err := in.Forge.put(ctx, c.Repository, branch, c.Path, strings.Replace(content, c.Old, c.New, 1), sha, message); err != nil {
		return fmt.Errorf("commit %s: %w", c.Path, err)
	}
	return in.refresh(ctx, s.Fault.Target)
}

// released is a release fault's files as they were.
type released struct {
	Source committed `json:"source"`
	Deploy committed `json:"deploy"`
}

// release injects a release fault: the person commits the code change to
// the source repository, the build command builds that commit as the
// source's pipeline would, and the person moves the deploy file's image
// tag to the build and asks the owner to apply it now. The run starts
// with the deploy commit, which is what changes the cluster.
func (in *Injector) release(ctx context.Context, s *Scenario, run *Run) error {
	if in.Forge == nil || in.Build == nil {
		return errors.New("a release fault needs the forge and a build command: give the harness --forge-api and --build")
	}
	r := s.Fault.Release
	src := committed{Repository: r.Source.Repository, Branch: cmp.Or(r.Source.Branch, "main"), Path: r.Source.Path}
	dep := committed{Repository: r.Deploy.Repository, Branch: cmp.Or(r.Deploy.Branch, "main"), Path: r.Deploy.Path}
	var srcSHA, depSHA string
	var err error
	if src.Content, srcSHA, err = in.Forge.file(ctx, src.Repository, src.Branch, src.Path); err != nil {
		return fmt.Errorf("read %s: %w", src.Path, err)
	}
	if !strings.Contains(src.Content, r.Source.Old) {
		return fmt.Errorf("%s in %s does not contain %q", src.Path, src.Repository, r.Source.Old)
	}
	if dep.Content, depSHA, err = in.Forge.file(ctx, dep.Repository, dep.Branch, dep.Path); err != nil {
		return fmt.Errorf("read %s: %w", dep.Path, err)
	}
	current := regexp.MustCompile(regexp.QuoteMeta(r.Image) + `:[\w][\w.-]*`).FindString(dep.Content)
	if current == "" {
		return fmt.Errorf("%s in %s names no tag of %s", dep.Path, dep.Repository, r.Image)
	}
	if run.Before, err = json.Marshal(released{Source: src, Deploy: dep}); err != nil {
		return err
	}
	rev, err := in.Forge.put(ctx, src.Repository, src.Branch, src.Path, strings.Replace(src.Content, r.Source.Old, r.Source.New, 1), srcSHA,
		cmp.Or(r.Source.Message, s.Title))
	switch {
	case err != nil:
		return fmt.Errorf("commit %s: %w", src.Path, err)
	case rev == "":
		return fmt.Errorf("the forge did not say which commit it made of %s", src.Path)
	}
	tag, err := in.Build(ctx, src.Repository, rev)
	if err != nil {
		return fmt.Errorf("build %s at %s: %w", src.Repository, rev, err)
	}
	run.InjectedAt = in.now()
	if _, err := in.Forge.put(ctx, dep.Repository, dep.Branch, dep.Path, strings.Replace(dep.Content, current, r.Image+":"+tag, 1), depSHA,
		"Release "+tag); err != nil {
		return fmt.Errorf("commit %s: %w", dep.Path, err)
	}
	return in.refresh(ctx, s.Fault.Target)
}

// refresh asks a GitOps owner to fetch its source and apply it now, rather
// than at its next interval.
func (in *Injector) refresh(ctx context.Context, owner Target) error {
	switch owner.Kind {
	case "Kustomization":
		ks := in.Dynamic.Resource(actor.KustomizationsResource).Namespace(owner.Namespace)
		k, err := ks.Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		src, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "name")
		srcNS, _, _ := unstructured.NestedString(k.Object, "spec", "sourceRef", "namespace")
		if srcNS == "" {
			srcNS = owner.Namespace
		}
		patch := []byte(`{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"` + in.now().Format(time.RFC3339Nano) + `"}}}`)
		if _, err := in.Dynamic.Resource(gitRepositoriesResource).Namespace(srcNS).Patch(ctx, src, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("refresh the GitRepository %s/%s: %w", srcNS, src, err)
		}
		_, err = ks.Patch(ctx, owner.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	case "Application":
		patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
		_, err := in.Dynamic.Resource(actor.ApplicationsResource).Namespace(owner.Namespace).Patch(ctx, owner.Name, types.MergePatchType, patch, metav1.PatchOptions{})
		return err
	}
	return fmt.Errorf("no way to refresh a %s", owner.Kind)
}

// revertCommit undoes a commit fault as the person who owns the repository
// would. If Sentinel proposed a fix, the person merges it, waits for it to
// reach the owner's source, resumes the owner as Sentinel says and waits for
// the owner to apply it. Whatever happened, the file ends as it was and the
// owner resumed, so the next run starts clean.
func (in *Injector) revertCommit(ctx context.Context, run *Run) error {
	if in.Forge == nil {
		return errors.New("reverting a commit fault needs the forge: give the harness --forge-api, as hack/run-scenarios.sh does")
	}
	var was committed
	if err := json.Unmarshal(run.Before, &was); err != nil {
		return fmt.Errorf("decode the file as it was: %w", err)
	}
	did, err := in.revertFile(ctx, run, was)
	if err != nil {
		return err
	}
	run.Reverted = strings.Join(did, "; ")
	t := in.now()
	run.RevertedAt = &t
	return nil
}

// revertRelease undoes a release fault: the deploy file as a commit
// fault's, then the source file put back. Sentinel's fix to the code, if
// it proposed one, stays open for a person to read.
func (in *Injector) revertRelease(ctx context.Context, run *Run) error {
	if in.Forge == nil {
		return errors.New("reverting a release fault needs the forge: give the harness --forge-api, as hack/run-scenarios.sh does")
	}
	var was released
	if err := json.Unmarshal(run.Before, &was); err != nil {
		return fmt.Errorf("decode the files as they were: %w", err)
	}
	did, err := in.revertFile(ctx, run, was.Deploy)
	if err != nil {
		return err
	}
	content, sha, err := in.Forge.file(ctx, was.Source.Repository, was.Source.Branch, was.Source.Path)
	if err != nil {
		return err
	}
	if content != was.Source.Content {
		if _, err := in.Forge.put(ctx, was.Source.Repository, was.Source.Branch, was.Source.Path, was.Source.Content, sha,
			"Put "+was.Source.Path+" back as it was"); err != nil {
			return err
		}
		did = append(did, "put "+was.Source.Path+" back in "+was.Source.Repository)
	}
	run.Reverted = strings.Join(did, "; ")
	t := in.now()
	run.RevertedAt = &t
	return nil
}

// revertFile puts one file a GitOps owner deploys from back as it was, as
// the person who owns it would. If Sentinel proposed a fix, the person
// merges it, waits for it to reach the owner's source, resumes the owner
// as Sentinel says and waits for the owner to apply it. Whatever happened,
// the file ends as it was and the owner resumed, so the next run starts
// clean. It returns what the person did.
func (in *Injector) revertFile(ctx context.Context, run *Run, was committed) ([]string, error) {
	var did []string
	fix, err := in.awaitFix(ctx, run, "Proposed", "Merged", "Ready", "Abstained", "Failed")
	switch {
	case err != nil:
		return nil, err
	case fix == nil:
		did = append(did, "Sentinel proposed no fix")
	case fix.Status.Phase == "Abstained" || fix.Status.Phase == "Failed":
		did = append(did, "Sentinel's fix "+strings.ToLower(string(fix.Status.Phase))+": "+fix.Status.Message)
	default:
		if fix.Status.Phase == "Proposed" {
			if err := in.Forge.merge(ctx, was.Repository, int(fix.Status.Number)); err != nil {
				return nil, fmt.Errorf("merge Sentinel's fix: %w", err)
			}
			did = append(did, fmt.Sprintf("merged Sentinel's fix #%d", fix.Status.Number))
		} else {
			did = append(did, fmt.Sprintf("Sentinel merged its fix #%d itself", fix.Status.Number))
		}
		if err := in.refresh(ctx, run.Target); err != nil {
			return nil, err
		}
		if ready, err := in.awaitFix(ctx, run, "Ready"); err != nil {
			return nil, err
		} else if ready == nil {
			did = append(did, "it did not reach the owner's source")
			break
		}
		said, err := in.resumeOwner(ctx, run.Target)
		if err != nil {
			return nil, err
		}
		did = append(did, said)
		if err := in.refresh(ctx, run.Target); err != nil {
			return nil, err
		}
		if applied, err := in.awaitFix(ctx, run, "Applied"); err != nil {
			return nil, err
		} else if applied == nil {
			did = append(did, "the owner did not apply it")
		}
	}
	content, sha, err := in.Forge.file(ctx, was.Repository, was.Branch, was.Path)
	if err != nil {
		return nil, err
	}
	if content != was.Content {
		if _, err := in.Forge.put(ctx, was.Repository, was.Branch, was.Path, was.Content, sha, "Put "+was.Path+" back as it was"); err != nil {
			return nil, err
		}
		did = append(did, "put "+was.Path+" back")
	}
	if said, err := in.resumeOwner(ctx, run.Target); err == nil {
		did = append(did, said)
	} else if !strings.Contains(err.Error(), "carries no note") {
		return nil, err
	}
	if err := in.refresh(ctx, run.Target); err != nil {
		return nil, err
	}
	return did, nil
}

// resumeOwner resumes the owner as a person does with sentinel resume.
func (in *Injector) resumeOwner(ctx context.Context, owner Target) (string, error) {
	if owner.Kind == "Application" {
		return resume.ArgoCD(ctx, in.Dynamic, owner.Namespace, owner.Name)
	}
	return resume.Flux(ctx, in.Dynamic, actor.KustomizationsResource, "Kustomization", owner.Namespace, owner.Name)
}

// awaitFix waits for Sentinel's fix for a run's root cause, asked for since
// the run began, to reach one of the phases, and returns it; nil when none
// did within FixWait.
func (in *Injector) awaitFix(ctx context.Context, run *Run, phases ...v1alpha1.FixPhase) (*v1alpha1.Fix, error) {
	wait, poll := in.FixWait, in.Poll
	if wait == 0 {
		wait = 5 * time.Minute
	}
	if poll == 0 {
		poll = 2 * time.Second
	}
	ns := in.SentinelNamespace
	if ns == "" {
		ns = "sentinel"
	}
	root := run.GroundTruth.RootCause
	deadline := time.Now().Add(wait)
	for {
		list, err := in.Dynamic.Resource(fixesResource).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, u := range list.Items {
			var f v1alpha1.Fix
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &f); err != nil {
				return nil, err
			}
			w := f.Spec.Workload
			if w.Namespace == root.Namespace && w.Name == root.Name && !f.CreationTimestamp.Time.Before(run.InjectedAt.Add(-time.Second)) &&
				slices.Contains(phases, f.Status.Phase) {
				return &f, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
