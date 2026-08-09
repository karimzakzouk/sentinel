package codeintel

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	pyFuncRe  = regexp.MustCompile(`^\s*def\s+(\w+)\s*\(([^)]*)\)`)
	pyClassRe = regexp.MustCompile(`^\s*class\s+(\w+)`)
	pyCallRe  = regexp.MustCompile(`\b(\w+)\s*\(`)

	jsFuncRe  = regexp.MustCompile(`(?:function\s+|(?:const|let|var)\s+)?(\w+)\s*\(([^)]*)\)`)
	jsClassRe = regexp.MustCompile(`(?:class|interface)\s+(\w+)`)
	jsCallRe  = regexp.MustCompile(`\b(\w+)\s*\(`)

	javaFuncRe  = regexp.MustCompile(`(?:public|private|protected|static|final)\s+(?:[\w<>\[\]]+)\s+(\w+)\s*\(([^)]*)\)`)
	javaClassRe = regexp.MustCompile(`(?:class|interface|enum)\s+(\w+)`)
	javaCallRe  = regexp.MustCompile(`\b(\w+)\s*\(`)

	rubyFuncRe  = regexp.MustCompile(`^\s*def\s+(\w+)`)
	rubyClassRe = regexp.MustCompile(`^\s*class\s+(\w+)`)
	rubyCallRe  = regexp.MustCompile(`\b([a-z_]\w*)\s*\(`)

	rustFuncRe = regexp.MustCompile(`\bfn\s+(\w+)\s*\(([^)]*)\)`)
	rustCallRe = regexp.MustCompile(`\b(\w+)\s*\(`)
)

func parseFile(path, repoRoot string) ([]Symbol, []CallEdge, error) {
	ext := strings.ToLower(filepath.Ext(path))
	relPath, _ := filepath.Rel(repoRoot, path)

	switch ext {
	case ".go":
		return parseGo(path, relPath)
	case ".py":
		return parseGeneric(path, relPath, pyFuncRe, pyClassRe, pyCallRe, KindFunction)
	case ".js", ".jsx", ".mjs":
		return parseGeneric(path, relPath, jsFuncRe, jsClassRe, jsCallRe, KindFunction)
	case ".ts", ".tsx":
		return parseGeneric(path, relPath, jsFuncRe, jsClassRe, jsCallRe, KindFunction)
	case ".java":
		return parseGeneric(path, relPath, javaFuncRe, javaClassRe, javaCallRe, KindMethod)
	case ".rb":
		return parseGeneric(path, relPath, rubyFuncRe, rubyClassRe, rubyCallRe, KindFunction)
	case ".rs":
		return parseGeneric(path, relPath, rustFuncRe, nil, rustCallRe, KindFunction)
	default:
		return nil, nil, nil
	}
}

func parseGo(path, relPath string) ([]Symbol, []CallEdge, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, nil, nil
	}

	var symbols []Symbol
	var edges []CallEdge

	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			name := node.Name.Name
			kind := KindFunction
			receiver := ""
			if node.Recv != nil && len(node.Recv.List) > 0 {
				kind = KindMethod
				rt := node.Recv.List[0].Type
				if star, ok := rt.(*ast.StarExpr); ok {
					if ident, ok := star.X.(*ast.Ident); ok {
						receiver = ident.Name
					}
				} else if ident, ok := rt.(*ast.Ident); ok {
					receiver = ident.Name
				}
			}
			params := extractGoParams(node.Type.Params)
			startLine := fset.Position(node.Pos()).Line
			endLine := fset.Position(node.End()).Line
			sym := Symbol{
				Name:     name,
				Kind:     kind,
				File:     relPath,
				Line:     startLine,
				EndLine:  endLine,
				Params:   params,
				Receiver: receiver,
			}
			symbols = append(symbols, sym)

			if node.Body != nil {
				ast.Inspect(node.Body, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						switch fun := call.Fun.(type) {
						case *ast.Ident:
							edges = append(edges, CallEdge{
								Caller:   sym,
								Callee:   fun.Name,
								CallFile: relPath,
								CallLine: fset.Position(call.Pos()).Line,
							})
						case *ast.SelectorExpr:
							edges = append(edges, CallEdge{
								Caller:   sym,
								Callee:   fun.Sel.Name,
								CallFile: relPath,
								CallLine: fset.Position(call.Pos()).Line,
							})
						}
					}
					return true
				})
			}
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					symbols = append(symbols, Symbol{
						Name: ts.Name.Name,
						Kind: KindType,
						File: relPath,
						Line: fset.Position(ts.Pos()).Line,
					})
				}
			}
		}
		return true
	})

	return symbols, edges, nil
}

func extractGoParams(params *ast.FieldList) string {
	if params == nil {
		return ""
	}
	var parts []string
	for _, param := range params.List {
		for _, name := range param.Names {
			parts = append(parts, name.Name)
		}
		if len(param.Names) == 0 {
			if ident, ok := param.Type.(*ast.Ident); ok {
				parts = append(parts, ident.Name)
			}
		}
	}
	return strings.Join(parts, ", ")
}

func parseGeneric(path, relPath string, funcRe, classRe, callRe *regexp.Regexp, funcKind Kind) ([]Symbol, []CallEdge, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	var symbols []Symbol
	var edges []CallEdge

	var currentFunc Symbol
	currentFunc.Line = 0

	for i, line := range lines {
		if funcRe != nil {
			if m := funcRe.FindStringSubmatch(line); m != nil {
				name := m[1]
				params := ""
				if len(m) > 2 {
					params = m[2]
				}
				currentFunc = Symbol{
					Name:   name,
					Kind:   funcKind,
					File:   relPath,
					Line:   i + 1,
					Params: strings.TrimSpace(params),
				}
				symbols = append(symbols, currentFunc)
			} else if classRe != nil {
				if m := classRe.FindStringSubmatch(line); m != nil {
					currentFunc = Symbol{}
					symbols = append(symbols, Symbol{
						Name: m[1],
						Kind: KindClass,
						File: relPath,
						Line: i + 1,
					})
				}
			}
		}
		if classRe != nil {
			if m := classRe.FindStringSubmatch(line); m != nil {
				symbols = append(symbols, Symbol{
					Name: m[1],
					Kind: KindClass,
					File: relPath,
					Line: i + 1,
				})
			}
		}
		isDefLine := funcRe != nil && funcRe.MatchString(line)
		if callRe != nil && currentFunc.Line > 0 && !isDefLine {
			matches := callRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				callee := m[1]
				if isBuiltin(callee) {
					continue
				}
				edges = append(edges, CallEdge{
					Caller:   currentFunc,
					Callee:   callee,
					CallFile: relPath,
					CallLine: i + 1,
				})
			}
		}
	}

	return symbols, edges, nil
}

var builtins = map[string]bool{
	"print": true, "println": true, "printf": true, "sprintf": true,
	"len": true, "cap": true, "append": true, "copy": true,
	"make": true, "new": true, "delete": true, "panic": true,
	"recover": true, "close": true, "range": true,
	"if": true, "for": true, "while": true, "switch": true,
	"return": true, "break": true, "continue": true,
	"import": true, "from": true, "def": true, "class": true,
	"function": true, "const": true, "let": true, "var": true,
	"require": true, "module": true, "exports": true,
	"open": true, "int": true, "str": true, "bool": true,
	"float": true, "list": true, "dict": true, "set": true,
	"tuple": true, "type": true, "isinstance": true, "hasattr": true,
	"getattr": true, "setattr": true, "super": true, "self": true,
	"this": true, "None": true, "null": true, "True": true,
	"False": true, "true": true, "false": true, "undefined": true,
}

func isBuiltin(name string) bool {
	return builtins[name]
}

// BuildIndex walks the repo at repoRoot, parses every source file, and
// builds a code intelligence index with symbols + call graph.
func BuildIndex(repoRoot string, maxFiles int) (*Index, error) {
	if maxFiles <= 0 {
		maxFiles = 500
	}

	ignoreDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, "dist": true,
		"build": true, "__pycache__": true, ".next": true, ".cache": true,
		".pytest_cache": true, "target": true, ".idea": true, ".vscode": true,
	}

	var idx Index
	idx.RepoPath = repoRoot
	cg := &CallGraph{
		Definitions: make(map[string][]Symbol),
		Callers:     make(map[string][]CallEdge),
	}

	fileCount := 0
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && ignoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if fileCount >= maxFiles {
			return filepath.SkipAll
		}

		symbols, edges, perr := parseFile(path, repoRoot)
		if perr != nil {
			return nil
		}
		if len(symbols) == 0 && len(edges) == 0 {
			return nil
		}

		fileCount++
		cg.Files = append(cg.Files, path)

		for _, sym := range symbols {
			idx.Symbols = append(idx.Symbols, sym)
			cg.Definitions[sym.Name] = append(cg.Definitions[sym.Name], sym)
		}
		for _, edge := range edges {
			cg.Callers[edge.Callee] = append(cg.Callers[edge.Callee], edge)
		}
		return nil
	})

	sort.Slice(idx.Symbols, func(i, j int) bool {
		if idx.Symbols[i].File != idx.Symbols[j].File {
			return idx.Symbols[i].File < idx.Symbols[j].File
		}
		return idx.Symbols[i].Line < idx.Symbols[j].Line
	})

	idx.CallGraph = cg
	return &idx, nil
}

// FindDefinition returns the defining Symbol(s) for name.
func (idx *Index) FindDefinition(name string) []Symbol {
	return idx.CallGraph.Definitions[name]
}

// FindCallers returns all call sites of name.
func (idx *Index) FindCallers(name string) []CallEdge {
	return idx.CallGraph.Callers[name]
}

// FindCallees returns all functions called by name.
func (idx *Index) FindCallees(name string) []CallEdge {
	var result []CallEdge
	for _, edges := range idx.CallGraph.Callers {
		for _, e := range edges {
			if e.Caller.Name == name {
				result = append(result, e)
			}
		}
	}
	return result
}

// FormatSymbolList returns a compact, LLM-friendly summary of all symbols.
func (idx *Index) FormatSymbolList() string {
	var b strings.Builder
	currentFile := ""
	for _, sym := range idx.Symbols {
		if sym.File != currentFile {
			currentFile = sym.File
			fmt.Fprintf(&b, "\n%s:\n", sym.File)
		}
		switch sym.Kind {
		case KindFunction:
			fmt.Fprintf(&b, "  L%d  func %s(%s)\n", sym.Line, sym.Name, sym.Params)
		case KindMethod:
			fmt.Fprintf(&b, "  L%d  (%s) %s(%s)\n", sym.Line, sym.Receiver, sym.Name, sym.Params)
		case KindClass:
			fmt.Fprintf(&b, "  L%d  class %s\n", sym.Line, sym.Name)
		case KindType:
			fmt.Fprintf(&b, "  L%d  type %s\n", sym.Line, sym.Name)
		default:
			fmt.Fprintf(&b, "  L%d  %s %s\n", sym.Line, sym.Kind, sym.Name)
		}
	}
	return b.String()
}

// FormatCallers returns a compact summary of who calls name.
func (idx *Index) FormatCallers(name string) string {
	callers := idx.FindCallers(name)
	if len(callers) == 0 {
		return fmt.Sprintf("No callers found for %q.\n", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d caller(s) of %q:\n", len(callers), name)
	for _, c := range callers {
		fmt.Fprintf(&b, "  %s:%d  in %s\n", c.CallFile, c.CallLine, c.Caller.Name)
	}
	return b.String()
}
