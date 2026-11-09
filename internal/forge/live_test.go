//go:build live

package forge_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/go-git/go-billy/v5/memfs"

	"github.com/karimzakzouk/sentinel/internal/forge"
)

// TestLiveForge runs the Fixer's whole conversation with a real forge: it
// pushes a branch as the Fixer pushes, opens a pull request, comments,
// merges it and reads back the merge commit, which must be on the base
// branch. hack/forge-live.sh starts a forge and runs it:
//
//	FORGE_KIND=gitlab FORGE_WEB=... FORGE_API=... FORGE_REPO=... FORGE_TOKEN_FILE=... \
//	  go test -tags live -run TestLiveForge ./internal/forge/
func TestLiveForge(t *testing.T) {
	token, err := os.ReadFile(os.Getenv("FORGE_TOKEN_FILE"))
	if err != nil {
		t.Skip("no FORGE_TOKEN_FILE")
	}
	c := &forge.Client{Kind: forge.Kind(os.Getenv("FORGE_KIND")), Web: os.Getenv("FORGE_WEB"), API: os.Getenv("FORGE_API"),
		Token: strings.TrimSpace(string(token))}
	repoURL, user := os.Getenv("FORGE_REPO"), os.Getenv("FORGE_USER")
	owner, name, err := c.Repo(repoURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	auth := &githttp.BasicAuth{Username: user, Password: c.Token}
	repo, err := git.CloneContext(ctx, memory.NewStorage(), memfs.New(), &git.CloneOptions{URL: repoURL, Auth: auth, ReferenceName: "refs/heads/main"})
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	branch := "sentinel/live-" + time.Now().UTC().Format("20060102-150405")
	if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Create: true}); err != nil {
		t.Fatal(err)
	}
	f, err := wt.Filesystem.Create("fix.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("fixed at " + branch + "\n"))
	_ = f.Close()
	if _, err := wt.Add("fix.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("Fix it", &git.CommitOptions{Author: &object.Signature{Name: "Sentinel", Email: "sentinel@localhost", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	spec := config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	if err := repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}, Auth: auth}); err != nil {
		t.Fatalf("push as %s: %v", user, err)
	}

	pr, err := c.Open(ctx, owner, name, branch, "main", "Fix it", "Sentinel's live test")
	if err != nil || pr.Number == 0 || pr.State != "open" || pr.URL == "" {
		t.Fatalf("Open = %+v, %v", pr, err)
	}
	t.Logf("opened %s", pr.URL)
	if err := c.Comment(ctx, owner, name, pr.Number, "The Lab's verdict would go here."); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	// A forge may still be checking whether a new pull request can merge.
	for try := 0; ; try++ {
		err := c.Merge(ctx, owner, name, pr.Number)
		if err == nil {
			break
		}
		if try == 20 {
			t.Fatalf("Merge: %v", err)
		}
		time.Sleep(3 * time.Second)
	}
	var got forge.PullRequest
	for try := 0; try < 20; try++ {
		if got, err = c.Get(ctx, owner, name, pr.Number); err == nil && got.Merged && got.MergeCommit != "" {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !got.Merged || got.State != "closed" || got.MergeCommit == "" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := repo.FetchContext(ctx, &git.FetchOptions{Auth: auth, RefSpecs: []config.RefSpec{"refs/heads/main:refs/remotes/origin/main"}}); err != nil &&
		err != git.NoErrAlreadyUpToDate {
		t.Fatalf("fetch main: %v", err)
	}
	head, err := repo.Reference("refs/remotes/origin/main", true)
	if err != nil {
		t.Fatal(err)
	}
	commits, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	_ = commits.ForEach(func(c *object.Commit) error {
		found = found || c.Hash.String() == got.MergeCommit
		return nil
	})
	if !found {
		t.Fatalf("merge commit %s is not on main", got.MergeCommit)
	}
	t.Logf("merged as %s, on main", got.MergeCommit)
}
