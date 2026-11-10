package fixer

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/codefix"
	"github.com/karimzakzouk/sentinel/internal/forge"
	"github.com/karimzakzouk/sentinel/internal/logs"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/verify"
)

// codeClasses are the faults a new image's code can cause. An image that
// cannot be pulled has no code to fix.
var codeClasses = map[string]bool{"deploy.crash-on-start": true, "runtime.crash": true, "deploy.new-errors": true}

// buildWait is how long the Fixer looks for the image the source's
// pipeline builds from a code fix's commit.
const buildWait = time.Hour

// codeFix proposes, beside the fix that pins the image back, a fix to the
// failing image's code: the first change the fixers propose, in order, that
// applies at the source's head. It returns nil when code fixes are off or
// the fault is not in code, and a CodeFix whose message says why when no
// fixer has a change.
func (f *Fixer) codeFix(ctx context.Context, fix *v1alpha1.Fix, change Change) *v1alpha1.CodeFix {
	if f.Registry == nil || change.field() != fieldImage || !codeClasses[fix.Spec.FaultClass] {
		return nil
	}
	bad, _ := change.Bad.(string)
	good, _ := change.Good.(string)
	cf := &v1alpha1.CodeFix{Failing: bad, Previous: good}
	src, why := f.codeSource(ctx, fix, good, bad)
	if src == nil {
		cf.Message = why
		return cf
	}
	cf.Repository, cf.Revision = src.bad.Repository, src.bad.Revision
	c, why := f.nextChange(ctx, src, cf)
	if c == nil {
		cf.Message = why
		return cf
	}
	if err := f.openCode(ctx, fix, cf, src, c, 1); err != nil {
		cf.Message = err.Error()
	}
	return cf
}

// codeSource is what every fixer works from: the source both images were
// built from, at the previous image's commit, the failing one's and the
// branch's head.
type codeSource struct {
	co          *Checkout
	owner, repo string
	good, bad   codefix.Built
	goodImage   string
	in          codefix.Input
	head        map[string][]byte
}

func (f *Fixer) codeSource(ctx context.Context, fix *v1alpha1.Fix, good, bad string) (*codeSource, string) {
	badSrc, err := f.source(ctx, bad)
	if err != nil {
		return nil, err.Error()
	}
	goodSrc, err := f.source(ctx, good)
	if err != nil {
		return nil, err.Error()
	}
	if goodSrc.Repository != badSrc.Repository {
		return nil, fmt.Sprintf("%s and %s come from different repositories, %s and %s", good, bad, goodSrc.Repository, badSrc.Repository)
	}
	owner, repo, err := f.Forge.Repo(badSrc.Repository)
	if err != nil {
		return nil, fmt.Sprintf("%v, the only forge the Fixer may write to", err)
	}
	co, err := CloneHistory(ctx, badSrc.Repository, "", f.auth())
	if err != nil {
		return nil, err.Error()
	}
	before, err := co.FilesAt(goodSrc.Revision)
	if err != nil {
		return nil, err.Error()
	}
	failing, err := co.FilesAt(badSrc.Revision)
	if err != nil {
		return nil, err.Error()
	}
	head, err := co.FilesAt(co.Head)
	if err != nil {
		return nil, err.Error()
	}
	in := codefix.Input{Good: before, Bad: failing}
	if crash := fix.Spec.Crash; crash != nil {
		in.Language, in.Types, in.Message, in.Frames = crash.Language, crash.Types, crash.Message, crashFrames(crash)
	}
	// Without a stack, the source that logs the failing revision's errors
	// stands in for one; and a revert needs neither.
	if len(in.Frames) == 0 && len(fix.Spec.Errors) > 0 {
		frames, language := codefix.LoggedAt(fix.Spec.Errors, failing)
		in.Frames = frames
		if in.Language == "" {
			in.Language = language
		}
	}
	return &codeSource{co: co, owner: owner, repo: repo, good: goodSrc, bad: badSrc, goodImage: good, head: head, in: in}, ""
}

// Fixer names, in the order they are tried by default.
var defaultCodeFixers = []string{"restore", "template", "revert", "llm"}

func (f *Fixer) codeFixers() []string {
	if len(f.CodeFixers) > 0 {
		return f.CodeFixers
	}
	return defaultCodeFixers
}

// nextChange is the first change, in the fixers' order, not tried yet that
// applies at the branch's head; the model is asked only when the fixers
// that never write new code have none left. It says why when there is
// none.
func (f *Fixer) nextChange(ctx context.Context, src *codeSource, cf *v1alpha1.CodeFix) (*codefix.Change, string) {
	var fixers []codefix.Fixer
	useLLM := false
	for _, name := range f.codeFixers() {
		switch name {
		case "restore":
			fixers = append(fixers, codefix.Restorer{})
		case "template":
			fixers = append(fixers, codefix.Templates{})
		case "revert":
			fixers = append(fixers, codefix.Reverter{})
		case "llm":
			useLLM = f.LLM != nil
		}
	}
	changes, refusals := codefix.Fixers(ctx, src.in, fixers...)
	for _, c := range changes {
		if tried(cf, c) {
			continue
		}
		atHead, err := codefix.Rebase(c, src.head)
		if err != nil {
			refusals = append(refusals, c.Fixer+": "+err.Error())
			continue
		}
		return atHead, ""
	}
	if useLLM && llmAttempts(cf) < max(1, f.LLMAttempts) {
		changes, err := codefix.LLM{Model: f.LLM, Retries: 2, Tried: describeTried(cf)}.Propose(ctx, src.in)
		if err == nil {
			atHead, err := codefix.Rebase(changes[0], src.head)
			if err == nil {
				return atHead, ""
			}
			refusals = append(refusals, "llm: "+err.Error())
		} else {
			refusals = append(refusals, "llm: "+err.Error())
		}
	}
	if len(refusals) == 0 {
		return nil, "every fixer's change has been tried"
	}
	return nil, "no fixer has a change to try: " + codefix.Explain(refusals)
}

// tried reports whether a change is the current one or an earlier one.
func tried(cf *v1alpha1.CodeFix, c *codefix.Change) bool {
	summary := strings.Join(c.Summary(), "\n")
	if cf.Fixer == c.Fixer && strings.Join(cf.Change, "\n") == summary {
		return true
	}
	for _, t := range cf.Tried {
		if t.Fixer == c.Fixer && strings.Join(t.Change, "\n") == summary {
			return true
		}
	}
	return false
}

func llmAttempts(cf *v1alpha1.CodeFix) int {
	n := 0
	if cf.Fixer == "llm" {
		n++
	}
	for _, t := range cf.Tried {
		if t.Fixer == "llm" {
			n++
		}
	}
	return n
}

// describeTried tells a model what was tried and why it was not enough.
func describeTried(cf *v1alpha1.CodeFix) []string {
	var out []string
	add := func(fixer string, change []string, verdict, msg string) {
		if fixer != "" {
			out = append(out, fmt.Sprintf("%s (%s): %s: %s", fixer, strings.Join(change, "; "), verdict, msg))
		}
	}
	for _, t := range cf.Tried {
		add(t.Fixer, t.Change, t.Verdict, t.Message)
	}
	if cf.Verdict != "" && cf.Verdict != "Passed" {
		add(cf.Fixer, cf.Change, cf.Verdict, cf.Message)
	}
	return out
}

// codeBranch is the branch of a code fix's attempt: one for the pull
// request's changes, a new one after a person closed it.
func codeBranch(fix *v1alpha1.Fix, pr int) string {
	b := "sentinel/code-fix-" + shortID(fix.Spec.IncidentID)
	if pr > 1 {
		b += fmt.Sprintf("-%d", pr)
	}
	return b
}

// openCode pushes a change to a new branch and opens its pull request.
func (f *Fixer) openCode(ctx context.Context, fix *v1alpha1.Fix, cf *v1alpha1.CodeFix, src *codeSource, c *codefix.Change, pr int) error {
	title := fmt.Sprintf("Fix the crash in %s that %s brought (%s)", lastPart(c.Function), short(src.bad.Revision), c.Fixer)
	if c.Fixer == "restore" {
		title = fmt.Sprintf("Put %s back as it was before %s", lastPart(c.Function), short(src.bad.Revision))
	}
	message := fmt.Sprintf("%s\n\nThe crash that made Sentinel roll %s/%s back ran through %s. %s. Incident %s.\n",
		title, fix.Spec.Workload.Namespace, fix.Spec.Workload.Name, c.Function, c.Why, fix.Spec.IncidentID)
	branch := codeBranch(fix, pr)
	commit, err := src.co.PushChange(ctx, c, branch, message, f.Author)
	if err != nil {
		return err
	}
	opened, err := f.Forge.Open(ctx, src.owner, src.repo, branch, src.co.Branch, title, codeBody(fix, c, src.goodImage, cf.Failing, src.bad, len(f.LabNamespaces) > 0))
	if err != nil {
		return err
	}
	cf.PullRequest, cf.Number, cf.State = opened.URL, int32(min(opened.Number, 1<<30)), "open" //nolint:gosec // bounded just above
	f.attempt(cf, c, commit)
	return nil
}

// attempt makes c the code fix's current change, at commit.
func (f *Fixer) attempt(cf *v1alpha1.CodeFix, c *codefix.Change, commit string) {
	cf.Attempt++
	cf.Fixer, cf.Function, cf.Change, cf.Commit = c.Fixer, c.Function, c.Summary(), commit
	cf.Image, cf.Verdict = "", ""
	cf.Message = fmt.Sprintf("proposed by %s; waiting for the source's pipeline to build an image of %s", c.Fixer, short(commit))
}

// advance moves a code fix on from its current change, which the Lab found
// does not fix the crash or a person closed, to the next fixer's: on the
// same pull request, or a new one when the old one is closed. It says so
// on the pull request, and why there is nothing left when there is not.
func (f *Fixer) advance(ctx context.Context, fix *v1alpha1.Fix, verdict, why string) {
	cf := fix.Status.Code
	cf.Tried = append(cf.Tried, v1alpha1.CodeAttempt{Fixer: cf.Fixer, Change: cf.Change, PullRequest: cf.PullRequest, Commit: cf.Commit,
		Image: cf.Image, Verdict: verdict, Message: why})
	if len(cf.Tried) > 10 {
		cf.Tried = cf.Tried[len(cf.Tried)-10:]
	}
	// The change is in Tried now; it is no longer the current one.
	cf.Fixer, cf.Change, cf.Function = "", nil, ""
	declined := verdict == "Declined"
	src, msg := f.codeSource(ctx, fix, cf.Previous, cf.Failing)
	if src == nil {
		cf.Message = "cannot try another fix: " + msg
		return
	}
	c, reason := f.nextChange(ctx, src, cf)
	if c == nil {
		cf.Message = "no other fix to try: " + reason
		if !declined {
			f.say(ctx, cf, "No fixer has another change to try: "+reason+". A person takes it from here.")
		}
		return
	}
	if declined {
		if err := f.openCode(ctx, fix, cf, src, c, prNumberOf(cf)); err != nil {
			cf.Message = "could not open another fix: " + err.Error()
		}
		return
	}
	commit, err := src.co.PushChange(ctx, c, codeBranch(fix, prNumberOf(cf)), fmt.Sprintf("Try %s instead\n\n%s.\n", c.Fixer, c.Why), f.Author)
	if err != nil {
		cf.Message = "could not push the next fix: " + err.Error()
		return
	}
	f.attempt(cf, c, commit)
	f.say(ctx, cf, fmt.Sprintf("That change did not fix the crash (%s). Sentinel now tries **%s** on this pull request:\n\n- %s\n\n%s.\n\nThe Lab races it once your pipeline builds it.",
		why, c.Fixer, strings.Join(c.Summary(), "\n- "), c.Why))
}

// prNumberOf counts the pull requests a code fix has opened, the current
// one included: one more after each a person closed.
func prNumberOf(cf *v1alpha1.CodeFix) int {
	n := 1
	for _, t := range cf.Tried {
		if t.Verdict == "Declined" {
			n++
		}
	}
	return n
}

// say comments on the code fix's pull request, as far as the forge allows.
func (f *Fixer) say(ctx context.Context, cf *v1alpha1.CodeFix, body string) {
	owner, repo, err := f.Forge.Repo(cf.Repository)
	if err != nil || cf.Number == 0 {
		return
	}
	if err := f.Forge.Comment(ctx, owner, repo, int(cf.Number), body); err != nil {
		f.Log.Error("could not comment on a fix to the code", "pullRequest", cf.PullRequest, "error", err)
	}
}

// codeClosed follows the code fix's pull request: once merged it is done,
// and once a person closes it unmerged the Fixer tries the next fixer's
// change in a new one. It reports whether the status changed.
func (f *Fixer) codeClosed(ctx context.Context, fix *v1alpha1.Fix) bool {
	cf := fix.Status.Code
	if cf == nil || cf.Number == 0 || cf.State != "open" {
		return false
	}
	owner, repo, err := f.Forge.Repo(cf.Repository)
	if err != nil {
		return false
	}
	pr, err := f.Forge.Get(ctx, owner, repo, int(cf.Number))
	switch {
	case err != nil:
		return false
	case pr.Merged:
		cf.State, cf.Message = "merged", "merged as "+pr.MergeCommit
		return true
	case pr.State == "closed":
		cf.State = "closed"
		f.advance(ctx, fix, "Declined", "a person closed the pull request without merging it")
		return true
	}
	return false
}

// crashFrames are the crash's frames, with where each ran when the runtime
// printed it.
func crashFrames(crash *v1alpha1.Crash) []logs.Frame {
	if crash == nil {
		return nil
	}
	if len(crash.Stack) > 0 {
		frames := make([]logs.Frame, 0, len(crash.Stack))
		for _, s := range crash.Stack {
			frames = append(frames, logs.Frame{Function: s.Function, File: s.File, Line: int(s.Line), InApp: true})
		}
		return frames
	}
	frames := make([]logs.Frame, 0, len(crash.Frames))
	for _, fn := range crash.Frames {
		frames = append(frames, logs.Frame{Function: fn, InApp: true})
	}
	return frames
}

func (f *Fixer) source(ctx context.Context, image string) (codefix.Built, error) {
	labels, err := f.Registry.Labels(ctx, image)
	if err != nil {
		if b, err2 := codefix.ResolveSourceWith(image, nil, f.Sources); err2 == nil {
			return b, nil
		}
		return codefix.Built{}, fmt.Errorf("could not read %s's labels: %w", image, err)
	}
	return codefix.ResolveSourceWith(image, labels, f.Sources)
}

func lastPart(fn string) string {
	if i := strings.LastIndexByte(fn, '.'); i >= 0 {
		return fn[i+1:]
	}
	return fn
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

func codeBody(fix *v1alpha1.Fix, c *codefix.Change, good, bad string, src codefix.Built, raced bool) string {
	w := fix.Spec.Workload
	checked := "It is not checked yet: once your pipeline builds an image from this branch, Sentinel races it against the failing one in its Lab and says here whether it fixes the crash. If it does not, Sentinel tries the next fixer's change on this pull request."
	if !raced {
		checked = "It is not checked: no Lab is installed to race an image built from this branch against the failing one."
	}
	what := "This pull request puts that function back as it was before the commit, and leaves the commit's other changes as they are."
	switch {
	case strings.HasPrefix(c.Fixer, "template:"):
		what = "This pull request changes only the line that crashed, or guards it, with a fixed template (" + strings.TrimPrefix(c.Fixer, "template:") + "). A guard can hide a bug as well as fix it: read it as a stopgap."
	case c.Fixer == "revert":
		what = "This pull request undoes the culprit commit: every file it changed, put back as it was."
	case c.Fixer == "llm":
		what = "This pull request's change was written by a language model, after the fixers that never write new code had none that held. Read it as you would a stranger's: it never merges itself."
	}
	return fmt.Sprintf(`Sentinel rolled **%s/%s** back from %s to %s after it crashed. The crash's stack ran through %s, and commit %s changed it.

%s %s

- **What changes**: %s.
- **Why there**: %s.

Incident %s.
`, w.Namespace, w.Name, "`"+bad+"`", "`"+good+"`", "`"+c.Function+"`", short(src.Revision), what, checked,
		strings.Join(c.Summary(), "; "), c.Why, fix.Spec.IncidentID)
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
		if len(f.LabNamespaces) == 0 {
			cf.Message = "built as " + image + "; no Lab is installed to race it, so it is not checked"
		}
		return true
	}
	if opened := fix.CreationTimestamp.Time; !opened.IsZero() && now.Sub(opened) > buildWait {
		cf.Message = fmt.Sprintf("no image was built from %s within %s", short(cf.Commit), buildWait)
		return true
	}
	return false
}

// reportVerdict says on the code fix's pull request what the Lab found,
// once it has raced the built image against the failing one. It reports
// whether the status changed.
func (f *Fixer) reportVerdict(ctx context.Context, fix *v1alpha1.Fix) bool {
	cf := fix.Status.Code
	if cf == nil || cf.Image == "" || cf.Verdict != "" || cf.Number == 0 || len(f.LabNamespaces) == 0 {
		return false
	}
	name := verify.CodeExperimentName(model.ID(fix.Spec.IncidentID), cf.Image)
	for _, ns := range f.LabNamespaces {
		u, err := f.Dynamic.Resource(verify.ExperimentsResource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
		msg, _, _ := unstructured.NestedString(u.Object, "status", "message")
		runs, _, _ := unstructured.NestedInt64(u.Object, "status", "runs")
		switch phase {
		case "Passed", "Failed", "NotSure", "Aborted":
		default:
			return false
		}
		owner, repo, err := f.Forge.Repo(cf.Repository)
		if err != nil {
			return false
		}
		word := "it could not tell"
		switch phase {
		case "Passed":
			word = "it fixes the crash"
		case "Failed":
			word = "it does not fix the crash"
		}
		body := fmt.Sprintf("Sentinel's Lab raced `%s` against the failing image in a sealed copy of the workload: **%s**.\n\n%s\n", cf.Image, word, msg)
		if runs == 0 && phase != "Passed" && phase != "Failed" {
			body = fmt.Sprintf("Sentinel's Lab did not race `%s` against the failing image, so it cannot say whether it fixes the crash.\n\n%s\n", cf.Image, msg)
		}
		cf.Verdict, cf.Message = phase, "the Lab: "+msg
		if err := f.Forge.Comment(ctx, owner, repo, int(cf.Number), body); err != nil {
			f.Log.Error("could not say the Lab's verdict on the pull request", "pullRequest", cf.PullRequest, "error", err)
			if !forge.Refused(err) {
				cf.Verdict = ""
				return false
			}
			cf.Message += "; the forge refused the comment that says so on the pull request: " + err.Error()
		}
		// A change the Lab ran and found wanting gives way to the next
		// fixer's; a Lab that could not run says nothing about the change.
		if phase == "Failed" || phase == "NotSure" && runs > 0 {
			f.advance(ctx, fix, phase, msg)
		}
		return true
	}
	return false
}
