package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/karimzakzouk/sentinel/api/v1alpha1"
	"github.com/karimzakzouk/sentinel/internal/actor"
)

// console serves a Console whose clients answer as the person whose token
// is "alice-token", and as nobody for any other.
func console(t *testing.T) (*httptest.Server, *dynamicfake.FakeDynamicClient, *[]string) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		actor.RequestsResource: "ActionRequestList", actor.PoliciesResource: "SentinelPolicyList"},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": v1alpha1.GroupVersion.String(), "kind": "ActionRequest",
			"metadata": map[string]any{"name": "sentinel-rollout-1", "namespace": "shop"}}})
	// The fake does not create objects through server-side apply, as an
	// API server does; this does, for policies.
	fakeDyn := dyn
	fakeDyn.PrependReactor("patch", "sentinelpolicies", func(a k8stesting.Action) (bool, runtime.Object, error) {
		patch := a.(k8stesting.PatchAction)
		if patch.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		obj := &unstructured.Unstructured{}
		if err := json.Unmarshal(patch.GetPatch(), &obj.Object); err != nil {
			return true, nil, err
		}
		gvr := actor.PoliciesResource
		if _, err := fakeDyn.Tracker().Get(gvr, patch.GetNamespace(), patch.GetName()); err != nil {
			return true, obj, fakeDyn.Tracker().Create(gvr, obj, patch.GetNamespace())
		}
		return true, obj, fakeDyn.Tracker().Update(gvr, obj, patch.GetNamespace())
	})
	var tokens []string
	c := &Console{Namespaces: []string{"shop"}, Clients: func(token string) (kubernetes.Interface, dynamic.Interface, error) {
		tokens = append(tokens, token)
		k := fake.NewClientset()
		k.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
			user := ""
			if token == "alice-token" {
				user = "alice@example.com"
			}
			return true, &authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{
				UserInfo: authenticationv1.UserInfo{Username: user, Groups: []string{"shop-admins"}}}}, nil
		})
		return k, dyn, nil
	}}
	srv := httptest.NewServer((&Server{Console: c}).Handler())
	t.Cleanup(srv.Close)
	return srv, dyn, &tokens
}

// reply is what a call got back.
type reply struct {
	status  int
	body    []byte
	cookies []*http.Cookie
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, cookie *http.Cookie, headers map[string]string) reply {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{status: res.StatusCode, body: data, cookies: res.Cookies()}
}

func signIn(t *testing.T, srv *httptest.Server) *http.Cookie {
	t.Helper()
	res := call(t, srv, http.MethodPost, "/api/session", `{"token": "alice-token"}`, nil, nil)
	if res.status != http.StatusOK {
		t.Fatalf("sign in: %d", res.status)
	}
	for _, c := range res.cookies {
		if c.Name == sessionCookie && c.HttpOnly && c.SameSite == http.SameSiteStrictMode {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// A person signs in with their own token, which the cluster names; a token
// it does not accept starts no session.
func TestTheConsoleSignsInWhomTheClusterNames(t *testing.T) {
	srv, _, _ := console(t)
	if res := call(t, srv, http.MethodPost, "/api/session", `{"token": "stolen"}`, nil, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("a token the cluster does not accept: %d", res.status)
	}
	cookie := signIn(t, srv)
	var who struct {
		User string `json:"user"`
	}
	_ = json.Unmarshal(call(t, srv, http.MethodGet, "/api/session", "", cookie, nil).body, &who)
	if who.User != "alice@example.com" {
		t.Fatalf("signed in as %q", who.User)
	}
	if res := call(t, srv, http.MethodDelete, "/api/session", "", cookie, nil); res.status != http.StatusNoContent {
		t.Fatalf("sign out: %d", res.status)
	}
	if res := call(t, srv, http.MethodGet, "/api/session", "", cookie, nil); res.status != http.StatusUnauthorized {
		t.Fatalf("after signing out: %d", res.status)
	}
}

// An approval is written with the person's token, in their name; without a
// session, from another page, or not as JSON, nothing is written.
func TestAnApprovalIsThePersonsOwn(t *testing.T) {
	srv, dyn, tokens := console(t)
	path := "/api/requests/shop/sentinel-rollout-1/approve"
	for name, tc := range map[string]struct {
		headers map[string]string
		signed  bool
		want    int
	}{
		"without signing in":   {nil, false, http.StatusUnauthorized},
		"from another page":    {map[string]string{"Origin": "https://evil.example"}, true, http.StatusForbidden},
		"as a form":            {map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, true, http.StatusForbidden},
		"outside Sentinel's":   {nil, true, http.StatusNotFound},
		"approved as yourself": {nil, true, http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			var cookie *http.Cookie
			if tc.signed {
				cookie = signIn(t, srv)
			}
			p := path
			if name == "outside Sentinel's" {
				p = "/api/requests/kube-system/sentinel-rollout-1/approve"
			}
			if res := call(t, srv, http.MethodPost, p, "{}", cookie, tc.headers); res.status != tc.want {
				t.Fatalf("%d, want %d", res.status, tc.want)
			}
		})
	}
	u, err := dyn.Resource(actor.RequestsResource).Namespace("shop").Get(t.Context(), "sentinel-rollout-1", metav1.GetOptions{})
	if err != nil || u.GetAnnotations()[actor.AnnotationApprovedBy] != "alice@example.com" {
		t.Fatalf("annotations %v, %v", u.GetAnnotations(), err)
	}
	for _, token := range *tokens {
		if token != "alice-token" {
			t.Fatalf("a client used %q", token)
		}
	}
}

// A policy set in the console is the person's, applied as them, and the
// console lists it with the namespace's others.
func TestAPolicySetInTheConsole(t *testing.T) {
	srv, dyn, _ := console(t)
	cookie := signIn(t, srv)
	spec := `{"autonomy": [{"action": "rollout.undo", "level": "autonomous"}, {"action": "fix.merge", "level": "autonomous", "earned": true}]}`
	if res := call(t, srv, http.MethodPut, "/api/policies/shop/rollbacks", spec, cookie, nil); res.status != http.StatusOK {
		t.Fatalf("save: %d %s", res.status, res.body)
	}
	u, err := dyn.Resource(actor.PoliciesResource).Namespace("shop").Get(t.Context(), "rollbacks", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	autonomy, _, _ := unstructured.NestedSlice(u.Object, "spec", "autonomy")
	if len(autonomy) != 2 {
		t.Fatalf("saved %v", u.Object)
	}
	if res := call(t, srv, http.MethodPut, "/api/policies/shop/rollbacks", `{"autonomy": [], "unknown": 1}`, cookie, nil); res.status != http.StatusBadRequest {
		t.Fatalf("a field policies do not have: %d", res.status)
	}
	var got []NamespacePolicies
	_ = json.Unmarshal(call(t, srv, http.MethodGet, "/api/policies", "", cookie, nil).body, &got)
	if len(got) != 1 || got[0].Namespace != "shop" || len(got[0].Policies) != 1 || got[0].Policies[0].Name != "rollbacks" {
		t.Fatalf("policies %+v", got)
	}
}
