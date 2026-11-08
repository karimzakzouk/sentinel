package redact

// Hand-written scanners for the three patterns that run on every line. Each
// finds exactly what the regular expression in its comment finds, leftmost
// first and without overlaps (the tests compare them), several times faster.

func keyStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func keyChar(c byte) bool {
	return keyStart(c) || c >= '0' && c <= '9' || c == '.' || c == '-'
}

func quote(c byte) bool { return c == '"' || c == '\'' }

func blank(c byte) bool { return c == ' ' || c == '\t' }

type keyMatch struct {
	start, end int // the whole match
	key        string
	delim      string
}

// scanKeys matches ["']?([A-Za-z_][A-Za-z0-9_.-]{0,63})["']?[ \t]*(=>|:=|=|:)[ \t]*
// The value starts at the end of the match.
func scanKeys(s string) []keyMatch {
	var out []keyMatch
	for i := 0; i < len(s); {
		q := i
		if quote(s[q]) {
			q++
		}
		if q >= len(s) || !keyStart(s[q]) {
			i++
			continue
		}
		r := q + 1
		for r < len(s) && keyChar(s[r]) {
			r++
		}
		if r-q > 64 {
			// Only a key starting within the run's last 64 characters fits.
			i = max(i+1, r-64)
			continue
		}
		k := r
		if k < len(s) && quote(s[k]) {
			k++
		}
		for k < len(s) && blank(s[k]) {
			k++
		}
		delim := ""
		switch {
		case k+1 < len(s) && s[k] == '=' && s[k+1] == '>':
			delim = "=>"
		case k+1 < len(s) && s[k] == ':' && s[k+1] == '=':
			delim = ":="
		case k < len(s) && (s[k] == '=' || s[k] == ':'):
			delim = s[k : k+1]
		}
		if delim == "" {
			// Every start inside this run ends at the same place and fails too.
			i = r
			continue
		}
		v := k + len(delim)
		for v < len(s) && blank(s[v]) {
			v++
		}
		out = append(out, keyMatch{start: i, end: v, key: s[q:r], delim: delim})
		i = v
	}
	return out
}

func flagChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// scanFlags matches --?([A-Za-z][A-Za-z0-9_-]{1,63})(?:=|[ \t]+)
// The value starts at the end of the match.
func scanFlags(s string) []keyMatch {
	var out []keyMatch
	for i := 0; i < len(s); {
		if s[i] != '-' {
			i++
			continue
		}
		n := i + 1
		if n < len(s) && s[n] == '-' && n+1 < len(s) && isLetter(s[n+1]) {
			n++
		}
		if n >= len(s) || !isLetter(s[n]) {
			i++
			continue
		}
		r := n + 1
		for r < len(s) && flagChar(s[r]) {
			r++
		}
		if r-n < 2 || r-n > 64 || r >= len(s) || s[r] != '=' && !blank(s[r]) {
			i++
			continue
		}
		v := r + 1
		if s[r] != '=' {
			for v < len(s) && blank(s[v]) {
				v++
			}
		}
		out = append(out, keyMatch{start: i, end: v, key: s[n:r]})
		i = v
	}
	return out
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func tokenChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '+' || c == '/' || c == '_' || c == '.' || c == '-'
}

// scanTokens matches [A-Za-z0-9+/_.-]{16,}={0,2}
func scanTokens(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); {
		if !tokenChar(s[i]) {
			i++
			continue
		}
		r := i + 1
		for r < len(s) && tokenChar(s[r]) {
			r++
		}
		if r-i >= 16 {
			end := r
			for end < len(s) && end-r < 2 && s[end] == '=' {
				end++
			}
			out = append(out, [2]int{i, end})
			i = end
			continue
		}
		i = r
	}
	return out
}
