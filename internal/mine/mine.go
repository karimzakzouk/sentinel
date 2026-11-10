// Package mine learns repair patterns from a repository's own history:
// recent commits that look like fixes, as before/after source snippets the
// Fixer can try where a crash matches the before.
package mine

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// DefaultCap is how many recent commits Mine scans when Cap is unset.
const DefaultCap = 50

const (
	maxPatterns     = 30
	maxSnippetBytes = 2 << 10
	maxSnippetLines = 40
	contextLines    = 1
)

// Pattern is one repair seen in history: a snippet that used to look like
// Before and was rewritten to After, in Language, with the commit's subject.
type Pattern struct {
	Language      string
	BeforeSnippet string
	AfterSnippet  string
	Message       string
}

// Options bounds a scan. Cap is the most recent commits to walk; PathFilter,
// when set, keeps only files whose path has that prefix.
type Options struct {
	Cap        int
	PathFilter string
}

// fixSubject is a commit that names a fix in its first line.
var fixSubject = regexp.MustCompile(`(?i)\b(fix|bug|crash|nil|npe|oom|revert)\b`)

// sourceExt maps a file extension to the language codefixers use.
var sourceExt = map[string]string{
	".go": "go", ".py": "python",
	".js": "node", ".mjs": "node", ".cjs": "node", ".jsx": "node",
	".ts": "node", ".mts": "node", ".cts": "node", ".tsx": "node",
	".java": "java", ".kt": "java", ".scala": "java",
	".cs": "dotnet", ".rb": "ruby", ".php": "php", ".rs": "rust",
	".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".c++": "cpp",
	".hh": "cpp", ".hpp": "cpp", ".hxx": "cpp",
	".swift": "swift",
}

// Mine scans a repository on disk for repair patterns.
func Mine(repoPath string, opts Options) ([]Pattern, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", repoPath, err)
	}
	return MineRepo(repo, opts)
}

// MineRepo scans repo's current branch tip back Cap commits for patterns.
func MineRepo(repo *git.Repository, opts Options) ([]Pattern, error) {
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	return MineFrom(repo, head.Hash(), opts)
}

// MineFrom walks first parents from start, Cap commits deep.
func MineFrom(repo *git.Repository, start plumbing.Hash, opts Options) ([]Pattern, error) {
	if opts.Cap <= 0 {
		opts.Cap = DefaultCap
	}
	commit, err := repo.CommitObject(start)
	if err != nil {
		return nil, err
	}
	var out []Pattern
	seen := map[string]bool{}
	for range opts.Cap {
		if len(out) >= maxPatterns {
			break
		}
		patterns, err := patternsFrom(commit, opts.PathFilter)
		if err != nil {
			return out, err
		}
		for _, p := range patterns {
			key := p.Language + "\x00" + p.BeforeSnippet + "\x00" + p.AfterSnippet
			if seen[key] || p.BeforeSnippet == "" || p.AfterSnippet == "" || p.BeforeSnippet == p.AfterSnippet {
				continue
			}
			seen[key] = true
			out = append(out, p)
			if len(out) >= maxPatterns {
				break
			}
		}
		if commit.NumParents() == 0 {
			break
		}
		parent, err := commit.Parent(0)
		if err != nil {
			return out, err
		}
		commit = parent
	}
	return out, nil
}

func patternsFrom(commit *object.Commit, pathFilter string) ([]Pattern, error) {
	if commit.NumParents() == 0 {
		return nil, nil
	}
	parent, err := commit.Parent(0)
	if err != nil {
		return nil, err
	}
	pt, err := parent.Tree()
	if err != nil {
		return nil, err
	}
	ct, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := object.DiffTree(pt, ct)
	if err != nil {
		return nil, err
	}
	subject := strings.TrimSpace(strings.Split(commit.Message, "\n")[0])
	keyword := fixSubject.MatchString(subject)

	type fileDiff struct {
		lang          string
		before, after string
	}
	var files []fileDiff
	for _, ch := range changes {
		from, to, err := ch.Files()
		if err != nil {
			continue
		}
		name := ch.To.Name
		if name == "" {
			name = ch.From.Name
		}
		if pathFilter != "" && !strings.HasPrefix(name, pathFilter) {
			continue
		}
		lang := sourceExt[path.Ext(name)]
		if lang == "" || strings.Contains(name, "vendor/") || strings.Contains(name, "node_modules/") {
			continue
		}
		var before, after string
		if from != nil {
			if s, err := from.Contents(); err == nil {
				before = s
			}
		}
		if to != nil {
			if s, err := to.Contents(); err == nil {
				after = s
			}
		}
		if before == "" || after == "" || before == after {
			continue
		}
		bSnip, aSnip, ok := changedSnippet(before, after)
		if !ok {
			continue
		}
		files = append(files, fileDiff{lang: lang, before: bSnip, after: aSnip})
	}
	if len(files) == 0 {
		return nil, nil
	}
	// Keyword commits contribute every source change; otherwise only a
	// commit that touches one source file in one region (a single function
	// body, in practice) is kept.
	if !keyword && len(files) != 1 {
		return nil, nil
	}
	out := make([]Pattern, 0, len(files))
	for _, f := range files {
		out = append(out, Pattern{
			Language: f.lang, BeforeSnippet: f.before, AfterSnippet: f.after, Message: subject,
		})
	}
	return out, nil
}

// changedSnippet is the one changed region of a file: lines that differ,
// with a little context, after dropping a shared prefix and suffix. A file
// with more than one separated change, or a region too large, is skipped.
func changedSnippet(before, after string) (string, string, bool) {
	bl := strings.Split(before, "\n")
	al := strings.Split(after, "\n")
	pre := 0
	for pre < len(bl) && pre < len(al) && bl[pre] == al[pre] {
		pre++
	}
	suf := 0
	for suf < len(bl)-pre && suf < len(al)-pre && bl[len(bl)-1-suf] == al[len(al)-1-suf] {
		suf++
	}
	bMid := bl[pre : len(bl)-suf]
	aMid := al[pre : len(al)-suf]
	if len(bMid) == 0 && len(aMid) == 0 {
		return "", "", false
	}
	if len(bMid) > maxSnippetLines || len(aMid) > maxSnippetLines {
		return "", "", false
	}
	// A second gap of equal lines inside the middles means more than one
	// change region; keep only a single contiguous body change.
	if separatedChanges(bMid, aMid) {
		return "", "", false
	}
	bFrom, aFrom := pre, pre
	bTo, aTo := len(bl)-suf, len(al)-suf
	for range contextLines {
		if bFrom > 0 {
			bFrom--
		}
		if aFrom > 0 {
			aFrom--
		}
		if bTo < len(bl) {
			bTo++
		}
		if aTo < len(al) {
			aTo++
		}
	}
	bSnip := strings.Join(bl[bFrom:bTo], "\n")
	aSnip := strings.Join(al[aFrom:aTo], "\n")
	if len(bSnip) > maxSnippetBytes || len(aSnip) > maxSnippetBytes || bSnip == aSnip {
		return "", "", false
	}
	return bSnip, aSnip, true
}

// separatedChanges reports whether the two middles look like two or more
// edits with a shared unchanged run between them.
func separatedChanges(bMid, aMid []string) bool {
	if len(bMid) < 3 || len(aMid) < 3 {
		return false
	}
	shared := map[string]bool{}
	for _, l := range aMid {
		shared[l] = true
	}
	gaps := 0
	inGap := false
	for _, l := range bMid {
		if shared[l] && strings.TrimSpace(l) != "" {
			if !inGap {
				gaps++
				inGap = true
			}
		} else {
			inGap = false
		}
	}
	return gaps >= 2
}
