package codefix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// Templates proposes small, fixed changes for the crashes every codebase
// has: a value missing where code assumed one, an index past the end, a
// key that is not there, a division by zero. Each recipe changes only the
// line the crash ran at, or adds a guard just before it, the way people
// write that guard in the language. A guard can hide a bug as well as fix
// it, so its pull request says so, and the Lab still judges it.
type Templates struct{}

func (Templates) Name() string { return "template" }

// site is where a crash ran in the failing commit's source.
type site struct {
	file   string
	line   int // 1-based
	text   string
	indent string
	fn     *fn // the function around it; nil for Go, read with go/parser
}

// recipe is one template: whether it reads this crash, and the edited file.
type recipe struct {
	name      string
	languages []string
	// matches reads the crash's types and message.
	matches func(types []string, message string) bool
	apply   func(s site, src []byte, message string) ([]byte, string, error)
}

func (Templates) Propose(_ context.Context, in Input) ([]*Change, error) {
	s, src, err := crashSite(in)
	if err != nil {
		return nil, err
	}
	var out []*Change
	var tried []string
	for _, r := range recipes {
		if !containsString(r.languages, in.Language) || !r.matches(in.Types, in.Message) {
			continue
		}
		after, what, err := r.apply(s, src, in.Message)
		if err != nil {
			tried = append(tried, r.name+": "+err.Error())
			continue
		}
		if err := checkParse(s.file, src, after); err != nil {
			tried = append(tried, r.name+": "+err.Error())
			continue
		}
		name := s.file + ":" + fmt.Sprint(s.line)
		if s.fn != nil {
			name = s.fn.name()
		}
		out = append(out, &Change{Fixer: "template:" + r.name, Function: name,
			Edits: []Edit{{Path: s.file, Line: s.line, Before: src, After: after}},
			Why:   what + "; a guard like this can hide a bug as well as fix it, so read it as a stopgap"})
	}
	if len(out) == 0 {
		if len(tried) > 0 {
			return nil, errors.New(strings.Join(tried, "; "))
		}
		return nil, fmt.Errorf("no template reads a %s crash like this one", in.Language)
	}
	return out, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// crashSite finds the innermost in-app frame's line in the repository.
func crashSite(in Input) (site, []byte, error) {
	exts := languageFiles[in.Language]
	if in.Language == "go" {
		exts = []string{".go"}
	}
	for _, fr := range in.Frames {
		if !fr.InApp || fr.File == "" || fr.Line <= 0 {
			continue
		}
		files := repoFiles(fr, in.Bad, exts)
		if len(files) != 1 {
			continue
		}
		src := in.Bad[files[0]]
		lines := strings.Split(string(src), "\n")
		if fr.Line > len(lines) {
			continue
		}
		text := lines[fr.Line-1]
		s := site{file: files[0], line: fr.Line, text: text, indent: text[:len(text)-len(strings.TrimLeft(text, " \t"))]}
		if in.Language != "go" {
			if p, err := parse(files[0], src); err == nil {
				s.fn = p.at(files[0], fr.Line)
				p.release()
			}
		}
		return s, src, nil
	}
	return site{}, nil, errors.New("no frame of the crash says where in the repository it ran")
}

// checkParse parses Go with go/parser and the rest with tree-sitter.
func checkParse(file string, before, after []byte) error {
	if strings.HasSuffix(file, ".go") {
		if _, err := parser.ParseFile(token.NewFileSet(), file, after, parser.SkipObjectResolution); err != nil {
			return fmt.Errorf("the change does not parse: %w", err)
		}
		return nil
	}
	return stillParses(file, before, after)
}

// replaceLine puts text in place of the 1-based line of src.
func replaceLine(src []byte, line int, text string) []byte {
	lines := strings.Split(string(src), "\n")
	lines[line-1] = text
	return []byte(strings.Join(lines, "\n"))
}

// insertBefore puts lines, each already indented, before the 1-based line.
func insertBefore(src []byte, line int, add ...string) []byte {
	lines := strings.Split(string(src), "\n")
	out := append(append(append([]string{}, lines[:line-1]...), add...), lines[line-1:]...)
	return []byte(strings.Join(out, "\n"))
}

func anyContains(types []string, message string, subs ...string) bool {
	all := strings.ToLower(strings.Join(types, " ") + " " + message)
	for _, s := range subs {
		if strings.Contains(all, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

var (
	identAssign   = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*\[(?:[^\[\]]|\[[^\[\]]*\])*\]\s*=[^=]`)
	identIndex    = regexp.MustCompile(`\b([A-Za-z_][\w.]*)\s*\[\s*([A-Za-z_]\w*|\d+)\s*\]`)
	identDivide   = regexp.MustCompile(`[\w)\]]\s*[/%]\s*([A-Za-z_][\w.]*)`)
	identSelector = regexp.MustCompile(`\b([a-z_]\w*)\.\w`)
	jsReading     = regexp.MustCompile(`reading '([^']+)'`)
	javaNullVar   = regexp.MustCompile(`because "([A-Za-z_]\w*)" is null`)
	rubyNilMethod = regexp.MustCompile(`undefined method [` + "`" + `']([^']+)' for nil`)
	phpOnNull     = regexp.MustCompile(`Call to a member function (\w+)\(\) on null`)
	pyKeyRead     = regexp.MustCompile(`\b([A-Za-z_][\w.]*)\[(['"][^'"]+['"])\]`)
)

var recipes = []recipe{
	{
		name: "make-the-map", languages: []string{"go"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "assignment to entry in nil map") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			m := identAssign.FindStringSubmatch(s.text)
			if m == nil {
				return nil, "", errors.New("no map is assigned to at the crash's line")
			}
			decl := regexp.MustCompile(`(?m)^(\s*)var ` + regexp.QuoteMeta(m[1]) + `\s+(map\[[^\n=]+?)\s*$`)
			loc := decl.FindSubmatchIndex(src[:lineOffset(src, s.line)])
			if loc == nil {
				return nil, "", fmt.Errorf("%s is not declared as a nil map before the crash's line", m[1])
			}
			typ := string(src[loc[4]:loc[5]])
			repl := string(src[loc[2]:loc[3]]) + m[1] + " := make(" + strings.TrimSpace(typ) + ")"
			after := append(append(append([]byte{}, src[:loc[0]]...), repl...), src[loc[1]:]...)
			return after, fmt.Sprintf("%s is made as an empty map instead of declared nil, so writing to it no longer panics", m[1]), nil
		},
	},
	{
		name: "nil-guard", languages: []string{"go"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "nil pointer dereference") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			zeros, pkgs, err := goResults(src, s.line)
			if err != nil {
				return nil, "", err
			}
			for _, m := range identSelector.FindAllStringSubmatch(s.text, -1) {
				if pkgs[m[1]] {
					continue
				}
				guard := []string{s.indent + "if " + m[1] + " == nil {", s.indent + "\t" + goReturn(zeros), s.indent + "}"}
				return insertBefore(src, s.line, guard...), fmt.Sprintf("returns early when %s is nil, before the line that dereferenced it", m[1]), nil
			}
			return nil, "", errors.New("no pointer is dereferenced at the crash's line")
		},
	},
	{
		name: "index-guard", languages: []string{"go"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "index out of range") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			zeros, _, err := goResults(src, s.line)
			if err != nil {
				return nil, "", err
			}
			m := identIndex.FindStringSubmatch(s.text)
			if m == nil {
				return nil, "", errors.New("nothing is indexed at the crash's line")
			}
			guard := []string{s.indent + "if " + m[2] + " >= len(" + m[1] + ") {", s.indent + "\t" + goReturn(zeros), s.indent + "}"}
			if m[2][0] >= '0' && m[2][0] <= '9' {
				guard[0] = s.indent + "if len(" + m[1] + ") <= " + m[2] + " {"
			}
			return insertBefore(src, s.line, guard...), fmt.Sprintf("returns early when %s has no element %s", m[1], m[2]), nil
		},
	},
	{
		name: "zero-guard", languages: []string{"go"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "integer divide by zero") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			zeros, _, err := goResults(src, s.line)
			if err != nil {
				return nil, "", err
			}
			m := identDivide.FindStringSubmatch(s.text)
			if m == nil {
				return nil, "", errors.New("nothing is divided at the crash's line")
			}
			guard := []string{s.indent + "if " + m[1] + " == 0 {", s.indent + "\t" + goReturn(zeros), s.indent + "}"}
			return insertBefore(src, s.line, guard...), fmt.Sprintf("returns early when %s is zero, before dividing by it", m[1]), nil
		},
	},
	{
		name: "start-empty", languages: []string{"python"},
		matches: func(t []string, m string) bool {
			return anyContains(t, m, "'NoneType' object does not support item assignment")
		},
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			m := identAssign.FindStringSubmatch(s.text)
			if m == nil {
				return nil, "", errors.New("nothing is assigned by key at the crash's line")
			}
			guard := []string{s.indent + "if " + m[1] + " is None:", s.indent + "    " + m[1] + " = {}"}
			return insertBefore(src, s.line, guard...), fmt.Sprintf("%s starts as an empty dict when it is None, so assigning into it works", m[1]), nil
		},
	},
	{
		name: "get-the-key", languages: []string{"python"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "KeyError") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			m := pyKeyRead.FindStringSubmatchIndex(s.text)
			if m == nil || strings.Contains(s.text[m[1]:], "=") && !strings.Contains(s.text[m[1]:], "==") && strings.TrimSpace(s.text[m[1]:])[0] == '=' {
				return nil, "", errors.New("no key is read at the crash's line")
			}
			text := s.text[:m[0]] + s.text[m[2]:m[3]] + ".get(" + s.text[m[4]:m[5]] + ")" + s.text[m[1]:]
			return replaceLine(src, s.line, text), "reads the key with .get, so a missing key gives None instead of raising KeyError", nil
		},
	},
	{
		name: "length-guard", languages: []string{"python"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "IndexError") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			m := identIndex.FindStringSubmatch(s.text)
			if m == nil {
				return nil, "", errors.New("nothing is indexed at the crash's line")
			}
			guard := []string{s.indent + "if len(" + m[1] + ") <= " + m[2] + ":", s.indent + "    return None"}
			return insertBefore(src, s.line, guard...), fmt.Sprintf("returns None when %s has no element %s", m[1], m[2]), nil
		},
	},
	{
		name: "optional-chaining", languages: []string{"node"},
		matches: func(t []string, m string) bool {
			return anyContains(t, m, "Cannot read properties of undefined", "Cannot read properties of null")
		},
		apply: func(s site, src []byte, message string) ([]byte, string, error) {
			m := jsReading.FindStringSubmatch(message)
			if m == nil {
				return nil, "", errors.New("the message names no property")
			}
			re := regexp.MustCompile(`([\w)\]])\.` + regexp.QuoteMeta(m[1]) + `\b`)
			loc := re.FindStringSubmatchIndex(s.text)
			if loc == nil {
				return nil, "", fmt.Errorf("the crash's line reads no .%s", m[1])
			}
			text := s.text[:loc[3]] + "?." + m[1] + s.text[loc[1]:]
			return replaceLine(src, s.line, text), fmt.Sprintf("reads .%s with optional chaining, so a missing value gives undefined instead of throwing", m[1]), nil
		},
	},
	{
		name: "null-guard", languages: []string{"java"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "NullPointerException") },
		apply: func(s site, src []byte, message string) ([]byte, string, error) {
			m := javaNullVar.FindStringSubmatch(message)
			if m == nil || !strings.HasSuffix(s.file, ".java") || s.fn == nil {
				return nil, "", errors.New("the message names no variable that is null")
			}
			guard := []string{s.indent + "if (" + m[1] + " == null) {", s.indent + "    " + javaReturn(s.fn), s.indent + "}"}
			return insertBefore(src, s.line, guard...), fmt.Sprintf("returns early when %s is null, before the line that used it", m[1]), nil
		},
	},
	{
		name: "or-default", languages: []string{"dotnet"},
		matches: func(t []string, m string) bool {
			return anyContains(t, m, "Sequence contains no elements", "Sequence contains no matching element")
		},
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			for _, call := range []string{"First", "Single", "Last"} {
				if i := strings.Index(s.text, "."+call+"("); i >= 0 {
					text := s.text[:i] + "." + call + "OrDefault(" + s.text[i+len(call)+2:]
					return replaceLine(src, s.line, text), fmt.Sprintf("takes %sOrDefault, so an empty sequence gives the default instead of throwing", call), nil
				}
			}
			return nil, "", errors.New("the crash's line takes no First, Single or Last")
		},
	},
	{
		name: "safe-navigation", languages: []string{"ruby"},
		matches: func(t []string, m string) bool {
			return anyContains(t, m, "NoMethodError") && anyContains(t, m, "for nil")
		},
		apply: func(s site, src []byte, message string) ([]byte, string, error) {
			m := rubyNilMethod.FindStringSubmatch(message)
			if m == nil {
				return nil, "", errors.New("the message names no method")
			}
			if m[1] == "[]" {
				re := regexp.MustCompile(`\b([a-z_@][\w]*)\[`)
				loc := re.FindStringSubmatchIndex(s.text)
				if loc == nil {
					return nil, "", errors.New("nothing is indexed at the crash's line")
				}
				v := s.text[loc[2]:loc[3]]
				text := s.text[:loc[2]] + "(" + v + " || {})[" + s.text[loc[1]:]
				return replaceLine(src, s.line, text), fmt.Sprintf("indexes %s as empty when it is nil", v), nil
			}
			re := regexp.MustCompile(`([\w)\]])\.` + regexp.QuoteMeta(m[1]) + `\b`)
			loc := re.FindStringSubmatchIndex(s.text)
			if loc == nil {
				return nil, "", fmt.Errorf("the crash's line calls no .%s", m[1])
			}
			text := s.text[:loc[3]] + "&." + m[1] + s.text[loc[1]:]
			return replaceLine(src, s.line, text), fmt.Sprintf("calls %s with safe navigation, so nil gives nil instead of raising", m[1]), nil
		},
	},
	{
		name: "nullsafe", languages: []string{"php"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "on null") },
		apply: func(s site, src []byte, message string) ([]byte, string, error) {
			m := phpOnNull.FindStringSubmatch(message)
			if m == nil {
				return nil, "", errors.New("the message names no method")
			}
			i := strings.Index(s.text, "->"+m[1]+"(")
			if i < 0 {
				return nil, "", fmt.Errorf("the crash's line calls no ->%s", m[1])
			}
			text := s.text[:i] + "?->" + s.text[i+2:]
			return replaceLine(src, s.line, text), fmt.Sprintf("calls %s with the nullsafe operator, so null gives null instead of an error", m[1]), nil
		},
	},
	{
		name: "unwrap-or-default", languages: []string{"rust"},
		matches: func(t []string, m string) bool {
			return anyContains(t, m, "on a `None` value", "on an `Err` value", "on a None value", "on an Err value")
		},
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			re := regexp.MustCompile(`\.(unwrap\(\)|expect\([^)]*\))`)
			loc := re.FindStringIndex(s.text)
			if loc == nil {
				return nil, "", errors.New("the crash's line unwraps nothing")
			}
			text := s.text[:loc[0]] + ".unwrap_or_default()" + s.text[loc[1]:]
			return replaceLine(src, s.line, text), "takes the default instead of panicking when there is no value", nil
		},
	},
	{
		name: "get-or-default", languages: []string{"rust"},
		matches: func(t []string, m string) bool { return anyContains(t, m, "index out of bounds") },
		apply: func(s site, src []byte, _ string) ([]byte, string, error) {
			m := identIndex.FindStringSubmatchIndex(s.text)
			if m == nil {
				return nil, "", errors.New("nothing is indexed at the crash's line")
			}
			text := s.text[:m[0]] + s.text[m[2]:m[3]] + ".get(" + s.text[m[4]:m[5]] + ").copied().unwrap_or_default()" + s.text[m[1]:]
			return replaceLine(src, s.line, text), "reads the element with get, so an index past the end gives the default instead of panicking", nil
		},
	},
}

// lineOffset is the byte offset where the 1-based line starts.
func lineOffset(src []byte, line int) int {
	off := 0
	for i := 1; i < line; i++ {
		j := bytes.IndexByte(src[off:], '\n')
		if j < 0 {
			return len(src)
		}
		off += j + 1
	}
	return off
}

// goResults are the zero values the Go function around line returns, and
// the file's imported package names.
func goResults(src []byte, line int) ([]string, map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	pkgs := map[string]bool{}
	for _, imp := range file.Imports {
		name := strings.Trim(imp.Path.Value, `"`)
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgs[name] = true
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fset.Position(fd.Pos()).Line > line || fset.Position(fd.End()).Line < line {
			continue
		}
		var zeros []string
		if fd.Type.Results != nil {
			for _, f := range fd.Type.Results.List {
				n := max(1, len(f.Names))
				for range n {
					zeros = append(zeros, goZero(src, fset, f.Type))
				}
			}
		}
		return zeros, pkgs, nil
	}
	return nil, nil, errors.New("the crash's line is in no function")
}

func goZero(src []byte, fset *token.FileSet, t ast.Expr) string {
	switch x := t.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType:
		return "nil"
	case *ast.ArrayType:
		if x.Len == nil {
			return "nil"
		}
	case *ast.Ident:
		switch x.Name {
		case "error", "any":
			return "nil"
		case "string":
			return `""`
		case "bool":
			return "false"
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"float32", "float64", "complex64", "complex128", "byte", "rune":
			return "0"
		}
	}
	return "*new(" + string(src[fset.Position(t.Pos()).Offset:fset.Position(t.End()).Offset]) + ")"
}

func goReturn(zeros []string) string {
	if len(zeros) == 0 {
		return "return"
	}
	return "return " + strings.Join(zeros, ", ")
}

// javaReturn returns from the Java method f with its type's default.
func javaReturn(f *fn) string {
	head := string(f.text)
	if i := strings.IndexByte(head, '('); i > 0 {
		head = head[:i]
	}
	words := strings.Fields(head)
	if len(words) < 2 {
		return "return;"
	}
	switch typ := words[len(words)-2]; typ {
	case "void":
		return "return;"
	case "boolean":
		return "return false;"
	case "int", "long", "short", "byte", "char", "float", "double":
		return "return 0;"
	}
	return "return null;"
}
