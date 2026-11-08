package logs

import "strings"

// Wildcard stands for the tokens in which a template's messages differ.
const Wildcard = "<*>"

// Template is a message pattern and how many messages it has absorbed.
type Template struct {
	// ID is stable for the Miner's lifetime; the tokens may gain wildcards.
	ID     int
	Tokens []string
	Count  int
}

func (t *Template) String() string { return strings.Join(t.Tokens, " ") }

// Miner groups masked messages into templates in the style of Drain (He et
// al., 2017): a fixed-depth prefix tree routes a message by its token count
// and leading tokens to a few candidate templates, and the most similar one
// absorbs it, turning the tokens that differ into wildcards.
type Miner struct {
	// Depth is the number of leading tokens used for routing.
	Depth int
	// Similarity is the share of a template's fixed tokens a message must
	// match to join it.
	Similarity float64
	// MaxChildren bounds each tree node; more distinct tokens route through
	// the wildcard child.
	MaxChildren int
	// MaxTemplates bounds memory: past it, unmatched messages count toward
	// one overflow template.
	MaxTemplates int

	root      map[int]*node
	templates []*Template
	overflow  *Template
}

type node struct {
	children  map[string]*node
	templates []*Template
}

// NewMiner returns a Miner that routes on the first token only. Drain's usual
// two routing tokens keep "user alice logged in" and "user bob logged in"
// apart forever; similarity alone separates unrelated messages well enough.
func NewMiner() *Miner {
	return &Miner{Depth: 1, Similarity: 0.4, MaxChildren: 100, MaxTemplates: 5000}
}

// Templates returns every template, in the order they were first seen.
func (m *Miner) Templates() []*Template { return m.templates }

// Add files a masked message under a template and returns the template.
func (m *Miner) Add(masked string) *Template {
	tokens := strings.Fields(masked)
	if m.root == nil {
		m.root = map[int]*node{}
	}
	n := m.root[len(tokens)]
	if n == nil {
		n = &node{children: map[string]*node{}}
		m.root[len(tokens)] = n
	}
	for i := 0; i < m.Depth && i < len(tokens); i++ {
		n = m.child(n, routeKey(tokens[i]))
	}
	var best *Template
	bestSim, bestWild := -1.0, -1
	for _, t := range n.templates {
		sim, wild := similarity(t.Tokens, tokens)
		if sim > bestSim || sim == bestSim && wild > bestWild {
			best, bestSim, bestWild = t, sim, wild
		}
	}
	if best != nil && bestSim >= m.Similarity {
		for i, tok := range tokens {
			if best.Tokens[i] != tok {
				best.Tokens[i] = Wildcard
			}
		}
		best.Count++
		return best
	}
	if len(m.templates) >= m.MaxTemplates {
		if m.overflow == nil {
			m.overflow = &Template{ID: 0, Tokens: []string{"<overflow>"}}
		}
		m.overflow.Count++
		return m.overflow
	}
	t := &Template{ID: len(m.templates) + 1, Tokens: tokens, Count: 1}
	m.templates = append(m.templates, t)
	n.templates = append(n.templates, t)
	return t
}

func (m *Miner) child(n *node, key string) *node {
	if c := n.children[key]; c != nil {
		return c
	}
	if len(n.children) >= m.MaxChildren {
		key = Wildcard
		if c := n.children[key]; c != nil {
			return c
		}
	}
	c := &node{children: map[string]*node{}}
	n.children[key] = c
	return c
}

// routeKey is a token's key in the tree: values route through the wildcard,
// so a message that starts with a number does not get its own branch.
func routeKey(tok string) string {
	if strings.ContainsAny(tok, "0123456789") || strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">") {
		return Wildcard
	}
	return tok
}

// similarity is the share of a template's tokens that a message matches,
// with wildcards counted as matching: Drain's simSeq, with the wildcard
// count as the tie-breaker.
func similarity(template, tokens []string) (float64, int) {
	if len(template) == 0 {
		return 1, 0
	}
	same, wild := 0, 0
	for i, t := range template {
		switch t {
		case Wildcard:
			wild++
		case tokens[i]:
			same++
		}
	}
	return float64(same) / float64(len(template)), wild
}
