package codefix

import (
	"errors"
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/logs"
)

// lineOf is the 1-based line of src holding marker.
func lineOf(t *testing.T, src, marker string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, marker) {
			return i + 1
		}
	}
	t.Fatalf("%q is not in the source", marker)
	return 0
}

// Each language's crash, at the line the runtime printed or by the name it
// gave, gets its function put back as it was, and the commit's other
// change to the same file stays.
func TestTheCrashingFunctionIsPutBackInEveryLanguage(t *testing.T) {
	cases := []struct {
		name, language, file string
		good, bad            string
		frame                func(bad string) logs.Frame
		function             string
		restored, kept       string
	}{
		{
			name: "python", language: "python", file: "shop/catalog.py",
			good: "class Catalog:\n    def load(self):\n        return dict(DEFAULTS)\n\n    def greet(self):\n        return 'hello'\n",
			bad:  "class Catalog:\n    def load(self):\n        return DEFAULTS['missing']  # BUG\n\n    def greet(self):\n        return 'welcome'\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "catalog.load", File: "/app/shop/catalog.py", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Catalog.load", restored: "return dict(DEFAULTS)", kept: "'welcome'",
		},
		{
			name: "python, decorated and nested", language: "python", file: "app.py",
			good: "@app.route('/')\ndef index():\n    def render():\n        return 'ok'\n    return render()\n",
			bad:  "@app.route('/')\ndef index():\n    def render():\n        return None.x  # BUG\n    return render()\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{File: "/srv/app.py", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "index.render", restored: "return 'ok'",
		},
		{
			name: "javascript", language: "node", file: "server.js",
			good: "function convert(x) {\n  return Number(x).toFixed(2);\n}\nconst greet = () => 'hello';\n",
			bad:  "function convert(x) {\n  return x.code.toFixed(2); // BUG\n}\nconst greet = () => 'welcome';\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "server.convert", File: "/usr/src/app/server.js", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "convert", restored: "Number(x).toFixed(2)", kept: "'welcome'",
		},
		{
			name: "typescript, a method", language: "node", file: "src/cart.ts",
			good: "export class Cart {\n  total(items: number[]): number {\n    return items.reduce((a, b) => a + b, 0);\n  }\n}\n",
			bad:  "export class Cart {\n  total(items: number[]): number {\n    return items[0].valueOf(); // BUG\n  }\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{File: "/app/src/cart.ts", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Cart.total", restored: "items.reduce",
		},
		{
			name: "java, an overload", language: "java", file: "src/main/java/com/example/shop/Catalog.java",
			good: "package com.example.shop;\npublic class Catalog {\n  void load() {\n    name = \"shop\";\n  }\n  void load(String n) {\n    name = n;\n  }\n}\n",
			bad:  "package com.example.shop;\npublic class Catalog {\n  void load() {\n    name = null.toString(); // BUG\n  }\n  void load(String n) {\n    name = n.trim();\n  }\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "com.example.shop.Catalog.load", File: "Catalog.java", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Catalog.load", restored: "name = \"shop\";", kept: "n.trim()",
		},
		{
			name: "kotlin", language: "java", file: "src/main/kotlin/shop/Catalog.kt",
			good: "package shop\nclass Catalog {\n  fun load(): Int {\n    return 1\n  }\n}\n",
			bad:  "package shop\nclass Catalog {\n  fun load(): Int {\n    return error(\"x\") // BUG\n  }\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "shop.Catalog.load", File: "Catalog.kt", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Catalog.load", restored: "return 1",
		},
		{
			name: "c#, by name alone", language: "dotnet", file: "src/cartstore/RedisCartStore.cs",
			good: "namespace cartservice.cartstore {\n  public class RedisCartStore {\n    public Cart GetCart(string id) {\n      return new Cart();\n    }\n  }\n}\n",
			bad:  "namespace cartservice.cartstore {\n  public class RedisCartStore {\n    public Cart GetCart(string id) {\n      return carts.First(); // BUG\n    }\n  }\n}\n",
			frame: func(string) logs.Frame {
				return logs.Frame{Function: "cartservice.cartstore.RedisCartStore.GetCart", InApp: true}
			},
			function: "cartservice.cartstore.RedisCartStore.GetCart", restored: "return new Cart();",
		},
		{
			name: "ruby", language: "ruby", file: "lib/shop.rb",
			good: "class Shop\n  def load(path)\n    items = {}\n    items[:x]\n  end\nend\n",
			bad:  "class Shop\n  def load(path)\n    items = nil\n    items[:x] # BUG\n  end\nend\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "Shop#load", File: "/app/lib/shop.rb", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Shop.load", restored: "items = {}",
		},
		{
			name: "php", language: "php", file: "src/Catalog.php",
			good: "<?php\nnamespace App;\nclass Catalog {\n  public function items() {\n    return $this->db->all();\n  }\n}\n",
			bad:  "<?php\nnamespace App;\nclass Catalog {\n  public function items() {\n    return $this->cache->all(); // BUG\n  }\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: `App\Catalog->items`, File: "/var/www/src/Catalog.php", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Catalog.items", restored: "$this->db->all()",
		},
		{
			name: "rust", language: "rust", file: "src/catalog.rs",
			good: "pub struct Catalog { items: Vec<u32> }\nimpl Catalog {\n    pub fn first(&self) -> u32 {\n        self.items.first().copied().unwrap_or(0)\n    }\n}\n",
			bad:  "pub struct Catalog { items: Vec<u32> }\nimpl Catalog {\n    pub fn first(&self) -> u32 {\n        self.items[0] // BUG\n    }\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "shop::catalog::Catalog::first", File: "./src/catalog.rs", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "Catalog.first", restored: "unwrap_or(0)",
		},
		{
			name: "c", language: "c", file: "src/catalog.c",
			good: "#include <stdlib.h>\nint load(int *items) {\n  if (!items) return 0;\n  return items[0];\n}\n",
			bad:  "#include <stdlib.h>\nint load(int *items) {\n  return items[0]; /* BUG */\n}\n",
			frame: func(bad string) logs.Frame {
				return logs.Frame{Function: "load", File: "src/catalog.c", Line: lineOf(t, bad, "BUG"), InApp: true}
			},
			function: "load", restored: "if (!items) return 0;",
		},
		{
			name: "c++, defined outside its class, by name", language: "cpp", file: "src/catalog.cpp",
			good: "#include \"catalog.h\"\nnamespace shop {\nint Catalog::load(int x) {\n  return x;\n}\n}\n",
			bad:  "#include \"catalog.h\"\nnamespace shop {\nint Catalog::load(int x) {\n  return *(int*)0; // BUG\n}\n}\n",
			frame: func(string) logs.Frame {
				return logs.Frame{Function: "shop::Catalog::load", InApp: true}
			},
			function: "shop.Catalog.load", restored: "return x;",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			good := map[string][]byte{tc.file: []byte(tc.good), "README.md": []byte("shop")}
			bad := map[string][]byte{tc.file: []byte(tc.bad), "README.md": []byte("shop")}
			p, err := Propose(tc.language, []logs.Frame{tc.frame(tc.bad)}, good, bad)
			if err != nil {
				t.Fatal(err)
			}
			after := string(p.After)
			switch {
			case p.Path != tc.file || p.Function != tc.function:
				t.Fatalf("put back %s in %s; want %s in %s", p.Function, p.Path, tc.function, tc.file)
			case !strings.Contains(after, tc.restored) || strings.Contains(after, "BUG"):
				t.Fatalf("the fix does not put the function back:\n%s", after)
			case tc.kept != "" && !strings.Contains(after, tc.kept):
				t.Fatalf("the fix undid the commit's other change:\n%s", after)
			}
		})
	}
}

// The restore abstains, saying why, where it cannot be sure: the crash ran
// through nothing the commit changed, the function is new, two functions
// could be the one the runtime named, or putting it back breaks the file.
func TestTheRestoreAbstainsWhereItCannotBeSure(t *testing.T) {
	py := func(s string) map[string][]byte { return map[string][]byte{"app.py": []byte(s)} }
	frame := func(src string) []logs.Frame {
		return []logs.Frame{{File: "/app/app.py", Line: lineOf(t, src, "HERE"), InApp: true}}
	}
	unchanged := "def load():\n    return 1  # HERE\n"
	if _, err := Propose("python", frame(unchanged), py(unchanged), py(unchanged)); !errors.Is(err, ErrNoMeeting) {
		t.Errorf("an unchanged function: %v", err)
	}
	added := "def load():\n    return 1\n\ndef fresh():\n    return None.x  # HERE\n"
	if _, err := Propose("python", frame(added), py("def load():\n    return 1\n"), py(added)); err == nil || !strings.Contains(err.Error(), "nothing to put back") {
		t.Errorf("a function the commit added: %v", err)
	}
	twice := map[string][]byte{"a.cs": []byte("class A { void Run() { } }\n"), "b.cs": []byte("class A { void Run() { } }\n")}
	if _, err := Propose("dotnet", []logs.Frame{{Function: "A.Run", InApp: true}}, twice, twice); err == nil || !strings.Contains(err.Error(), "could be") {
		t.Errorf("two functions with the runtime's name: %v", err)
	}
	if _, err := Propose("cobol", frame(unchanged), py(unchanged), py(unchanged)); err == nil {
		t.Error("a language without a grammar was accepted")
	}
	outside := []logs.Frame{{File: "/usr/lib/python3/json.py", Line: 3, InApp: true}}
	if _, err := Propose("python", outside, py(unchanged), py(unchanged)); !errors.Is(err, ErrNoMeeting) {
		t.Errorf("a frame in no file of the repository: %v", err)
	}
}

// A function the commit moved to another file is found where it was, and
// put back where it is now.
func TestAMovedFunctionIsFoundWhereItWas(t *testing.T) {
	good := map[string][]byte{"util.js": []byte("function parse(s) {\n  return JSON.parse(s);\n}\nmodule.exports = { parse };\n")}
	bad := map[string][]byte{"lib/parse.js": []byte("function parse(s) {\n  return s.body.data; // HERE\n}\nmodule.exports = { parse };\n")}
	p, err := Propose("node", []logs.Frame{{File: "/app/lib/parse.js", Line: 2, InApp: true}}, good, bad)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != "lib/parse.js" || !strings.Contains(string(p.After), "JSON.parse(s)") {
		t.Fatalf("put back in %s:\n%s", p.Path, p.After)
	}
}

// A failure that left no stack is found where the source logs its error:
// the longest piece masking kept, in one place; a piece logged in many
// places says nothing.
func TestTheSourceThatLogsAnErrorStandsInForAStack(t *testing.T) {
	files := map[string][]byte{
		"catalog.py": []byte("def load(path):\n    if not path:\n        log.error('could not load the catalog from %s', path)\n        raise SystemExit(1)\n"),
		"util.py":    []byte("def a():\n    log.error('retrying')\ndef b():\n    log.error('retrying')\ndef c():\n    log.error('retrying')\ndef d():\n    log.error('retrying')\n"),
	}
	frames, language := LoggedAt([]string{"retrying", "ERROR could not load the catalog from <path>", "<num> retrying in <duration>"}, files)
	if language != "python" || len(frames) != 1 || frames[0].File != "catalog.py" || frames[0].Line != 3 {
		t.Fatalf("%s %+v", language, frames)
	}
	p, err := Propose(language, frames, map[string][]byte{"catalog.py": []byte("def load(path):\n    return read(path)\n")}, files)
	if err != nil || p.Function != "load" {
		t.Fatalf("%+v %v", p, err)
	}
}

// Where an image's labels do not name its source, the Fixer's sources do,
// and its tag names the commit.
func TestAnImageWithoutLabelsIsResolvedFromTheFixersSources(t *testing.T) {
	sources := map[string]string{"registry.test/shop": "https://forge.test/team/shop", "team/api": "https://forge.test/team/api"}
	for image, want := range map[string]Built{
		"registry.test/shop:4f2a9c1":     {Repository: "https://forge.test/team/shop", Revision: "4f2a9c1"},
		"registry.test/shop:sha-4F2A9C1": {Repository: "https://forge.test/team/shop", Revision: "4f2a9c1"},
		"team/api:9e1b2c3d":              {Repository: "https://forge.test/team/api", Revision: "9e1b2c3d"},
	} {
		got, err := ResolveSourceWith(image, nil, sources)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", image, got, err)
		}
	}
	for _, image := range []string{"registry.test/shop:v1.2.0", "registry.test/other:4f2a9c1"} {
		if _, err := ResolveSourceWith(image, nil, sources); err == nil {
			t.Errorf("%s resolved", image)
		}
	}
	labelled := map[string]string{"org.opencontainers.image.source": "https://forge.test/team/shop", "org.opencontainers.image.revision": "4f2a9c1d00"}
	if got, err := ResolveSourceWith("registry.test/shop:4f2a9c1", labelled, nil); err != nil || got.Revision != "4f2a9c1d00" {
		t.Errorf("labels: %+v %v", got, err)
	}
}
