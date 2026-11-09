package codefix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Built is where an image's code came from.
type Built struct {
	// Repository is the source repository's URL, and Revision the commit
	// the image was built from.
	Repository, Revision string
}

var hexRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ResolveSource names the repository and commit an image was built from.
// Its OCI labels name both, but a label alone is one unsigned claim; a tag
// that is the same commit, as most pipelines tag an image, is a second,
// independent one. Without two that agree the Fixer does not touch code.
func ResolveSource(image string, labels map[string]string) (Built, error) {
	repo, rev := labels["org.opencontainers.image.source"], strings.ToLower(labels["org.opencontainers.image.revision"])
	switch {
	case repo == "" || rev == "":
		return Built{}, fmt.Errorf("%s does not name its source and commit in its labels", image)
	case !hexRevision.MatchString(rev):
		return Built{}, fmt.Errorf("%s names its commit as %q, not a commit hash", image, rev)
	}
	ref, err := ParseReference(image)
	if err != nil {
		return Built{}, err
	}
	tag := strings.TrimPrefix(strings.ToLower(ref.Tag), "sha-")
	version := strings.ToLower(labels["org.opencontainers.image.version"])
	switch {
	case hexRevision.MatchString(tag) && strings.HasPrefix(rev, tag):
	case hexRevision.MatchString(version) && strings.HasPrefix(rev, version):
	default:
		return Built{}, fmt.Errorf("only %s's labels say it was built from %s; a fix to its code needs its tag or version to say so too", image, short(rev))
	}
	return Built{Repository: repo, Revision: rev}, nil
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

// Reference is an image reference split into its parts.
type Reference struct {
	Registry, Repository, Tag, Digest string
}

// ParseReference splits an image reference as a container runtime reads
// it: a registry only when the first part looks like a host, Docker Hub
// otherwise, with library/ for one-part names.
func ParseReference(image string) (Reference, error) {
	var r Reference
	name, digest, _ := strings.Cut(image, "@")
	r.Digest = digest
	if i := strings.LastIndexByte(name, ':'); i > strings.LastIndexByte(name, '/') {
		name, r.Tag = name[:i], name[i+1:]
	}
	if name == "" {
		return Reference{}, fmt.Errorf("%q names no image", image)
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Registry, r.Repository = first, rest
	} else {
		r.Registry, r.Repository = "registry-1.docker.io", name
		if !found {
			r.Repository = "library/" + name
		}
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	return r, nil
}

// Registry reads images' labels from OCI registries.
type Registry struct {
	HTTP *http.Client
	// Insecure lists registries reached over plain HTTP, such as one in a
	// test cluster.
	Insecure []string
	// Platform is the architecture whose image an index's labels are read
	// from; amd64 when empty.
	Platform string
}

// Labels returns an image's labels from its config, following an index to
// the platform's image and a registry's anonymous token, as a runtime that
// pulls it would.
func (g *Registry) Labels(ctx context.Context, image string) (map[string]string, error) {
	ref, err := ParseReference(image)
	if err != nil {
		return nil, err
	}
	reference := ref.Digest
	if reference == "" {
		reference = ref.Tag
	}
	token := ""
	var manifest struct {
		MediaType string `json:"mediaType"`
		Config    struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	for range 2 {
		if err := g.get(ctx, ref, "manifests/"+reference, manifestTypes, &token, &manifest); err != nil {
			return nil, err
		}
		if len(manifest.Manifests) == 0 {
			break
		}
		arch := g.Platform
		if arch == "" {
			arch = "amd64"
		}
		next := ""
		for _, m := range manifest.Manifests {
			if m.Platform.OS == "linux" && m.Platform.Architecture == arch {
				next = m.Digest
			}
		}
		if next == "" {
			return nil, fmt.Errorf("%s has no linux/%s image", image, arch)
		}
		reference, manifest.Manifests = next, nil
	}
	if manifest.Config.Digest == "" {
		return nil, fmt.Errorf("%s's manifest names no config", image)
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := g.get(ctx, ref, "blobs/"+manifest.Config.Digest, "*/*", &token, &config); err != nil {
		return nil, err
	}
	return config.Config.Labels, nil
}

var manifestTypes = strings.Join([]string{"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json"}, ", ")

func (g *Registry) get(ctx context.Context, ref Reference, path, accept string, token *string, out any) error {
	scheme := "https"
	for _, h := range g.Insecure {
		if h == ref.Registry {
			scheme = "http"
		}
	}
	u := scheme + "://" + ref.Registry + "/v2/" + ref.Repository + "/" + path
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", accept)
		if *token != "" {
			req.Header.Set("Authorization", "Bearer "+*token)
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		_ = res.Body.Close()
		if err != nil {
			return err
		}
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if *token, err = g.anonymousToken(ctx, res.Header.Get("WWW-Authenticate"), ref); err != nil {
				return err
			}
			continue
		}
		if res.StatusCode/100 != 2 {
			return fmt.Errorf("%s: %s", u, res.Status)
		}
		return json.Unmarshal(body, out)
	}
}

// anonymousToken asks a registry's token service for a pull token, as a
// registry's 401 says to.
func (g *Registry) anonymousToken(ctx context.Context, challenge string, ref Reference) (string, error) {
	scheme, params, ok := strings.Cut(challenge, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Errorf("%s asks for credentials the Fixer does not have", ref.Registry)
	}
	fields := map[string]string{}
	for _, p := range strings.Split(params, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		fields[k] = strings.Trim(v, `"`)
	}
	realm, err := url.Parse(fields["realm"])
	if err != nil || realm.Scheme != "https" && realm.Scheme != "http" {
		return "", fmt.Errorf("%s names no token service", ref.Registry)
	}
	q := realm.Query()
	if s := fields["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+ref.Repository+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	client := g.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", errors.New(ref.Registry + " gave no token")
	}
	return t.Token, nil
}
