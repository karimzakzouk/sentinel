package logs

import (
	"fmt"
	"strings"
	"testing"
)

func parse(t *testing.T, trace string) Exception {
	t.Helper()
	recs := Assemble(lines(trace))
	if len(recs) != 1 {
		t.Fatalf("trace assembled into %d records", len(recs))
	}
	e, ok := ParseException(recs[0])
	if !ok {
		t.Fatalf("no exception in:\n%s", trace)
	}
	return e
}

func inApp(e Exception) []string {
	var out []string
	for _, f := range e.Frames {
		if f.InApp {
			out = append(out, f.Function)
		}
	}
	return out
}

func TestParseException(t *testing.T) {
	for name, tc := range map[string]struct {
		trace, language, types, inApp string
	}{
		"go": {goPanicTrace, "go", "[runtime error: invalid memory address or nil pointer dereference]",
			"[main.(*server).handle main.main]"},
		"java": {javaTrace, "java", "[java.lang.IllegalStateException java.lang.NullPointerException]",
			"[com.example.shop.Catalog.load com.example.shop.Catalog.<init>]"},
		"python":         {pythonTrace, "python", "[redis.exceptions.ConnectionError]", "[main.main main.<module>]"},
		"python chained": {pythonChainedTrace, "python", "[KeyError RuntimeError]", "[config.load main.<module>]"},
		"node":           {nodeTrace, "node", "[TypeError]", "[server.convert server.<anonymous>]"},
		"dotnet": {dotnetTrace, "dotnet", "[System.InvalidOperationException]",
			"[cartservice.cartstore.RedisCartStore.GetCartAsync cartservice.services.CartService.GetCart]"},
		"logged java": {loggedJavaTrace, "java",
			"[<time> <time> ERROR <num> --- [main] o.s.boot.SpringApplication : Application run failed]",
			"[com.example.shop.App.main]"},
		"ruby":                     {rubyTrace, "ruby", "[NoMethodError]", "[Shop#load Shop.run <main>]"},
		"ruby, before 3.4":         {rubyOldTrace, "ruby", "[NoMethodError]", "[load <main>]"},
		"php":                      {phpTrace, "php", "[Error]", "[App\\Catalog->items App\\Shop->run {main}]"},
		"rust":                     {rustTrace, "rust", "[panic: index out of bounds: the len is <num> but the index is <num>]", "[shop::catalog::Catalog::first shop::main]"},
		"rust without a backtrace": {rustShortTrace, "rust", "[panic: called `Option::unwrap()` on a `None` value]", "[]"},
		"glibc assert":             {glibcAssertTrace, "c", "[assertion failed: items != NULL]", "[load]"},
		"musl assert":              {muslAssertTrace, "cpp", "[assertion failed: items != NULL]", "[shop::Catalog::load]"},
		"abseil":                   {abseilTrace, "cpp", "[SIGSEGV]", "[shop::Catalog::Load main]"},
		"backward-cpp":             {backwardTrace, "cpp", "[crash]", "[shop::Catalog::load shop::Catalog::run main]"},
	} {
		t.Run(name, func(t *testing.T) {
			e := parse(t, tc.trace)
			if e.Language != tc.language || fmt.Sprint(e.Types) != tc.types || fmt.Sprint(inApp(e)) != tc.inApp {
				t.Errorf("got %s %v in-app %v\nwant %s %s in-app %s", e.Language, e.Types, inApp(e), tc.language, tc.types, tc.inApp)
			}
			if !strings.HasPrefix(e.Fingerprint, FingerprintVersion+"-") {
				t.Errorf("fingerprint %q", e.Fingerprint)
			}
		})
	}
}

// The same bug, seen again with different line numbers, addresses and
// message values, must group; a different bug must not.
func TestFingerprintsGroupTheSameErrorOnly(t *testing.T) {
	same := map[string][2]string{
		"go":     {goPanicTrace, strings.NewReplacer("server.go:42", "server.go:44", "0x4a3b2c", "0x4a3f00", "0x7f8a12", "0x7f9b00").Replace(goPanicTrace)},
		"java":   {javaTrace, strings.NewReplacer("Catalog.java:42", "Catalog.java:47", `"name"`, `"title"`, "12 more", "9 more").Replace(javaTrace)},
		"python": {pythonTrace, strings.NewReplacer("line 6", "line 8", "redis-cart:6379", "10.0.0.9:6379").Replace(pythonTrace)},
		"node":   {nodeTrace, strings.NewReplacer("162:31", "170:9", "'code'", "'id'").Replace(nodeTrace)},
	}
	for name, pair := range same {
		if a, b := parse(t, pair[0]).Fingerprint, parse(t, pair[1]).Fingerprint; a != b {
			t.Errorf("%s: the same error got fingerprints %s and %s", name, a, b)
		}
	}
	different := [][2]string{
		{javaTrace, strings.Replace(javaTrace, "NullPointerException", "IllegalArgumentException", 1)},
		{pythonTrace, strings.Replace(pythonTrace, "in main\n", "in start\n", 1)},
		{nodeTrace, strings.Replace(nodeTrace, "TypeError", "RangeError", 1)},
		{"Error: connect ECONNREFUSED", "Error: getaddrinfo ENOTFOUND"},
	}
	for _, pair := range different {
		if a, b := parse(t, pair[0]).Fingerprint, parse(t, pair[1]).Fingerprint; a == b {
			t.Errorf("different errors share fingerprint %s:\n%s\n---\n%s", a, pair[0], pair[1])
		}
	}
}

// Each frame says where it ran, as the runtime printed it, so the Fixer can
// find the function in the source; a frame without a source says nothing.
func TestFramesSayWhereTheyRan(t *testing.T) {
	type at struct {
		file string
		line int
	}
	for name, tc := range map[string]struct {
		trace string
		want  []at
	}{
		"go":           {goPanicTrace, []at{{"/app/server.go", 42}, {"/usr/local/go/src/net/http/server.go", 2136}, {"/app/main.go", 17}}},
		"java":         {javaTrace, []at{{"Catalog.java", 42}, {"Catalog.java", 20}, {"Constructor.java", 480}}},
		"python":       {pythonTrace, []at{{"/usr/local/lib/python3.11/site-packages/redis/connection.py", 707}, {"/app/main.py", 6}, {"/app/main.py", 10}}},
		"node":         {nodeTrace, []at{{"/usr/src/app/server.js", 162}, {"/usr/src/app/server.js", 110}, {"", 0}, {"", 0}}},
		"dotnet":       {dotnetTrace, []at{{"", 0}, {"", 0}, {"/app/cartstore/RedisCartStore.cs", 87}, {"/app/services/CartService.cs", 43}}},
		"ruby":         {rubyTrace, []at{{"/app/lib/shop.rb", 4}, {"/usr/local/bundle/gems/rack-3.1.0/lib/rack/builder.rb", 12}, {"/app/lib/shop.rb", 10}, {"/app/app.rb", 3}}},
		"php":          {phpTrace, []at{{"/var/www/src/Catalog.php", 12}, {"/var/www/src/Shop.php", 30}, {"/var/www/vendor/slim/slim/Slim/App.php", 205}, {"/var/www/public/index.php", 8}}},
		"rust":         {rustTrace, []at{{"/rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/std/src/panicking.rs", 665}, {"/rustc/90b35a6239c3d8bdabc530a6a0816f7ff89a0aaf/library/core/src/panicking.rs", 266}, {"./src/catalog.rs", 17}, {"./src/main.rs", 6}}},
		"rust, short":  {rustShortTrace, []at{{"src/main.rs", 6}}},
		"glibc assert": {glibcAssertTrace, []at{{"src/catalog.c", 12}}},
		"backward-cpp": {backwardTrace, []at{{"/src/catalog.cpp", 12}, {"/src/catalog.cpp", 31}, {"/src/main.cpp", 20}}},
	} {
		t.Run(name, func(t *testing.T) {
			var got []at
			for _, f := range parse(t, tc.trace).Frames {
				got = append(got, at{f.File, f.Line})
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("frames ran at %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestPlainRecordsAreNotExceptions(t *testing.T) {
	for _, text := range []string{"listening on :8080", "level=error msg=\"db down\"", "  indented"} {
		if e, ok := ParseException(Record{Lines: []string{text}}); ok {
			t.Errorf("%q parsed as %+v", text, e)
		}
	}
}
