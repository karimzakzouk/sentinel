//go:build live

package api

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestLiveOIDC signs a person in to the console through a real identity
// provider, as a browser would: hack/oidc-live.sh starts Dex with a user
// and a client, and runs it. The API server, which would check the ID
// token's signature against the provider's keys, is played by a fake that
// names the person from the token's email claim.
func TestLiveOIDC(t *testing.T) {
	issuer, listen := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_LISTEN")
	secret, err := os.ReadFile(os.Getenv("OIDC_CLIENT_SECRET_FILE"))
	if issuer == "" || listen == "" || err != nil {
		t.Skip("no OIDC_ISSUER, OIDC_LISTEN or OIDC_CLIENT_SECRET_FILE")
	}
	var tokens []string
	c := &Console{Namespaces: []string{"shop"}, Clients: func(token string) (kubernetes.Interface, dynamic.Interface, error) {
		tokens = append(tokens, token)
		k := fake.NewClientset()
		k.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			var claims struct {
				Email string `json:"email"`
			}
			if parts := strings.Split(token, "."); len(parts) == 3 {
				payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
				_ = json.Unmarshal(payload, &claims)
			}
			return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{
				UserInfo: authenticationv1.UserInfo{Username: claims.Email}}}, nil
		})
		return k, nil, nil
	}}
	c.OIDC = &OIDC{Issuer: issuer, ClientID: "sentinel", ClientSecret: strings.TrimSpace(string(secret)),
		RedirectURL: "http://" + listen + oidcPath + "/callback", Scopes: []string{"email"}}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: (&Server{Console: c}).Handler(), ReadHeaderTimeout: 5 * time.Second}}
	srv.Start()
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	// The person asks to sign in, and lands on the provider's login page.
	res, err := browser.Get(srv.URL + oidcPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	login := res.Request.URL
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(login.String(), issuer) {
		t.Fatalf("the sign-in led to %d %s", res.StatusCode, login)
	}
	// They give the provider their password; it sends them back.
	res, err = browser.PostForm(login.String(), url.Values{"login": {"alice@example.com"}, "password": {"password"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if !strings.HasPrefix(res.Request.URL.String(), srv.URL+"/") {
		t.Fatalf("after the password, at %s (%d)", res.Request.URL, res.StatusCode)
	}
	who, err := browser.Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = who.Body.Close() }()
	var me struct{ User string }
	_ = json.NewDecoder(who.Body).Decode(&me)
	if who.StatusCode != http.StatusOK || me.User != "alice@example.com" || len(tokens) != 1 {
		t.Fatalf("signed in: %d %q, %d tokens", who.StatusCode, me.User, len(tokens))
	}
	t.Logf("signed in as %s with the provider's ID token", me.User)
}
