package capability

import (
	"context"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Rule is one permission: a verb on a resource, in a namespace or cluster-wide.
type Rule struct {
	Verb        string
	Group       string
	Resource    string
	Subresource string
	Namespace   string
}

func (r Rule) String() string {
	res := r.Resource
	if r.Group != "" {
		res += "." + r.Group
	}
	if r.Subresource != "" {
		res += "/" + r.Subresource
	}
	where := "cluster-wide"
	if r.Namespace != "" {
		where = "in " + r.Namespace
	}
	return r.Verb + " " + res + " " + where
}

func (r Rule) attributes() *authorizationv1.ResourceAttributes {
	return &authorizationv1.ResourceAttributes{
		Namespace: r.Namespace, Verb: r.Verb, Group: r.Group, Resource: r.Resource, Subresource: r.Subresource,
	}
}

// WatcherRules are the read permissions the watcher needs in one namespace.
func WatcherRules(ns string) []Rule {
	var rules []Rule
	for _, res := range []struct{ group, resource string }{
		{"", "pods"}, {"", "events"}, {"", "configmaps"}, {"", "services"},
		{"apps", "deployments"}, {"apps", "replicasets"}, {"apps", "statefulsets"}, {"apps", "daemonsets"},
		{"discovery.k8s.io", "endpointslices"},
	} {
		for _, verb := range []string{"list", "watch"} {
			rules = append(rules, Rule{Verb: verb, Group: res.group, Resource: res.resource, Namespace: ns})
		}
	}
	return rules
}

// LogRules are the permissions log evidence needs in one namespace. Without
// them the watcher still works from Kubernetes facts alone.
func LogRules(ns string) []Rule {
	return []Rule{{Verb: "get", Resource: "pods", Subresource: "log", Namespace: ns}}
}

// ActorRules are the write permissions the actor needs in one namespace. The
// actor only patches existing workloads: creating one would implicitly grant
// every Secret and ServiceAccount in the namespace.
func ActorRules(ns string) []Rule {
	return []Rule{
		{Verb: "patch", Group: "apps", Resource: "deployments", Namespace: ns},
		{Verb: "patch", Group: "apps", Resource: "statefulsets", Namespace: ns},
		{Verb: "patch", Group: "apps", Resource: "deployments", Subresource: "scale", Namespace: ns},
		{Verb: "patch", Group: "apps", Resource: "statefulsets", Subresource: "scale", Namespace: ns},
	}
}

// ForbiddenRules are permissions Sentinel's identities must never hold. Each
// one would let a compromised Sentinel read credentials, run code in other
// workloads, or raise its own privileges.
func ForbiddenRules(namespaces []string) []Rule {
	rules := []Rule{
		{Verb: "list", Resource: "secrets"},
		{Verb: "get", Resource: "nodes", Subresource: "proxy"},
		{Verb: "escalate", Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
		{Verb: "bind", Group: "rbac.authorization.k8s.io", Resource: "clusterroles"},
		{Verb: "impersonate", Resource: "users"},
		{Verb: "impersonate", Resource: "serviceaccounts"},
		{Verb: "create", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicies"},
		{Verb: "delete", Group: "admissionregistration.k8s.io", Resource: "validatingadmissionpolicybindings"},
		{Verb: "create", Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
	}
	for _, ns := range namespaces {
		rules = append(rules,
			Rule{Verb: "get", Resource: "secrets", Namespace: ns},
			Rule{Verb: "list", Resource: "secrets", Namespace: ns},
			Rule{Verb: "create", Resource: "pods", Subresource: "exec", Namespace: ns},
			Rule{Verb: "create", Resource: "serviceaccounts", Subresource: "token", Namespace: ns},
			Rule{Verb: "create", Group: "apps", Resource: "deployments", Namespace: ns},
			Rule{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "rolebindings", Namespace: ns},
		)
	}
	return rules
}

// selfCan asks the API server whether the caller may perform the rule.
func (d *Detector) selfCan(ctx context.Context, r Rule) (bool, error) {
	res, err := d.Client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
		&authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: r.attributes()}},
		metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

// userCan asks whether another identity may perform the rule.
func (d *Detector) userCan(ctx context.Context, user string, groups []string, r Rule) (bool, error) {
	res, err := d.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx,
		&authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
			User: user, Groups: groups, ResourceAttributes: r.attributes(),
		}}, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	return res.Status.Allowed, nil
}

type accessResult struct {
	missing []Rule
	err     error
}

func (d *Detector) checkAll(ctx context.Context, rules []Rule, can func(context.Context, Rule) (bool, error)) accessResult {
	var res accessResult
	for _, r := range rules {
		ok, err := can(ctx, r)
		if err != nil {
			return accessResult{err: fmt.Errorf("%s: %w", r, err)}
		}
		if !ok {
			res.missing = append(res.missing, r)
		}
	}
	return res
}

func (d *Detector) checkWatcherAccess(ctx context.Context, rep *Report) (watchable, loggable []string) {
	for _, ns := range d.Options.Namespaces {
		res := d.checkAll(ctx, WatcherRules(ns), d.selfCan)
		id := "access.watch/" + ns
		switch {
		case res.err != nil:
			rep.add(id, Unknown, "could not check permissions: %v", res.err)
		case len(res.missing) > 0:
			rep.add(id, Unavailable, "missing: %s", joinRules(res.missing))
		default:
			rep.add(id, Available, "can read pods, events and workloads")
			watchable = append(watchable, ns)
		}
		logs := d.checkAll(ctx, LogRules(ns), d.selfCan)
		id = "access.logs/" + ns
		switch {
		case logs.err != nil:
			rep.add(id, Unknown, "could not check permissions: %v", logs.err)
		case len(logs.missing) > 0:
			rep.add(id, Unavailable, "missing: %s", joinRules(logs.missing))
		default:
			rep.add(id, Available, "can read container logs")
			loggable = append(loggable, ns)
		}
	}
	nodes := d.checkAll(ctx, []Rule{{Verb: "list", Resource: "nodes"}, {Verb: "watch", Resource: "nodes"}}, d.selfCan)
	switch {
	case nodes.err != nil:
		rep.add("access.nodes", Unknown, "could not check permissions: %v", nodes.err)
	case len(nodes.missing) > 0:
		rep.add("access.nodes", Unavailable, "missing: %s", joinRules(nodes.missing))
	default:
		rep.add("access.nodes", Available, "can read nodes")
	}
	return watchable, loggable
}

func (d *Detector) checkLeastPrivilege(ctx context.Context, rep *Report) {
	var excess []Rule
	for _, r := range ForbiddenRules(d.Options.Namespaces) {
		ok, err := d.selfCan(ctx, r)
		if err != nil {
			rep.add("security.least-privilege", Unknown, "could not check %s: %v", r, err)
			return
		}
		if ok {
			excess = append(excess, r)
		}
	}
	if len(excess) == 0 {
		rep.add("security.least-privilege", Available, "holds none of the %d forbidden permissions", len(ForbiddenRules(d.Options.Namespaces)))
		return
	}
	rep.add("security.least-privilege", Unavailable, "holds %d permission(s) it must not have", len(excess))
	for _, r := range excess {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s can %s; Sentinel must not hold this permission, remove it", rep.Identity, r))
	}
}

func (d *Detector) checkActorAccess(ctx context.Context, rep *Report) (actable []string) {
	if d.Options.ActorUser == "" {
		rep.add("access.act", Unknown, "no actor identity configured")
		return nil
	}
	can := func(ctx context.Context, r Rule) (bool, error) {
		return d.userCan(ctx, d.Options.ActorUser, d.Options.ActorGroups, r)
	}
	for _, ns := range d.Options.Namespaces {
		res := d.checkAll(ctx, ActorRules(ns), can)
		id := "access.act/" + ns
		switch {
		case res.err != nil:
			rep.add(id, Unknown, "could not check %s: %v", d.Options.ActorUser, res.err)
		case len(res.missing) > 0:
			rep.add(id, Unavailable, "%s is missing: %s", d.Options.ActorUser, joinRules(res.missing))
		default:
			rep.add(id, Available, "%s can patch workloads and their scale", d.Options.ActorUser)
			actable = append(actable, ns)
		}
	}
	return actable
}

func joinRules(rules []Rule) string {
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = r.String()
	}
	return strings.Join(parts, "; ")
}
