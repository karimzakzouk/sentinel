package codefix

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// Propose puts back, as it was before the culprit commit, the innermost
// function of the application's own that the crash ran through and that
// commit changed. good and bad are the repository's files at the commits
// the previous image and the failing one were built from. Go is read with
// Go's own parser; the other languages with tree-sitter's, each frame's
// function found where the runtime said it ran, or by name where it named
// only the function. It never writes new code, and abstains when the
// stack and the commit do not meet in exactly one function.
func Propose(language string, frames []logs.Frame, good, bad map[string][]byte) (*Patch, error) {
	if language == "go" {
		return ProposeGo(frames, good, bad)
	}
	exts, ok := languageFiles[language]
	if !ok {
		return nil, fmt.Errorf("fixes to code do not read %s", language)
	}
	var unchanged []string
	for _, fr := range frames {
		if !fr.InApp {
			continue
		}
		now, err := locate(fr, bad, exts)
		if err != nil {
			return nil, err
		}
		if now == nil {
			continue
		}
		before, err := counterpart(now, good)
		if err != nil {
			return nil, err
		}
		if before == nil {
			return nil, fmt.Errorf("%s was not in the source before the culprit commit, so there is nothing to put back", now.name())
		}
		if bytes.Equal(before.text, now.text) {
			unchanged = append(unchanged, now.name())
			continue
		}
		src := bad[now.file]
		after := append(append(append([]byte{}, src[:now.start]...), before.text...), src[now.end:]...)
		if err := stillParses(now.file, src, after); err != nil {
			return nil, fmt.Errorf("putting %s back does not parse: %w", now.name(), err)
		}
		return &Patch{Path: now.file, Line: now.line, Before: src, After: after, Function: now.name(),
			Why: fmt.Sprintf("the crash ran through %s, which the culprit commit changed; this puts it back as it was", now.name())}, nil
	}
	if len(unchanged) > 0 {
		return nil, fmt.Errorf("%w: it ran through %s, unchanged", ErrNoMeeting, strings.Join(unchanged, ", "))
	}
	return nil, fmt.Errorf("%w: none of its frames is in the repository", ErrNoMeeting)
}

// Reads reports whether fixes to code read a crash of language.
func Reads(language string) bool {
	_, ok := languageFiles[language]
	return ok || language == "go"
}

// languageFiles are the source files a runtime's crash may run through.
var languageFiles = map[string][]string{
	"python": {".py"},
	"node":   {".js", ".mjs", ".cjs", ".jsx", ".ts", ".mts", ".cts", ".tsx"},
	"java":   {".java", ".kt", ".scala"},
	"dotnet": {".cs"},
	"ruby":   {".rb"},
	"php":    {".php"},
	"rust":   {".rs"},
	"c":      {".c", ".h"},
	"cpp":    {".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx", ".h", ".c"},
	"swift":  {".swift"},
}

// grammar is what a language's tree-sitter grammar calls a function, and
// the scopes a function's name is qualified by.
type grammar struct {
	funcs, scopes map[string]bool
}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

var jsGrammar = grammar{
	funcs: set("function_declaration", "generator_function_declaration", "method_definition", "function_expression", "arrow_function",
		"generator_function", "function"),
	scopes: set("class_declaration", "class", "abstract_class_declaration", "internal_module", "module"),
}

var grammarsByName = map[string]grammar{
	"go":         {set("function_declaration", "method_declaration"), set()},
	"python":     {set("function_definition"), set("class_definition")},
	"javascript": jsGrammar,
	"typescript": jsGrammar,
	"tsx":        jsGrammar,
	"java": {set("method_declaration", "constructor_declaration", "compact_constructor_declaration"),
		set("class_declaration", "interface_declaration", "enum_declaration", "record_declaration")},
	"kotlin": {set("function_declaration", "secondary_constructor"), set("class_declaration", "object_declaration", "companion_object")},
	"scala":  {set("function_definition"), set("class_definition", "object_definition", "trait_definition")},
	"c_sharp": {set("method_declaration", "constructor_declaration", "local_function_statement", "destructor_declaration", "operator_declaration"),
		set("class_declaration", "struct_declaration", "interface_declaration", "record_declaration", "namespace_declaration",
			"file_scoped_namespace_declaration")},
	"ruby":  {set("method", "singleton_method"), set("class", "module", "singleton_class")},
	"php":   {set("method_declaration", "function_definition"), set("class_declaration", "trait_declaration", "interface_declaration", "enum_declaration", "namespace_definition")},
	"rust":  {set("function_item"), set("impl_item", "trait_item", "mod_item")},
	"c":     {set("function_definition"), set()},
	"cpp":   {set("function_definition"), set("class_specifier", "struct_specifier", "namespace_definition")},
	"swift": {set("function_declaration", "init_declaration"), set("class_declaration", "protocol_declaration")},
}

// fn is a function found in a file: where it is, its name qualified by its
// scopes, and its text.
type fn struct {
	file       string
	start, end int
	line       int
	path       []string
	params     string
	text       []byte
}

func (f *fn) name() string { return strings.Join(f.path, ".") }

// parsed is a file's syntax tree and what its grammar calls functions.
type parsed struct {
	tree *gts.BoundTree
	g    grammar
	src  []byte
}

func parse(file string, src []byte) (*parsed, error) {
	entry := grammars.DetectLanguage(file)
	if entry == nil {
		return nil, fmt.Errorf("no grammar reads %s", file)
	}
	g, ok := grammarsByName[entry.Name]
	if !ok {
		return nil, fmt.Errorf("fixes to code do not read %s files", entry.Name)
	}
	bt, err := grammars.ParseFile(file, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return &parsed{tree: bt, g: g, src: src}, nil
}

func (p *parsed) release() { p.tree.Release() }

func (p *parsed) typ(n *gts.Node) string { return p.tree.NodeType(n) }

func (p *parsed) text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return string(p.src[n.StartByte():n.EndByte()])
}

func (p *parsed) field(n *gts.Node, name string) *gts.Node { return p.tree.ChildByField(n, name) }

// functions lists every named function in the file.
func (p *parsed) functions(file string) []*fn {
	var out []*fn
	var walk func(n *gts.Node)
	walk = func(n *gts.Node) {
		if f := p.function(file, n); f != nil {
			out = append(out, f)
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(p.tree.RootNode())
	return out
}

// function is n as a named function of the file, or nil when n is no
// function or has no name, as an anonymous callback has none.
func (p *parsed) function(file string, n *gts.Node) *fn {
	if !p.g.funcs[p.typ(n)] {
		return nil
	}
	name, scopes := p.funcName(n)
	if name == "" {
		return nil
	}
	path := append(p.scopes(n), scopes...)
	span := n
	if parent := n.Parent(); parent != nil && p.typ(parent) == "decorated_definition" {
		span = parent // Python's decorators belong to the function
	}
	params := p.field(n, "parameters")
	if params == nil {
		params = p.field(n, "declarator")
	}
	return &fn{file: file, start: int(span.StartByte()), end: int(span.EndByte()), line: int(span.StartPoint().Row) + 1,
		path: append(path, name), params: strings.Join(strings.Fields(p.text(params)), ""),
		text: bytes.Clone(p.src[span.StartByte():span.EndByte()])}
}

// funcName is a function's own name, and any scopes the name itself
// carries, as C++'s Catalog::load carries Catalog.
func (p *parsed) funcName(n *gts.Node) (string, []string) {
	switch p.typ(n) {
	case "function_expression", "arrow_function", "generator_function", "function":
		if name := p.field(n, "name"); name != nil {
			return p.text(name), nil
		}
		return p.boundName(n), nil
	case "function_definition":
		if d := p.field(n, "declarator"); d != nil {
			return p.declaratorName(d)
		}
	case "impl_item":
		return generic(p.text(p.field(n, "type"))), nil
	case "method_declaration":
		if recv := p.field(n, "receiver"); recv != nil && p.field(n, "name") != nil {
			words := strings.Fields(strings.Trim(p.text(recv), "()"))
			if len(words) > 0 {
				typ := strings.TrimPrefix(words[len(words)-1], "*")
				if i := strings.IndexByte(typ, '['); i > 0 {
					typ = typ[:i]
				}
				return p.text(p.field(n, "name")), []string{typ}
			}
		}
	}
	if name := p.field(n, "name"); name != nil {
		return p.text(name), nil
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		switch c := n.NamedChild(i); p.typ(c) {
		case "simple_identifier", "identifier", "type_identifier", "constant":
			return p.text(c), nil
		}
	}
	return "", nil
}

// boundName is the name a function expression is bound to: a variable, an
// object's key, a property it is assigned to, or a class field.
func (p *parsed) boundName(n *gts.Node) string {
	parent := n.Parent()
	if parent == nil {
		return ""
	}
	switch p.typ(parent) {
	case "variable_declarator":
		return p.text(p.field(parent, "name"))
	case "pair":
		return strings.Trim(p.text(p.field(parent, "key")), `"'`)
	case "assignment_expression":
		left := p.field(parent, "left")
		if left != nil && p.typ(left) == "member_expression" {
			return p.text(p.field(left, "property"))
		}
		return p.text(left)
	case "public_field_definition", "field_definition":
		if name := p.field(parent, "name"); name != nil {
			return p.text(name)
		}
		return p.text(p.field(parent, "property"))
	}
	return ""
}

// declaratorName walks a C or C++ declarator to the declared name.
func (p *parsed) declaratorName(d *gts.Node) (string, []string) {
	for range 8 {
		switch p.typ(d) {
		case "identifier", "field_identifier", "destructor_name", "operator_name":
			return p.text(d), nil
		case "qualified_identifier":
			parts := strings.Split(generic(p.text(d)), "::")
			return parts[len(parts)-1], parts[:len(parts)-1]
		}
		next := p.field(d, "declarator")
		if next == nil {
			return "", nil
		}
		d = next
	}
	return "", nil
}

// scopes are the names of what encloses n: classes, modules, namespaces,
// impls and enclosing functions, outermost first.
func (p *parsed) scopes(n *gts.Node) []string {
	var out []string
	for a := n.Parent(); a != nil; a = a.Parent() {
		t := p.typ(a)
		switch {
		case p.g.scopes[t]:
			name := ""
			if t == "impl_item" {
				name = generic(p.text(p.field(a, "type")))
			} else if f := p.field(a, "name"); f != nil {
				name = p.text(f)
			} else {
				name, _ = p.funcName(a)
			}
			if name != "" {
				out = append(out, name)
			}
		case p.g.funcs[t]:
			if name, _ := p.funcName(a); name != "" {
				out = append(out, name)
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// generic drops type arguments: Shop<T> is Shop.
func generic(s string) string {
	if i := strings.IndexByte(s, '<'); i > 0 {
		return s[:i]
	}
	return s
}

// at is the innermost named function whose text holds 1-based line.
func (p *parsed) at(file string, line int) *fn {
	var best *fn
	for _, f := range p.functions(file) {
		lines := bytes.Count(f.text, []byte("\n"))
		if line >= f.line && line <= f.line+lines && (best == nil || f.start >= best.start && f.end <= best.end) {
			best = f
		}
	}
	return best
}

// locate finds the function a frame ran in, in files: where the runtime
// said it ran, or by its name when it named only the function. It returns
// nil when the frame is in no function of the repository.
func locate(fr logs.Frame, files map[string][]byte, exts []string) (*fn, error) {
	if fr.File != "" {
		for _, file := range repoFiles(fr, files, exts) {
			p, err := parse(file, files[file])
			if err != nil {
				continue
			}
			var f *fn
			if fr.Line > 0 {
				f = p.at(file, fr.Line)
			} else {
				f, err = p.named(file, fr.Function)
			}
			p.release()
			if err != nil || f != nil {
				return f, err
			}
		}
		if fr.Line > 0 {
			return nil, nil
		}
	}
	if fr.Function == "" {
		return nil, nil
	}
	var found []*fn
	for _, file := range sortedFiles(files, exts) {
		p, err := parse(file, files[file])
		if err != nil {
			continue
		}
		if f, err := p.named(file, fr.Function); err == nil && f != nil {
			found = append(found, f)
		}
		p.release()
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("%d functions in the repository could be %s", len(found), fr.Function)
}

// named finds the one function of the file the runtime's name for it
// names: its own name, and its scope where the name gives one.
func (p *parsed) named(file, runtimeName string) (*fn, error) {
	name, scope := splitName(runtimeName)
	if name == "" {
		return nil, nil
	}
	var found []*fn
	for _, f := range p.functions(file) {
		if f.path[len(f.path)-1] != name {
			continue
		}
		if scope != "" && len(f.path) > 1 && f.path[len(f.path)-2] != scope {
			continue
		}
		found = append(found, f)
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("%d functions in %s could be %s", len(found), file, runtimeName)
}

// splitName reads a runtime's name for a function into its own name and
// the scope before it: Shop#load, App\Catalog->items, shop::Catalog::load,
// com.example.Catalog.load, Catalog$Inner.load.
func splitName(s string) (name, scope string) {
	s = goClosure.ReplaceAllString(s, "")
	if m := goMethod.FindStringSubmatch(s); m != nil {
		return m[2], m[1]
	}
	if i := strings.IndexAny(s, "(<["); i > 0 {
		s = s[:i]
	}
	s = strings.NewReplacer("::", ".", "#", ".", "->", ".", `\`, ".", "$", ".").Replace(s)
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == ' ' })
	if len(parts) == 0 {
		return "", ""
	}
	name = parts[len(parts)-1]
	if strings.HasPrefix(name, "<") || strings.HasPrefix(name, "{") || name == "<module>" || name == "<main>" {
		return "", ""
	}
	if len(parts) > 1 {
		scope = parts[len(parts)-2]
		if scope != "" && (scope[0] < 'A' || scope[0] > 'Z') {
			scope = "" // a module or package, not a class
		}
	}
	return name, scope
}

var (
	// goMethod reads a Go frame's method: example.com/shop/cart.(*Store).Get.
	goMethod = regexp.MustCompile(`\.\(\*?([A-Za-z_]\w*)(?:\[[^\]]*\])?\)\.([A-Za-z_]\w*)$`)
	// goClosure is a closure's suffix, which stands for the function it is in.
	goClosure = regexp.MustCompile(`(\.func\d+)+(\.\d+)*$`)
)

// repoFiles are the repository's files a frame's file may be, best match
// first: the most trailing path parts in common. A Java frame names only
// the file, and its package says the directory.
func repoFiles(fr logs.Frame, files map[string][]byte, exts []string) []string {
	want := strings.TrimPrefix(strings.TrimPrefix(fr.File, "file://"), "./")
	if !strings.Contains(want, "/") {
		if pkg := javaPackage(fr.Function); pkg != "" {
			want = pkg + "/" + want
		}
	}
	parts := strings.Split(path.Clean(want), "/")
	best, score := []string(nil), 0
	for _, name := range sortedFiles(files, exts) {
		have := strings.Split(name, "/")
		n := 0
		for n < len(parts) && n < len(have) && parts[len(parts)-1-n] == have[len(have)-1-n] {
			n++
		}
		switch {
		case n == 0 || n < score:
		case n > score:
			best, score = []string{name}, n
		default:
			best = append(best, name)
		}
	}
	return best
}

// javaPackage is a JVM frame's package as a directory: com.example.shop.
// Catalog.load is com/example/shop.
func javaPackage(function string) string {
	parts := strings.Split(function, ".")
	if len(parts) < 3 {
		return ""
	}
	var pkg []string
	for _, p := range parts[:len(parts)-2] {
		if p == "" || p[0] < 'a' || p[0] > 'z' {
			break
		}
		pkg = append(pkg, p)
	}
	return strings.Join(pkg, "/")
}

// maxSourceBytes skips files too big to be the application's own source.
const maxSourceBytes = 1 << 20

func sortedFiles(files map[string][]byte, exts []string) []string {
	var out []string
	for name, src := range files {
		if len(src) > maxSourceBytes || strings.Contains(name, "node_modules/") || strings.Contains(name, "vendor/") {
			continue
		}
		for _, e := range exts {
			if strings.HasSuffix(name, e) {
				out = append(out, name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// counterpart is the function in good the same name qualified the same
// way names: in the same file, or wherever it was before the commit moved
// it. Overloads are told apart by their parameters.
func counterpart(now *fn, good map[string][]byte) (*fn, error) {
	find := func(file string) []*fn {
		src, ok := good[file]
		if !ok {
			return nil
		}
		p, err := parse(file, src)
		if err != nil {
			return nil
		}
		defer p.release()
		var out []*fn
		for _, f := range p.functions(file) {
			if strings.Join(f.path, "\x00") == strings.Join(now.path, "\x00") {
				out = append(out, f)
			}
		}
		return out
	}
	found := find(now.file)
	if len(found) == 0 {
		for _, file := range sortedFiles(good, []string{path.Ext(now.file)}) {
			if file != now.file {
				found = append(found, find(file)...)
			}
		}
	}
	if len(found) > 1 {
		var same []*fn
		for _, f := range found {
			if f.params == now.params {
				same = append(same, f)
			}
		}
		found = same
	}
	switch len(found) {
	case 0:
		return nil, nil
	case 1:
		return found[0], nil
	}
	return nil, fmt.Errorf("%d functions were %s before the culprit commit", len(found), now.name())
}

// errBroken says a restore broke a file that parsed.
var errBroken = errors.New("the file no longer parses")

// stillParses checks that after parses at least as well as before: a
// grammar may miss a construct the compiler takes, so a file that already
// had errors may keep them, but no new one.
func stillParses(file string, before, after []byte) error {
	errs := func(src []byte) (int, error) {
		p, err := parse(file, src)
		if err != nil {
			return 0, err
		}
		defer p.release()
		n := 0
		var walk func(*gts.Node)
		walk = func(x *gts.Node) {
			if x.IsError() || x.IsMissing() {
				n++
			}
			for i := 0; i < x.ChildCount(); i++ {
				walk(x.Child(i))
			}
		}
		walk(p.tree.RootNode())
		return n, nil
	}
	was, err := errs(before)
	if err != nil {
		return err
	}
	is, err := errs(after)
	if err != nil {
		return err
	}
	if is > was {
		return errBroken
	}
	return nil
}

// Located is the function a crash's innermost in-app frame ran in, as the
// failing commit had it.
type Located struct{ f *fn }

// CrashFunction finds the function the crash's innermost in-app frame ran
// in, in the failing commit's source.
func CrashFunction(in Input) (*Located, error) {
	exts := languageFiles[in.Language]
	if in.Language == "go" {
		exts = []string{".go"}
	}
	if len(exts) == 0 {
		return nil, fmt.Errorf("fixes to code do not read %s", in.Language)
	}
	for _, fr := range in.Frames {
		if !fr.InApp {
			continue
		}
		f, err := locate(fr, in.Bad, exts)
		if err != nil {
			return nil, err
		}
		if f != nil {
			return &Located{f}, nil
		}
	}
	return nil, fmt.Errorf("%w: none of its frames is in the repository", ErrNoMeeting)
}

// Name is the function's name, qualified by its scopes.
func (l *Located) Name() string { return l.f.name() }

// Path is the file it is in.
func (l *Located) Path() string { return l.f.file }

// Same reports whether src, the same file at another commit, holds the
// function as it is.
func (l *Located) Same(src string) bool {
	if src == "" {
		return false
	}
	p, err := parse(l.f.file, []byte(src))
	if err != nil {
		return false
	}
	defer p.release()
	for _, f := range p.functions(l.f.file) {
		if strings.Join(f.path, "\x00") == strings.Join(l.f.path, "\x00") {
			return bytes.Equal(f.text, l.f.text)
		}
	}
	return false
}
