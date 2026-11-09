package fixer

import (
	"bytes"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

const (
	good = "ghcr.io/stefanprodan/podinfo:6.17.0"
	bad  = "ghcr.io/stefanprodan/podinfo:does-not-exist"
)

func repoOf(t *testing.T, files map[string]string) filesys.FileSystem {
	t.Helper()
	fs := filesys.MakeFsInMemory()
	for p, content := range files {
		if err := fs.WriteFile("/"+p, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return fs
}

func deployment(image string) string {
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
          image: ` + image + ` # the version we run
          args: [--port=9898]
`
}

const service = `apiVersion: v1
kind: Service
metadata:
  name: podinfo
spec:
  selector: {app: podinfo}
  ports: [{port: 9898}]
`

// changedLines lists the lines that differ between two versions of a file
// with the same number of lines.
func changedLines(t *testing.T, before, after []byte) []string {
	t.Helper()
	b, a := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(a) != len(b) {
		t.Fatalf("the edit changed the number of lines: %d to %d", len(b), len(a))
	}
	var changed []string
	for i := range b {
		if a[i] != b[i] {
			changed = append(changed, a[i])
		}
	}
	return changed
}

// A bad image written in the workload's own manifest is set back there,
// on its line, keeping the comment beside it.
func TestLocateFindsTheImageInTheManifest(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"kustomize/kustomization.yaml": "resources: [deployment.yaml, service.yaml]\n",
		"kustomize/deployment.yaml":    deployment(bad),
		"kustomize/service.yaml":       service,
	})
	edit, err := Locate(repo, "./kustomize", Target{Kind: "Deployment", Name: "podinfo"}, Change{Container: "podinfod", Bad: bad, Good: good})
	if err != nil {
		t.Fatal(err)
	}
	if edit.Path != "kustomize/deployment.yaml" || edit.Line != 14 {
		t.Fatalf("edit at %s:%d", edit.Path, edit.Line)
	}
	if got := changedLines(t, edit.Before, edit.After); len(got) != 1 || got[0] != "          image: "+good+" # the version we run" {
		t.Fatalf("changed lines = %q", got)
	}
}

// An overlay's images entry that set the bad tag is where the fix goes; the
// base it transforms stays as it is.
func TestLocateFindsTheImagesEntryThatSetTheTag(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"base/kustomization.yaml": "resources: [deployment.yaml, service.yaml]\n",
		"base/deployment.yaml":    deployment("ghcr.io/stefanprodan/podinfo:6.0.0"),
		"base/service.yaml":       service,
		"prod/kustomization.yaml": `# production
resources: [../base]
images:
  - name: ghcr.io/stefanprodan/podinfo
    newTag: "does-not-exist" # bumped by the release
`,
	})
	edit, err := Locate(repo, "prod", Target{Kind: "Deployment", Name: "podinfo"}, Change{Container: "podinfod", Bad: bad, Good: good})
	if err != nil {
		t.Fatal(err)
	}
	if edit.Path != "prod/kustomization.yaml" || edit.Line != 5 || !strings.Contains(edit.Why, "sets the tag does-not-exist") {
		t.Fatalf("edit at %s:%d: %s", edit.Path, edit.Line, edit.Why)
	}
	if got := changedLines(t, edit.Before, edit.After); len(got) != 1 || got[0] != `    newTag: "6.17.0" # bumped by the release` {
		t.Fatalf("changed lines = %q", got)
	}
}

// The owner's own name prefix is not in the files.
func TestLocateStripsTheOwnersNamePrefix(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    deployment(bad),
	})
	if _, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "child-podinfo", NamePrefix: "child-"},
		Change{Container: "podinfod", Bad: bad, Good: good}); err != nil {
		t.Fatal(err)
	}
}

// When the files render the good image, the bad one was set elsewhere, for
// example on the Flux Kustomization or Argo CD Application itself; the
// Fixer says so rather than editing anything.
func TestLocateAbstainsWhenTheFilesDoNotSetTheBadImage(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"app/kustomization.yaml": "resources: [deployment.yaml]\n",
		"app/deployment.yaml":    deployment(good),
	})
	_, err := Locate(repo, "app", Target{Kind: "Deployment", Name: "podinfo"}, Change{Container: "podinfod", Bad: bad, Good: good})
	if err == nil || !strings.Contains(err.Error(), "set outside these files") {
		t.Fatalf("err = %v", err)
	}
}

// A directory of plain manifests, as Argo CD and Flux deploy one without a
// kustomization, is searched file by file.
func TestLocateSearchesPlainManifests(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"plain/all.yaml":  service + "---\n" + deployment(`"`+bad+`"`),
		"plain/README.md": "not a manifest",
	})
	edit, err := Locate(repo, "plain", Target{Kind: "Deployment", Name: "podinfo"}, Change{Container: "podinfod", Bad: bad, Good: good})
	if err != nil {
		t.Fatal(err)
	}
	if got := changedLines(t, edit.Before, edit.After); edit.Path != "plain/all.yaml" || len(got) != 1 ||
		got[0] != `          image: "`+good+`" # the version we run` {
		t.Fatalf("edit at %s: %q", edit.Path, got)
	}
}

// An images entry that pins a tag cannot be set back to a digest by
// changing one value, so the Fixer abstains.
func TestLocateAbstainsWhenOneValueCannotRestoreTheImage(t *testing.T) {
	repo := repoOf(t, map[string]string{
		"base/kustomization.yaml": "resources: [deployment.yaml]\n",
		"base/deployment.yaml":    deployment("ghcr.io/stefanprodan/podinfo:6.0.0"),
		"prod/kustomization.yaml": "resources: [../base]\nimages:\n  - {name: ghcr.io/stefanprodan/podinfo, newTag: does-not-exist}\n",
	})
	_, err := Locate(repo, "prod", Target{Kind: "Deployment", Name: "podinfo"},
		Change{Container: "podinfod", Bad: bad, Good: "ghcr.io/stefanprodan/podinfo@sha256:0123"})
	if err == nil || !strings.Contains(err.Error(), "by changing one value") {
		t.Fatalf("err = %v", err)
	}
}

// A value that would read back as something other than a string when
// written plain gets quotes; quoted values keep their quotes.
func TestSpliceKeepsValuesStrings(t *testing.T) {
	for _, tc := range []struct{ src, value, want string }{
		{"tag: v1 # pinned\n", "1.0", "tag: \"1.0\" # pinned\n"},
		{"tag: 'v1'\n", "v2", "tag: 'v2'\n"},
		{"tag: v1\n", "v2", "tag: v2\n"},
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(tc.src), &doc); err != nil {
			t.Fatal(err)
		}
		out, err := splice([]byte(tc.src), valueOf(doc.Content[0], "tag"), tc.value)
		if err != nil || !bytes.Equal(out, []byte(tc.want)) {
			t.Fatalf("splice(%q, %q) = %q, %v; want %q", tc.src, tc.value, out, err, tc.want)
		}
	}
}
