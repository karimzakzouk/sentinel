package redact

import (
	"strings"
	"unicode"
)

// secretWords mark a key as holding a secret wherever they appear in it.
var secretWords = set("password", "passwd", "pwd", "pass", "passphrase", "secret", "secrets", "token", "tokens",
	"apikey", "credential", "credentials", "creds", "auth", "authorization", "cookie", "session", "sessionid",
	"signature", "sig", "otp", "pin", "privatekey", "accesskey", "secretkey", "dsn")

// secretStems mark a key as holding a secret even inside a longer word, as in
// PGPASSWORD. "token" is not one: tokenizer.
var secretStems = []string{"password", "passwd", "secret", "apikey", "credential", "privatekey", "accesskey"}

func containsAny(w string, stems []string) bool {
	for _, s := range stems {
		if strings.Contains(w, s) {
			return true
		}
	}
	return false
}

// secretPairs mark a key as holding a secret when the two words are adjacent.
var secretPairs = set("api key", "access key", "secret key", "private key", "signing key", "encryption key",
	"master key", "license key", "account key", "shared key", "storage key", "subscription key", "client key",
	"app key", "auth key", "connection string", "conn str")

// Keys that describe a secret rather than hold it: token_type, max_tokens,
// secretName, password_policy, session_timeout.
var (
	describingHeads = set("type", "types", "count", "counts", "used", "max", "min", "total", "length", "len", "size",
		"url", "uri", "endpoint", "name", "names", "ref", "path", "file", "dir", "expiry", "expires", "expiration",
		"ttl", "timeout", "enabled", "required", "policy", "method", "provider", "mode", "header", "prefix", "kind",
		"version", "format", "field", "source", "hint", "strength", "rotation", "age", "at", "retries", "attempts",
		"changed", "updated", "created", "valid", "rule", "rules", "error", "errors", "status", "state", "result",
		"reason", "message", "msg", "manager", "store", "reset")
	describingModifiers = set("max", "min", "total", "num", "prompt", "completion", "input", "output", "cached",
		"remaining", "has", "is", "no", "use", "enable", "require", "skip", "allow")
)

// Keys whose values are identifiers, not secrets: the randomness check
// leaves them alone. Provider formats still apply.
var (
	identifierHeads = set("id", "ids", "uid", "uuid", "sha", "digest", "revision", "commit", "etag", "checksum",
		"hash", "fingerprint", "version")
	identifierWords = set("trace", "traceid", "traceparent", "span", "spanid", "requestid", "correlation")
)

// benignValues are values that are not secrets whatever the key says.
var benignValues = set("", "true", "false", "yes", "no", "on", "off", "ok", "none", "null", "nil", "undefined",
	"empty", "enabled", "disabled", "required", "optional", "unset", "missing", "invalid", "expired", "redacted",
	"<redacted>", "hidden", "masked")

// authSchemes are the first word of an authorization value; the credential follows.
var authSchemes = set("bearer", "basic", "token", "digest", "negotiate", "ntlm", "apikey", "aws4-hmac-sha256")

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// keyValues returns the values of secret keys, and the values of identifier
// keys, which the randomness check must not redact.
func keyValues(s string) (secrets, identifiers []Finding) {
	classify := func(key string, start, end int) {
		switch keyClass(key) {
		case keySecret:
			if !benignValue(s[start:end]) {
				secrets = append(secrets, Finding{Rule: "secret-value", Start: start, End: end})
			}
		case keyIdentifier:
			identifiers = append(identifiers, Finding{Start: start, End: end})
		}
	}
	// Keys and their delimiters: key=, key: , "key": , key => , key := .
	for _, m := range scanKeys(s) {
		start, end := valueSpan(s, m.end, m.delim == ":" || m.delim == "=>")
		classify(m.key, start, end)
	}
	for _, m := range scanFlags(s) {
		// A flag starts the text or follows a space; "well-known value" has none.
		if m.start > 0 && !unicode.IsSpace(rune(s[m.start-1])) {
			continue
		}
		start, end := valueSpan(s, m.end, false)
		classify(m.key, start, end)
	}
	return secrets, identifiers
}

// valueSpan returns the span of the value that starts at i, without quotes.
// Values after a colon may be an authorization scheme and its credential.
func valueSpan(s string, i int, colon bool) (int, int) {
	if i >= len(s) {
		return i, i
	}
	if strings.HasPrefix(s[i:], markerPrefix) {
		return i, i
	}
	switch q := s[i]; q {
	case '"', '\'':
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				j++
			case q:
				return i + 1, j
			}
		}
		return i + 1, len(s)
	case '{', '[':
		// A structure: its own keys are matched separately.
		return i, i
	case '$':
		// A reference such as ${DB_PASSWORD} or $(cat /run/secret) ends at its
		// closing bracket.
		if close := map[byte]byte{'{': '}', '(': ')'}[byteAt(s, i+1)]; close != 0 {
			if j := strings.IndexByte(s[i:], close); j > 0 {
				return i, i + j + 1
			}
		}
	}
	end := scalarEnd(s, i)
	if colon && authSchemes[strings.ToLower(s[i:end])] && end < len(s) && s[end] == ' ' {
		end = scalarEnd(s, end+1)
	}
	return i, end
}

func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func scalarEnd(s string, i int) int {
	for i < len(s) && !strings.ContainsRune(" \t\r\n,;&)]}\"'<>", rune(s[i])) {
		i++
	}
	return i
}

func benignValue(v string) bool {
	v = strings.TrimSpace(v)
	if benignValues[strings.ToLower(v)] || strings.HasPrefix(v, markerPrefix) {
		return true
	}
	if strings.Trim(v, "*x•.") == "" {
		return true
	}
	// A reference to where the secret comes from, not the secret: ${DB_PASSWORD}.
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") || strings.HasPrefix(v, "$(") && strings.HasSuffix(v, ")")
}

type class int

const (
	keyOther class = iota
	keySecret
	keyIdentifier
)

func keyClass(key string) class {
	words := splitWords(key)
	if len(words) == 0 {
		return keyOther
	}
	head, first := words[len(words)-1], words[0]
	secret := false
	for i, w := range words {
		if secretWords[w] || i > 0 && secretPairs[words[i-1]+" "+w] || containsAny(w, secretStems) {
			secret = true
		}
	}
	describing := describingHeads[head] || len(words) > 1 && describingModifiers[first]
	switch {
	case secret && !describing:
		return keySecret
	case identifierHeads[head]:
		return keyIdentifier
	}
	for _, w := range words {
		if identifierWords[w] {
			return keyIdentifier
		}
	}
	return keyOther
}

// splitWords lowercases a key and splits it into words at separators and
// camelCase boundaries: "X-Api-Key", "apiKey" and "API_KEY" give [api key].
func splitWords(key string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
			continue
		case unicode.IsUpper(r) && i > 0 && len(cur) > 0:
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || unicode.IsUpper(prev) && nextLower {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	if len(words) == 1 && words[0] == "x" {
		return nil
	}
	if len(words) > 1 && words[0] == "x" {
		words = words[1:]
	}
	return words
}
