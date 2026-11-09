//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/karimzakzouk/sentinel/internal/cli"
)

const gitopsNS = "e2e-gitops"

var applications = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}

// A bad image delivered through Argo CD, with automated sync and self-heal:
// a plain rollback would be undone at the next sync, so Sentinel pauses the
// Application's automated sync, rolls the Deployment back and verifies it,
// and Argo CD reports the Application out of sync instead of reverting.
// hack/e2e-up.sh installs Argo CD when E2E_ARGOCD is set.
func TestArgoCDRollbackPausesTheApplication(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1"); err != nil {
		t.Skip("Argo CD is not installed: run hack/e2e-up.sh with E2E_ARGOCD=1")
	}
	admin := dynamicAs(t, "")
	apps := admin.Resource(applications).Namespace("argocd")
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "e2e-guestbook", "namespace": "argocd"},
		"spec": map[string]any{
			"project": "default",
			"source": map[string]any{"repoURL": "https://github.com/argoproj/argocd-example-apps", "targetRevision": "master",
				"path": "kustomize-guestbook"},
			"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": gitopsNS},
			"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
		},
	}}
	if _, err := apps.Create(ctx, app, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = apps.Delete(ctx, "e2e-guestbook", metav1.DeleteOptions{})
	})
	healthy := func(want string) func() (bool, error) {
		return func() (bool, error) {
			u, err := apps.Get(ctx, "e2e-guestbook", metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			sync, _, _ := unstructured.NestedString(u.Object, "status", "sync", "status")
			health, _, _ := unstructured.NestedString(u.Object, "status", "health", "status")
			return sync+"/"+health == want, nil
		}
	}
	eventually(t, 5*time.Minute, "the guestbook synced and healthy", healthy("Synced/Healthy"))
	before, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "kustomize-guestbook-ui", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	image := before.Spec.Template.Spec.Containers[0].Image

	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-argocd", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "argocd.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-argocd", metav1.DeleteOptions{})
	})

	injected := time.Now()
	bad := `{"spec":{"source":{"kustomize":{"images":["gcr.io/google-samples/gb-frontend:does-not-exist"]}}}}`
	if _, err := apps.Patch(ctx, "e2e-guestbook", types.MergePatchType, []byte(bad), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	waitForLogEntry(t, client, injected, 3*time.Minute, "a request to roll back through Argo CD", func(e map[string]any) bool {
		return e["msg"] == "asked the actor for an action" && e["action"] == "argocd.rollback"
	})
	eventually(t, 3*time.Minute, "the Application paused and the guestbook back on its image", func() (bool, error) {
		u, err := apps.Get(ctx, "e2e-guestbook", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		_, automated, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated")
		d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, "kustomize-guestbook-ui", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return !automated && u.GetAnnotations()["sentinel.karimzakzouk.github.io/paused-sync"] != "" &&
			d.Spec.Template.Spec.Containers[0].Image == image && d.Status.AvailableReplicas == *d.Spec.Replicas, nil
	})
	eventually(t, time.Minute, "Argo CD reporting the drift instead of undoing it", healthy("OutOfSync/Healthy"))
	verified := waitForLogEntry(t, client, injected, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
		return e["msg"] == "action verified" || e["msg"] == "action unverified"
	})
	if verified["msg"] != "action verified" {
		t.Fatalf("the rollback was not verified: %v", verified)
	}
}

var applicationSets = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}

// An ApplicationSet writes the Applications it generates back to its
// template. One that rewrites their sync policy would restore the automated
// sync Sentinel pauses, so Sentinel leaves the rollback to people and says
// why; one that ignores differences in it gets the pause and the rollback
// any Application does. It drops Sentinel's note of the pause, so `sentinel
// resume` restores the sync its template sets.
func TestArgoCDApplicationSets(t *testing.T) {
	ctx := context.Background()
	client := clientAs(t, "")
	if _, err := client.Discovery().ServerResourcesForGroupVersion("argoproj.io/v1alpha1"); err != nil {
		t.Skip("Argo CD is not installed: run hack/e2e-up.sh with E2E_ARGOCD=1")
	}
	admin := dynamicAs(t, "")
	policies := schema.GroupVersionResource{Group: "sentinel.karimzakzouk.github.io", Version: "v1alpha1", Resource: "sentinelpolicies"}
	policy := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.karimzakzouk.github.io/v1alpha1", "kind": "SentinelPolicy",
		"metadata": map[string]any{"name": "e2e-argocd-sets", "namespace": gitopsNS},
		"spec":     map[string]any{"autonomy": []any{map[string]any{"action": "argocd.rollback", "level": "autonomous"}}},
	}}
	if _, err := admin.Resource(policies).Namespace(gitopsNS).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Resource(policies).Namespace(gitopsNS).Delete(ctx, "e2e-argocd-sets", metav1.DeleteOptions{})
	})

	for _, ignores := range []bool{false, true} {
		name := map[bool]string{false: "e2e-set-rewrites", true: "e2e-set-ignores"}[ignores]
		t.Run(name, func(t *testing.T) {
			sets, apps := admin.Resource(applicationSets).Namespace("argocd"), admin.Resource(applications).Namespace("argocd")
			spec := map[string]any{
				"generators": []any{map[string]any{"list": map[string]any{"elements": []any{map[string]any{"name": name}}}}},
				"template": map[string]any{
					// The finalizer has Argo CD delete what the Application deployed with it.
					"metadata": map[string]any{"name": name, "finalizers": []any{"resources-finalizer.argocd.argoproj.io"}},
					"spec": map[string]any{
						"project": "default",
						"source": map[string]any{"repoURL": "https://github.com/argoproj/argocd-example-apps", "targetRevision": "master",
							"path": "kustomize-guestbook", "kustomize": map[string]any{"namePrefix": name + "-"}},
						"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": gitopsNS},
						"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}},
					},
				},
			}
			if ignores {
				spec["ignoreApplicationDifferences"] = []any{map[string]any{"jsonPointers": []any{"/spec/syncPolicy"}}}
			}
			set := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
				"metadata": map[string]any{"name": name, "namespace": "argocd"}, "spec": spec,
			}}
			if _, err := sets.Create(ctx, set, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sets.Delete(ctx, name, metav1.DeleteOptions{}) })

			var deployment, image string
			eventually(t, 5*time.Minute, "the generated Application's guestbook available", func() (bool, error) {
				list, err := client.AppsV1().Deployments(gitopsNS).List(ctx, metav1.ListOptions{})
				if err != nil {
					return false, err
				}
				for _, d := range list.Items {
					if strings.HasPrefix(d.Annotations["argocd.argoproj.io/tracking-id"], name+":") {
						deployment, image = d.Name, d.Spec.Template.Spec.Containers[0].Image
						return d.Status.AvailableReplicas > 0 && d.Status.AvailableReplicas == d.Status.Replicas, nil
					}
				}
				return false, nil
			})

			injected := time.Now()
			bad := `{"spec":{"template":{"spec":{"source":{"kustomize":{"images":["gcr.io/google-samples/gb-frontend:does-not-exist"]}}}}}}`
			if _, err := sets.Patch(ctx, name, types.MergePatchType, []byte(bad), metav1.PatchOptions{}); err != nil {
				t.Fatal(err)
			}
			target := "Deployment.apps/" + gitopsNS + "/" + deployment
			waitForLogEntry(t, client, injected, 4*time.Minute, "a request to roll back through Argo CD", func(e map[string]any) bool {
				return e["msg"] == "asked the actor for an action" && e["action"] == "argocd.rollback" && e["target"] == target
			})
			if !ignores {
				refused := waitForLogEntry(t, client, injected, 2*time.Minute, "the actor leaving it to people", func(e map[string]any) bool {
					return e["msg"] == "the actor did not run the action; escalating"
				})
				if msg, _ := refused["message"].(string); !strings.Contains(msg, "the ApplicationSet argocd/"+name+" generates the Application") {
					t.Fatalf("refused for another reason: %v", refused)
				}
				u, err := apps.Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, automated, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated"); !automated {
					t.Fatal("Sentinel paused an Application its ApplicationSet would resume")
				}
				return
			}
			eventually(t, 3*time.Minute, "the Application paused and the guestbook back on its image", func() (bool, error) {
				u, err := apps.Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				_, automated, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated")
				d, err := client.AppsV1().Deployments(gitopsNS).Get(ctx, deployment, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				return !automated && d.Spec.Template.Spec.Containers[0].Image == image && d.Status.AvailableReplicas == *d.Spec.Replicas, nil
			})
			verified := waitForLogEntry(t, client, injected, 5*time.Minute, "the rollback verified", func(e map[string]any) bool {
				return e["msg"] == "action verified" || e["msg"] == "action unverified"
			})
			if verified["msg"] != "action verified" {
				t.Fatalf("the rollback was not verified: %v", verified)
			}
			// The ApplicationSet has reconciled the Application since: the pause
			// holds, though Sentinel's note, an annotation it does not set, is gone.
			u, err := apps.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, automated, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated"); automated {
				t.Fatal("the ApplicationSet undid the pause")
			}

			// Fixed at its source, the Application resumes as its template says.
			fixed := `{"spec":{"template":{"spec":{"source":{"kustomize":{"images":null}}}}}}`
			if _, err := sets.Patch(ctx, name, types.MergePatchType, []byte(fixed), metav1.PatchOptions{}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			resume := cli.NewRoot()
			resume.SetArgs([]string{"--context", flags("").Context, "resume", "argocd", "argocd/" + name})
			resume.SetOut(&out)
			if err := resume.ExecuteContext(ctx); err != nil || !strings.Contains(out.String(), "as the ApplicationSet argocd/"+name+" sets it") {
				t.Fatalf("sentinel resume: %q, %v", out.String(), err)
			}
			eventually(t, 3*time.Minute, "the Application syncing on its own again, synced and healthy", func() (bool, error) {
				u, err := apps.Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				automated, _, _ := unstructured.NestedMap(u.Object, "spec", "syncPolicy", "automated")
				sync, _, _ := unstructured.NestedString(u.Object, "status", "sync", "status")
				health, _, _ := unstructured.NestedString(u.Object, "status", "health", "status")
				return automated["selfHeal"] == true && sync+"/"+health == "Synced/Healthy", nil
			})
		})
	}
}
