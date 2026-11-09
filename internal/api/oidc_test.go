package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// idp is an identity provider that gives one code, for which it issues an
// ID token carrying the nonce its sign-in asked for.
type idp struct {
	*httptest.Server
	mu        sync.Mutex
	challenge string
	nonce     string
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token"})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		p.mu.Lock()
		challenge, nonce := p.challenge, p.nonce
		p.mu.Unlock()
		if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.Form.Get("client_id") != "sentinel" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		claims, _ := json.Marshal(map[string]any{"iss": p.URL, "aud": "sentinel", "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce})
		token := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": token, "token_type": "Bearer"})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// A person signs in through the cluster's identity provider; the ID token
// it issues is the token the console acts with. A callback that did not
// start in this browser, came twice, or carries a token for another
// sign-in starts no session.
func TestTheConsoleSignsInThroughTheIdentityProvider(t *testing.T) {
	provider := newIDP(t)
	var used []string
	c := &Console{Namespaces: []string{"shop"}, Clients: func(token string) (kubernetes.Interface, dynamic.Interface, error) {
		used = append(used, token)
		k := fake.NewClientset()
		k.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			user := ""
			if strings.HasPrefix(token, "eyJhbGciOiJSUzI1NiJ9.") {
				user = "alice@example.com"
			}
			return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{
				UserInfo: authenticationv1.UserInfo{Username: user}}}, nil
		})
		return k, nil, nil
	}}
	c.OIDC = &OIDC{Issuer: provider.URL, ClientID: "sentinel", Scopes: []string{"email"}, HTTP: provider.Client()}
	srv := httptest.NewServer((&Server{Console: c}).Handler())
	defer srv.Close()
	c.OIDC.RedirectURL = srv.URL + oidcPath + "/callback"
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var sso struct{ Provider, Start string }
	_ = json.Unmarshal(call(t, srv, http.MethodGet, "/api/session/sso", "", nil, nil).body, &sso)
	if sso.Start != oidcPath || !strings.HasPrefix(provider.URL, "http://"+sso.Provider) {
		t.Fatalf("sso %+v", sso)
	}
	start := func() (url.Values, *http.Cookie) {
		t.Helper()
		res, err := noFollow.Get(srv.URL + oidcPath)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		to, _ := url.Parse(res.Header.Get("Location"))
		q := to.Query()
		if res.StatusCode != http.StatusFound || !strings.HasPrefix(to.String(), provider.URL+"/authorize") || q.Get("scope") != "openid email" ||
			q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != c.OIDC.RedirectURL {
			t.Fatalf("start: %d %s", res.StatusCode, to)
		}
		provider.mu.Lock()
		provider.challenge, provider.nonce = q.Get("code_challenge"), q.Get("nonce")
		provider.mu.Unlock()
		for _, ck := range res.Cookies() {
			if ck.Name == oidcCookie && ck.Value == q.Get("state") && ck.SameSite == http.SameSiteLaxMode && ck.HttpOnly {
				return q, ck
			}
		}
		t.Fatal("no state cookie")
		return nil, nil
	}
	back := func(query string, ck *http.Cookie) reply {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+oidcPath+"/callback?"+query, nil)
		if ck != nil {
			req.AddCookie(ck)
		}
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return reply{status: res.StatusCode, cookies: res.Cookies()}
	}

	q, ck := start()
	if res := back("code=the-code&state="+q.Get("state"), nil); res.status != http.StatusBadRequest {
		t.Fatalf("without the browser's state: %d", res.status)
	}
	res := back("code=the-code&state="+q.Get("state"), ck)
	var session *http.Cookie
	for _, k := range res.cookies {
		if k.Name == sessionCookie {
			session = k
		}
	}
	if res.status != http.StatusSeeOther || session == nil {
		t.Fatalf("callback: %d", res.status)
	}
	var who struct{ User string }
	_ = json.Unmarshal(call(t, srv, http.MethodGet, "/api/session", "", session, nil).body, &who)
	if who.User != "alice@example.com" || len(used) != 1 || !strings.HasPrefix(used[0], "eyJ") {
		t.Fatalf("signed in as %q with %d tokens", who.User, len(used))
	}
	if res := back("code=the-code&state="+q.Get("state"), ck); res.status != http.StatusBadRequest {
		t.Fatalf("the same sign-in twice: %d", res.status)
	}

	q, ck = start()
	provider.mu.Lock()
	provider.nonce = "someone-elses"
	provider.mu.Unlock()
	if res := back("code=the-code&state="+q.Get("state"), ck); res.status != http.StatusUnauthorized {
		t.Fatalf("a token for another sign-in: %d", res.status)
	}
	q, ck = start()
	if res := back("error=access_denied&state="+q.Get("state"), ck); res.status != http.StatusUnauthorized {
		t.Fatalf("a refusal: %d", res.status)
	}
	if len(used) != 1 {
		t.Fatalf("tokens used %d", len(used))
	}
}

// Without a provider, the console says so, and offers only tokens.
func TestNoSingleSignOnWithoutAProvider(t *testing.T) {
	srv, _, _ := console(t)
	if res := call(t, srv, http.MethodGet, "/api/session/sso", "", nil, nil); res.status != http.StatusNotFound {
		t.Fatalf("sso: %d", res.status)
	}
	if res := call(t, srv, http.MethodGet, oidcPath, "", nil, nil); res.status != http.StatusNotFound {
		t.Fatalf("start: %d", res.status)
	}
}
