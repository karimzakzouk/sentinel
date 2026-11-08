package redact

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
)

type pattern struct {
	rule string
	re   *regexp.Regexp
	// group is the submatch to redact; 0 is the whole match.
	group int
	// check, when set, must accept the span for it to be redacted.
	check func(string) bool
	// literals are substrings every match contains; the regular expression
	// runs only on text that holds one of them. Lower case for patterns
	// that ignore case.
	literals []string
	// ignoreCase marks patterns whose literals are matched against lower case.
	ignoreCase bool
	// prefilter, when set, replaces literals.
	prefilter func(string) bool
}

func (p *pattern) mayMatch(s, lower string) bool {
	switch {
	case p.prefilter != nil:
		return p.prefilter(s)
	case len(p.literals) == 0:
		return true
	case p.ignoreCase:
		s = lower
	}
	for _, l := range p.literals {
		if strings.Contains(s, l) {
			return true
		}
	}
	return false
}

func lit(l ...string) []string { return l }

// The provider formats are written from each provider's published token
// format; most carry a fixed prefix, which keeps false positives rare.
var patterns = []pattern{
	// A key block that is cut off is redacted to the end of the text.
	{rule: "private-key", re: regexp.MustCompile(`(?s)-----BEGIN[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END[A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|$)`), literals: lit("PRIVATE KEY")},
	{rule: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`), literals: lit("eyJ")},
	{rule: "url-password", re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s/@:]*:([^\s/@]+)@`), group: 1, literals: lit("://")},
	{rule: "authorization", re: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{16,}=*)`), group: 1, literals: lit("bearer"), ignoreCase: true},
	{rule: "authorization", re: regexp.MustCompile(`(?i)\bbasic\s+([A-Za-z0-9+/]{8,}={0,2})`), group: 1, check: basicCredentials, literals: lit("basic"), ignoreCase: true},
	{rule: "card-number", re: regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`), check: cardNumber, prefilter: atLeastDigits(13)},
	{rule: "email", re: regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`), literals: lit("@")},

	{rule: "aws-access-key", re: regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16}\b`), literals: lit("A3T", "AKIA", "ASIA", "ABIA", "ACCA")},
	{rule: "github-token", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`), literals: lit("ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_")},
	{rule: "gitlab-token", re: regexp.MustCompile(`\bgl(?:pat|ptt|dt|rt|cbt|oas|soat|ft|imt|agent)-[A-Za-z0-9_-]{20,}`), literals: lit("glpat-", "glptt-", "gldt-", "glrt-", "glcbt-", "gloas-", "glsoat-", "glft-", "glimt-", "glagent-")},
	{rule: "slack-token", re: regexp.MustCompile(`\bxox[abposre]-[A-Za-z0-9-]{10,}`), literals: lit("xox")},
	{rule: "slack-webhook", re: regexp.MustCompile(`https://hooks\.slack\.com/(?:services|workflows|triggers)/[A-Za-z0-9+/_-]+`), literals: lit("hooks.slack.com")},
	{rule: "discord-webhook", re: regexp.MustCompile(`https://(?:[a-z]+\.)?discord(?:app)?\.com/api/webhooks/\d+/[A-Za-z0-9_-]+`), literals: lit("/api/webhooks/")},
	{rule: "stripe-key", re: regexp.MustCompile(`\b(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]{10,}\b|\bwhsec_[A-Za-z0-9+/=]{20,}`), literals: lit("_live_", "_test_", "whsec_")},
	{rule: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), literals: lit("AIza")},
	{rule: "google-oauth-secret", re: regexp.MustCompile(`\bGOCSPX-[A-Za-z0-9_-]{28}\b`), literals: lit("GOCSPX-")},
	{rule: "llm-api-key", re: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-|ant-)?[A-Za-z0-9_-]{20,}`), literals: lit("sk-")},
	{rule: "huggingface-token", re: regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}\b`), literals: lit("hf_")},
	{rule: "npm-token", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`), literals: lit("npm_")},
	{rule: "pypi-token", re: regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_-]{50,}`), literals: lit("pypi-")},
	{rule: "sendgrid-key", re: regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`), literals: lit("SG.")},
	{rule: "twilio-key", re: regexp.MustCompile(`\bSK[0-9a-fA-F]{32}\b`), literals: lit("SK")},
	{rule: "mailgun-key", re: regexp.MustCompile(`\bkey-[0-9a-f]{32}\b`), literals: lit("key-")},
	{rule: "docker-token", re: regexp.MustCompile(`\bdckr_pat_[A-Za-z0-9_-]{27,}`), literals: lit("dckr_pat_")},
	{rule: "vault-token", re: regexp.MustCompile(`\bhv[sbr]\.[A-Za-z0-9_-]{24,}`), literals: lit("hvs.", "hvb.", "hvr.")},
	{rule: "digitalocean-token", re: regexp.MustCompile(`\bdo[por]_v1_[a-f0-9]{64}\b`), literals: lit("_v1_")},
	{rule: "shopify-token", re: regexp.MustCompile(`\bshp(?:at|ca|pa|ss)_[a-fA-F0-9]{32}\b`), literals: lit("shpat_", "shpca_", "shppa_", "shpss_")},
	{rule: "square-token", re: regexp.MustCompile(`\bsq0(?:atp|csp)-[0-9A-Za-z_-]{22,43}\b`), literals: lit("sq0")},
	{rule: "telegram-token", re: regexp.MustCompile(`\b\d{8,10}:AA[A-Za-z0-9_-]{33}\b`), literals: lit(":AA")},
	{rule: "grafana-token", re: regexp.MustCompile(`\bglsa_[A-Za-z0-9]{32}_[a-fA-F0-9]{8}\b|\bglc_[A-Za-z0-9+/]{32,}={0,2}`), literals: lit("glsa_", "glc_")},
	{rule: "newrelic-key", re: regexp.MustCompile(`\bNRAK-[A-Z0-9]{27}\b`), literals: lit("NRAK-")},
	{rule: "linear-key", re: regexp.MustCompile(`\blin_api_[A-Za-z0-9]{40}\b`), literals: lit("lin_api_")},
	{rule: "atlassian-token", re: regexp.MustCompile(`\bATATT3[A-Za-z0-9_=-]{180,}`), literals: lit("ATATT3")},
	{rule: "age-key", re: regexp.MustCompile(`\bAGE-SECRET-KEY-1[QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L]{58}\b`), literals: lit("AGE-SECRET-KEY-1")},
}

func atLeastDigits(n int) func(string) bool {
	return func(s string) bool {
		digits := 0
		for i := 0; i < len(s) && digits < n; i++ {
			if s[i] >= '0' && s[i] <= '9' {
				digits++
			}
		}
		return digits >= n
	}
}

// basicCredentials accepts base64 that decodes to "user:password".
func basicCredentials(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	return err == nil && bytes.IndexByte(b, ':') > 0
}

// cardNumber accepts 13 to 19 digits with a card network's issuer prefix and
// a valid Luhn checksum, so most long numeric IDs and timestamps pass.
func cardNumber(s string) bool {
	d := make([]int, 0, 19)
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= '0' && c <= '9' {
			d = append(d, int(c-'0'))
		}
	}
	if len(d) < 13 || len(d) > 19 || !issuerPrefix(d) {
		return false
	}
	sum := 0
	for i := range d {
		v := d[len(d)-1-i]
		if i%2 == 1 {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
	}
	return sum%10 == 0
}

func issuerPrefix(d []int) bool {
	two := d[0]*10 + d[1]
	four := two*100 + d[2]*10 + d[3]
	switch {
	case d[0] == 4: // Visa
	case two >= 51 && two <= 55, four >= 2221 && four <= 2720: // Mastercard
	case two == 34, two == 37: // American Express
	case four == 6011, two == 65, four/10 >= 644 && four/10 <= 649: // Discover
	case two == 35, two == 36, two == 38, two == 30, two == 62: // JCB, Diners Club, UnionPay
	default:
		return false
	}
	return true
}
