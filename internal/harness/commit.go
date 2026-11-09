package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if err := in.Forge.put(ctx, c.Repository, branch, c.Path, strings.Replace(content, c.Old, c.New, 1), sha, message); err != nil {
		return fmt.Errorf("commit %s: %w", c.Path, err)
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
	var did []string
	fix, err := in.awaitFix(ctx, run, "Proposed", "Merged", "Ready", "Abstained", "Failed")
	switch {
	case err != nil:
		return err
	case fix == nil:
		did = append(did, "Sentinel proposed no fix")
	case fix.Status.Phase == "Abstained" || fix.Status.Phase == "Failed":
		did = append(did, "Sentinel's fix "+strings.ToLower(string(fix.Status.Phase))+": "+fix.Status.Message)
	default:
		if fix.Status.Phase == "Proposed" {
			if err := in.Forge.merge(ctx, was.Repository, int(fix.Status.Number)); err != nil {
				return fmt.Errorf("merge Sentinel's fix: %w", err)
			}
			did = append(did, fmt.Sprintf("merged Sentinel's fix #%d", fix.Status.Number))
		} else {
			did = append(did, fmt.Sprintf("Sentinel merged its fix #%d itself", fix.Status.Number))
		}
		if err := in.refresh(ctx, run.Target); err != nil {
			return err
		}
		if ready, err := in.awaitFix(ctx, run, "Ready"); err != nil {
			return err
		} else if ready == nil {
			did = append(did, "it did not reach the owner's source")
			break
		}
		said, err := in.resumeOwner(ctx, run.Target)
		if err != nil {
			return err
		}
		did = append(did, said)
		if err := in.refresh(ctx, run.Target); err != nil {
			return err
		}
		if applied, err := in.awaitFix(ctx, run, "Applied"); err != nil {
			return err
		} else if applied == nil {
			did = append(did, "the owner did not apply it")
		}
	}
	content, sha, err := in.Forge.file(ctx, was.Repository, was.Branch, was.Path)
	if err != nil {
		return err
	}
	if content != was.Content {
		if err := in.Forge.put(ctx, was.Repository, was.Branch, was.Path, was.Content, sha, "Put "+was.Path+" back as it was"); err != nil {
			return err
		}
		did = append(did, "put "+was.Path+" back")
	}
	if said, err := in.resumeOwner(ctx, run.Target); err == nil {
		did = append(did, said)
	} else if !strings.Contains(err.Error(), "carries no note") {
		return err
	}
	if err := in.refresh(ctx, run.Target); err != nil {
		return err
	}
	run.Reverted = strings.Join(did, "; ")
	t := in.now()
	run.RevertedAt = &t
	return nil
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
