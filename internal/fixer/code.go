package fixer

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/codefix"
	"github.com/karimzakzouk/sentinel/internal/logs"
)

// codeClasses are the faults a new image's code can cause. An image that
// cannot be pulled has no code to fix.
var codeClasses = map[string]bool{"deploy.crash-on-start": true, "runtime.crash": true, "deploy.new-errors": true}

// buildWait is how long the Fixer looks for the image the source's
// pipeline builds from a code fix's commit.
const buildWait = time.Hour

// codeFix proposes, beside the fix that pins the image back, a fix to the
// failing image's code: in the source both images name, the function of
// the crash's stack that the failing image's commit changed, put back as it
// was. It returns nil when code fixes are off or the fault is not in code,
// and a CodeFix whose message says why when it proposes none.
func (f *Fixer) codeFix(ctx context.Context, fix *v1alpha1.Fix, change Change) *v1alpha1.CodeFix {
	if f.Registry == nil || change.field() != fieldImage || !codeClasses[fix.Spec.FaultClass] {
		return nil
	}
	bad, _ := change.Bad.(string)
	good, _ := change.Good.(string)
	cf := &v1alpha1.CodeFix{Failing: bad}
	say := func(format string, args ...any) *v1alpha1.CodeFix {
		cf.Message = fmt.Sprintf(format, args...)
		return cf
	}
	crash := fix.Spec.Crash
	switch {
	case crash == nil || len(crash.Frames) == 0:
		return say("the failing revision's logs held no crash stack to start a fix to its code from")
	case crash.Language != "go":
		return say("fixes to code are for Go so far, and this crash is %s", crash.Language)
	}
	badSrc, err := f.source(ctx, bad)
	if err != nil {
		return say("%v", err)
	}
	goodSrc, err := f.source(ctx, good)
	if err != nil {
		return say("%v", err)
	}
	if goodSrc.Repository != badSrc.Repository {
		return say("%s and %s come from different repositories, %s and %s", good, bad, goodSrc.Repository, badSrc.Repository)
	}
	cf.Repository, cf.Revision = badSrc.Repository, badSrc.Revision
	owner, repo, err := f.Forge.Repo(badSrc.Repository)
	if err != nil {
		return say("%v, the only forge the Fixer may write to", err)
	}
	co, err := CloneHistory(ctx, badSrc.Repository, "", f.auth())
	if err != nil {
		return say("%v", err)
	}
	before, err := co.FilesAt(goodSrc.Revision)
	if err != nil {
		return say("%v", err)
	}
	failing, err := co.FilesAt(badSrc.Revision)
	if err != nil {
		return say("%v", err)
	}
	head, err := co.FilesAt(co.Head)
	if err != nil {
		return say("%v", err)
	}
	frames := make([]logs.Frame, 0, len(crash.Frames))
	for _, fn := range crash.Frames {
		frames = append(frames, logs.Frame{Function: fn, InApp: true})
	}
	patch, err := codefix.ProposeGo(frames, before, failing)
	if err != nil {
		return say("%v", err)
	}
	// The branch's head may have moved on; the fix applies there only if
	// the function is still as the failing commit left it.
	atHead, err := codefix.ProposeGo(frames, before, head)
	if err != nil || atHead.Function != patch.Function || !bytes.Equal(funcText(atHead), funcText(patch)) {
		return say("%s changed again after %s; a person reconciles the two", patch.Function, short(badSrc.Revision))
	}
	title := fmt.Sprintf("Put %s back as it was before %s", lastPart(patch.Function), short(badSrc.Revision))
	message := fmt.Sprintf("%s\n\nThe crash that made Sentinel roll %s/%s back ran through %s, which %s changed. Incident %s.\n",
		title, fix.Spec.Workload.Namespace, fix.Spec.Workload.Name, patch.Function, short(badSrc.Revision), fix.Spec.IncidentID)
	branch := "sentinel/code-fix-" + shortID(fix.Spec.IncidentID)
	commit, err := co.Push(ctx, &Edit{Path: atHead.Path, Line: atHead.Line, Before: atHead.Before, After: atHead.After, Why: atHead.Why},
		branch, message, f.Author)
	if err != nil {
		return say("%v", err)
	}
	pr, err := f.Forge.Open(ctx, owner, repo, branch, co.Branch, title, codeBody(fix, patch, good, bad, badSrc))
	if err != nil {
		return say("%v", err)
	}
	cf.Function, cf.PullRequest, cf.Number, cf.Commit = patch.Function, pr.URL, int32(min(pr.Number, 1<<30)), commit //nolint:gosec // bounded just above
	cf.Change = []string{fmt.Sprintf("%s:%d: %s put back as it was before %s", atHead.Path, atHead.Line, patch.Function, short(badSrc.Revision))}
	cf.Message = "proposed; waiting for the source's pipeline to build an image of " + short(commit)
	return cf
}

func (f *Fixer) source(ctx context.Context, image string) (codefix.Built, error) {
	labels, err := f.Registry.Labels(ctx, image)
	if err != nil {
		return codefix.Built{}, fmt.Errorf("could not read %s's labels: %w", image, err)
	}
	return codefix.ResolveSource(image, labels)
}

// funcText is the function a patch puts back, as the file had it.
func funcText(p *codefix.Patch) []byte {
	start, end := commonPrefix(p.Before, p.After), commonSuffix(p.Before, p.After)
	if start+end > len(p.Before) {
		return nil
	}
	return p.Before[start : len(p.Before)-end]
}

func commonPrefix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func commonSuffix(a, b []byte) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

func lastPart(fn string) string {
	if i := strings.LastIndexByte(fn, '.'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

func codeBody(fix *v1alpha1.Fix, p *codefix.Patch, good, bad string, src codefix.Built) string {
	w := fix.Spec.Workload
	return fmt.Sprintf(`Sentinel rolled **%s/%s** back from %s to %s after it crashed. The crash's stack ran through %s, which commit %s changed.

This pull request puts that function back as it was before the commit, and leaves the commit's other changes as they are. It is not checked yet: once your pipeline builds an image from this branch, Sentinel races it against the failing one in its Lab and says here whether it fixes the crash.

- **What changes**: %s, line %d.
- **Why there**: %s.

Incident %s.
`, w.Namespace, w.Name, "`"+bad+"`", "`"+good+"`", "`"+p.Function+"`", short(src.Revision), "`"+p.Path+"`", p.Line, p.Why, fix.Spec.IncidentID)
}

// awaitBuild looks for the image the source's pipeline built from a code
// fix's commit: the failing image's repository, tagged with the commit as
// pipelines tag one, its labels naming the commit. It reports whether the
// status changed.
func (f *Fixer) awaitBuild(ctx context.Context, fix *v1alpha1.Fix, now time.Time) bool {
	cf := fix.Status.Code
	if f.Registry == nil || cf == nil || cf.Commit == "" || cf.Image != "" || strings.HasPrefix(cf.Message, "no image") {
		return false
	}
	ref, err := codefix.ParseReference(cf.Failing)
	if err != nil {
		return false
	}
	name := ref.Registry + "/" + ref.Repository
	if ref.Registry == "registry-1.docker.io" {
		name = ref.Repository
	}
	for _, tag := range []string{cf.Commit[:min(7, len(cf.Commit))], cf.Commit, "sha-" + cf.Commit[:min(7, len(cf.Commit))]} {
		image := name + ":" + tag
		labels, err := f.Registry.Labels(ctx, image)
		if err != nil || !strings.HasPrefix(strings.ToLower(labels["org.opencontainers.image.revision"]), strings.ToLower(cf.Commit)) {
			continue
		}
		cf.Image, cf.Message = image, "built as "+image+"; the Lab races it against the failing image"
		return true
	}
	if opened := fix.CreationTimestamp.Time; !opened.IsZero() && now.Sub(opened) > buildWait {
		cf.Message = fmt.Sprintf("no image was built from %s within %s", short(cf.Commit), buildWait)
		return true
	}
	return false
}
