package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
	"github.com/karimzakzouk/sentinel/internal/resume"
	"github.com/karimzakzouk/sentinel/internal/settings"
)

// Console lets a person act through the UI: approve or veto an action
// Sentinel asked for, resume an owner it paused, and set a namespace's
// SentinelPolicy. The person signs in with their own Kubernetes token, and
// every change is made with it, so Kubernetes' permissions and the fence
// decide what they may do, as they would for kubectl. Sentinel's own
// identity, which may not approve its own actions, never makes them; the
// token stays in the controller's memory, and is never logged or recorded.
type Console struct {
	// Clients builds clients that act with a person's token.
	Clients func(token string) (kubernetes.Interface, dynamic.Interface, error)
	// Namespaces are those Sentinel watches, whose policies the console
	// shows.
	Namespaces []string
	// AgentNamespace is where the cluster-wide SentinelSettings object lives.
	AgentNamespace string
	// SessionTTL is how long a sign-in lasts; 8 hours when zero.
	SessionTTL time.Duration
	// OIDC, when set, signs people in through the cluster's identity
	// provider as well as with a token.
	OIDC *OIDC
	Now  func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	token   string
	user    string
	groups  []string
	expires time.Time
}

// maxSessions bounds the sign-ins held at once; the oldest goes first.
const maxSessions = 200

const sessionCookie = "sentinel_session"

// ClientsFor returns a Console's Clients for an API server: the
// controller's own address and trust, with the person's token as the only
// credential.
func ClientsFor(base *rest.Config) func(string) (kubernetes.Interface, dynamic.Interface, error) {
	return func(token string) (kubernetes.Interface, dynamic.Interface, error) {
		c := rest.AnonymousClientConfig(base)
		c.BearerToken = token
		k, err := kubernetes.NewForConfig(c)
		if err != nil {
			return nil, nil, err
		}
		d, err := dynamic.NewForConfig(c)
		return k, d, err
	}
}

func (c *Console) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Console) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/session", c.signIn)
	mux.HandleFunc("GET /api/session", c.whoami)
	mux.HandleFunc("DELETE /api/session", c.signOut)
	mux.HandleFunc("GET /api/session/sso", c.sso)
	if c.OIDC != nil {
		mux.HandleFunc("GET "+oidcPath, c.oidcStart)
		mux.HandleFunc("GET "+oidcPath+"/callback", c.oidcCallback)
	}
	mux.HandleFunc("POST /api/requests/{namespace}/{name}/{decision}", c.write(c.decide))
	mux.HandleFunc("POST /api/resume", c.write(c.resume))
	mux.HandleFunc("GET /api/policies", c.read(c.policies))
	mux.HandleFunc("PUT /api/policies/{namespace}/{name}", c.write(c.putPolicy))
	mux.HandleFunc("DELETE /api/policies/{namespace}/{name}", c.write(c.deletePolicy))
	mux.HandleFunc("GET /api/settings", c.read(c.getSettings))
	mux.HandleFunc("PUT /api/settings", c.write(c.putSettings))
}

// signIn checks a token by asking the API server who it belongs to, as
// itself, and starts a session for that person.
func (c *Console) signIn(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || !isJSON(r) {
		http.Error(w, "sign in from Sentinel's own page", http.StatusForbidden)
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Token) == "" {
		http.Error(w, "give a token", http.StatusBadRequest)
		return
	}
	s, status, why := c.begin(w, r, strings.TrimSpace(in.Token), time.Time{})
	if s == nil {
		http.Error(w, why, status)
		return
	}
	writeJSON(w, map[string]any{"user": s.user, "groups": s.groups})
}

// begin checks a token by asking the API server who it belongs to, as
// itself, and starts a session for that person, until the session's time
// or until, if sooner. It returns the session, or the status and reason
// it does not start.
func (c *Console) begin(w http.ResponseWriter, r *http.Request, token string, until time.Time) (*session, int, string) {
	k, _, err := c.Clients(token)
	if err != nil {
		return nil, http.StatusBadRequest, "could not use the token"
	}
	review, err := k.AuthenticationV1().SelfSubjectReviews().Create(r.Context(), &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil || review.Status.UserInfo.Username == "" {
		return nil, http.StatusUnauthorized, "the cluster does not accept that token"
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, http.StatusInternalServerError, "could not start a session"
	}
	ttl := c.SessionTTL
	if ttl == 0 {
		ttl = 8 * time.Hour
	}
	expires := c.now().Add(ttl)
	if !until.IsZero() && until.Before(expires) {
		expires = until
	}
	s := &session{token: token, user: review.Status.UserInfo.Username, groups: review.Status.UserInfo.Groups, expires: expires}
	key := hex.EncodeToString(id)
	c.mu.Lock()
	if c.sessions == nil {
		c.sessions = map[string]*session{}
	}
	c.expire()
	for len(c.sessions) >= maxSessions {
		oldest := ""
		for k, v := range c.sessions {
			if oldest == "" || v.expires.Before(c.sessions[oldest].expires) {
				oldest = k
			}
		}
		delete(c.sessions, oldest)
	}
	c.sessions[key] = s
	c.mu.Unlock()
	// Secure only where the page came over TLS: through kubectl port-forward
	// it is http://localhost, where some browsers drop Secure cookies.
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: key, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, //nolint:gosec // Secure follows the page's scheme, above
		Secure: overTLS(r), Expires: s.expires})
	return s, http.StatusOK, ""
}

// overTLS reports whether the page came over TLS.
func overTLS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (c *Console) whoami(w http.ResponseWriter, r *http.Request) {
	s := c.session(r)
	if s == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"user": s.user, "groups": s.groups})
}

func (c *Console) signOut(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		c.mu.Lock()
		delete(c.sessions, ck.Value)
		c.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, //nolint:gosec // clears the cookie; nothing to protect
		Secure: overTLS(r)})
	w.WriteHeader(http.StatusNoContent)
}

func (c *Console) session(r *http.Request) *session {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sessions[ck.Value]
	if s == nil || !c.now().Before(s.expires) {
		delete(c.sessions, ck.Value)
		return nil
	}
	return s
}

// expire drops sessions past their time. Callers hold c.mu.
func (c *Console) expire() {
	now := c.now()
	for k, s := range c.sessions {
		if !now.Before(s.expires) {
			delete(c.sessions, k)
		}
	}
}

type handler func(w http.ResponseWriter, r *http.Request, s *session, k kubernetes.Interface, d dynamic.Interface)

// read serves a signed-in person's request with clients that act as them.
func (c *Console) read(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := c.session(r)
		if s == nil {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		k, d, err := c.Clients(s.token)
		if err != nil {
			http.Error(w, "could not use your token", http.StatusInternalServerError)
			return
		}
		h(w, r, s, k, d)
	}
}

// write is read for changes: they must come from Sentinel's own page, as
// JSON, which a page elsewhere cannot send with the person's cookie.
func (c *Console) write(h handler) http.HandlerFunc {
	return c.read(func(w http.ResponseWriter, r *http.Request, s *session, k kubernetes.Interface, d dynamic.Interface) {
		if !sameOrigin(r) || !isJSON(r) {
			http.Error(w, "changes come from Sentinel's own page, as JSON", http.StatusForbidden)
			return
		}
		h(w, r, s, k, d)
	})
}

// sameOrigin reports whether a request's Origin, when the browser sends
// one, is the page's own.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func isJSON(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
}

// decide approves or vetoes an action request, as the signed-in person:
// the fence lets only that person write their own name there.
func (c *Console) decide(w http.ResponseWriter, r *http.Request, s *session, _ kubernetes.Interface, d dynamic.Interface) {
	annotation := map[string]string{"approve": actor.AnnotationApprovedBy, "veto": actor.AnnotationVetoedBy}[r.PathValue("decision")]
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	switch {
	case annotation == "":
		http.Error(w, "approve or veto", http.StatusNotFound)
		return
	case !slices.Contains(c.Namespaces, ns):
		http.Error(w, "Sentinel does not act in "+ns, http.StatusNotFound)
		return
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{annotation: s.user}}})
	if _, err := d.Resource(actor.RequestsResource).Namespace(ns).Patch(r.Context(), name, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: "sentinel-console"}); err != nil {
		kubeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"done": r.PathValue("decision") + "d " + ns + "/" + name + " as " + s.user})
}

// resume resumes an owner Sentinel paused, as the signed-in person, as
// sentinel resume does.
func (c *Console) resume(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	var in struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in); err != nil || in.Namespace == "" || in.Name == "" {
		http.Error(w, "name the owner: kind, namespace and name", http.StatusBadRequest)
		return
	}
	var said string
	var err error
	switch in.Kind {
	case "Application":
		said, err = resume.ArgoCD(r.Context(), d, in.Namespace, in.Name)
	case "Kustomization":
		said, err = resume.Flux(r.Context(), d, actor.KustomizationsResource, in.Kind, in.Namespace, in.Name)
	case "HelmRelease":
		said, err = resume.Flux(r.Context(), d, actor.HelmReleasesResource, in.Kind, in.Namespace, in.Name)
	default:
		http.Error(w, "Sentinel pauses Argo CD Applications and Flux Kustomizations and HelmReleases", http.StatusBadRequest)
		return
	}
	if err != nil {
		kubeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"done": said})
}

// NamespacePolicies are one namespace's SentinelPolicies, or why the
// signed-in person cannot see them.
type NamespacePolicies struct {
	Namespace string                    `json:"namespace"`
	Policies  []v1alpha1.SentinelPolicy `json:"policies"`
	Error     string                    `json:"error,omitempty"`
}

func (c *Console) policies(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	out := []NamespacePolicies{}
	for _, ns := range c.Namespaces {
		np := NamespacePolicies{Namespace: ns, Policies: []v1alpha1.SentinelPolicy{}}
		list, err := d.Resource(actor.PoliciesResource).Namespace(ns).List(r.Context(), metav1.ListOptions{})
		if err != nil {
			np.Error = kubeMessage(err)
		} else {
			for _, u := range list.Items {
				var p v1alpha1.SentinelPolicy
				if err := fromUnstructured(u, &p); err == nil {
					np.Policies = append(np.Policies, p)
				}
			}
		}
		out = append(out, np)
	}
	writeJSON(w, out)
}

// putPolicy writes a namespace's SentinelPolicy as the signed-in person,
// with server-side apply, so the fields the console sets are its own and a
// policy kept in Git keeps the rest.
func (c *Console) putPolicy(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !slices.Contains(c.Namespaces, ns) {
		http.Error(w, "Sentinel does not watch "+ns, http.StatusNotFound)
		return
	}
	var spec v1alpha1.SentinelPolicySpec
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		http.Error(w, "the policy does not read: "+err.Error(), http.StatusBadRequest)
		return
	}
	p := v1alpha1.SentinelPolicy{TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SentinelPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	body, err := json.Marshal(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	force := true
	if _, err := d.Resource(actor.PoliciesResource).Namespace(ns).Patch(r.Context(), name, types.ApplyPatchType, body,
		metav1.PatchOptions{FieldManager: "sentinel-console", Force: &force}); err != nil {
		kubeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"done": "saved " + ns + "/" + name})
}

func (c *Console) deletePolicy(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	if !slices.Contains(c.Namespaces, ns) {
		http.Error(w, "Sentinel does not watch "+ns, http.StatusNotFound)
		return
	}
	if err := d.Resource(actor.PoliciesResource).Namespace(ns).Delete(r.Context(), name, metav1.DeleteOptions{}); err != nil {
		kubeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"done": "deleted " + ns + "/" + name})
}

func (c *Console) settingsNS() string {
	if c.AgentNamespace != "" {
		return c.AgentNamespace
	}
	return "sentinel"
}

// getSettings returns the cluster-wide SentinelSettings the chart seeded.
func (c *Console) getSettings(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	u, err := d.Resource(settings.Resource).Namespace(c.settingsNS()).Get(r.Context(), settings.Name, metav1.GetOptions{})
	if err != nil {
		kubeError(w, err)
		return
	}
	var s v1alpha1.SentinelSettings
	if err := fromUnstructured(*u, &s); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, s)
}

// putSettings writes SentinelSettings as the signed-in person. The controller
// applies the Spec to ConfigMaps; the console does not rewrite them itself.
func (c *Console) putSettings(w http.ResponseWriter, r *http.Request, _ *session, _ kubernetes.Interface, d dynamic.Interface) {
	var spec v1alpha1.SentinelSettingsSpec
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		http.Error(w, "the settings do not read: "+err.Error(), http.StatusBadRequest)
		return
	}
	s := v1alpha1.SentinelSettings{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SentinelSettings"},
		ObjectMeta: metav1.ObjectMeta{Name: settings.Name, Namespace: c.settingsNS()},
		Spec:       spec,
	}
	body, err := json.Marshal(s)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	force := true
	if _, err := d.Resource(settings.Resource).Namespace(c.settingsNS()).Patch(r.Context(), settings.Name, types.ApplyPatchType, body,
		metav1.PatchOptions{FieldManager: "sentinel-console", Force: &force}); err != nil {
		kubeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"done": "saved settings; Sentinel reloads within a minute"})
}

// kubeError passes on what the API server said, with its status: a
// Forbidden is the person's permissions or the fence speaking.
func kubeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var se apierrors.APIStatus
	if errors.As(err, &se) && se.Status().Code != 0 {
		status = int(se.Status().Code)
	}
	http.Error(w, kubeMessage(err), status)
}

func kubeMessage(err error) string {
	var se apierrors.APIStatus
	if errors.As(err, &se) && se.Status().Message != "" {
		return se.Status().Message
	}
	return err.Error()
}

func fromUnstructured(u unstructured.Unstructured, out any) error {
	data, err := json.Marshal(u.Object)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
