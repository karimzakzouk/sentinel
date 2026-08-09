package codeintel

// Symbol is a code symbol extracted from a source file.
type Symbol struct {
	Name     string `json:"name"`
	Kind     Kind   `json:"kind"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	EndLine  int    `json:"end_line,omitempty"`
	Params   string `json:"params,omitempty"`
	Receiver string `json:"receiver,omitempty"`
}

// Kind classifies a symbol.
type Kind string

const (
	KindFunction Kind = "function"
	KindMethod   Kind = "method"
	KindClass    Kind = "class"
	KindVariable Kind = "variable"
	KindImport   Kind = "import"
	KindType     Kind = "type"
)

// CallEdge is a directed edge in the call graph: Caller calls Callee.
type CallEdge struct {
	Caller   Symbol `json:"caller"`
	Callee   string `json:"callee"`
	CallFile string `json:"call_file"`
	CallLine int    `json:"call_line"`
}

// CallGraph is the directed graph of function/method calls in a repo.
type CallGraph struct {
	// Definitions maps symbol name → defining Symbol.
	Definitions map[string][]Symbol `json:"definitions"`
	// Callers maps callee name → list of call sites.
	Callers map[string][]CallEdge `json:"callers"`
	// Files is the set of files that were parsed.
	Files []string `json:"files"`
}

// Index is the pre-computed code intelligence for a repo.
type Index struct {
	Symbols   []Symbol   `json:"symbols"`
	CallGraph *CallGraph `json:"call_graph"`
	RepoPath  string     `json:"-"`
}
