// Package settings applies cluster-wide SentinelSettings to the ConfigMaps
// Helm installed, then rolls the pods that read them. The console writes the
// Settings object as the signed-in person; the controller alone rewrites its
// own ConfigMaps so operators need no rights on those.
package settings

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// Name is the one Settings object the chart seeds and the console edits.
const Name = "sentinel"

// Resource is the SentinelSettings GVR.
var Resource = v1alpha1.GroupVersion.WithResource("sentinelsettings")

// Targets names where Helm put each component's ConfigMap and workload.
type Targets struct {
	AgentNamespace   string
	ActorNamespace   string
	CaptureNamespace string
	ControllerCM     string
	ActorCM          string
	FixerCM          string
	LabCM            string
	CaptureCM        string
	ControllerDeploy string
	ActorDeploy      string
	FixerDeploy      string
	LabDeploy        string
	CaptureDaemonSet string
	CaptureMITM      string
}

// DefaultTargets are the chart's fixed names.
func DefaultTargets(agentNamespace string) Targets {
	return Targets{
		AgentNamespace:   agentNamespace,
		ActorNamespace:   "sentinel-actor",
		CaptureNamespace: "sentinel-capture",
		ControllerCM:     "sentinel-config",
		ActorCM:          "sentinel-actor-config",
		FixerCM:          "sentinel-fixer-config",
		LabCM:            "sentinel-lab-config",
		CaptureCM:        "sentinel-capture",
		ControllerDeploy: "sentinel",
		ActorDeploy:      "sentinel-actor",
		FixerDeploy:      "sentinel-fixer",
		LabDeploy:        "sentinel-lab",
		CaptureDaemonSet: "sentinel-capture",
		CaptureMITM:      "sentinel-capture-mitm",
	}
}

// Applied is what Apply changed and which components are installed.
type Applied struct {
	Installed v1alpha1.InstalledComponents
	Message   string
}

// Apply writes Spec into the installed ConfigMaps and bumps pod-template
// annotations so those pods reload. Missing components are skipped and
// reported on Installed. dyn may be nil; MITM inject sync then no-ops.
func Apply(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) (Applied, error) {
	return ApplyWithDynamic(ctx, client, nil, t, spec)
}

// ApplyWithDynamic is Apply plus MITM CA inject sync against SentinelPolicies.
func ApplyWithDynamic(ctx context.Context, client kubernetes.Interface, dyn dynamic.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) (Applied, error) {
	out := Applied{Installed: probe(ctx, client, t)}
	var parts []string
	if err := patchController(ctx, client, t, spec); err != nil {
		return out, err
	}
	parts = append(parts, "controller")
	if out.Installed.Actor {
		if err := patchActor(ctx, client, t, spec); err != nil {
			return out, err
		}
		parts = append(parts, "actor")
	}
	if out.Installed.Fixer {
		if err := patchFixer(ctx, client, t, spec); err != nil {
			return out, err
		}
		parts = append(parts, "fixer")
	}
	if out.Installed.Capture {
		if err := patchCapture(ctx, client, t, spec); err != nil {
			return out, err
		}
		parts = append(parts, "capture")
	}
	if out.Installed.CaptureMITM && spec.Capture.MitmEnabled != nil {
		replicas := int32(0)
		if *spec.Capture.MitmEnabled {
			replicas = 1
		}
		scale, err := client.AppsV1().Deployments(t.CaptureNamespace).GetScale(ctx, t.CaptureMITM, metav1.GetOptions{})
		if err != nil {
			return out, fmt.Errorf("capture mitm scale: %w", err)
		}
		if scale.Spec.Replicas != replicas {
			scale.Spec.Replicas = replicas
			if _, err := client.AppsV1().Deployments(t.CaptureNamespace).UpdateScale(ctx, t.CaptureMITM, scale, metav1.UpdateOptions{FieldManager: "sentinel-settings"}); err != nil {
				return out, fmt.Errorf("capture mitm scale: %w", err)
			}
		}
		parts = append(parts, "capture-mitm")
	}
	if out.Installed.CaptureMITM {
		if err := syncMITMInject(ctx, client, dyn, t, spec); err != nil {
			return out, err
		}
		if spec.Capture.MitmInject != nil {
			parts = append(parts, "capture-mitm-inject")
		}
	}
	if err := bounce(ctx, client, t, out.Installed, spec.LogLevel); err != nil {
		return out, err
	}
	out.Message = "applied to " + strings.Join(parts, ", ")
	return out, nil
}

func probe(ctx context.Context, client kubernetes.Interface, t Targets) v1alpha1.InstalledComponents {
	hasCM := func(ns, name string) bool {
		_, err := client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	hasDeploy := func(ns, name string) bool {
		_, err := client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	return v1alpha1.InstalledComponents{
		Actor:       hasCM(t.ActorNamespace, t.ActorCM),
		Lab:         hasCM(t.AgentNamespace, t.LabCM),
		Fixer:       hasCM(t.AgentNamespace, t.FixerCM),
		Capture:     hasCM(t.CaptureNamespace, t.CaptureCM),
		CaptureMITM: hasDeploy(t.CaptureNamespace, t.CaptureMITM),
		NodeAgent:   hasCM(t.AgentNamespace, "sentinel-node-agent-token") || hasCM("sentinel-node-agent", "sentinel-node-agent-token"),
	}
}

func patchController(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	cm, err := client.CoreV1().ConfigMaps(t.AgentNamespace).Get(ctx, t.ControllerCM, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("controller config: %w", err)
	}
	raw := cm.Data["config.yaml"]
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return fmt.Errorf("controller config: %w", err)
	}
	if spec.LogLevel != "" {
		// Log level is an env var on the Deployment; annotation bounce picks
		// SENTINEL_LOG_LEVEL up only after a chart change. Record it on the
		// ConfigMap for status and future readers.
		doc["logLevel"] = spec.LogLevel
	}
	if d := spec.Detection; d.FalseAlarmsPerWeek != nil || d.Window != nil || d.Tick != nil || d.HalfLife != nil || d.IncidentGap != nil || d.WarmUp != nil {
		det, _ := doc["detection"].(map[string]any)
		if det == nil {
			det = map[string]any{}
		}
		if d.FalseAlarmsPerWeek != nil {
			det["falseAlarmsPerWeek"] = float64(*d.FalseAlarmsPerWeek)
		}
		setDuration(det, "window", d.Window)
		setDuration(det, "tick", d.Tick)
		setDuration(det, "halfLife", d.HalfLife)
		setDuration(det, "incidentGap", d.IncidentGap)
		setDuration(det, "warmUp", d.WarmUp)
		doc["detection"] = det
	}
	if l := spec.Logs; l.Enabled != nil || l.SampleInterval != nil {
		logs, _ := doc["logs"].(map[string]any)
		if logs == nil {
			logs = map[string]any{}
		}
		if l.Enabled != nil {
			logs["enabled"] = *l.Enabled
		}
		setDuration(logs, "sampleInterval", l.SampleInterval)
		doc["logs"] = logs
	}
	if spec.Lab.VerifyRollbacks != nil {
		doc["verifyRollbacks"] = *spec.Lab.VerifyRollbacks
	}
	if spec.Fixer.Code.WithoutRollback != nil {
		doc["codeFixWithoutRollback"] = *spec.Fixer.Code.WithoutRollback
	}
	if c := spec.Capabilities; c.Interval != nil {
		cap, _ := doc["capabilities"].(map[string]any)
		if cap == nil {
			cap = map[string]any{}
		}
		setDuration(cap, "interval", c.Interval)
		doc["capabilities"] = cap
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data["config.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.AgentNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func patchActor(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	if spec.Actor.ActionsEnabled == nil && spec.Actor.PerHour == nil {
		return nil
	}
	cm, err := client.CoreV1().ConfigMaps(t.ActorNamespace).Get(ctx, t.ActorCM, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("actor config: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &doc); err != nil {
		return err
	}
	if spec.Actor.ActionsEnabled != nil {
		doc["enabled"] = *spec.Actor.ActionsEnabled
	}
	if spec.Actor.PerHour != nil {
		doc["perHour"] = int(*spec.Actor.PerHour)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	cm.Data["config.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.ActorNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func patchFixer(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	code := spec.Fixer.Code
	if code.WithoutRollback == nil && len(code.Fixers) == 0 && code.LLM == nil {
		return nil
	}
	cm, err := client.CoreV1().ConfigMaps(t.AgentNamespace).Get(ctx, t.FixerCM, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("fixer config: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &doc); err != nil {
		return err
	}
	block, _ := doc["code"].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	if len(code.Fixers) > 0 {
		fixers := make([]any, len(code.Fixers))
		for i, f := range code.Fixers {
			fixers[i] = f
		}
		block["fixers"] = fixers
	}
	if code.LLM != nil {
		if code.LLM.URL == "" {
			delete(block, "llm")
		} else {
			llm, _ := block["llm"].(map[string]any)
			if llm == nil {
				llm = map[string]any{}
			}
			if code.LLM.Format != "" {
				llm["format"] = code.LLM.Format
			}
			llm["url"] = code.LLM.URL
			if code.LLM.Model != "" {
				llm["model"] = code.LLM.Model
			}
			if code.LLM.MaxAttempts != nil {
				llm["maxAttempts"] = int(*code.LLM.MaxAttempts)
			}
			block["llm"] = llm
		}
	}
	doc["code"] = block
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	cm.Data["config.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.AgentNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func patchCapture(ctx context.Context, client kubernetes.Interface, t Targets, spec v1alpha1.SentinelSettingsSpec) error {
	if spec.Capture.ExportBodies == nil && spec.Capture.BodyBufferBytes == nil {
		return nil
	}
	cm, err := client.CoreV1().ConfigMaps(t.CaptureNamespace).Get(ctx, t.CaptureCM, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("capture config: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["obi.yaml"]), &doc); err != nil {
		return err
	}
	export := false
	if spec.Capture.ExportBodies != nil {
		export = *spec.Capture.ExportBodies
	} else {
		_, export = doc["ebpf"]
	}
	buf := int32(8192)
	if spec.Capture.BodyBufferBytes != nil {
		buf = *spec.Capture.BodyBufferBytes
	}
	if !export {
		delete(doc, "ebpf")
		delete(doc, "attributes")
	} else {
		doc["ebpf"] = map[string]any{
			"buffer_sizes": map[string]any{"http": int(buf)},
			"payload_extraction": map[string]any{
				"http": map[string]any{
					"enrichment": map[string]any{
						"enabled": true,
						"policy": map[string]any{
							"default_action": map[string]any{"headers": "exclude", "body": "exclude"},
						},
						"rules": []any{
							map[string]any{
								"action": "include", "type": "body", "scope": "request",
								"match": map[string]any{"methods": []any{"POST", "PUT", "PATCH", "DELETE"}, "url_path_patterns": []any{"*"}},
							},
							map[string]any{
								"action": "include", "type": "body", "scope": "response",
								"match": map[string]any{"methods": []any{"POST", "PUT", "PATCH", "DELETE"}, "url_path_patterns": []any{"*"}},
							},
						},
					},
				},
			},
		}
		doc["attributes"] = map[string]any{
			"select": map[string]any{
				"traces": map[string]any{
					"include": []any{"http.request.body.content", "http.response.body.content"},
				},
			},
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	cm.Data["obi.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.CaptureNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func bounce(ctx context.Context, client kubernetes.Interface, t Targets, inst v1alpha1.InstalledComponents, logLevel string) error {
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	ctrlPatch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/settings-applied":%q}}}}}`, stamp)
	if logLevel != "" {
		ctrlPatch = fmt.Sprintf(
			`{"spec":{"template":{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/settings-applied":%q}},"spec":{"containers":[{"name":"controller","env":[{"name":"SENTINEL_LOG_LEVEL","value":%q}]}]}}}}`,
			stamp, logLevel)
	}
	if _, err := client.AppsV1().Deployments(t.AgentNamespace).Patch(ctx, t.ControllerDeploy, types.StrategicMergePatchType, []byte(ctrlPatch), metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("restart controller: %w", err)
	}
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"sentinel.karimzakzouk.github.io/settings-applied":%q}}}}}`, stamp))
	if inst.Actor {
		if _, err := client.AppsV1().Deployments(t.ActorNamespace).Patch(ctx, t.ActorDeploy, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("restart actor: %w", err)
		}
	}
	if inst.Fixer {
		if _, err := client.AppsV1().Deployments(t.AgentNamespace).Patch(ctx, t.FixerDeploy, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("restart fixer: %w", err)
		}
	}
	if inst.Lab {
		if _, err := client.AppsV1().Deployments(t.AgentNamespace).Patch(ctx, t.LabDeploy, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("restart lab: %w", err)
		}
	}
	if inst.Capture {
		if _, err := client.AppsV1().DaemonSets(t.CaptureNamespace).Patch(ctx, t.CaptureDaemonSet, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("restart capture: %w", err)
		}
	}
	return nil
}

func setDuration(m map[string]any, key string, d *metav1.Duration) {
	if d != nil {
		m[key] = d.Duration.String()
	}
}
