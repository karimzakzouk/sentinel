package fixer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// maxRepoBytes bounds what a checkout copies for rendering: a repository
// of manifests is small, and a bigger one is not what the Fixer edits.
const maxRepoBytes = 64 << 20

// Checkout is the head of a branch, checked out in memory.
type Checkout struct {
	repo   *git.Repository
	tree   billy.Filesystem
	branch string
	auth   transport.AuthMethod
	// Head is the commit checked out.
	Head string
}

// Clone checks out the head of a branch in memory, without its history.
func Clone(ctx context.Context, url, branch string, auth transport.AuthMethod) (*Checkout, error) {
	tree := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), tree, &git.CloneOptions{
		URL: url, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true, Depth: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("clone %s at %s: %w", url, branch, err)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	return &Checkout{repo: repo, tree: tree, branch: branch, auth: auth, Head: head.Hash().String()}, nil
}

// Files copies the checkout into a filesystem Kustomize reads, with the
// repository's root at "/".
func (c *Checkout) Files() (filesys.FileSystem, error) {
	files := filesys.MakeFsInMemory()
	total := int64(0)
	err := util.Walk(c.tree, "/", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if total += info.Size(); total > maxRepoBytes {
			return fmt.Errorf("the repository holds more than %d MiB", maxRepoBytes>>20)
		}
		f, err := c.tree.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return files.WriteFile(path.Join("/", p), data)
	})
	return files, err
}

// Push commits the edit on a new branch from the checked out head, as
// author, and pushes the branch. It returns the commit.
func (c *Checkout) Push(ctx context.Context, edit *Edit, branch, message string, author object.Signature) (string, error) {
	w, err := c.repo.Worktree()
	if err != nil {
		return "", err
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Create: true}); err != nil {
		return "", err
	}
	if err := util.WriteFile(c.tree, edit.Path, edit.After, 0o644); err != nil {
		return "", err
	}
	if _, err := w.Add(edit.Path); err != nil {
		return "", err
	}
	if author.When.IsZero() {
		author.When = time.Now()
	}
	hash, err := w.Commit(message, &git.CommitOptions{Author: &author})
	if err != nil {
		return "", err
	}
	spec := config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	if err := c.repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}, Auth: c.auth}); err != nil {
		return "", fmt.Errorf("push %s: %w", branch, err)
	}
	return hash.String(), nil
}
