package redact

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var rng = rand.New(rand.NewPCG(7, 11))

const (
	hexLower  = "0123456789abcdef"
	base36    = "abcdefghijklmnopqrstuvwxyz0123456789"
	base62    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	base64URL = base62 + "-_"
	base64Std = base62 + "+/"
)

func random(alphabet string, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.IntN(len(alphabet))]
	}
	return string(b)
}

type canary struct {
	name, line, secret string
}

func canaries() []canary {
	pw := func() string { return "Cn" + random(base62, 14) }
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"canary-`+random(base36, 8)+`","iat":1731100451}`)) + "." +
		random(base64URL, 43)
	pemBody := random(base64Std, 64) + "\n" + random(base64Std, 64)
	basic := base64.StdEncoding.EncodeToString([]byte("app:" + pw()))
	var cs []canary
	add := func(name, format, secret string) {
		cs = append(cs, canary{name: name, line: fmt.Sprintf(format, secret), secret: secret})
	}
	add("aws access key", "2026/11/08 21:14:11 using credentials %s for s3", "AKIA"+random("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", 16))
	add("aws secret key", "aws_secret_access_key = %s", random(base64Std, 40))
	add("github token in a clone URL", "git clone https://%s@github.com/org/repo.git", "ghp_"+random(base62, 36))
	add("github fine-grained token", `token: %s`, "github_pat_"+random(base62, 22)+"_"+random(base62, 59))
	add("gitlab token", "PRIVATE-TOKEN %s rejected", "glpat-"+random(base64URL, 20))
	add("slack bot token", "slack: posting with %s", "xoxb-"+random("0123456789", 12)+"-"+random("0123456789", 13)+"-"+random(base62, 24))
	add("slack webhook", "notify url=%s", "https://hooks.slack.com/services/T"+random(base36, 8)+"/B"+random(base36, 8)+"/"+random(base62, 24))
	add("stripe key", "stripe.api_key set to %s", "sk_live_"+random(base62, 24))
	add("google api key", "maps request key=%s failed", "AIza"+random(base64URL, 35))
	add("openai key", "OpenAI error 401 for %s", "sk-proj-"+random(base64URL, 48))
	add("anthropic key", "x-api-key header %s invalid", "sk-ant-api03-"+random(base64URL, 80))
	add("jwt", `{"level":"debug","msg":"verifying","jwt":"%s"}`, jwt)
	add("private key", "loaded key:\n-----BEGIN RSA PRIVATE KEY-----\n%s\n-----END RSA PRIVATE KEY-----\ndone", pemBody)
	add("json-escaped private key", `{"private_key":"-----BEGIN PRIVATE KEY-----\n%s\n-----END PRIVATE KEY-----\n"}`, strings.ReplaceAll(pemBody, "\n", `\n`))
	add("truncated private key", "-----BEGIN OPENSSH PRIVATE KEY-----\n%s", pemBody)
	add("url password", "connecting to postgres://app:%s@db:5432/shop", pw())
	add("url password without user", "redis: dial redis://:%s@redis-cart:6379", pw())
	add("bearer token", "Authorization: Bearer %s", random(base62, 32))
	add("bearer token in prose", "retrying request with bearer %s after 401", random(base64URL, 40))
	add("basic credentials", "proxy-authorization: Basic %s", basic)
	add("logfmt password", "level=error msg=\"login failed\" user=bob password=%s", pw())
	add("json password", `{"user":"bob","password":"%s","attempt":2}`, pw())
	add("yaml password", "  PASSWORD: %s", pw())
	add("python dict", `{'api_key': '%s', 'region': 'eu-west-1'}`, pw())
	add("cli flag with a space", "exec /app --db-password %s --port 8080", pw())
	add("cli flag after another flag", "exec /app --verbose --token %s", pw())
	add("cli flag with equals", "exec /app --token=%s", pw())
	add("query string", "GET /reset?token=%s&page=2 HTTP/1.1", pw())
	add("cookie", "Cookie: session=%s; theme=dark", pw())
	add("set-cookie", "Set-Cookie: sid=%s; Path=/; HttpOnly", pw())
	add("export", "export DB_PASSWORD=%s", pw())
	add("concatenated env name", "PGPASSWORD=%s psql -h db", pw())
	add("go struct", "config: {User:bob Password:%s Host:db}", pw())
	add("ruby hash", `{:user => "bob", :password => "%s"}`, pw())
	add("header", "X-Api-Key: %s", pw())
	add("mysql dsn", `dsn="app:%s@tcp(mysql:3306)/shop"`, pw())
	add("sql server connection string", "Server=db;Database=shop;User Id=sa;Password=%s;", pw())
	add("azure account key", "DefaultEndpointsProtocol=https;AccountName=shop;AccountKey=%s;EndpointSuffix=core.windows.net", random(base64Std, 86)+"==")
	add("azure sas signature", "GET https://shop.blob.core.windows.net/c/x?sv=2022-11-02&sig=%s&se=2026", random(base64Std, 43)+"%3D")
	add("card number with spaces", "charging card %s for 12.99", "4111 1111 1111 1111")
	add("card number", "payment declined card=%s", "5555555555554444")
	add("email address", "password reset sent to %s", "jane.doe@example.com")
	add("unkeyed base62 token", "retrying with %s after timeout", random(base62, 32))
	add("unkeyed hex token", "handshake %s accepted", random(hexLower, 40))
	add("unkeyed standard base64", "payload %s", random(base64Std, 24))
	add("unkeyed lowercase token", "using %s", random(base36, 28))
	return cs
}

func TestCanariesNeverSurvive(t *testing.T) {
	r := New()
	for _, c := range canaries() {
		out, found := r.String(c.line)
		if strings.Contains(out, c.secret) || len(found) == 0 {
			t.Errorf("%s: secret survived:\n  in:  %s\n  out: %s", c.name, c.line, out)
		}
		if !strings.Contains(out, markerPrefix) {
			t.Errorf("%s: no marker in %q", c.name, out)
		}
	}
}

var benign = []string{
	`2026-11-08T21:14:11.123456789Z INFO GET /api/v1/products/OLJCESPC7Z 200 12ms`,
	`level=info msg="request served" request_id=3f2a1b9c-6d4e-4f8a-9b7c-2d1e0f3a4b5c duration=1.234567s`,
	`{"severity":"info","message":"conversion request successful","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","span_id":"00f067aa0ba902b7"}`,
	`Successfully pulled image "registry.k8s.io/e2e-test-images/agnhost:2.61" in 1.2s`,
	`Pulling image "us-central1-docker.pkg.dev/google-samples/microservices-demo/cartservice:v0.10.7"`,
	`Back-off restarting failed container server in pod currencyservice-7d9f8b6c5-x2x4k_boutique(5b0a1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d)`,
	`image: docker.io/library/redis@sha256:` + strings.Repeat("4a2b3c9d", 8),
	`containerID: containerd://` + strings.Repeat("9f8e7d6c", 8),
	`Readiness probe failed: HTTP probe failed with statuscode: 503`,
	`dial tcp 10.96.12.34:6379: connect: connection refused`,
	`max_tokens=4096 total_tokens=812 token_type=Bearer expires_in=3600`,
	`secretName: db-credentials`,
	`cache miss key=user:42:profile`,
	`panic: runtime error: invalid memory address or nil pointer dereference`,
	`goroutine 1 [running]:`,
	`github.com/karimzakzouk/sentinel/internal/watcher.(*Watcher).Run(0xc000123456, {0x1f2e3d0, 0xc0001a2b3c})`,
	`	/app/main.go:42 +0x1d`,
	`Traceback (most recent call last):`,
	`  File "/usr/local/lib/python3.11/site-packages/requests/adapters.py", line 486, in send`,
	`requests.exceptions.ConnectionError: HTTPConnectionPool(host='productcatalogservice', port=3550): Max retries exceeded`,
	`Exception in thread "main" java.lang.IllegalStateException: Failed to load ApplicationContext`,
	`	at org.springframework.test.context.cache.DefaultCacheAwareContextLoaderDelegate.loadContext(DefaultCacheAwareContextLoaderDelegate.java:132)`,
	`Caused by: java.sql.SQLException: Access denied for user 'root'@'10.0.0.1' (using password: YES)`,
	`Error: Cannot find module '@grpc/grpc-js'`,
	`    at Module._resolveFilename (node:internal/modules/cjs/loader:1145:15)`,
	`node: --no-such-node-option is not allowed in NODE_OPTIONS`,
	`GET /cart?currency=EUR&page=2 HTTP/1.1`,
	`version=v2.0.0-alpha.0-9-g51b09ba commit=51b09ba7c0ffee12345678901234567890abcdef`,
	`user_id=8f14e45fceea167a5a36dedd4bea2543`,
	`the session ended after 30s`,
	`auth: ok`,
	`password reset link sent`,
	`connecting to postgres://app@db:5432/shop`,
	`ts=1731100451123456789 level=warn msg="slow query"`,
	`Successfully assigned boutique/frontend-6b7f8d9c5-abcde to sentinel-e2e-worker`,
	`Created pod: productcatalogservice-5f9b8c7d6-x2x4k`,
	`io.grpc.StatusRuntimeException: UNAVAILABLE: io exception`,
	`AbstractSingletonProxyFactoryBean initialized in 120ms`,
	`go: downloading golang.org/x/net v0.0.0-20231108123456-abcdef123456`,
	`using basic authentication for the upstream`,
	`password: ${DB_PASSWORD}`,
	`apiVersion: sentinel.karimzakzouk.github.io/v1alpha1`,
}

func TestBenignLinesAreUntouched(t *testing.T) {
	r := New()
	for _, line := range benign {
		if out, found := r.String(line); out != line {
			t.Errorf("benign line changed by %v:\n  in:  %s\n  out: %s", found, line, out)
		}
	}
}

func TestRedactionIsIdempotent(t *testing.T) {
	r := New()
	lines := append([]string{}, benign...)
	for _, c := range canaries() {
		lines = append(lines, c.line)
	}
	for _, line := range lines {
		once, _ := r.String(line)
		if twice, found := r.String(once); twice != once {
			t.Errorf("second pass changed %q to %q (%v)", once, twice, found)
		}
	}
}

// The randomness check is a backstop behind provider formats and key names,
// so it is measured rather than required to be perfect: short random strings
// come out word-like by chance, and 16-character base64 often splits at a
// separator into pieces too short to judge.
func TestRandomStringsAreCaughtAndIdentifiersAreNot(t *testing.T) {
	for _, alphabet := range []string{hexLower, base36, base62, base64URL, base64Std} {
		for _, n := range []int{16, 20, 24, 32, 40, 64} {
			missed := 0
			for range 1000 {
				if !randomToken(random(alphabet, n)) {
					missed++
				}
			}
			limit := 10
			switch {
			case n == 16 && len(alphabet) == 64:
				limit = 500
			case n == 16:
				limit = 60
			case n <= 24:
				limit = 30
			}
			if missed > limit {
				t.Errorf("alphabet of %d, length %d: missed %d of 1000, limit %d", len(alphabet), n, missed, limit)
			}
		}
	}
	for _, id := range []string{
		"DefaultCacheAwareContextLoaderDelegate", "AbstractSingletonProxyFactoryBean", "recommendationservice",
		"checkoutservice-7d9f8b6c5-x2x4k", "HTTPConnectionPool", "kubernetes.io/service-account-token",
		"ThisIsALongCamelCaseIdentifier2", "application/vnd.kubernetes.protobuf", "spring-boot-starter-web-3.2.1",
		"sentinel.karimzakzouk.github.io", "1731100451123456789", "src/main/java/com/example/App",
		"grpcHealthCheckService", "XMLHttpRequestHandler", "OAuth2AuthorizationCodeGrant", "v0.0.0-20231108123456-abcdef123456",
		"IllegalStateException", "NullPointerException", "ConcurrentModificationException",
	} {
		if randomToken(id) {
			t.Errorf("identifier %q looks random", id)
		}
	}
}

func TestExpectedEntropyMatchesSimulation(t *testing.T) {
	for _, tc := range []struct {
		alphabet string
		n        int
	}{{hexLower, 16}, {hexLower, 40}, {base62, 24}, {base62, 64}} {
		var sum float64
		const trials = 4000
		for range trials {
			sum += entropy(random(tc.alphabet, tc.n))
		}
		sim, exact := sum/trials, expectedEntropy(tc.n, len(tc.alphabet))
		if d := sim - exact; d > 0.02 || d < -0.02 {
			t.Errorf("k=%d n=%d: simulated %.3f, formula %.3f", len(tc.alphabet), tc.n, sim, exact)
		}
	}
}

func TestKeyClasses(t *testing.T) {
	for key, want := range map[string]class{
		"password": keySecret, "db_password": keySecret, "X-Api-Key": keySecret, "apiKey": keySecret,
		"APIKey": keySecret, "API_KEY": keySecret, "aws_secret_access_key": keySecret, "AccountKey": keySecret,
		"GITHUB_TOKEN": keySecret, "accessToken": keySecret, "session_id": keySecret, "PGPASSWORD": keySecret,
		"Set-Cookie": keySecret, "Authorization": keySecret, "password_hash": keySecret,
		"token_type": keyOther, "max_tokens": keyOther, "secretName": keyOther, "auth_method": keyOther,
		"password_policy": keyOther, "session_timeout": keyOther, "tokenizer": keyOther, "key": keyOther,
		"monkey": keyOther, "author": keyOther,
		"trace_id": keyIdentifier, "X-Request-Id": keyIdentifier, "user_id": keyIdentifier, "commit": keyIdentifier,
	} {
		if got := keyClass(key); got != want {
			t.Errorf("keyClass(%q) = %d, want %d (words %v)", key, got, want, splitWords(key))
		}
	}
}

func prefiltersHide(s string) []string {
	var hidden []string
	lower := strings.ToLower(s)
	for i := range patterns {
		p := &patterns[i]
		if p.re.MatchString(s) && !p.mayMatch(s, lower) {
			hidden = append(hidden, p.rule)
		}
	}
	return hidden
}

func TestPrefiltersNeverHideAMatch(t *testing.T) {
	lines := append([]string{}, benign...)
	for _, c := range canaries() {
		lines = append(lines, c.line)
	}
	for _, line := range lines {
		if hidden := prefiltersHide(line); len(hidden) > 0 {
			t.Errorf("prefilters of %v hide a match in %q", hidden, line)
		}
	}
}

func FuzzPrefiltersNeverHideAMatch(f *testing.F) {
	for _, c := range canaries() {
		f.Add(c.line)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if hidden := prefiltersHide(s); len(hidden) > 0 {
			t.Fatalf("prefilters of %v hide a match in %q", hidden, s)
		}
	})
}

func BenchmarkRedactTypicalLine(b *testing.B) {
	r := New()
	line := `{"level":"error","ts":"2026-11-08T21:14:11.123Z","msg":"payment failed","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736",` +
		`"user":"bob","error":"rpc error: code = Unavailable desc = connection refused to paymentservice:50051"}`
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		r.String(line)
	}
}

// A long line full of secrets must stay linear.
func BenchmarkRedactManySecrets(b *testing.B) {
	r := New()
	line := strings.Repeat("password=hunter2 token=Zx8kQ2mN7pL4rT9vW1yB ", 2000)
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		r.String(line)
	}
}

func FuzzRedactionIsIdempotent(f *testing.F) {
	for _, line := range benign {
		f.Add(line)
	}
	for _, c := range canaries() {
		f.Add(c.line)
	}
	r := New()
	f.Fuzz(func(t *testing.T, s string) {
		once, _ := r.String(s)
		if twice, _ := r.String(once); twice != once {
			t.Fatalf("second pass changed %q to %q", once, twice)
		}
	})
}
