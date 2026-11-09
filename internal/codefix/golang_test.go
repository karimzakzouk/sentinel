package codefix

import (
	"errors"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

func TestParseGoFrame(t *testing.T) {
	for in, want := range map[string]string{
		"main.(*server).handle":                    "main.(server).handle",
		"github.com/x/app/cart.(*Store).Get.func1": "github.com/x/app/cart.(Store).Get",
		"github.com/x/app/cart.Load[...]":          "github.com/x/app/cart.Load",
		"github.com/x/app/cart.(*Cache[...]).Get":  "github.com/x/app/cart.(Cache).Get",
		"example.com/app.main":                     "example.com/app.main",
		"net/http.(*conn).serve":                   "net/http.(conn).serve",
	} {
		f, ok := ParseGoFrame(in)
		if !ok || f.String() != want {
			t.Errorf("%s: %s, %v; want %s", in, f, ok, want)
		}
	}
}

const goMod = "module github.com/x/shop\n\ngo 1.27\n"

const cartGood = `package cart

// Get returns the cart.
func (s *Store) Get(id string) *Cart {
	c := s.carts[id]
	if c == nil {
		return &Cart{}
	}
	return c
}

func (s *Store) Count() int { return len(s.carts) }
`

// The culprit commit dropped the nil check, and also changed Count.
const cartBad = `package cart

// Get returns the cart.
func (s *Store) Get(id string) *Cart {
	c := s.carts[id]
	c.Seen++
	return c
}

func (s *Store) Count() int { return len(s.carts) + 0 }
`

func files(cart string) map[string][]byte {
	return map[string][]byte{"go.mod": []byte(goMod), "cart/store.go": []byte(cart),
		"main.go": []byte("package main\n\nfunc main() {}\n")}
}

// The crash ran through Get, which the commit changed: Get goes back as it
// was, and Count, which the commit changed too but the crash did not run
// through, stays.
func TestTheCrashingFunctionGoesBackAsItWas(t *testing.T) {
	frames := []logs.Frame{
		{Function: "github.com/x/shop/cart.(*Store).Get", InApp: true},
		{Function: "main.main", InApp: true},
		{Function: "runtime.goexit"},
	}
	p, err := ProposeGo(frames, files(cartGood), files(cartBad))
	if err != nil {
		t.Fatal(err)
	}
	after := string(p.After)
	if p.Path != "cart/store.go" || p.Line != 4 || !strings.Contains(after, "if c == nil") || !strings.Contains(after, "len(s.carts) + 0") {
		t.Fatalf("patch of %s line %d:\n%s", p.Path, p.Line, after)
	}
}

// A crash in a function the commit left alone, or in one it added, has no
// function to put back.
func TestNoPatchWithoutAFunctionToPutBack(t *testing.T) {
	frames := []logs.Frame{{Function: "main.main", InApp: true}}
	if _, err := ProposeGo(frames, files(cartGood), files(cartBad)); !errors.Is(err, ErrNoMeeting) {
		t.Fatalf("an unchanged function: %v", err)
	}
	added := strings.Replace(cartBad, "func (s *Store) Count", "func (s *Store) Reset() { s.carts = nil }\n\nfunc (s *Store) Count", 1)
	frames = []logs.Frame{{Function: "github.com/x/shop/cart.(*Store).Reset", InApp: true}}
	if _, err := ProposeGo(frames, files(cartGood), files(added)); err == nil || !strings.Contains(err.Error(), "nothing to put back") {
		t.Fatalf("a new function: %v", err)
	}
}
