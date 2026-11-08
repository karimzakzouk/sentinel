// Package harness injects faults into a test cluster and records the ground
// truth of each injection. Ground truth is written outside the cluster, so the
// system under test can never read it.
package harness

import (
	"errors"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Scenario is one injectable fault, or a no-fault control, with its ground truth.
type Scenario struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	App         string        `json:"app"`
	Fault       Fault         `json:"fault"`
	GroundTruth GroundTruth   `json:"groundTruth"`
	Manifest    Manifestation `json:"manifest,omitempty"`
}

// FaultKind is how a fault is injected.
type FaultKind string

const (
	// FaultPatch patches one object, such as a Deployment's image or limits.
	FaultPatch FaultKind = "patch"
	// FaultDeletePods deletes the pods matching a selector.
	FaultDeletePods FaultKind = "delete-pods"
	// FaultNone injects nothing. Controls measure false alarms.
	FaultNone FaultKind = "none"
)

// Fault describes the injection.
type Fault struct {
	Kind     FaultKind `json:"kind"`
	Target   Target    `json:"target,omitempty"`
	Patch    *Patch    `json:"patch,omitempty"`
	Selector string    `json:"selector,omitempty"`
}

// Target names a Kubernetes object.
type Target struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Patch is a patch body and its type: strategic, merge or json.
type Patch struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// GroundTruth is what a correct diagnosis and mitigation would say.
type GroundTruth struct {
	FaultClass string   `json:"faultClass"`
	RootCause  Target   `json:"rootCause,omitempty"`
	ChangeType string   `json:"changeType"`
	Symptoms   []string `json:"symptoms,omitempty"`
	Mitigation string   `json:"mitigation"`
}

// Condition is an observable effect that shows a fault took hold.
type Condition string

const (
	ConditionNone           Condition = ""
	ConditionImagePullError Condition = "image-pull-error"
	ConditionCrashLoop      Condition = "crash-loop"
	ConditionOOMKilled      Condition = "oom-killed"
	ConditionNotReady       Condition = "not-ready"
)

// Manifestation says how to confirm the fault took effect.
type Manifestation struct {
	Condition Condition       `json:"condition,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Selector  string          `json:"selector,omitempty"`
	Timeout   metav1.Duration `json:"timeout,omitempty"`
}

// LoadScenario reads and validates a scenario file. Unknown fields are
// rejected so a typo cannot silently change what is injected.
func LoadScenario(path string) (*Scenario, error) {
	b, err := os.ReadFile(path) //nolint:gosec // scenario paths come from the operator
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := yaml.UnmarshalStrict(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Manifest.Timeout.Duration == 0 {
		s.Manifest.Timeout.Duration = 3 * time.Minute
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("invalid scenario %s: %w", path, err)
	}
	return &s, nil
}

// Validate reports every problem at once.
func (s *Scenario) Validate() error {
	var errs []error
	if s.ID == "" || s.App == "" {
		errs = append(errs, errors.New("id and app are required"))
	}
	if s.GroundTruth.FaultClass == "" || s.GroundTruth.Mitigation == "" {
		errs = append(errs, errors.New("groundTruth.faultClass and groundTruth.mitigation are required"))
	}
	f := s.Fault
	switch f.Kind {
	case FaultPatch:
		if f.Target.Kind == "" || f.Target.Name == "" || f.Target.Namespace == "" || f.Target.APIVersion == "" {
			errs = append(errs, errors.New("a patch fault needs a full target"))
		}
		if f.Patch == nil || f.Patch.Content == "" {
			errs = append(errs, errors.New("a patch fault needs patch content"))
		} else if f.Patch.Type != "strategic" && f.Patch.Type != "merge" && f.Patch.Type != "json" {
			errs = append(errs, fmt.Errorf("unknown patch type %q", f.Patch.Type))
		}
	case FaultDeletePods:
		if f.Selector == "" || f.Target.Namespace == "" {
			errs = append(errs, errors.New("a delete-pods fault needs a selector and target.namespace"))
		}
	case FaultNone:
		if s.Manifest.Condition != ConditionNone {
			errs = append(errs, errors.New("a control scenario cannot expect a manifestation"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown fault kind %q", f.Kind))
	}
	switch s.Manifest.Condition {
	case ConditionNone:
	case ConditionImagePullError, ConditionCrashLoop, ConditionOOMKilled, ConditionNotReady:
		if s.Manifest.Selector == "" || s.Manifest.Namespace == "" {
			errs = append(errs, errors.New("a manifestation needs a namespace and selector"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown manifestation condition %q", s.Manifest.Condition))
	}
	return errors.Join(errs...)
}
