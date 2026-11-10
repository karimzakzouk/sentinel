package codefix

import (
	"context"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// model replies in turn, and keeps what it was asked.
type model struct {
	replies []string
	asked   []string
}

func (m *model) Complete(_ context.Context, _, user string) (string, error) {
	m.asked = append(m.asked, user)
	r := m.replies[0]
	if len(m.replies) > 1 {
		m.replies = m.replies[1:]
	}
	return r, nil
}

const coupledGood = "class Cart:\n    def total(self):\n        return sum(self.prices())\n\n    def prices(self):\n        return [i.price for i in self.items]\n"
const coupledBad = "class Cart:\n    def total(self):\n        return sum(self.prices()['all'])\n\n    def prices(self):\n        return [i.price for i in self.items]\n"

func coupledInput(t *testing.T) Input {
	return Input{Language: "python", Types: []string{"TypeError"}, Message: "list indices must be integers or slices, not str",
		Frames: []logs.Frame{{Function: "cart.total", File: "/app/cart.py", Line: lineOf(t, coupledBad, "['all']"), InApp: true}},
		Good:   map[string][]byte{"cart.py": []byte(coupledGood)}, Bad: map[string][]byte{"cart.py": []byte(coupledBad)}}
}

// The model sees the crash, the function as it is and as it was, and what
// was tried; the function it returns, without its indentation, is put
// back where the method sat, and the file parses.
func TestTheModelsFunctionIsPutInPlace(t *testing.T) {
	m := &model{replies: []string{"Here you go.\n\nFILE: cart.py\nFUNCTION: Cart.total\n```python\ndef total(self):\n    prices = self.prices()\n    return sum(prices)\n```\n"}}
	changes, err := LLM{Model: m, Retries: 2, Tried: []string{"restore: the Lab found it still crashed"}}.Propose(context.Background(), coupledInput(t))
	if err != nil {
		t.Fatal(err)
	}
	got := string(changes[0].Edits[0].After)
	want := "class Cart:\n    def total(self):\n        prices = self.prices()\n        return sum(prices)\n\n    def prices(self):"
	if !strings.HasPrefix(got, want) || changes[0].Fixer != "llm" {
		t.Fatalf("put in place:\n%s", got)
	}
	asked := m.asked[0]
	for _, w := range []string{"TypeError", "list indices must be integers", "cart.total /app/cart.py:3", "Before the culprit commit:",
		"return sum(self.prices())", "restore: the Lab found it still crashed"} {
		if !strings.Contains(asked, w) {
			t.Errorf("the model was not told %q:\n%s", w, asked)
		}
	}
}

// A reply that does not parse, or changes a function the model was not
// shown, is sent back; the model's next reply is used.
func TestAReplyThatDoesNotApplyIsSentBack(t *testing.T) {
	m := &model{replies: []string{
		"FILE: cart.py\nFUNCTION: Cart.total\n```\ndef total(self:\n    return\n```",
		"FILE: cart.py\nFUNCTION: Cart.checkout\n```\ndef checkout(self):\n    pass\n```",
		"FILE: cart.py\nFUNCTION: total\n```\ndef total(self):\n    return sum(self.prices())\n```",
	}}
	changes, err := LLM{Model: m, Retries: 2}.Propose(context.Background(), coupledInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.asked) != 3 || !strings.Contains(m.asked[1], "could not be used") || !strings.Contains(m.asked[2], "was not shown") {
		t.Fatalf("asked %d times:\n%s", len(m.asked), strings.Join(m.asked, "\n---\n"))
	}
	if !strings.Contains(string(changes[0].Edits[0].After), "return sum(self.prices())\n\n    def prices") {
		t.Fatalf("%s", changes[0].Edits[0].After)
	}
	m = &model{replies: []string{"I cannot help with that."}}
	if _, err := (LLM{Model: m, Retries: 1}).Propose(context.Background(), coupledInput(t)); err == nil || len(m.asked) != 2 {
		t.Fatalf("a reply without functions: %v after %d asks", err, len(m.asked))
	}
}

// Two functions changed together, in a brace language, come back as one
// change.
func TestTheModelMayChangeFunctionsTogether(t *testing.T) {
	good := "class Cart {\n  total() {\n    return this.sum(this.items);\n  }\n  sum(xs) {\n    return xs.reduce((a, b) => a + b, 0);\n  }\n}\n"
	bad := "class Cart {\n  total() {\n    return this.sum(this.items, 'usd');\n  }\n  sum(xs, cur) {\n    return xs[cur].reduce((a, b) => a + b, 0);\n  }\n}\n"
	in := Input{Language: "node", Types: []string{"TypeError"}, Message: "Cannot read properties of undefined (reading 'reduce')",
		Frames: []logs.Frame{{File: "/app/cart.js", Line: 6, InApp: true}, {File: "/app/cart.js", Line: 3, InApp: true}},
		Good:   map[string][]byte{"cart.js": []byte(good)}, Bad: map[string][]byte{"cart.js": []byte(bad)}}
	m := &model{replies: []string{"FILE: cart.js\nFUNCTION: Cart.sum\n```js\nsum(xs) {\n  return xs.reduce((a, b) => a + b, 0);\n}\n```\n" +
		"FILE: cart.js\nFUNCTION: Cart.total\n```js\ntotal() {\n  return this.sum(this.items);\n}\n```\n"}}
	changes, err := LLM{Model: m}.Propose(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(changes[0].Edits[0].After); got != good || changes[0].Function != "Cart.sum, Cart.total" {
		t.Fatalf("%s:\n%s", changes[0].Function, got)
	}
}
