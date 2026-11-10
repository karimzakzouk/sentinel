package settings

import (
	"context"
	"fmt"
	"slices"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// WatchedLabel marks namespaces the fence treats as in scope.
const WatchedLabel = "sentinel.karimzakzouk.github.io/watched"

const watchAnnKey = "sentinel.karimzakzouk.github.io/watch-namespaces"

// SyncWatch applies the watch allowlist: labels namespaces, RoleBindings for
// the watch (and act) ClusterRoles, and the namespaces list on controller and
// actor ConfigMaps.
func SyncWatch(ctx context.Context, client kubernetes.Interface, t Targets, release string, want []string, actorInstalled bool) error {
	if release == "" {
		release = "sentinel"
	}
	want = cleanNamespaces(want)
	if err := denyOwnNamespaces(want, t); err != nil {
		return err
	}
	prev, err := previousWatch(ctx, client, t)
	if err != nil {
		return err
	}
	if err := syncNamespaceLabels(ctx, client, prev, want); err != nil {
		return err
	}
	if err := syncRoleBindings(ctx, client, t, release, prev, want, actorInstalled); err != nil {
		return err
	}
	if err := patchControllerNamespaces(ctx, client, t, want); err != nil {
		return err
	}
	if actorInstalled {
		if err := patchActorNamespaces(ctx, client, t, want); err != nil {
			return err
		}
	}
	return rememberWatch(ctx, client, t, want)
}

func cleanNamespaces(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, ns := range in {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	slices.Sort(out)
	return out
}

func denyOwnNamespaces(want []string, t Targets) error {
	blocked := map[string]struct{}{
		t.AgentNamespace:   {},
		t.ActorNamespace:   {},
		t.CaptureNamespace: {},
		"kube-system":      {},
		"kube-public":      {},
		"kube-node-lease":  {},
	}
	for _, ns := range want {
		if _, bad := blocked[ns]; bad {
			return fmt.Errorf("namespace %q cannot be watched (system or Sentinel's own)", ns)
		}
	}
	return nil
}

func previousWatch(ctx context.Context, client kubernetes.Interface, t Targets) ([]string, error) {
	cm, err := client.CoreV1().ConfigMaps(t.AgentNamespace).Get(ctx, t.ControllerCM, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if cm.Annotations != nil {
		if raw := cm.Annotations[watchAnnKey]; raw != "" {
			return cleanNamespaces(strings.Split(raw, ",")), nil
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &doc); err != nil {
		return nil, nil
	}
	switch v := doc["namespaces"].(type) {
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return cleanNamespaces(out), nil
	case []string:
		return cleanNamespaces(v), nil
	default:
		return nil, nil
	}
}

func rememberWatch(ctx context.Context, client kubernetes.Interface, t Targets, want []string) error {
	cm, err := client.CoreV1().ConfigMaps(t.AgentNamespace).Get(ctx, t.ControllerCM, metav1.GetOptions{})
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[watchAnnKey] = strings.Join(want, ",")
	_, err = client.CoreV1().ConfigMaps(t.AgentNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func syncNamespaceLabels(ctx context.Context, client kubernetes.Interface, prev, want []string) error {
	wantSet := toSet(want)
	for _, name := range prev {
		if _, keep := wantSet[name]; keep {
			continue
		}
		ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if ns.Labels == nil || ns.Labels[WatchedLabel] == "" {
			continue
		}
		ns = ns.DeepCopy()
		delete(ns.Labels, WatchedLabel)
		if _, err := client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{FieldManager: "sentinel-settings"}); err != nil {
			return fmt.Errorf("unlabel %s: %w", name, err)
		}
	}
	for _, name := range want {
		ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("watch namespace %s: %w", name, err)
		}
		if ns.Labels != nil && ns.Labels[WatchedLabel] == "true" {
			continue
		}
		ns = ns.DeepCopy()
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels[WatchedLabel] = "true"
		if _, err := client.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{FieldManager: "sentinel-settings"}); err != nil {
			return fmt.Errorf("label %s: %w", name, err)
		}
	}
	return nil
}

func syncRoleBindings(ctx context.Context, client kubernetes.Interface, t Targets, release string, prev, want []string, actorInstalled bool) error {
	wantSet := toSet(want)
	kinds := []struct {
		binding, role string
		sa            rbacv1.Subject
	}{
		{release + "-watch", release + "-watch", rbacv1.Subject{Kind: "ServiceAccount", Name: "sentinel", Namespace: t.AgentNamespace}},
	}
	if actorInstalled {
		kinds = append(kinds, struct {
			binding, role string
			sa            rbacv1.Subject
		}{
			release + "-act", release + "-act",
			rbacv1.Subject{Kind: "ServiceAccount", Name: "sentinel-actor", Namespace: t.ActorNamespace},
		})
	}
	for _, k := range kinds {
		for _, ns := range prev {
			if _, keep := wantSet[ns]; keep {
				continue
			}
			if err := client.RbacV1().RoleBindings(ns).Delete(ctx, k.binding, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete RoleBinding %s/%s: %w", ns, k.binding, err)
			}
		}
		for _, ns := range want {
			if err := ensureRoleBinding(ctx, client, ns, k.binding, k.role, k.sa); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureRoleBinding(ctx context.Context, client kubernetes.Interface, ns, name, clusterRole string, sa rbacv1.Subject) error {
	want := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/part-of":            "sentinel",
				"app.kubernetes.io/name":               name,
				"sentinel.karimzakzouk.github.io/rbac": name,
			},
		},
		RoleRef:  rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: clusterRole},
		Subjects: []rbacv1.Subject{sa},
	}
	cur, err := client.RbacV1().RoleBindings(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().RoleBindings(ns).Create(ctx, want, metav1.CreateOptions{FieldManager: "sentinel-settings"})
		return err
	}
	if err != nil {
		return err
	}
	cur = cur.DeepCopy()
	cur.RoleRef = want.RoleRef
	cur.Subjects = want.Subjects
	if cur.Labels == nil {
		cur.Labels = map[string]string{}
	}
	for k, v := range want.Labels {
		cur.Labels[k] = v
	}
	_, err = client.RbacV1().RoleBindings(ns).Update(ctx, cur, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func patchControllerNamespaces(ctx context.Context, client kubernetes.Interface, t Targets, want []string) error {
	cm, err := client.CoreV1().ConfigMaps(t.AgentNamespace).Get(ctx, t.ControllerCM, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &doc); err != nil {
		return err
	}
	doc["namespaces"] = want
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	cm.Data["config.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.AgentNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func patchActorNamespaces(ctx context.Context, client kubernetes.Interface, t Targets, want []string) error {
	cm, err := client.CoreV1().ConfigMaps(t.ActorNamespace).Get(ctx, t.ActorCM, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &doc); err != nil {
		return err
	}
	doc["namespaces"] = want
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	cm = cm.DeepCopy()
	cm.Data["config.yaml"] = string(out)
	_, err = client.CoreV1().ConfigMaps(t.ActorNamespace).Update(ctx, cm, metav1.UpdateOptions{FieldManager: "sentinel-settings"})
	return err
}

func toSet(in []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range in {
		out[s] = struct{}{}
	}
	return out
}

// WatchFromSpec returns cleaned namespaces from Settings.
func WatchFromSpec(spec v1alpha1.SentinelSettingsSpec) []string {
	return cleanNamespaces(spec.Watch.Namespaces)
}
