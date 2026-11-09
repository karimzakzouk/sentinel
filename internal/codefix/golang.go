// Package codefix proposes a change to an application's source for a
// crash that a new image brought: it finds, among the functions the crash's
// stack ran through, the one the culprit commit changed, and puts it back
// as it was before that commit. It never writes new code, and abstains when
// the stack and the commit do not meet in exactly one function. Go first.
package codefix

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// GoFunc is a function a Go stack frame names: its package's import path,
// the receiver's type for a method, and its name.
type GoFunc struct {
	Package, Receiver, Name string
}

func (f GoFunc) String() string {
	if f.Receiver != "" {
		return fmt.Sprintf("%s.(%s).%s", f.Package, f.Receiver, f.Name)
	}
	return f.Package + "." + f.Name
}

// ParseGoFrame reads a Go frame's function as a panic prints it:
// "main.(*server).handle", "github.com/x/app/cart.(*Store).Get.func1",
// "github.com/x/app/cart.Load[...]". A closure stands for the function it
// is in.
func ParseGoFrame(fn string) (GoFunc, bool) {
	slash := strings.LastIndexByte(fn, '/')
	dot := strings.IndexByte(fn[slash+1:], '.')
	if dot < 0 {
		return GoFunc{}, false
	}
	f := GoFunc{Package: fn[:slash+1+dot]}
	rest := fn[slash+1+dot+1:]
	if strings.HasPrefix(rest, "(") {
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return GoFunc{}, false
		}
		f.Receiver = strings.TrimPrefix(rest[1:end], "*")
		if i := strings.IndexByte(f.Receiver, '['); i >= 0 {
			f.Receiver = f.Receiver[:i]
		}
		rest = strings.TrimPrefix(rest[end+1:], ".")
	}
	name, _, _ := strings.Cut(rest, ".")
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	f.Name = name
	return f, f.Name != ""
}

// span is where a function's declaration sits in a file.
type span struct {
	file       string
	start, end int // byte offsets
	line       int
}

// findGo finds a function's declaration among a repository's Go files, the
// package mapped to a directory through the module's path. Package main
// may be any directory, so it is found by the function in it.
func findGo(files map[string][]byte, module string, f GoFunc) ([]span, error) {
	var dirs []string
	switch {
	case f.Package == "main":
		dirs = nil // every directory, below
	case f.Package == module:
		dirs = []string{"."}
	case strings.HasPrefix(f.Package, module+"/"):
		dirs = []string{strings.TrimPrefix(f.Package, module+"/")}
	default:
		return nil, fmt.Errorf("%s is not in module %s", f.Package, module)
	}
	var out []span
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if dirs != nil && path.Dir(name) != dirs[0] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, files[name], parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if f.Package == "main" && file.Name.Name != "main" {
			continue
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != f.Name || receiverName(fd) != f.Receiver {
				continue
			}
			start := fd.Pos()
			if fd.Doc != nil {
				start = fd.Doc.Pos()
			}
			out = append(out, span{file: name, start: fset.Position(start).Offset, end: fset.Position(fd.End()).Offset,
				line: fset.Position(fd.Pos()).Line})
		}
	}
	return out, nil
}

func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// Patch is a change to one file of an application's source.
type Patch struct {
	Path          string
	Line          int
	Before, After []byte
	Function      string
	Why           string
}

// ErrNoMeeting reports that the crash's stack and the culprit commit share
// no function the Fixer can put back.
var ErrNoMeeting = errors.New("the crash ran through no function the culprit commit changed")

// ProposeGo puts back, as it was before the culprit commit, the innermost
// function of the application's own that the crash ran through and that
// commit changed. good and bad are the repository's files at the commits
// the previous image and the failing one were built from.
func ProposeGo(frames []logs.Frame, good, bad map[string][]byte) (*Patch, error) {
	module := modulePath(bad["go.mod"])
	if module == "" {
		return nil, errors.New("the repository has no go.mod naming its module")
	}
	var unchanged []string
	for _, fr := range frames {
		if !fr.InApp {
			continue
		}
		f, ok := ParseGoFrame(fr.Function)
		if !ok {
			continue
		}
		now, err := findGo(bad, module, f)
		if err != nil || len(now) == 0 {
			continue
		}
		if len(now) > 1 {
			return nil, fmt.Errorf("%d functions in the repository are %s", len(now), f)
		}
		before, err := findGo(good, module, f)
		if err != nil {
			return nil, err
		}
		if len(before) != 1 || before[0].file != now[0].file {
			return nil, fmt.Errorf("%s was not in %s before the culprit commit, so there is nothing to put back", f, now[0].file)
		}
		was := good[before[0].file][before[0].start:before[0].end]
		is := bad[now[0].file][now[0].start:now[0].end]
		if bytes.Equal(was, is) {
			unchanged = append(unchanged, f.Name)
			continue
		}
		src := bad[now[0].file]
		after := append(append(append([]byte{}, src[:now[0].start]...), was...), src[now[0].end:]...)
		if _, err := format.Source(after); err != nil {
			return nil, fmt.Errorf("putting %s back does not parse: %w", f, err)
		}
		return &Patch{Path: now[0].file, Line: now[0].line, Before: src, After: after, Function: f.String(),
			Why: fmt.Sprintf("the crash ran through %s, which the culprit commit changed; this puts it back as it was", f)}, nil
	}
	if len(unchanged) > 0 {
		return nil, fmt.Errorf("%w: it ran through %s, unchanged", ErrNoMeeting, strings.Join(unchanged, ", "))
	}
	return nil, fmt.Errorf("%w: none of its frames is in the repository", ErrNoMeeting)
}

func modulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}
