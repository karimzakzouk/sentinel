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
	for _, tc := range []struct {
		name        string
		bad         corev1.PodTemplateSpec
		spec        v1alpha1.FixSpec
		want, kept  string
		wantRefusal string
	}{
		{name: "a bad image beside a new greeting", bad: template("web:nope", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "deploy.bad-image"}, want: "image web:nope -> web:1", kept: "web's environment variable GREETING"},
		{name: "a crash two changes could explain", bad: template("web:2", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "deploy.crash-on-start"}, wantRefusal: "2 values changed in revision bad that a deploy.crash-on-start fault could be blamed on"},
		{name: "a memory limit, learned", bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit", MemoryLimits: map[string]string{"web": "14Mi"}}, want: "limits.memory 6Mi -> 14Mi"},
		{name: "a memory limit learned above the one that held", bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit", MemoryLimits: map[string]string{"web": "100Mi"}}, want: "limits.memory 6Mi -> 64Mi"},
		{name: "a memory limit with nothing learned", bad: template("web:1", "hi", "6Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit"}, wantRefusal: "has not measured enough"},
		{name: "a change the class is not blamed on", bad: template("web:1", "hello", "64Mi"),
			spec: v1alpha1.FixSpec{FaultClass: "resource.memory-limit"}, wantRefusal: "none of which a resource.memory-limit fault is blamed on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: types.UID("dep")},
				Spec: appsv1.DeploymentSpec{Template: template("web:1", "hi", "64Mi")}}
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
