package redact

import (
	"math"
	"strings"
	"sync"
)

// identifierContexts precede identifiers that look random but are public.
var identifierContexts = []string{"sha256:", "sha512:", "sha1:", "containerd://", "docker://", "cri-o://"}

// entropyMargin is how far, in bits per character, a string may fall below the
// expected entropy of a random string of its length and still count as random.
// Random strings vary by about 0.15 bits at the lengths that matter.
const entropyMargin = 0.6

// randomStrings finds tokens as random as generated secrets. A token that
// touches a secret already found is redacted whole under that secret's rule:
// the rest of it is most likely part of the secret.
func randomStrings(s string, found, identifiers spans) []Finding {
	var out []Finding
	// Runs of the characters secrets are written in, with base64 padding. "="
	// is otherwise a delimiter, so "state=<random>" is two tokens.
	for _, m := range scanTokens(s) {
		f := Finding{Rule: "random-string", Start: m[0], End: m[1]}
		if _, ok := identifiers.find(f); ok || afterIdentifierContext(s, m[0]) {
			continue
		}
		if g, ok := found.find(f); ok {
			f.Rule = g.Rule
			out = append(out, f)
			continue
		}
		if randomToken(s[m[0]:m[1]]) {
			out = append(out, f)
		}
	}
	return out
}

func randomToken(tok string) bool {
	if lowercaseName(tok) {
		return false
	}
	parts := strings.FieldsFunc(tok, isSeparator)
	for _, p := range parts {
		if len(p) >= 16 && looksRandom(p) {
			return true
		}
	}
	// Base64 secrets contain separators, so a token without the marks of a
	// name or a path is also judged whole: no dots, and no English word among
	// its parts.
	if len(parts) < 2 || strings.ContainsRune(tok, '.') {
		return false
	}
	for _, p := range parts {
		if englishWord(p) {
			return false
		}
	}
	joined := strings.Join(parts, "")
	return len(joined) >= 16 && looksRandom(joined)
}

// englishWord reports a part of five or more letters, cased like a word (word,
// WORD or Word) and reading like English. Fragments of random base64 are
// rarely cased like words.
func englishWord(p string) bool {
	if len(p) < 5 {
		return false
	}
	upper := 0
	for i := 0; i < len(p); i++ {
		switch charClass(p[i]) {
		case 1:
			upper++
		case 2, 3:
			return false
		}
	}
	cased := upper == 0 || upper == len(p) || upper == 1 && charClass(p[0]) == 1
	return cased && englishness(p) >= 0
}

func isSeparator(r rune) bool {
	return r == '-' || r == '_' || r == '.' || r == '/' || r == '+' || r == '='
}

// lowercaseName recognizes Kubernetes names, hostnames, image references and
// paths: lower case, and every part a number, short, or English-like.
// Generated parts of Kubernetes names (pod-template hashes, pod suffixes,
// UIDs) are short.
func lowercaseName(tok string) bool {
	if strings.ContainsFunc(tok, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		return false
	}
	for _, part := range strings.FieldsFunc(tok, isSeparator) {
		number := strings.Trim(part, "0123456789") == ""
		if !number && len(part) > 12 && englishness(part) < 0 {
			return false
		}
	}
	return true
}

func afterIdentifierContext(s string, i int) bool {
	for _, c := range identifierContexts {
		if strings.HasSuffix(s[:i], c) {
			return true
		}
	}
	return false
}

// looksRandom compares an alphanumeric string with a random one of the same
// length and alphabet. Numbers, single-case words and identifiers made of
// words (camelCase class names, package paths) are not random.
func looksRandom(s string) bool {
	var lower, upper, digit int
	for i := 0; i < len(s); i++ {
		switch charClass(s[i]) {
		case 0:
			lower++
		case 1:
			upper++
		case 2:
			digit++
		}
	}
	if digit == len(s) || digit == 0 && (lower == 0 || upper == 0) {
		return false
	}
	k := 62
	switch {
	case isHex(s):
		k = 16
	case upper == 0 || lower == 0:
		k = 36
	}
	// Hex letters are as common in English as any, so hex is judged on
	// entropy alone.
	if k != 16 && identifier(s, upper > 0 && lower > 0) {
		return false
	}
	return entropy(s) >= expectedEntropy(len(s), k)-entropyMargin
}

// identifier recognizes names made of words: English-like letters, or, in
// mixed case, long capitalized pieces with somewhat English-like letters, as
// in QueryExecutionException. Random strings score about −0.9 and split into
// pieces of two or three characters.
func identifier(s string, mixedCase bool) bool {
	e := englishness(s)
	return e >= 0 || mixedCase && e >= -0.5 && meanPieceLength(s) >= 5
}

// meanPieceLength is the length-weighted mean length of the pieces s splits
// into at case changes and digits: HTTPConnectionPool2 gives HTTP,
// Connection, Pool and 2.
func meanPieceLength(s string) float64 {
	sumSquares, start := 0, 0
	for i := 1; i <= len(s); i++ {
		if i < len(s) && !pieceBoundary(s, i) {
			continue
		}
		sumSquares += (i - start) * (i - start)
		start = i
	}
	return float64(sumSquares) / float64(len(s))
}

func pieceBoundary(s string, i int) bool {
	prev, cur := charClass(s[i-1]), charClass(s[i])
	switch {
	case prev == 2 || cur == 2:
		return prev != cur
	case prev == 0 && cur == 1:
		return true
	case prev == 1 && cur == 1:
		return i+1 < len(s) && charClass(s[i+1]) == 0
	}
	return false
}

// englishLetterFrequency is the share of each letter, a to z, in English text.
var englishLetterFrequency = [26]float64{
	0.08167, 0.01492, 0.02782, 0.04253, 0.12702, 0.02228, 0.02015, 0.06094, 0.06966, 0.00153, 0.00772, 0.04025, 0.02406,
	0.06749, 0.07507, 0.01929, 0.00095, 0.05987, 0.06327, 0.09056, 0.02758, 0.00978, 0.02360, 0.00150, 0.01974, 0.00074,
}

// digitScore makes digits count against English-likeness: identifiers carry
// few, random strings many.
const digitScore = -1.0

// englishness is the mean log-likelihood ratio, in bits per character, of
// English letter frequencies against uniformly random letters.
func englishness(s string) float64 {
	sum := 0.0
	for i := 0; i < len(s); i++ {
		switch c := s[i] | 0x20; {
		case c >= 'a' && c <= 'z':
			sum += math.Log2(englishLetterFrequency[c-'a'] * 26)
		case s[i] >= '0' && s[i] <= '9':
			sum += digitScore
		}
	}
	return sum / float64(len(s))
}

func charClass(b byte) int {
	switch {
	case b >= 'a' && b <= 'z':
		return 0
	case b >= 'A' && b <= 'Z':
		return 1
	case b >= '0' && b <= '9':
		return 2
	}
	return 3
}

func isHex(s string) bool {
	lower, upper := true, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		digit := c >= '0' && c <= '9'
		lower = lower && (digit || c >= 'a' && c <= 'f')
		upper = upper && (digit || c >= 'A' && c <= 'F')
	}
	return lower || upper
}

// entropy is the plug-in Shannon entropy of s, in bits per character.
func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	n, h := float64(len(s)), 0.0
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

var expected sync.Map

// expectedEntropy is the mean plug-in entropy of a uniformly random string of
// n characters from an alphabet of k: log2 n − (k/n)·E[C log2 C], with
// C ~ Binomial(n, 1/k) the count of one character. Short random strings
// repeat few characters, so their entropy is well below log2 k.
func expectedEntropy(n, k int) float64 {
	n = min(n, 512)
	key := [2]int{n, k}
	if v, ok := expected.Load(key); ok {
		return v.(float64)
	}
	p := 1 / float64(k)
	var e float64
	for c := 2; c <= n; c++ {
		logP := logChoose(n, c) + float64(c)*math.Log(p) + float64(n-c)*math.Log1p(-p)
		e += math.Exp(logP) * float64(c) * math.Log2(float64(c))
	}
	v := math.Log2(float64(n)) - float64(k)/float64(n)*e
	expected.Store(key, v)
	return v
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}
