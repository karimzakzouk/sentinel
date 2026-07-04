// Package safeactions implements L2 remediation actions: restart, rollback, scale.
// These run immediately (no PR, no merge wait) when the policy allows them.
// The LLM selects the action + generates parameters; the action itself is
// deterministic Go code — not free-form kubectl.
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

type ActionType string

const (
        ActionRestart  ActionType = "restart"
        ActionRollback ActionType = "rollback"
        ActionScale    ActionType = "scale"
)

type ActionTier int

const (
        TierAuto      ActionTier = 1 // auto-allowed if namespace is in allow-list
        TierApproval  ActionTier = 2 // requires /sentinel approve within 5 min
        TierNever     ActionTier = 3 // never autonomous
)

type Config struct {
        Enabled           bool
        AllowedNamespaces []string
        MaxActionsPerHour int
        MaxScaleDeltaPct  int // max % change for auto-scale (default 50)
}

func DefaultConfig() Config {
        return Config{
                Enabled:           false,
                MaxActionsPerHour: 3,
                MaxScaleDeltaPct:  50,
        }
}

type Controller struct {
        cfg      Config
        clientset *kubernetes.Clientset
        mu       sync.Mutex
        actionLog []time.Time // sliding window for rate limiting
}

func New(clientset *kubernetes.Clientset, cfg Config) *Controller {
        return &Controller{
                cfg:       cfg,
                clientset: clientset,
        }
}

type Result struct {
        Action    ActionType `json:"action"`
        Success   bool       `json:"success"`
        Detail    string     `json:"detail"`
        Namespace string     `json:"namespace"`
        Service   string     `json:"service"`
}

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

        _, err = c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
        if err != nil {
                return Result{Action: ActionRestart, Success: false, Detail: fmt.Sprintf("restart deployment: %v", err), Namespace: namespace, Service: service}
        }

        logging.Info("L2 safe-action: restarted deployment", "namespace", namespace, "service", service)
        return Result{Action: ActionRestart, Success: true, Detail: "deployment restarted", Namespace: namespace, Service: service}
}

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
                        if prevRS == nil || rs.CreationTimestamp.Time.After(prevRS.CreationTimestamp.Time) {
                                prevRS = rs
                        }
                }
        }
        if prevRS == nil {
                return Result{Action: ActionRollback, Success: false, Detail: "no previous revision to rollback to", Namespace: namespace, Service: service}
        }

        // Rollback by setting the deployment's template to the previous ReplicaSet's template
        deploy.Spec.Template = prevRS.Spec.Template
        _, err = c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
        if err != nil {
                return Result{Action: ActionRollback, Success: false, Detail: fmt.Sprintf("rollback deployment: %v", err), Namespace: namespace, Service: service}
        }

        logging.Info("L2 safe-action: rolled back deployment", "namespace", namespace, "service", service, "from_revision", currentRevision, "to_revision", prevRS.Annotations["deployment.kubernetes.io/revision"])
        return Result{Action: ActionRollback, Success: true, Detail: fmt.Sprintf("rolled back to revision %s", prevRS.Annotations["deployment.kubernetes.io/revision"]), Namespace: namespace, Service: service}
}

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

        // Check blast radius: max % change
        if currentReplicas > 0 {
                deltaPct := int(float64(targetReplicas-currentReplicas) / float64(currentReplicas) * 100)
                if deltaPct < 0 {
                        deltaPct = -deltaPct
                }
                if deltaPct > c.cfg.MaxScaleDeltaPct {
                        return Result{Action: ActionScale, Success: false, Detail: fmt.Sprintf("scale delta %d%% exceeds max %d%% (requires approval)", deltaPct, c.cfg.MaxScaleDeltaPct), Namespace: namespace, Service: service}
                }
        }

        deploy.Spec.Replicas = &targetReplicas
        _, err = c.clientset.AppsV1().Deployments(namespace).Update(ctx, deploy, metav1.UpdateOptions{})
        if err != nil {
                return Result{Action: ActionScale, Success: false, Detail: fmt.Sprintf("scale deployment: %v", err), Namespace: namespace, Service: service}
        }

        logging.Info("L2 safe-action: scaled deployment", "namespace", namespace, "service", service, "from", currentReplicas, "to", targetReplicas)
        return Result{Action: ActionScale, Success: true, Detail: fmt.Sprintf("scaled from %d to %d replicas", currentReplicas, targetReplicas), Namespace: namespace, Service: service}
}

func (c *Controller) findDeployment(ctx context.Context, namespace, service string) (*appsv1.Deployment, error) {
        // Try exact name match first
        deploy, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{})
        if err == nil {
                return deploy, nil
        }

        // Fall back to label selector
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

func AvailableActions() []string {
        return []string{"restart", "rollback", "scale <N>"}
}
