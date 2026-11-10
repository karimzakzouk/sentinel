package lab

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/karimzakzouk/sentinel/internal/model"
)

func ref(kind, name string) model.ObjectRef {
	return model.ObjectRef{Kind: kind, Namespace: "shop", Name: name}
}

func service(name string, selector map[string]string) corev1.Service {
	return corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec: corev1.ServiceSpec{Selector: selector, ClusterIP: "10.96.0.9",
			Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080), NodePort: 30080}}}}
}

func workload(kind, name string, replicas int32, c corev1.Container) Workload {
	return Workload{Ref: ref(kind, name), Replicas: replicas, Template: corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{c}},
	}}
}

// shop is a front that calls a backend, an external API and a database it
// does not depend on, with everything the Lab must strip or replace.
func shop() World {
	front := workload("Deployment", "front", 3, corev1.Container{Name: "front", Image: "shop/front:v2",
		Env: []corev1.EnvVar{
			{Name: "BACKEND", Value: "backend:8080"},
			{Name: "PAYMENTS", Value: "https://api.stripe.com/v1"},
			{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "front-secrets"}, Key: "token"}}},
			{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "front-config"}, Key: "mode"}}},
		},
		EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "front-env"}}}},
	})
	front.Template.Spec.Containers = append(front.Template.Spec.Containers, corev1.Container{Name: "istio-proxy", Image: "istio/proxyv2"})
	front.Template.Spec.InitContainers = []corev1.Container{{Name: "istio-init", Image: "istio/proxyv2"}}
	front.Template.Spec.ServiceAccountName = "front"
	front.Template.Spec.PriorityClassName = "critical"
	front.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry"}}
	front.Template.Spec.Volumes = []corev1.Volume{
		{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "front-tls"}}},
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "front-config"}}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	backend := workload("Deployment", "backend", 1, corev1.Container{Name: "backend", Image: "shop/backend:v7"})
	db := workload("StatefulSet", "db", 1, corev1.Container{Name: "db", Image: "postgres:18"})
	db.Claims = true
	return World{
		Namespace: "shop",
		Workloads: map[model.ObjectRef]Workload{front.Ref: front, backend.Ref: backend, db.Ref: db},
		Services: []corev1.Service{service("front", map[string]string{"app": "front"}), service("backend", map[string]string{"app": "backend"}),
			service("db", map[string]string{"app": "db"})},
		ConfigMaps: map[string]*corev1.ConfigMap{"front-config": {ObjectMeta: metav1.ObjectMeta{Name: "front-config"}, Data: map[string]string{"mode": "live"}}},
		Digests:    map[string]string{"shop/front:v2": "shop/front@sha256:aaa", "shop/backend:v7": "shop/backend@sha256:bbb"},
	}
}

func TestSynthesisCopiesTheWorkloadAndWhatItCalls(t *testing.T) {
	plan, err := Synthesize(shop(), ref("Deployment", "front"), "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Workloads; len(got) != 2 || got[0].Name != "front" || got[1].Name != "backend" {
		t.Fatalf("copied %v; want the front and the backend it calls, not the database", got)
	}
	var names []string
	for _, s := range plan.Services {
		names = append(names, s.Name)
		if s.Namespace != "sentinel-lab-0" || s.Spec.ClusterIP != "" || s.Spec.Ports[0].NodePort != 0 || s.Labels[Label] != WorldRole {
			t.Fatalf("Service %s is not a fresh Lab copy: %+v", s.Name, s)
		}
	}
	if !slices.Equal(names, []string{"front", "backend", "fake-api-stripe-com"}) {
		t.Fatalf("copied Services %v; want front, backend and a fake of api.stripe.com", names)
	}
	var frontConfig *corev1.ConfigMap
	for _, cm := range plan.ConfigMaps {
		if cm.Name == "front-config" {
			frontConfig = cm
		}
	}
	if frontConfig == nil || frontConfig.Data["mode"] != "live" || frontConfig.Namespace != "sentinel-lab-0" {
		t.Fatalf("ConfigMaps %+v; want front-config copied", plan.ConfigMaps)
	}

	front := plan.Pods[0]
	spec := front.Spec
	switch {
	case front.Name != "front-lab" || front.Labels["app"] != "front" || front.Labels[Label] != WorldRole || front.Labels[CopyOf] != "Deployment.front":
		t.Fatalf("pod %s labels %v", front.Name, front.Labels)
	case spec.ServiceAccountName != "" || !ptr.Equal(spec.AutomountServiceAccountToken, ptr.To(false)):
		t.Fatal("the copy keeps production's service account or a token")
	case spec.PriorityClassName != "" || spec.ImagePullSecrets != nil || spec.DNSPolicy != corev1.DNSNone:
		t.Fatalf("the copy keeps production's priority, pull secrets or DNS: %+v", spec)
	case len(spec.Containers) != 1 || len(spec.InitContainers) != 0:
		t.Fatal("the mesh's containers were copied")
	case spec.Containers[0].Image != "shop/front@sha256:aaa":
		t.Fatalf("image %s is not production's digest", spec.Containers[0].Image)
	case len(spec.Containers[0].EnvFrom) != 0:
		t.Fatal("a secret's variables were copied")
	case spec.Volumes[0].Secret != nil || spec.Volumes[0].EmptyDir == nil || spec.Volumes[1].ConfigMap == nil:
		t.Fatalf("volumes %+v; want the secret emptied and the ConfigMap kept", spec.Volumes)
	}
	for _, e := range spec.Containers[0].Env {
		if e.Name == "TOKEN" && (e.ValueFrom != nil || e.Value != Honeytoken("front-secrets", "token")) {
			t.Fatalf("the secret value was not replaced: %+v", e)
		}
	}
	for _, want := range []string{
		"Deployment/shop/front runs one replica instead of 3",
		"Deployment/shop/front runs without its service account front or any API token",
		"Deployment/shop/front runs at the default priority, so it never preempts production",
		"Deployment/shop/front runs without its service mesh container istio-proxy",
		"Deployment/shop/front's TOKEN holds a honeytoken instead of secret front-secrets",
		"Deployment/shop/front runs without the variables of secret front-env",
		"Deployment/shop/front's volume tls is an empty directory instead of secret front-tls",
		"Deployment/shop/front's api.stripe.com is a Lab fake of the outside host (no captured statuses)",
		"Deployment/shop/front's api.stripe.com speaks TLS with a certificate the Lab CA issued",
		"Deployment/shop/front likely presents a client certificate to an HTTPS fake; pinned or mutual TLS is NOT SURE",
	} {
		if !slices.Contains(plan.Fidelity, want) {
			t.Errorf("fidelity lacks %q; got:\n%s", want, strings.Join(plan.Fidelity, "\n"))
		}
	}
	if !slices.ContainsFunc(plan.Fidelity, func(f string) bool {
		return strings.Contains(f, "trusts the Lab CA at")
	}) {
		t.Errorf("fidelity lacks Lab CA trust note:\n%s", strings.Join(plan.Fidelity, "\n"))
	}
}

func TestSynthesisRefusesWhatItCannotCopy(t *testing.T) {
	cases := map[string]struct {
		change func(*World)
		why    string
	}{
		"a dependency with persistent state": {func(w *World) {
			b := w.Workloads[ref("Deployment", "backend")]
			b.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "DB", Value: "db:5432"}}
			w.Workloads[b.Ref] = b
			db := w.Workloads[ref("StatefulSet", "db")]
			db.Template.Spec.Containers[0].Image = "shop/ledger:1"
			w.Workloads[db.Ref] = db
		}, "StatefulSet/shop/db keeps state in persistent volumes"},
		"the node's network": {func(w *World) {
			f := w.Workloads[ref("Deployment", "front")]
			f.Template.Spec.HostNetwork = true
			w.Workloads[f.Ref] = f
		}, "shares the node's namespaces"},
		"a path from the node": {func(w *World) {
			f := w.Workloads[ref("Deployment", "front")]
			f.Template.Spec.Volumes = append(f.Template.Spec.Volumes, corev1.Volume{Name: "logs",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/log"}}})
			w.Workloads[f.Ref] = f
		}, "mounts a path from the node"},
		"a missing ConfigMap": {func(w *World) { delete(w.ConfigMaps, "front-config") }, "needs ConfigMap front-config"},
		"a Service named like the Lab's own": {func(w *World) {
			w.Services = append(w.Services, service("lab-dns", map[string]string{"app": "front"}))
		}, "has a name the Lab uses"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := shop()
			tc.change(&w)
			if _, err := Synthesize(w, ref("Deployment", "front"), "sentinel-lab-0"); err == nil || !strings.Contains(err.Error(), tc.why) {
				t.Fatalf("got %v; want a refusal saying %q", err, tc.why)
			}
		})
	}
}

// A datastore the world needs runs empty, its claims empty directories,
// with the engine and version production runs.
func TestADatastoreRunsEmpty(t *testing.T) {
	w := shop()
	b := w.Workloads[ref("Deployment", "backend")]
	b.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "DB", Value: "db:5432"}}
	w.Workloads[b.Ref] = b
	db := w.Workloads[ref("StatefulSet", "db")]
	db.ClaimTemplates = []string{"data"}
	db.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/postgresql"}}
	w.Workloads[db.Ref] = db
	plan, err := Synthesize(w, ref("Deployment", "front"), "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(plan.Pods, func(p *corev1.Pod) bool { return p.Labels[CopyOf] == "StatefulSet.db" })
	if i < 0 {
		t.Fatalf("no copy of db among %d pods", len(plan.Pods))
	}
	spec := plan.Pods[i].Spec
	var data *corev1.Volume
	for i := range spec.Volumes {
		if spec.Volumes[i].Name == "data" {
			data = &spec.Volumes[i]
		}
	}
	if data == nil || data.EmptyDir == nil || !strings.HasPrefix(spec.Containers[0].Image, "postgres") {
		t.Fatalf("db copy: image %s, volumes %+v", spec.Containers[0].Image, spec.Volumes)
	}
	if want := "StatefulSet/shop/db runs as an empty postgres: the Lab copies none of its data"; !slices.Contains(plan.Fidelity, want) {
		t.Fatalf("fidelity lacks %q:\n%s", want, strings.Join(plan.Fidelity, "\n"))
	}
}

// A copy is fitted to the restricted Pod Security standard, each change
// noted; one that must run privileged or with extra capabilities is
// refused, and one already fitted is left as it is.
func TestCopiesFitThePodSecurityStandardTheLabEnforces(t *testing.T) {
	bare := shop()
	plan, err := Synthesize(bare, ref("Deployment", "backend"), "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	spec := plan.Pods[0].Spec
	sc := spec.Containers[0].SecurityContext
	switch {
	case spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault || !*spec.SecurityContext.RunAsNonRoot:
		t.Fatalf("pod security context %+v", spec.SecurityContext)
	case *sc.AllowPrivilegeEscalation || !slices.Contains(sc.Capabilities.Drop, "ALL"):
		t.Fatalf("container security context %+v", sc)
	}
	for _, want := range []string{"runs under the runtime's default seccomp profile", "must run as a non-root user",
		"may not gain privileges", "runs without Linux capabilities"} {
		if !slices.ContainsFunc(plan.Fidelity, func(f string) bool { return strings.Contains(f, "backend") && strings.Contains(f, want) }) {
			t.Errorf("fidelity lacks %q: %v", want, plan.Fidelity)
		}
	}

	hardened := shop()
	b := hardened.Workloads[ref("Deployment", "backend")]
	b.Template.Spec.SecurityContext = &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	b.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"NET_BIND_SERVICE"}}}
	hardened.Workloads[b.Ref] = b
	plan, err = Synthesize(hardened, b.Ref, "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(plan.Fidelity, func(f string) bool { return strings.Contains(f, "seccomp") || strings.Contains(f, "capabilities") }) {
		t.Fatalf("a fitted copy was changed: %v", plan.Fidelity)
	}

	for name, sc := range map[string]*corev1.SecurityContext{
		"privileged":     {Privileged: ptr.To(true)},
		"with SYS_ADMIN": {Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}}},
	} {
		w := shop()
		b := w.Workloads[ref("Deployment", "backend")]
		b.Template.Spec.Containers[0].SecurityContext = sc
		w.Workloads[b.Ref] = b
		if _, err := Synthesize(w, b.Ref, "sentinel-lab-0"); err == nil || !strings.Contains(err.Error(), "no Lab pod may") {
			t.Errorf("%s: got %v; want a refusal", name, err)
		}
	}
}

// A copy asks for little CPU, so it fits beside production; its limit,
// which caps it as it caps production, stays.
func TestCopiesAskForLittleCPUButKeepTheirLimits(t *testing.T) {
	w := shop()
	b := w.Workloads[ref("Deployment", "backend")]
	b.Template.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")},
	}
	w.Workloads[b.Ref] = b
	plan, err := Synthesize(w, b.Ref, "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	r := plan.Pods[0].Spec.Containers[0].Resources
	if r.Requests.Cpu().String() != "50m" || r.Limits.Cpu().String() != "1" || r.Requests.Memory().String() != "256Mi" {
		t.Fatalf("resources = %+v", r)
	}
	if !slices.ContainsFunc(plan.Fidelity, func(f string) bool { return strings.Contains(f, "asks for 50m of CPU instead of 500m") }) {
		t.Fatalf("fidelity lacks the CPU request: %v", plan.Fidelity)
	}
}

// A name a copy looked up from a secret's placeholder names that secret.
func TestAPlaceholderInALookupNamesItsSecret(t *testing.T) {
	w := shop()
	tokens := secretTokens(w)
	host := SecretPlaceholder("front-secrets", "token")
	if got := tokenIn(host+".svc", tokens); got != "key token of secret front-secrets" {
		t.Fatalf("lookup of %s names %q", host, got)
	}
	if got := tokenIn("api.stripe.com", tokens); got != "" {
		t.Fatalf("an ordinary name named %q", got)
	}
}
