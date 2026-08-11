package codeintel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGoFile(t *testing.T) {
	dir := t.TempDir()
	src := `package main

import "fmt"

type Payment struct {
	Amount int
}

func processPayment(amount int) error {
	fmt.Println("processing")
	return nil
}

func (p *Payment) Refund() error {
	processPayment(p.Amount)
	return nil
}
`
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte(src), 0o644)

	symbols, edges, err := parseFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}

	wantSymbols := map[string]bool{
		"processPayment": false,
		"Refund":         false,
		"Payment":        false,
	}
	for _, s := range symbols {
		wantSymbols[s.Name] = true
	}
	for name, found := range wantSymbols {
		if !found {
			t.Errorf("expected symbol %q not found", name)
		}
	}

	var foundEdge bool
	for _, e := range edges {
		if e.Callee == "processPayment" && e.Caller.Name == "Refund" {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Error("expected call edge Refund -> processPayment")
	}
}

func TestParsePythonFile(t *testing.T) {
	dir := t.TempDir()
	src := `def create_payment(amount, currency):
    validate_amount(amount)
    return amount

class PaymentProcessor:
    def process(self, payment):
        return create_payment(payment.amount, "USD")
`
	path := filepath.Join(dir, "main.py")
	os.WriteFile(path, []byte(src), 0o644)

	symbols, _, err := parseFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}

	wantSymbols := map[string]bool{
		"create_payment":   false,
		"PaymentProcessor": false,
		"process":          false,
	}
	for _, s := range symbols {
		wantSymbols[s.Name] = true
	}
	for name, found := range wantSymbols {
		if !found {
			t.Errorf("expected symbol %q not found", name)
		}
	}
}

func TestBuildIndex(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "app"), 0o755)

	os.WriteFile(filepath.Join(dir, "app", "main.go"), []byte(`package app
func HandleRequest() {
	ProcessPayment()
}
func ProcessPayment() {}
`), 0o644)

	os.WriteFile(filepath.Join(dir, "app", "util.py"), []byte(`def validate(amount):
    check_positive(amount)
def check_positive(n):
    pass
`), 0o644)

	os.MkdirAll(filepath.Join(dir, "node_modules", "lib"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "lib", "x.js"), []byte(`function ignore_me() {}`), 0o644)

	idx, err := BuildIndex(dir, 100)
	if err != nil {
		t.Fatal(err)
	}

	if len(idx.Symbols) < 4 {
		t.Errorf("expected >=4 symbols, got %d", len(idx.Symbols))
	}

	callers := idx.FindCallers("ProcessPayment")
	if len(callers) != 1 {
		t.Errorf("expected 1 caller of ProcessPayment, got %d", len(callers))
	}

	callers = idx.FindCallers("check_positive")
	if len(callers) != 1 {
		t.Errorf("expected 1 caller of check_positive, got %d", len(callers))
	}

	for _, f := range idx.CallGraph.Files {
		if strings.Contains(f, "node_modules") {
			t.Error("node_modules should be ignored")
		}
	}
}

func TestFormatSymbolList(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
func Foo() {}
func Bar() {}
`), 0o644)

	idx, _ := BuildIndex(dir, 10)
	out := idx.FormatSymbolList()

	if !strings.Contains(out, "Foo") || !strings.Contains(out, "Bar") {
		t.Errorf("expected Foo + Bar in output, got: %s", out)
	}
}

func TestFindCallees(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main
func Outer() {
	Inner1()
	Inner2()
}
func Inner1() {}
func Inner2() {}
`), 0o644)

	idx, _ := BuildIndex(dir, 10)
	callees := idx.FindCallees("Outer")

	if len(callees) != 2 {
		t.Errorf("expected 2 callees of Outer, got %d", len(callees))
	}
}

func TestParseJSFile(t *testing.T) {
	dir := t.TempDir()
	src := `function handleSubmit(event) {
    validateForm(event);
    submitData();
}

class FormHandler {
    constructor() {
        this.init();
    }
}
`
	path := filepath.Join(dir, "app.js")
	os.WriteFile(path, []byte(src), 0o644)

	symbols, _, err := parseFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}

	wantSymbols := map[string]bool{
		"handleSubmit": false,
		"FormHandler":  false,
		"constructor":  false,
	}
	for _, s := range symbols {
		wantSymbols[s.Name] = true
	}
	for name, found := range wantSymbols {
		if !found {
			t.Errorf("expected symbol %q not found", name)
		}
	}
}

func TestIsBuiltin(t *testing.T) {
	if !isBuiltin("print") {
		t.Error("print should be builtin")
	}
	if isBuiltin("processPayment") {
		t.Error("processPayment should not be builtin")
	}
}
