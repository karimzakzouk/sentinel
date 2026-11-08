package redact

import (
	"fmt"
	"regexp"
	"testing"
)

// The expressions the scanners implement.
var (
	keyRE   = regexp.MustCompile(`["']?([A-Za-z_][A-Za-z0-9_.-]{0,63})["']?[ \t]*(=>|:=|=|:)[ \t]*`)
	flagRE  = regexp.MustCompile(`--?([A-Za-z][A-Za-z0-9_-]{1,63})(?:=|[ \t]+)`)
	tokenRE = regexp.MustCompile(`[A-Za-z0-9+/_.-]{16,}={0,2}`)
)

func scannersDiffer(s string) error {
	var want []keyMatch
	for _, m := range keyRE.FindAllStringSubmatchIndex(s, -1) {
		want = append(want, keyMatch{start: m[0], end: m[1], key: s[m[2]:m[3]], delim: s[m[4]:m[5]]})
	}
	if got := scanKeys(s); fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("keys: scanner %v, expression %v", got, want)
	}
	want = nil
	for _, m := range flagRE.FindAllStringSubmatchIndex(s, -1) {
		want = append(want, keyMatch{start: m[0], end: m[1], key: s[m[2]:m[3]]})
	}
	if got := scanFlags(s); fmt.Sprint(got) != fmt.Sprint(want) {
		return fmt.Errorf("flags: scanner %v, expression %v", got, want)
	}
	var tokens [][2]int
	for _, m := range tokenRE.FindAllStringIndex(s, -1) {
		tokens = append(tokens, [2]int{m[0], m[1]})
	}
	if got := scanTokens(s); fmt.Sprint(got) != fmt.Sprint(tokens) {
		return fmt.Errorf("tokens: scanner %v, expression %v", got, tokens)
	}
	return nil
}

func TestScannersMatchTheirExpressions(t *testing.T) {
	lines := append([]string{
		`"a":"b"`, `'key' => 'v'`, `x:=1`, `--a=b -bc d ---e f`, `well-known value --token x`,
		"k\t:\t v", `"unterminated`, `a` + string(make([]byte, 70)) + `=`, `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=1`,
		`QUJDREVGR0hJSktMTU5PUA===x`, `/a/b/c/d/e/f/g/h/i/j`,
	}, benign...)
	for _, c := range canaries() {
		lines = append(lines, c.line)
	}
	for _, line := range lines {
		if err := scannersDiffer(line); err != nil {
			t.Errorf("%q: %v", line, err)
		}
	}
}

func FuzzScannersMatchTheirExpressions(f *testing.F) {
	f.Add(`password = "x" --token y`)
	f.Add(`'k':=v aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:x`)
	f.Fuzz(func(t *testing.T, s string) {
		if err := scannersDiffer(s); err != nil {
			t.Fatal(err)
		}
	})
}
