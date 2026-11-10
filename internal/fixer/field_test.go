package fixer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
)

// podinfo writes a Deployment whose podinfod container has the given lines
// after its image line.
func podinfo(lines string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: podinfo
spec:
  selector:
    matchLabels: {app: podinfo}
  template:
    metadata:
      labels: {app: podinfo}
    spec:
      containers:
        - name: podinfod
          image: ghcr.io/stefanprodan/podinfo:6.17.0
` + lines + `          ports:
            - containerPort: 9898
`
}

// The values a commit can break besides the image: each is restored where
// it is written, and the render, or the file, reads back with only it
// changed.
func TestLocateRestoresCommandsArgumentsAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, lines string
		change      Change
		want        string
		line        int
	}{
		{"a command added", "          command: [./podinfo, --no-such-flag] # turn it on\n",
			Change{Container: "podinfod", Field: "command", Bad: []string{"./podinfo", "--no-such-flag"}}, "", 15},
		{"arguments changed, one a line", "          args:\n            - --port=9898\n            - --level=debug\n",
			Change{Container: "podinfod", Field: "args", Bad: []string{"--port=9898", "--level=debug"}, Good: []string{"--port=9898", "--level=info"}},
			"          args:\n            - --port=9898\n            - --level=info\n", 15},
		{"arguments changed, items at the key's column", "          args:\n          - --level=debug\n",
			Change{Container: "podinfod", Field: "args", Bad: []string{"--level=debug"}, Good: []string{"--level=info"}},
			"          args:\n          - --level=info\n", 15},
		{"arguments changed on one line", "          args: [--level=debug] # verbose\n",
			Change{Container: "podinfod", Field: "args", Bad: []string{"--level=debug"}, Good: []string{"--level=info"}},
			"          args: [--level=info] # verbose\n", 15},
		{"an environment value changed", "          env:\n            - name: PODINFO_UI_COLOR\n              value: \"#ff0000\"\n",
			Change{Container: "podinfod", Field: "env:PODINFO_UI_COLOR", Bad: "#ff0000", Good: "#34577c"},
			"          env:\n            - name: PODINFO_UI_COLOR\n              value: \"#34577c\"\n", 17},
		{"an environment variable added", "          env:\n            - name: KEEP\n              value: \"1\"\n            - name: PODINFO_CACHE_SERVER\n              value: tcp://nowhere:6379\n",
			Change{Container: "podinfod", Field: "env:PODINFO_CACHE_SERVER", Bad: "tcp://nowhere:6379"},
			"          env:\n            - name: KEEP\n              value: \"1\"\n", 18},
		{"the only environment variable added", "          env:\n          - name: PODINFO_CACHE_SERVER\n            value: tcp://nowhere:6379\n",
			Change{Container: "podinfod", Field: "env:PODINFO_CACHE_SERVER", Bad: "tcp://nowhere:6379"}, "", 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := repoOf(t, map[string]string{
				"app/kustomization.yaml": "resources: [deployment.yaml]\n",
				"app/deployment.yaml":    podinfo(tc.lines),
			})
			edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"}, tc.change)
			if err != nil {
				t.Fatal(err)
			}
			if want := podinfo(tc.want); string(edit.After) != want {
				t.Fatalf("after the fix:\n%s\nwant:\n%s", edit.After, want)
			}
			if on := fmt.Sprintf("on line %d", tc.line); edit.Path != "app/deployment.yaml" || edit.Line != tc.line || !strings.Contains(edit.Why, on) {
				t.Fatalf("edit at %s:%d: %s", edit.Path, edit.Line, edit.Why)
			}
		})
	}
}

// Plain manifests get the same edits, checked by reading the file back.
func TestLocateRestoresArgumentsInPlainManifests(t *testing.T) {
	repo := repoOf(t, map[string]string{"plain/deployment.yaml": podinfo("          args: [--no-such-flag]\n")})
	edit, err := Locate(repo, "plain", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "args", Bad: []string{"--no-such-flag"}})
	if err != nil || string(edit.After) != podinfo("") {
		t.Fatalf("edit = %v, %v", edit, err)
	}
}

// ContainerChange finds the one value a revision changed; two, or one taken
// from a ConfigMap, is more than the Fixer restores.
func TestContainerChangeFindsTheOneValue(t *testing.T) {
	ctx := context.Background()
	container := func(edit func(*corev1.Container)) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "podinfod", Image: "podinfo:1", Env: []corev1.EnvVar{{Name: "LEVEL", Value: "info"}}}
		edit(&c)
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	fromConfigMap := func(name string) func(*corev1.Container) {
		return func(c *corev1.Container) {
			c.Env[0] = corev1.EnvVar{Name: "LEVEL", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "level",
				LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}
		}
	}
	for _, tc := range []struct {
		name      string
		bad, good func(*corev1.Container)
		want      string
	}{
		{"arguments", func(c *corev1.Container) { c.Args = []string{"--no-such-flag"} }, nil, "args [--no-such-flag] -> not set"},
		{"a readiness probe", func(c *corev1.Container) {
			c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9999)}}, PeriodSeconds: 2, FailureThreshold: 1}
		}, func(c *corev1.Container) {
			c.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(9898)}}, PeriodSeconds: 10, FailureThreshold: 3}
		}, "readinessProbe"},
		{"an environment value", func(c *corev1.Container) { c.Env[0].Value = "debug" }, nil, "env:LEVEL debug -> info"},
		{"two values", func(c *corev1.Container) { c.Image, c.Args = "podinfo:2", []string{"-v"} }, nil, "2 values changed"},
		{"a value from a ConfigMap", fromConfigMap("settings"), nil, "literal values only"},
		{"a value from another ConfigMap", fromConfigMap("settings-b"), fromConfigMap("settings-a"), "literal values only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good := tc.good
			if good == nil {
				good = func(*corev1.Container) {}
			}
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "shop", UID: types.UID("dep")},
				Spec: appsv1.DeploymentSpec{Template: container(good)}}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
				OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: container(tc.bad)}}
			c, err := ContainerChange(ctx, fake.NewClientset(dep, rs), dep, "bad")
			got := ""
			if err != nil {
				got = err.Error()
			} else {
				got = c.Field + " " + shown(c.Bad) + " -> " + shown(c.Good)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A memory limit is not put back. The old limit can be as wrong as the new
// one; a proposal has to come from measured use.
func TestAMemoryLimitIsNotRestored(t *testing.T) {
	ctx := context.Background()
	pod := func(limit string) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "podinfod", Image: "podinfo:1",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "shop", UID: types.UID("dep")},
		Spec: appsv1.DeploymentSpec{Template: pod("64Mi")}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: pod("16Mi")}}
	_, err := ContainerChange(ctx, fake.NewClientset(dep, rs), dep, "bad")
	if err == nil || !strings.Contains(err.Error(), "memory limit") {
		t.Fatalf("err = %v", err)
	}
}

// An ephemeral-storage limit is not put back either.
func TestADiskLimitIsNotRestored(t *testing.T) {
	ctx := context.Background()
	pod := func(limit string) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "podinfod", Image: "podinfo:1",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse(limit)}}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "shop", UID: types.UID("dep")},
		Spec: appsv1.DeploymentSpec{Template: pod("1Gi")}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: pod("100Mi")}}
	_, err := ContainerChange(ctx, fake.NewClientset(dep, rs), dep, "bad")
	if err == nil || !strings.Contains(err.Error(), "ephemeral-storage limit") {
		t.Fatalf("err = %v", err)
	}
}

// A CPU limit is not put back either.
func TestACPULimitIsNotRestored(t *testing.T) {
	ctx := context.Background()
	pod := func(limit string) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "podinfod", Image: "podinfo:1",
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(limit)}}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "shop", UID: types.UID("dep")},
		Spec: appsv1.DeploymentSpec{Template: pod("100m")}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "podinfo-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: pod("10m")}}
	_, err := ContainerChange(ctx, fake.NewClientset(dep, rs), dep, "bad")
	if err == nil || !strings.Contains(err.Error(), "CPU limit") {
		t.Fatalf("err = %v", err)
	}
}

func diskTemplate(image, limit string) corev1.PodTemplateSpec {
	c := corev1.Container{Name: "web", Image: image,
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse(limit)}}}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
}

func graceTemplate(seconds int64) corev1.PodTemplateSpec {
	c := corev1.Container{Name: "web", Image: "web:1"}
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		TerminationGracePeriodSeconds: &seconds,
		Containers:                      []corev1.Container{c},
	}}
}

// Of several values a revision changed, the fix sets the one the fault
// class blames and leaves the rest; a class that blames two refuses, and a
// memory limit gets the learned value, never more than the limit that held.
func TestChangeForBlamesByFaultClass(t *testing.T) {
	ctx := context.Background()
	template := func(image, greeting, limit string) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "web", Image: image, Env: []corev1.EnvVar{{Name: "GREETING", Value: greeting}},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limit)}}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	cpuTemplate := func(image, cpu string) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "web", Image: image,
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu)}}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	for _, tc := range []struct {
		name        string
		good, bad   corev1.PodTemplateSpec
		spec        v1alpha1.FixSpec
		want, kept  string
		wantRefusal string
	}{
		{name: "a bad image beside a new greeting", good: template("web:1", "hi", "64Mi"), bad: template("web:nope", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "deploy.bad-image"}, want: "image web:nope -> web:1", kept: "web's environment variable GREETING"},
		{name: "a crash two changes could explain", good: template("web:1", "hi", "64Mi"), bad: template("web:2", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "deploy.crash-on-start"}, wantRefusal: "2 values changed in revision bad that a deploy.crash-on-start fault could be blamed on"},
		{name: "a memory limit, learned", good: template("web:1", "hi", "64Mi"), bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit", MemoryLimits: map[string]string{"web": "14Mi"}}, want: "limits.memory 6Mi -> 14Mi"},
		{name: "a memory limit learned above the one that held", good: template("web:1", "hi", "64Mi"), bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit", MemoryLimits: map[string]string{"web": "100Mi"}}, want: "limits.memory 6Mi -> 64Mi"},
		{name: "a memory limit with nothing learned", good: template("web:1", "hi", "64Mi"), bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit"}, wantRefusal: "has not measured enough"},
		{name: "a change the class is not blamed on", good: template("web:1", "hi", "64Mi"), bad: template("web:1", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit"}, wantRefusal: "none of which a resource.memory-limit fault is blamed on"},
		{name: "a CPU limit, learned", good: cpuTemplate("web:1", "100m"), bad: cpuTemplate("web:1", "10m"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.cpu-limit", CPULimits: map[string]string{"web": "80m"}}, want: "limits.cpu 10m -> 80m"},
		{name: "a CPU limit learned above the one that held", good: cpuTemplate("web:1", "100m"), bad: cpuTemplate("web:1", "10m"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.cpu-limit", CPULimits: map[string]string{"web": "500m"}}, want: "limits.cpu 10m -> 100m"},
		{name: "a CPU limit with nothing learned", good: cpuTemplate("web:1", "100m"), bad: cpuTemplate("web:1", "10m"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.cpu-limit"}, wantRefusal: "has not measured enough"},
		{name: "a disk limit, learned", good: diskTemplate("web:1", "1Gi"), bad: diskTemplate("web:1", "50Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.disk-limit", DiskLimits: map[string]string{"web": "200Mi"}}, want: "limits.ephemeral-storage 50Mi -> 200Mi"},
		{name: "termination grace restored", good: graceTemplate(30), bad: graceTemplate(1),
			spec: v1alpha1.FixSpec{FaultClass: "config.timeout"}, want: "terminationGracePeriodSeconds 1 -> 30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: types.UID("dep")},
				Spec: appsv1.DeploymentSpec{Template: tc.good}}
			rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
				OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: tc.bad}}
			tc.spec.FromRevision = "bad"
			c, kept, err := ChangeFor(ctx, fake.NewClientset(dep, rs), dep, tc.spec)
			if tc.wantRefusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantRefusal) {
					t.Fatalf("err = %v, want %q", err, tc.wantRefusal)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Field + " " + shown(c.Bad) + " -> " + shown(c.Good); got != tc.want || listed(kept) != tc.kept {
				t.Fatalf("got %q keeping %q, want %q keeping %q", got, listed(kept), tc.want, tc.kept)
			}
		})
	}
}

// A learned memory limit is written where the manifest sets the limit, as
// any quantity it was written as.
func TestLocateSetsALearnedMemoryLimit(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo("          resources:\n            limits: {memory: 6291456} # tight\n"),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "limits.memory", Bad: "6Mi", Good: "14Mi"})
	if err != nil {
		t.Fatal(err)
	}
	if want := podinfo("          resources:\n            limits: {memory: 14Mi} # tight\n"); string(edit.After) != want || edit.Line != 16 {
		t.Fatalf("line %d:\n%s\nwant:\n%s", edit.Line, edit.After, want)
	}
}

// A learned CPU limit is written where the manifest sets the limit.
func TestLocateSetsALearnedCPULimit(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo("          resources:\n            limits: {cpu: 10m} # tight\n"),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "limits.cpu", Bad: "10m", Good: "80m"})
	if err != nil {
		t.Fatal(err)
	}
	if want := podinfo("          resources:\n            limits: {cpu: 80m} # tight\n"); string(edit.After) != want || edit.Line != 16 {
		t.Fatalf("line %d:\n%s\nwant:\n%s", edit.Line, edit.After, want)
	}
}

// A readiness probe the blamed revision broke is put back where it is written.
func TestLocateRestoresAReadinessProbe(t *testing.T) {
	bad := "          readinessProbe:\n            tcpSocket:\n              port: 9999\n            periodSeconds: 2\n            failureThreshold: 1\n"
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo(bad),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "readinessProbe",
			Bad:  map[string]any{"tcpSocket": map[string]any{"port": 9999}, "periodSeconds": 2, "failureThreshold": 1},
			Good: map[string]any{"tcpSocket": map[string]any{"port": 9898}, "periodSeconds": 10, "failureThreshold": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(edit.After), "port: 9999") || !strings.Contains(string(edit.After), "port: 9898") {
		t.Fatalf("after the fix:\n%s", edit.After)
	}
	if edit.Line != 15 {
		t.Fatalf("line %d, want 15", edit.Line)
	}
}

// A readiness probe written on one line is restored the same way.
func TestLocateRestoresAFlowReadinessProbe(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo("          readinessProbe: {tcpSocket: {port: 9999}, periodSeconds: 2}\n"),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "readinessProbe",
			Bad:  map[string]any{"tcpSocket": map[string]any{"port": 9999}, "periodSeconds": 2},
			Good: map[string]any{"tcpSocket": map[string]any{"port": 9898}, "periodSeconds": 10}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edit.After), "port: 9898") || strings.Contains(string(edit.After), "port: 9999") {
		t.Fatalf("after the fix:\n%s", edit.After)
	}
}

// A probe the bad revision added is removed.
func TestLocateRemovesAnAddedReadinessProbe(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo("          readinessProbe:\n            tcpSocket: {port: 9999}\n"),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "readinessProbe",
			Bad: map[string]any{"tcpSocket": map[string]any{"port": 9999}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := podinfo(""); string(edit.After) != want {
		t.Fatalf("after the fix:\n%s\nwant:\n%s", edit.After, want)
	}
}

// A config.probe fault restores the probe and leaves an image change alone.
func TestChangeForRestoresAProbeAndKeepsTheImage(t *testing.T) {
	ctx := context.Background()
	probe := func(image string, port int32) corev1.PodTemplateSpec {
		c := corev1.Container{Name: "web", Image: image,
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, PeriodSeconds: 2, FailureThreshold: 1}}
		return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: types.UID("dep")},
		Spec: appsv1.DeploymentSpec{Template: probe("web:1", 8080)}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: probe("web:2", 9999)}}
	c, kept, err := ChangeFor(ctx, fake.NewClientset(dep, rs), dep, v1alpha1.FixSpec{FromRevision: "bad", FaultClass: "config.probe"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Field != "readinessProbe" || listed(kept) != "web's image" {
		t.Fatalf("got %s keeping %q", c.Field, listed(kept))
	}
}

func TestLocateSetsALearnedDiskLimit(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    podinfo("          resources:\n            limits: {ephemeral-storage: 100Mi}\n"),
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Field: "limits.ephemeral-storage", Bad: "100Mi", Good: "200Mi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edit.After), "200Mi") {
		t.Fatalf("after the fix:\n%s", edit.After)
	}
}

func TestLocateRestoresReplicas(t *testing.T) {
	yaml := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: podinfo
spec:
  replicas: 0
  selector:
    matchLabels: {app: podinfo}
  template:
    metadata:
      labels: {app: podinfo}
    spec:
      containers:
        - name: podinfod
          image: ghcr.io/stefanprodan/podinfo:6.17.0
`
	repo := repoOf(t, map[string]string{"app/kustomization.yaml": "resources: [deployment.yaml]\n", "app/deployment.yaml": yaml})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Field: "replicas", Bad: 0, Good: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edit.After), "replicas: 3") && !strings.Contains(string(edit.After), `replicas: "3"`) {
		t.Fatalf("after the fix:\n%s", edit.After)
	}
}

func TestLocateRestoresTerminationGrace(t *testing.T) {
	dep := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: podinfo
spec:
  selector:
    matchLabels: {app: podinfo}
  template:
    metadata:
      labels: {app: podinfo}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: podinfod
          image: ghcr.io/stefanprodan/podinfo:6.17.0
          ports:
            - containerPort: 9898
`
	repo := repoOf(t, map[string]string{"app/kustomization.yaml": "resources: [deployment.yaml]\n", "app/deployment.yaml": dep})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Field: "terminationGracePeriodSeconds", Bad: 1, Good: 30})
	if err != nil {
		t.Fatal(err)
	}
	got := string(edit.After)
	if !strings.Contains(got, "terminationGracePeriodSeconds") || !strings.Contains(got, "30") || strings.Contains(got, "terminationGracePeriodSeconds: 1") {
		t.Fatalf("after the fix:\n%s", edit.After)
	}
}

func TestChangeForRestoresReplicasWhenAsked(t *testing.T) {
	ctx := context.Background()
	tmpl := func(replicas int32) appsv1.DeploymentSpec {
		c := corev1.Container{Name: "web", Image: "web:1"}
		return appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}},
		}
	}
	three := int32(3)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: "shop", UID: types.UID("dep")},
		Spec: tmpl(3)}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "redis-bad", Namespace: "shop", Labels: map[string]string{"pod-template-hash": "bad"},
		OwnerReferences: []metav1.OwnerReference{{UID: "dep"}}}, Spec: appsv1.ReplicaSetSpec{Template: tmpl(0).Template}}
	bad := int32(0)
	c, _, err := ChangeFor(ctx, fake.NewClientset(dep, rs), dep, v1alpha1.FixSpec{FromRevision: "bad", FaultClass: "dependency.unavailable", ReplicasBad: &bad})
	if err != nil {
		t.Fatal(err)
	}
	if c.Field != fieldReplicas || c.Good != three || c.Bad != bad {
		t.Fatalf("got %+v", c)
	}
}
