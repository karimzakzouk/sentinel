package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Run is one injection and its recorded ground truth.
type Run struct {
	RunID        string      `json:"runId"`
	Scenario     string      `json:"scenario"`
	App          string      `json:"app"`
	FaultKind    FaultKind   `json:"faultKind"`
	Target       Target      `json:"target,omitempty"`
	InjectedAt   time.Time   `json:"injectedAt"`
	ManifestedAt *time.Time  `json:"manifestedAt,omitempty"`
	RevertedAt   *time.Time  `json:"revertedAt,omitempty"`
	GroundTruth  GroundTruth `json:"groundTruth"`
	Observation  string      `json:"observation,omitempty"`
	// Before is the target's spec before injection, used to revert it.
	Before json.RawMessage `json:"before,omitempty"`
	// DeletedPods lists the pods a delete-pods fault removed.
	DeletedPods []string `json:"deletedPods,omitempty"`
}

// Quiet reports a run during which Sentinel should raise nothing: a control,
// or a fault that heals itself, whose mitigation is to do nothing.
func (r *Run) Quiet() bool {
	return r.FaultKind == FaultNone || r.GroundTruth.FaultClass == "none" || r.GroundTruth.Mitigation == "none"
}

// Injector applies scenarios to a cluster.
type Injector struct {
	Client  kubernetes.Interface
	Dynamic dynamic.Interface
	Mapper  meta.RESTMapper
	Now     func() time.Time
	// Poll is the wait between manifestation checks.
	Poll time.Duration
}

func (in *Injector) now() time.Time {
	if in.Now != nil {
		return in.Now().UTC()
	}
	return time.Now().UTC()
}

// Inject applies the scenario's fault and, when the scenario names a
// condition, waits until the fault visibly takes effect.
func (in *Injector) Inject(ctx context.Context, s *Scenario) (*Run, error) {
	run := &Run{
		RunID: string(model.NewID()), Scenario: s.ID, App: s.App, FaultKind: s.Fault.Kind,
		Target: s.Fault.Target, GroundTruth: s.GroundTruth,
	}
	switch s.Fault.Kind {
	case FaultPatch:
		if err := in.patch(ctx, s, run); err != nil {
			return nil, err
		}
	case FaultDeletePods:
		if err := in.deletePods(ctx, s, run); err != nil {
			return nil, err
		}
	case FaultNone:
		run.InjectedAt = in.now()
		run.Observation = "control: nothing injected"
		return run, nil
	default:
		return nil, fmt.Errorf("unknown fault kind %q", s.Fault.Kind)
	}
	if s.Manifest.Condition == ConditionNone {
		return run, nil
	}
	if err := in.waitFor(ctx, s.Manifest, run); err != nil {
		run.Observation = err.Error()
	}
	return run, nil
}

func (in *Injector) resource(t Target) (dynamic.ResourceInterface, error) {
	gv, err := schema.ParseGroupVersion(t.APIVersion)
	if err != nil {
		return nil, err
	}
	m, err := in.Mapper.RESTMapping(gv.WithKind(t.Kind).GroupKind(), gv.Version)
	if err != nil {
		return nil, fmt.Errorf("map %s %s: %w", t.APIVersion, t.Kind, err)
	}
	return in.Dynamic.Resource(m.Resource).Namespace(t.Namespace), nil
}

func (in *Injector) patch(ctx context.Context, s *Scenario, run *Run) error {
	res, err := in.resource(s.Fault.Target)
	if err != nil {
		return err
	}
	obj, err := res.Get(ctx, s.Fault.Target.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get target: %w", err)
	}
	spec, found, err := unstructured.NestedFieldCopy(obj.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("target %s has no spec to restore later", s.Fault.Target.Name)
	}
	if run.Before, err = json.Marshal(spec); err != nil {
		return err
	}
	pt := map[string]types.PatchType{"strategic": types.StrategicMergePatchType, "merge": types.MergePatchType, "json": types.JSONPatchType}[s.Fault.Patch.Type]
	run.InjectedAt = in.now()
	if _, err := res.Patch(ctx, s.Fault.Target.Name, pt, []byte(s.Fault.Patch.Content), metav1.PatchOptions{FieldManager: "sentinel-harness"}); err != nil {
		return fmt.Errorf("apply patch: %w", err)
	}
	return nil
}

func (in *Injector) deletePods(ctx context.Context, s *Scenario, run *Run) error {
	pods, err := in.Client.CoreV1().Pods(s.Fault.Target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: s.Fault.Selector})
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods match %q in %s", s.Fault.Selector, s.Fault.Target.Namespace)
	}
	run.InjectedAt = in.now()
	for _, p := range pods.Items {
		if err := in.Client.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil {
			return fmt.Errorf("delete pod %s: %w", p.Name, err)
		}
		run.DeletedPods = append(run.DeletedPods, p.Name)
	}
	return nil
}

func (in *Injector) waitFor(ctx context.Context, m Manifestation, run *Run) error {
	poll := in.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	deadline := time.Now().Add(m.Timeout.Duration)
	for {
		pods, err := in.Client.CoreV1().Pods(m.Namespace).List(ctx, metav1.ListOptions{LabelSelector: m.Selector})
		if err != nil {
			return err
		}
		for i := range pods.Items {
			if hit, detail := matches(m.Condition, &pods.Items[i]); hit {
				t := in.now()
				run.ManifestedAt = &t
				run.Observation = detail
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fault did not manifest as %s within %s", m.Condition, m.Timeout.Duration)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// matches reports whether a pod shows the condition, with a description.
func matches(c Condition, p *corev1.Pod) (bool, string) {
	for _, cs := range p.Status.ContainerStatuses {
		w, last := cs.State.Waiting, cs.LastTerminationState.Terminated
		switch c {
		case ConditionImagePullError:
			if w != nil && (w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff") {
				return true, fmt.Sprintf("pod %s container %s: %s", p.Name, cs.Name, w.Reason)
			}
		case ConditionCrashLoop:
			if w != nil && w.Reason == "CrashLoopBackOff" {
				return true, fmt.Sprintf("pod %s container %s: CrashLoopBackOff after %d restarts", p.Name, cs.Name, cs.RestartCount)
			}
			// Recent Kubernetes versions show a container waiting out its
			// back-off as terminated, and CrashLoopBackOff only briefly.
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 && cs.RestartCount > 0 {
				return true, fmt.Sprintf("pod %s container %s: exited with code %d after %d restarts", p.Name, cs.Name, t.ExitCode, cs.RestartCount)
			}
			if last != nil && last.ExitCode != 0 && cs.RestartCount >= 2 {
				return true, fmt.Sprintf("pod %s container %s: restarted %d times, last exit code %d", p.Name, cs.Name, cs.RestartCount, last.ExitCode)
			}
		case ConditionOOMKilled:
			if last != nil && last.Reason == "OOMKilled" {
				return true, fmt.Sprintf("pod %s container %s: OOMKilled", p.Name, cs.Name)
			}
		}
	}
	if c == ConditionNotReady && p.Status.Phase == corev1.PodRunning {
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionFalse {
				return true, fmt.Sprintf("pod %s is running but not ready: %s", p.Name, cond.Message)
			}
		}
	}
	return false, ""
}

// Revert restores a patched target's spec. Deleted pods are recreated by their
// controllers, and controls need nothing.
func (in *Injector) Revert(ctx context.Context, run *Run) error {
	if run.FaultKind != FaultPatch {
		t := in.now()
		run.RevertedAt = &t
		return nil
	}
	res, err := in.resource(run.Target)
	if err != nil {
		return err
	}
	var spec map[string]any
	if err := json.Unmarshal(run.Before, &spec); err != nil {
		return fmt.Errorf("decode saved spec: %w", err)
	}
	obj, err := res.Get(ctx, run.Target.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := unstructured.SetNestedField(obj.Object, spec, "spec"); err != nil {
		return err
	}
	if _, err := res.Update(ctx, obj, metav1.UpdateOptions{FieldManager: "sentinel-harness"}); err != nil {
		return fmt.Errorf("restore spec: %w", err)
	}
	t := in.now()
	run.RevertedAt = &t
	return nil
}
