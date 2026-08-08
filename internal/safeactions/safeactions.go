package safeactions

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ActionType is the kind of L2 remediation action.
type ActionType string

const (
	ActionRestart  ActionType = "restart"
	ActionRollback ActionType = "rollback"
	ActionScale    ActionType = "scale"
)

// ActionTier classifies how autonomous an action may be. TierAuto is the
// only tier the controller acts on autonomously; the other tiers are
// surfaced to callers (Slack bot, API) for routing.
type ActionTier int

const (
	TierAuto     ActionTier = 1
	TierApproval ActionTier = 2
	TierNever    ActionTier = 3
)

// Config controls the L2 controller. AllowedNamespaces is the
// namespace allowlist; MaxActionsPerHour is the sliding-window rate limit;
// MaxScaleDeltaPct caps the percent change in replicas for Scale.
type Config struct {
	Enabled           bool
	AllowedNamespaces []string
	MaxActionsPerHour int
	MaxScaleDeltaPct  int
}

// DefaultConfig returns the built-in defaults. The controller is disabled
// by default — operators opt in via Config.Enabled.
func DefaultConfig() Config {
	return Config{
		Enabled:           false,
		MaxActionsPerHour: 3,
		MaxScaleDeltaPct:  50,
	}
}

// Controller applies L2 remediation actions through the K8s API. Every
// action is gated by the namespace allowlist and the rate limit.
type Controller struct {
	cfg       Config
	clientset kubernetes.Interface

	mu        sync.Mutex
	actionLog []time.Time
}

// New returns a Controller. clientset may be a *kubernetes.Clientset in
// production or a fake.NewSimpleClientset in tests.
func New(clientset kubernetes.Interface, cfg Config) *Controller {
	return &Controller{
		cfg:       cfg,
		clientset: clientset,
	}
}

// Result is the outcome of an L2 action. Detail carries either a
// human-readable success summary or the failure reason.
type Result struct {
	Action    ActionType `json:"action"`
	Success   bool       `json:"success"`
	Detail    string     `json:"detail"`
	Namespace string     `json:"namespace"`
	Service   string     `json:"service"`
}

// IsAllowed reports whether namespace is in the allowlist AND the controller
// is enabled.
func (c *Controller) IsAllowed(namespace string) bool {
	if !c.cfg.Enabled {
		return false
	}
	for _, ns := range c.cfg.AllowedNamespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}

// rateLimited records an action attempt and reports whether the rate limit
// is now exceeded. Returns true when the action should be rejected.
func (c *Controller) rateLimited() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	var recent []time.Time
	for _, t := range c.actionLog {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	c.actionLog = recent
	if len(recent) >= c.cfg.MaxActionsPerHour {
		return true
	}
	c.actionLog = append(c.actionLog, now)
	return false
}

// Restart triggers a rollout restart on the deployment matching service in
// namespace by patching the kubectl.kubernetes.io/restartedAt annotation.
func (c *Controller) Restart(ctx context.Context, namespace, service string) Result {
	if !c.IsAllowed(namespace) {
		return Result{Action: ActionRestart, Success: false, Detail: "namespace not in safe-actions allow-list", Namespace: namespace, Service: service}
	}
	if c.rateLimited() {
		return Result{Action: ActionRestart, Success: false, Detail: "rate limit exceeded", Namespace: namespace, Service: service}
	}

	deploy, err := c.findDeployment(ctx, namespace, service)
	if err != nil {
		return Result{Action: ActionRestart, Success: false, Detail: fmt.Sprintf("find deployment: %v", err), Namespace: namespace, Service: service}
	}

	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return Result{Action: ActionRestart, Success: false, Detail: fmt.Sprintf("restart deployment: %v", err), Namespace: namespace, Service: service}
	}

	logging.Info("safeactions: restarted deployment", "namespace", namespace, "service", service)
	return Result{Action: ActionRestart, Success: true, Detail: "deployment restarted", Namespace: namespace, Service: service}
}

// Rollback rolls the deployment back to the most recent prior ReplicaSet
// revision by copying that ReplicaSet's pod template onto the Deployment.
func (c *Controller) Rollback(ctx context.Context, namespace, service string) Result {
	if !c.IsAllowed(namespace) {
		return Result{Action: ActionRollback, Success: false, Detail: "namespace not in safe-actions allow-list", Namespace: namespace, Service: service}
	}
	if c.rateLimited() {
		return Result{Action: ActionRollback, Success: false, Detail: "rate limit exceeded", Namespace: namespace, Service: service}
	}

	deploy, err := c.findDeployment(ctx, namespace, service)
	if err != nil {
		return Result{Action: ActionRollback, Success: false, Detail: fmt.Sprintf("find deployment: %v", err), Namespace: namespace, Service: service}
	}

	replicaSets, err := c.clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", service),
	})
	if err != nil {
		return Result{Action: ActionRollback, Success: false, Detail: fmt.Sprintf("list replicasets: %v", err), Namespace: namespace, Service: service}
	}

	var prevRS *appsv1.ReplicaSet
	currentRevision := deploy.Annotations["deployment.kubernetes.io/revision"]
	for i := range replicaSets.Items {
		rs := &replicaSets.Items[i]
		rev := rs.Annotations["deployment.kubernetes.io/revision"]
		if rev != currentRevision && rev != "" {
			if prevRS == nil || rs.CreationTimestamp.After(prevRS.CreationTimestamp.Time) {
				prevRS = rs
			}
		}
	}
	if prevRS == nil {
		return Result{Action: ActionRollback, Success: false, Detail: "no previous revision to rollback to", Namespace: namespace, Service: service}
	}

	deploy.Spec.Template = prevRS.Spec.Template
	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return Result{Action: ActionRollback, Success: false, Detail: fmt.Sprintf("rollback deployment: %v", err), Namespace: namespace, Service: service}
	}

	logging.Info("safeactions: rolled back deployment",
		"namespace", namespace, "service", service,
		"from_revision", currentRevision,
		"to_revision", prevRS.Annotations["deployment.kubernetes.io/revision"])
	return Result{Action: ActionRollback, Success: true,
		Detail:    fmt.Sprintf("rolled back to revision %s", prevRS.Annotations["deployment.kubernetes.io/revision"]),
		Namespace: namespace, Service: service}
}

// Scale patches the deployment's replica count to targetReplicas. The
// percent change is bounded by Config.MaxScaleDeltaPct.
func (c *Controller) Scale(ctx context.Context, namespace, service string, targetReplicas int32) Result {
	if !c.IsAllowed(namespace) {
		return Result{Action: ActionScale, Success: false, Detail: "namespace not in safe-actions allow-list", Namespace: namespace, Service: service}
	}
	if c.rateLimited() {
		return Result{Action: ActionScale, Success: false, Detail: "rate limit exceeded", Namespace: namespace, Service: service}
	}

	deploy, err := c.findDeployment(ctx, namespace, service)
	if err != nil {
		return Result{Action: ActionScale, Success: false, Detail: fmt.Sprintf("find deployment: %v", err), Namespace: namespace, Service: service}
	}

	currentReplicas := int32(1)
	if deploy.Spec.Replicas != nil {
		currentReplicas = *deploy.Spec.Replicas
	}

	if currentReplicas > 0 {
		deltaPct := int(float64(targetReplicas-currentReplicas) / float64(currentReplicas) * 100)
		if deltaPct < 0 {
			deltaPct = -deltaPct
		}
		if deltaPct > c.cfg.MaxScaleDeltaPct {
			return Result{Action: ActionScale, Success: false,
				Detail:    fmt.Sprintf("scale delta %d%% exceeds max %d%% (requires approval)", deltaPct, c.cfg.MaxScaleDeltaPct),
				Namespace: namespace, Service: service}
		}
	}

	deploy.Spec.Replicas = &targetReplicas
	if _, err := c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{}); err != nil {
		return Result{Action: ActionScale, Success: false, Detail: fmt.Sprintf("scale deployment: %v", err), Namespace: namespace, Service: service}
	}

	logging.Info("safeactions: scaled deployment",
		"namespace", namespace, "service", service,
		"from", currentReplicas, "to", targetReplicas)
	return Result{Action: ActionScale, Success: true,
		Detail:    fmt.Sprintf("scaled from %d to %d replicas", currentReplicas, targetReplicas),
		Namespace: namespace, Service: service}
}

// findDeployment resolves the deployment backing service. Tries an exact
// name match first, then falls back to the label selector app=<service>.
func (c *Controller) findDeployment(ctx context.Context, namespace, service string) (*appsv1.Deployment, error) {
	deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{})
	if err == nil {
		return deploy, nil
	}

	deploys, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", service),
	})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	if len(deploys.Items) == 0 {
		return nil, fmt.Errorf("no deployment found for service %s in namespace %s", service, namespace)
	}
	return &deploys.Items[0], nil
}

// ParseAction parses a free-form action string like "scale 5" into an
// ActionType and (for Scale) a replica count.
func ParseAction(input string) (ActionType, int32, error) {
	parts := strings.Fields(strings.TrimSpace(input))
	if len(parts) == 0 {
		return "", 0, fmt.Errorf("empty action")
	}
	switch strings.ToLower(parts[0]) {
	case "restart":
		return ActionRestart, 0, nil
	case "rollback":
		return ActionRollback, 0, nil
	case "scale":
		if len(parts) < 2 {
			return "", 0, fmt.Errorf("scale requires a replica count: 'scale 3'")
		}
		var replicas int32
		if _, err := fmt.Sscanf(parts[1], "%d", &replicas); err != nil {
			return "", 0, fmt.Errorf("invalid replica count: %s", parts[1])
		}
		return ActionScale, replicas, nil
	default:
		return "", 0, fmt.Errorf("unknown action: %s (valid: restart, rollback, scale <N>)", parts[0])
	}
}

// AvailableActions returns the list of action keywords for help text.
func AvailableActions() []string {
	return []string{"restart", "rollback", "scale <N>"}
}
