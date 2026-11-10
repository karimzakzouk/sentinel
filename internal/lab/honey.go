package lab

import (
	"fmt"
	"strings"
)

// Honeytoken is a shape-preserving stand-in for a Secret value the Lab never
// reads. It always embeds SecretPlaceholder so a copy that looks the token
// up as a host, or sends it out, names which secret leaked into the Lab DNS.
func Honeytoken(secret, key string) string {
	token := SecretPlaceholder(secret, key)
	switch shapeOf(key) {
	case shapePEM:
		return fmt.Sprintf("-----BEGIN PRIVATE KEY-----\n%s\n-----END PRIVATE KEY-----\n", token)
	case shapeJWT:
		return "eyJhbGciOiJub25lIn0." + token + ".sentinel"
	case shapeHex:
		// Pad the hex-looking token so clients that check length still accept it.
		return token + strings.Repeat("0", 8)
	default:
		return "honey-" + token
	}
}

// HoneyMarker is the unique substring of a honeytoken that identifies which
// Secret key it stands for. Empty when the value holds no Lab honeytoken.
func HoneyMarker(value string) string {
	i := strings.Index(value, placeholderPrefix)
	if i < 0 {
		return ""
	}
	rest := value[i:]
	// placeholderPrefix + 10 hex chars (5 bytes).
	n := len(placeholderPrefix) + 10
	if len(rest) < n {
		return ""
	}
	return rest[:n]
}

type secretShape int

const (
	shapePlain secretShape = iota
	shapePEM
	shapeJWT
	shapeHex
)

func shapeOf(key string) secretShape {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "cert"), strings.Contains(k, "pem"), strings.Contains(k, "key"),
		strings.HasSuffix(k, ".crt"), strings.HasSuffix(k, ".key"), strings.Contains(k, "tls"):
		return shapePEM
	case strings.Contains(k, "token"), strings.Contains(k, "jwt"), strings.Contains(k, "bearer"):
		return shapeJWT
	case strings.Contains(k, "hex"), strings.Contains(k, "sha"), strings.Contains(k, "hash"):
		return shapeHex
	default:
		return shapePlain
	}
}
