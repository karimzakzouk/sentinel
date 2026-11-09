package logs

import (
	"strings"
)

// Placeholders that masking puts in place of values.
const (
	MaskTime     = "<time>"
	MaskUUID     = "<uuid>"
	MaskIP       = "<ip>"
	MaskNumber   = "<num>"
	MaskDuration = "<duration>"
	MaskSize     = "<size>"
	MaskHex      = "<hex>"
)

// Mask replaces the values in a message with typed placeholders, so lines
// that differ only in values share a template. It works on whitespace
// tokens; punctuation around a token, and key= or key: before a value, are
// kept.
func Mask(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		fields[i] = maskToken(f)
	}
	return strings.Join(fields, " ")
}

func maskToken(tok string) string {
	start, end := 0, len(tok)
	for start < end && strings.IndexByte(`"'({<,;`, tok[start]) >= 0 {
		start++
	}
	if m, ok := maskBracketedIP(tok[start:]); ok {
		return tok[:start] + m
	}
	for start < end && tok[start] == '[' {
		start++
	}
	for end > start && strings.IndexByte(`"')]}>,;.:`, tok[end-1]) >= 0 {
		end--
	}
	core := tok[start:end]
	if core == "" {
		return tok
	}
	if i := strings.Index(core, "://"); i > 0 && isScheme(core[:i]) {
		return tok[:start] + core[:i+3] + maskURL(core[i+3:]) + tok[end:]
	}
	// key=value and key:value keep their key.
	if i := strings.IndexAny(core, "=:"); i > 0 && i < len(core)-1 && !looksLikeTime(core) && !looksLikeIP(core) {
		return tok[:start] + core[:i+1] + maskToken(core[i+1:]) + tok[end:]
	}
	if m := maskCore(core); m != "" {
		return tok[:start] + m + tok[end:]
	}
	// Paths keep their words and lose their values: /api/v1/products/<num>.
	if strings.Count(core, "/") >= 2 {
		parts := strings.Split(core, "/")
		for i, p := range parts {
			if m := maskCore(p); m != "" {
				parts[i] = m
			}
		}
		return tok[:start] + strings.Join(parts, "/") + tok[end:]
	}
	return tok
}

// maskCore returns the placeholder for a value, or "" for a word.
func maskCore(s string) string {
	switch {
	case looksLikeTime(s):
		return MaskTime
	case isUUID(s):
		return MaskUUID
	case looksLikeIP(s):
		return MaskIP
	case isNumber(s):
		return MaskNumber
	case isQuantity(s, durationUnits):
		return MaskDuration
	case isQuantity(s, sizeUnits):
		return MaskSize
	case isHexValue(s):
		return MaskHex
	}
	return ""
}

func isNumber(s string) bool {
	s = strings.TrimLeft(s, "+-")
	dot, seen := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			seen = true
		case c == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return seen
}

var (
	durationUnits = []string{"ns", "us", "µs", "ms", "s", "m", "h"}
	sizeUnits     = []string{"B", "KB", "MB", "GB", "TB", "Ki", "Mi", "Gi", "Ti", "KiB", "MiB", "GiB", "TiB", "kB", "k", "K", "M", "G"}
)

// isQuantity recognizes numbers with units, compound ones too: 1.5s, 3m20s, 512Mi.
func isQuantity(s string, units []string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for s != "" {
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 {
			return false
		}
		s = s[i:]
		matched := ""
		for _, u := range units {
			if strings.HasPrefix(s, u) && len(u) > len(matched) {
				matched = u
			}
		}
		if matched == "" {
			return false
		}
		s = s[len(matched):]
	}
	return true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isHexValue recognizes 0x-prefixed values and hex strings of six or more
// characters with a digit, so words such as "facade" stay.
func isHexValue(s string) bool {
	if strings.HasPrefix(s, "0x") && len(s) > 2 {
		s = s[2:]
	} else if len(s) < 6 || !strings.ContainsAny(s, "0123456789") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isHex(s[i]) {
				return false
			}
		}
	}
	return true
}

func isScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		later := i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')
		if !letter && !later {
			return false
		}
	}
	return s != ""
}

// maskURL masks what follows a URL's scheme: an address host and the port,
// values in the path, and query values. Host names are words and stay.
func maskURL(s string) string {
	authority, rest := s, ""
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		authority, rest = s[:i], s[i:]
	}
	if m, ok := maskBracketedIP(authority); ok {
		authority = m
	} else if looksLikeIP(authority) {
		authority = MaskIP
	} else if host, port, ok := strings.Cut(authority, ":"); ok && isNumber(port) {
		authority = host + ":" + MaskNumber
	}
	path, query, hasQuery := strings.Cut(rest, "?")
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if m := maskCore(p); m != "" {
			parts[i] = m
		}
	}
	out := authority + strings.Join(parts, "/")
	if hasQuery {
		pairs := strings.Split(query, "&")
		for i, kv := range pairs {
			if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
				pairs[i] = k + "=<*>"
			}
		}
		out += "?" + strings.Join(pairs, "&")
	}
	return out
}

// maskBracketedIP masks an IPv6 address in brackets, with its port if any,
// at the start of s: [fd00::1] and [fd00::1]:6379.
func maskBracketedIP(s string) (string, bool) {
	j := strings.IndexByte(s, ']')
	if !strings.HasPrefix(s, "[") || j < 3 || !strings.Contains(s[1:j], ":") || !looksLikeIP(s[1:j]) {
		return "", false
	}
	rest := s[j+1:]
	if strings.HasPrefix(rest, ":") {
		k := 1
		for k < len(rest) && rest[k] >= '0' && rest[k] <= '9' {
			k++
		}
		if k > 1 {
			rest = rest[k:]
		}
	}
	return MaskIP + rest, true
}

// looksLikeIP recognizes IPv4 and IPv6 addresses, with a port or not.
func looksLikeIP(s string) bool {
	if dots := strings.Count(s, "."); dots == 3 {
		host, _, _ := strings.Cut(s, ":")
		for _, p := range strings.Split(host, ".") {
			if len(p) == 0 || len(p) > 3 || !isNumber(p) || strings.Contains(p, ".") {
				return false
			}
		}
		return true
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if strings.Count(s, ":") < 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) && s[i] != ':' && s[i] != '.' && s[i] != ']' && s[i] != '[' {
			return false
		}
	}
	return true
}

// looksLikeTime recognizes RFC 3339 and similar timestamps, and clock times.
func looksLikeTime(s string) bool {
	if len(s) >= 10 && digits(s[:4]) && s[4] == '-' && digits(s[5:7]) && s[7] == '-' && digits(s[8:10]) {
		return true
	}
	// 21:14:11 and 21:14:11.123
	return len(s) >= 8 && digits(s[:2]) && s[2] == ':' && digits(s[3:5]) && s[5] == ':' && digits(s[6:8])
}
