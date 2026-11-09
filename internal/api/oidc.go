package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// OIDC signs a person in to the console through the identity provider the
// cluster's API server trusts: the provider's ID token is the Kubernetes
// token the console then acts with, as kubectl's OIDC plugins use it, so
// every change is still the person's own. The API server checks the
// token's signature when the console asks who it belongs to; the console
// checks that it answers this sign-in, by its state, nonce and code
// verifier. A session lasts as long as its token.
type OIDC struct {
	// Issuer is the provider's issuer URL, as the API server's
	// authentication configuration names it.
	Issuer string
	// ClientID is the client the API server expects ID tokens for;
	// ClientSecret, when the provider gives the client one.
	ClientID     string
	ClientSecret string
	// RedirectURL is where the provider sends the person back: the
	// console's address followed by /api/session/oidc/callback.
	RedirectURL string
	// Scopes asked for beyond openid, such as email and groups.
	Scopes []string
	HTTP   *http.Client

	mu        sync.Mutex
	endpoints *oidcEndpoints
	pending   map[string]oidcPending
}

type oidcEndpoints struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
}

// oidcPending is a sign-in sent to the provider, by its state.
type oidcPending struct {
	verifier, nonce string
	expires         time.Time
}

const (
	oidcCookie = "sentinel_oidc"
	oidcPath   = "/api/session/oidc"
	oidcWait   = 10 * time.Minute
	// maxPending bounds the sign-ins waiting on the provider at once.
	maxPending = 100
)

func (o *OIDC) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// discover reads the provider's endpoints, once.
func (o *OIDC) discover(r *http.Request) (*oidcEndpoints, error) {
	o.mu.Lock()
	known := o.endpoints
	o.mu.Unlock()
	if known != nil {
		return known, nil
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimSuffix(o.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	res, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the provider's discovery answered %s", res.Status)
	}
	var e oidcEndpoints
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&e); err != nil {
		return nil, err
	}
	if e.Issuer != o.Issuer || e.Authorization == "" || e.Token == "" {
		return nil, fmt.Errorf("the provider names itself %q, not %q, or lacks its endpoints", e.Issuer, o.Issuer)
	}
	o.mu.Lock()
	o.endpoints = &e
	o.mu.Unlock()
	return &e, nil
}

func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// start sends the person to the provider, with a state this browser alone
// holds, a nonce the token must carry back, and a code verifier's
// challenge, so a code taken on the way is of no use to anyone else.
func (c *Console) oidcStart(w http.ResponseWriter, r *http.Request) {
	o := c.OIDC
	e, err := o.discover(r)
	if err != nil {
		http.Error(w, "the identity provider cannot be reached: "+err.Error(), http.StatusBadGateway)
		return
	}
	state, err1 := random()
	nonce, err2 := random()
	verifier, err3 := random()
	if err := errors.Join(err1, err2, err3); err != nil {
		http.Error(w, "could not start a sign-in", http.StatusInternalServerError)
		return
	}
	now := c.now()
	o.mu.Lock()
	if o.pending == nil {
		o.pending = map[string]oidcPending{}
	}
	for k, p := range o.pending {
		if !now.Before(p.expires) {
			delete(o.pending, k)
		}
	}
	if len(o.pending) >= maxPending {
		o.mu.Unlock()
		http.Error(w, "too many sign-ins at once; try again in a few minutes", http.StatusServiceUnavailable)
		return
	}
	o.pending[state] = oidcPending{verifier: verifier, nonce: nonce, expires: now.Add(oidcWait)}
	o.mu.Unlock()
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {o.ClientID}, "redirect_uri": {o.RedirectURL},
		"scope": {strings.Join(append([]string{"openid"}, o.Scopes...), " ")}, "state": {state}, "nonce": {nonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	// Lax, unlike the session's cookie: the provider sends the person back
	// from its own site, and the browser must bring this along.
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state, Path: oidcPath, HttpOnly: true, SameSite: http.SameSiteLaxMode, //nolint:gosec // Secure follows the page's scheme
		Secure: overTLS(r), MaxAge: int(oidcWait.Seconds())})
	sep := "?"
	if strings.Contains(e.Authorization, "?") {
		sep = "&"
	}
	http.Redirect(w, r, e.Authorization+sep+q.Encode(), http.StatusFound)
}

// oidcCallback takes the person back from the provider: it trades the code
// for the ID token, checks the token answers this sign-in, and starts a
// session with it.
func (c *Console) oidcCallback(w http.ResponseWriter, r *http.Request) {
	o := c.OIDC
	q := r.URL.Query()
	state := q.Get("state")
	ck, err := r.Cookie(oidcCookie)
	if err != nil || state == "" || ck.Value != state {
		http.Error(w, "this sign-in did not start in this browser; sign in again", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: oidcPath, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, //nolint:gosec // clears the cookie
		Secure: overTLS(r)})
	o.mu.Lock()
	p, ok := o.pending[state]
	delete(o.pending, state)
	o.mu.Unlock()
	switch {
	case !ok || !c.now().Before(p.expires):
		http.Error(w, "this sign-in took too long or was used already; sign in again", http.StatusBadRequest)
		return
	case q.Get("error") != "":
		http.Error(w, "the identity provider refused: "+q.Get("error")+" "+q.Get("error_description"), http.StatusUnauthorized)
		return
	case q.Get("code") == "":
		http.Error(w, "the identity provider sent no code", http.StatusBadRequest)
		return
	}
	token, expires, err := o.exchange(r, q.Get("code"), p, c.now())
	if err != nil {
		http.Error(w, "the sign-in failed: "+err.Error(), http.StatusUnauthorized)
		return
	}
	if s, status, why := c.begin(w, r, token, expires); s == nil {
		http.Error(w, why, status)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// exchange trades a code for the ID token, and checks that the token was
// issued by the provider, for this client, for this sign-in, and is not
// expired. Its signature is the API server's to check.
func (o *OIDC) exchange(r *http.Request, code string, p oidcPending, now time.Time) (string, time.Time, error) {
	e, err := o.discover(r)
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.RedirectURL},
		"client_id": {o.ClientID}, "code_verifier": {p.verifier}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, e.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if o.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(o.ClientID), url.QueryEscape(o.ClientSecret))
	}
	res, err := o.client().Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil || res.StatusCode != http.StatusOK || out.IDToken == "" {
		return "", time.Time{}, fmt.Errorf("the provider gave no ID token (%s %s)", res.Status, out.Error)
	}
	claims, err := idClaims(out.IDToken)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Unix(claims.Expiry, 0)
	switch {
	case claims.Issuer != o.Issuer:
		return "", time.Time{}, fmt.Errorf("the token's issuer is %q", claims.Issuer)
	case !slices.Contains(claims.Audience, o.ClientID):
		return "", time.Time{}, errors.New("the token is not for this client")
	case claims.Nonce != p.nonce:
		return "", time.Time{}, errors.New("the token answers another sign-in")
	case !now.Before(expires):
		return "", time.Time{}, errors.New("the token has expired")
	}
	return out.IDToken, expires, nil
}

type claims struct {
	Issuer   string   `json:"iss"`
	Audience audience `json:"aud"`
	Expiry   int64    `json:"exp"`
	Nonce    string   `json:"nonce"`
}

// audience is a token's aud, one string or several.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	err := json.Unmarshal(b, &many)
	*a = many
	return err
}

// idClaims reads a JWT's claims, unverified.
func idClaims(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("the ID token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errors.New("the ID token's claims do not decode")
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return claims{}, errors.New("the ID token's claims do not read")
	}
	return c, nil
}

// sso says whether the console signs people in through a provider.
func (c *Console) sso(w http.ResponseWriter, _ *http.Request) {
	if c.OIDC == nil {
		http.Error(w, "no single sign-on", http.StatusNotFound)
		return
	}
	issuer := c.OIDC.Issuer
	if u, err := url.Parse(issuer); err == nil && u.Host != "" {
		issuer = u.Host
	}
	writeJSON(w, map[string]string{"provider": issuer, "start": oidcPath})
}
