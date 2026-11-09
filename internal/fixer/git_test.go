package fixer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// seed creates a bare repository on disk whose main branch holds files.
func seed(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "delivery.git")
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
	tree := memfs.New()
	repo, err := git.Init(memory.NewStorage(), tree)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := repo.Worktree()
	for p, content := range files {
		if err := util.WriteFile(tree, p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Commit("Deploy podinfo", &git.CommitOptions{Author: &object.Signature{Name: "A person", Email: "person@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{"refs/heads/master:refs/heads/main"}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A fix is located in a clone of the branch's head and pushed as a branch of
// its own, which changes only the fixed file.
func TestAFixIsPushedAsABranch(t *testing.T) {
	ctx := context.Background()
	dir := seed(t, map[string]string{
		"kustomize/kustomization.yaml": "resources: [deployment.yaml, service.yaml]\n",
		"kustomize/deployment.yaml":    deployment(bad),
		"kustomize/service.yaml":       service,
	})
	co, err := Clone(ctx, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	files, err := co.Files()
	if err != nil {
		t.Fatal(err)
	}
	edit, err := Locate(files, "kustomize", Target{Kind: "Deployment", Name: "podinfo"}, Change{Container: "podinfod", Bad: bad, Good: good})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := co.Push(ctx, edit, "sentinel/fix-1", "Set podinfod's image back", object.Signature{Name: "Sentinel", Email: "sentinel@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	remote, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := remote.Reference(plumbing.NewBranchReferenceName("sentinel/fix-1"), true)
	if err != nil || ref.Hash().String() != commit {
		t.Fatalf("branch = %v, %v; want commit %s", ref, err, commit)
	}
	c, err := remote.CommitObject(ref.Hash())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := c.Parent(0)
	if err != nil || parent.Hash.String() != co.Head {
		t.Fatalf("the fix's parent = %v, %v; want the head it was located in, %s", parent, err, co.Head)
	}
	stats, err := c.Stats()
	if err != nil || len(stats) != 1 || stats[0].Name != "kustomize/deployment.yaml" || stats[0].Addition != 1 || stats[0].Deletion != 1 {
		t.Fatalf("the fix changes %+v, %v; want one line of kustomize/deployment.yaml", stats, err)
	}
	f, err := c.File("kustomize/deployment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if content, _ := f.Contents(); content != string(edit.After) {
		t.Fatal("the pushed file is not the fix")
	}
}
