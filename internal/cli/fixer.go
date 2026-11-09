package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/karimzakzouk/sentinel/internal/codefix"
	"github.com/karimzakzouk/sentinel/internal/fixer"
	"github.com/karimzakzouk/sentinel/internal/forge"
	"github.com/karimzakzouk/sentinel/internal/kube"
	"github.com/karimzakzouk/sentinel/internal/lead"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// FixerConfig is the Fixer's own configuration.
type FixerConfig struct {
	// Namespace is where Fix requests live.
	Namespace string `json:"namespace"`
	Forge     struct {
		// Kind is the forge's API: github, gitea, gitlab, bitbucket or
		// bitbucket-server; empty means GitHub at api.github.com and Gitea
		// anywhere else.
		Kind forge.Kind `json:"kind,omitempty"`
		// Web is the forge's address, which its repositories' URLs start
		// with, and API its API's base URL.
		Web string `json:"web"`
		API string `json:"api"`
		// TokenFile holds the forge token; User is who Git pushes as.
		TokenFile string `json:"tokenFile"`
		User      string `json:"user"`
	} `json:"forge"`
	Author struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
	ArgoCDNamespace string        `json:"argocdNamespace,omitempty"`
	Poll            time.Duration `json:"poll,omitempty"`
	HealthAddr      string        `json:"healthAddr,omitempty"`
	// LeaderElection, when set, has replicas hold this Lease in turn; only
	// its holder works.
	LeaderElection *lead.Config `json:"leaderElection,omitempty"`
	// Code, when set, turns on fixes to a failing image's code: the Fixer
	// reads images' labels from their registries, those listed as insecure
	// over plain HTTP, the platform's image of an index.
	Code *struct {
		InsecureRegistries []string `json:"insecureRegistries,omitempty"`
		Platform           string   `json:"platform,omitempty"`
		// LabNamespaces are where the Lab races images built from fixes.
		LabNamespaces []string `json:"labNamespaces,omitempty"`
	} `json:"code,omitempty"`
}

func loadFixerConfig(path string) (FixerConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's command line
	if err != nil {
		return FixerConfig{}, err
	}
	var c FixerConfig
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return FixerConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	switch {
	case c.Namespace == "":
		return FixerConfig{}, errors.New("namespace is empty: name where Fix requests live")
	case c.Forge.Web == "" || c.Forge.API == "":
		return FixerConfig{}, errors.New("forge.web and forge.api must name the forge the Fixer proposes changes on")
	case c.Forge.TokenFile == "":
		return FixerConfig{}, errors.New("forge.tokenFile is empty: the Fixer needs a token to push and open pull requests")
	case c.Forge.Kind != "" && !slices.Contains(forge.Kinds, c.Forge.Kind):
		return FixerConfig{}, fmt.Errorf("forge.kind %q is none of %v", c.Forge.Kind, forge.Kinds)
	}
	if c.Forge.User == "" {
		c.Forge.User = gitUsers[c.Forge.Kind]
	}
	if c.Author.Name == "" {
		c.Author.Name = "Sentinel"
	}
	if c.ArgoCDNamespace == "" {
		c.ArgoCDNamespace = "argocd"
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8081"
	}
	return c, nil
}

// gitUsers are the names Git pushes as with a forge's tokens, by default:
// Bitbucket's access tokens take only x-token-auth, and GitLab's any name.
var gitUsers = map[forge.Kind]string{"": "sentinel", forge.GitHub: "sentinel", forge.Gitea: "sentinel",
	forge.GitLab: "oauth2", forge.Bitbucket: "x-token-auth", forge.BitbucketServer: "x-token-auth"}

func newFixerCmd(kf *kube.Flags) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "fixer",
		Short: "Run the Fixer, which proposes the change that makes a GitOps rollback hold in Git as a pull request",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadFixerConfig(configPath)
			if err != nil {
				return err
			}
			token, err := os.ReadFile(cfg.Forge.TokenFile)
			if err != nil {
				return fmt.Errorf("read the forge token: %w", err)
			}
			if strings.TrimSpace(string(token)) == "" {
				return fmt.Errorf("%s is empty: the Fixer needs a token to push and open pull requests", cfg.Forge.TokenFile)
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			rc, err := kf.RESTConfig(version.UserAgent("fixer"))
			if err != nil {
				return err
			}
			client, err := kubernetes.NewForConfig(rc)
			if err != nil {
				return err
			}
			dyn, err := dynamic.NewForConfig(rc)
			if err != nil {
				return err
			}
			f := &fixer.Fixer{Client: client, Dynamic: dyn, Namespace: cfg.Namespace, GitUser: cfg.Forge.User,
				Forge: &forge.Client{Kind: cfg.Forge.Kind, Web: cfg.Forge.Web, API: cfg.Forge.API, Token: strings.TrimSpace(string(token)),
					HTTP: &http.Client{Timeout: time.Minute}},
				Author:          object.Signature{Name: cfg.Author.Name, Email: cfg.Author.Email},
				ArgoCDNamespace: cfg.ArgoCDNamespace, Poll: cfg.Poll, Log: log}
			if c := cfg.Code; c != nil {
				f.Registry = &codefix.Registry{HTTP: &http.Client{Timeout: 30 * time.Second}, Insecure: c.InsecureRegistries, Platform: c.Platform}
				f.LabNamespaces = c.LabNamespaces
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			var ready atomic.Bool
			f.OnReady = func() { ready.Store(true) }
			srv := &http.Server{Addr: cfg.HealthAddr, Handler: healthHandler(&ready), ReadHeaderTimeout: 5 * time.Second}
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("health server stopped", "error", err)
				}
			}()
			defer func() { _ = srv.Close() }()
			if c := cfg.LeaderElection; c != nil {
				c.Identity = podName()
				// A replica standing by is ready: it takes over when the
				// holder stops renewing the Lease.
				ready.Store(true)
				return lead.Run(ctx, client, *c, log, f.Run)
			}
			return f.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "/etc/sentinel-fixer/config.yaml", "path to the Fixer's configuration")
	return cmd
}
