package fixer

import (
	"strings"
	"testing"
)

// echo is a Deployment whose container takes its port from PORT.
const echo = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
spec:
  selector:
    matchLabels: {app: echo}
  template:
    metadata:
      labels: {app: echo}
    spec:
      containers:
        - name: echo
          image: registry.k8s.io/e2e-test-images/agnhost:2.61
          args: [netexec, "--http-port=$(PORT)"]
          env:
            - name: PORT
              value: "8080"
`

// A value a kustomization's patch sets is restored in that patch, in each
// form Kustomize reads one; the manifest is left alone.
func TestLocateRestoresAValueAPatchSets(t *testing.T) {
	port := func(value string) Change {
		return Change{Container: "echo", Field: "env:PORT", Bad: value, Good: "8080"}
	}
	cases := []struct {
		name   string
		files  map[string]string
		change Change
		path   string
		want   string
		line   int
	}{
		{
			name: "a JSON 6902 patch written in place",
			files: map[string]string{"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "replace", "path": "/spec/template/spec/containers/0/env/0/value", "value": "eighty"}]'
`},
			change: port("eighty"),
			path:   "app/kustomization.yaml",
			want: `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "replace", "path": "/spec/template/spec/containers/0/env/0/value", "value": "8080"}]'
`,
			line: 4,
		},
		{
			name: "a JSON 6902 patch written as a YAML block",
			files: map[string]string{"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/env/0/value
        value: eighty # a port by name
`},
			change: port("eighty"),
			path:   "app/kustomization.yaml",
			want: `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: |-
      - op: replace
        path: /spec/template/spec/containers/0/env/0/value
        value: "8080" # a port by name
`,
			line: 7,
		},
		{
			name: "a strategic merge patch in its own file",
			files: map[string]string{
				"app/kustomization.yaml": "resources: [deployment.yaml]\npatches:\n  - path: port.yaml\n",
				"app/port.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
spec:
  template:
    spec:
      containers:
        - name: echo
          env:
            - name: PORT
              value: eighty
`},
			change: port("eighty"),
			path:   "app/port.yaml",
			want: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
spec:
  template:
    spec:
      containers:
        - name: echo
          env:
            - name: PORT
              value: "8080"
`,
			line: 12,
		},
		{
			name: "a strategic merge patch aimed by a target, under another name",
			files: map[string]string{
				"app/kustomization.yaml": "resources: [deployment.yaml]\npatchesStrategicMerge:\n  - port.yaml\n",
				"app/port.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
spec:
  template:
    spec:
      containers:
        - name: echo
          args: [netexec, --http-port=8080, --no-such-flag]
`},
			change: Change{Container: "echo", Field: "args", Bad: []string{"netexec", "--http-port=8080", "--no-such-flag"},
				Good: []string{"netexec", "--http-port=$(PORT)"}},
			path: "app/port.yaml",
			want: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo
spec:
  template:
    spec:
      containers:
        - name: echo
          args: [netexec, --http-port=$(PORT)]
`,
			line: 10,
		},
		{
			name: "an operation that added a variable, one of two",
			files: map[string]string{"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "MODE", "value": "crash"}}, {"op": "add", "path": "/spec/replicas", "value": 2}]'
`},
			change: Change{Container: "echo", Field: "env:MODE", Bad: "crash"},
			path:   "app/kustomization.yaml",
			want: `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "add", "path": "/spec/replicas", "value": 2}]'
`,
			line: 4,
		},
		{
			name: "the only patch, which only added a variable",
			files: map[string]string{"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "add", "path": "/spec/template/spec/containers/0/env/-", "value": {"name": "MODE", "value": "crash"}}]'
images:
  - name: registry.k8s.io/e2e-test-images/agnhost
    newTag: "2.61"
`},
			change: Change{Container: "echo", Field: "env:MODE", Bad: "crash"},
			path:   "app/kustomization.yaml",
			want: `resources: [deployment.yaml]
images:
  - name: registry.k8s.io/e2e-test-images/agnhost
    newTag: "2.61"
`,
			line: 3,
		},
		{
			name: "a JSON 6902 patch file left with nothing to do",
			files: map[string]string{
				"app/kustomization.yaml": "resources: [deployment.yaml]\npatches:\n  - path: mode.yaml\n    target: {kind: Deployment}\n",
				"app/mode.yaml":          "- op: add\n  path: /spec/template/spec/containers/0/env/-\n  value: {name: MODE, value: crash}\n",
			},
			change: Change{Container: "echo", Field: "env:MODE", Bad: "crash"},
			path:   "app/mode.yaml",
			want:   "[]\n",
			line:   1,
		},
		{
			name: "the manifest, when a patch changes something else",
			files: map[string]string{"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "add", "path": "/spec/replicas", "value": 2}]'
`},
			change: Change{Container: "echo", Field: "env:PORT", Bad: "8080", Good: "9090"},
			path:   "app/deployment.yaml",
			want:   strings.Replace(echo, `value: "8080"`, `value: "9090"`, 1),
			line:   18,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"app/deployment.yaml": echo}
			for k, v := range tc.files {
				files[k] = v
			}
			edit, err := Locate(repoOf(t, files), "app", Target{Kind: "Deployment", Name: "echo"}, tc.change)
			if err != nil {
				t.Fatal(err)
			}
			if edit.Path != tc.path || string(edit.After) != tc.want || edit.Line != tc.line {
				t.Fatalf("edit of %s at line %d:\n%s\nwant %s at line %d:\n%s", edit.Path, edit.Line, edit.After, tc.path, tc.line, tc.want)
			}
		})
	}
}

// When the manifest and a patch both write the bad value, the patch is what
// renders, so the fix edits it, and says the manifest holds the value too.
func TestLocateEditsThePatchThatHidesTheManifest(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/deployment.yaml": strings.Replace(echo, `value: "8080"`, `value: eighty`, 1),
		"app/kustomization.yaml": `resources: [deployment.yaml]
patches:
  - target: {kind: Deployment, name: echo}
    patch: '[{"op": "replace", "path": "/spec/template/spec/containers/0/env/0/value", "value": "eighty"}]'
`,
	})
	edit, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "echo"}, Change{Container: "echo", Field: "env:PORT", Bad: "eighty", Good: "8080"})
	if err != nil {
		t.Fatal(err)
	}
	if edit.Path != "app/kustomization.yaml" || !strings.Contains(edit.Why, "app/deployment.yaml sets the same value under it") {
		t.Fatalf("edit of %s: %s", edit.Path, edit.Why)
	}
}
