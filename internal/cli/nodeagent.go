package cli

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/karimzakzouk/sentinel/internal/nodeagent"
)

func newNodeAgentCmd() *cobra.Command {
	var (
		logRoot    string
		cgroupRoot string
		endpoint   string
		token      string
		node       string
		poll       time.Duration
		namespaces []string
	)
	cmd := &cobra.Command{
		Use:   "node-agent",
		Short: "Tail host pod logs and cgroup samples, and post redacted batches to the controller",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if endpoint == "" {
				return fmt.Errorf("--endpoint is required")
			}
			if token == "" {
				token = strings.TrimSpace(os.Getenv("SENTINEL_NODE_AGENT_TOKEN"))
			}
			if token == "" {
				return fmt.Errorf("--token or SENTINEL_NODE_AGENT_TOKEN is required")
			}
			if node == "" {
				node = strings.TrimSpace(os.Getenv("NODE_NAME"))
			}
			ns := map[string]bool{}
			for _, n := range namespaces {
				n = strings.TrimSpace(n)
				if n != "" {
					ns[n] = true
				}
			}
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			return nodeagent.Run(ctx, nodeagent.Config{
				LogRoot:    logRoot,
				CgroupRoot: cgroupRoot,
				Endpoint:   endpoint,
				Token:      token,
				Node:       node,
				Namespaces: ns,
				Poll:       poll,
				Log:        log,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&logRoot, "log-root", "/host/var/log/pods", "host path where the kubelet writes pod logs")
	f.StringVar(&cgroupRoot, "cgroup-root", "/host/sys/fs/cgroup", "host path of cgroup v2; empty skips cgroup samples")
	f.StringVar(&endpoint, "endpoint", "", "controller base URL, for example http://sentinel-node-agent.sentinel.svc:4319")
	f.StringVar(&token, "token", "", "bearer token; SENTINEL_NODE_AGENT_TOKEN is used when empty")
	f.StringVar(&node, "node", "", "node name posted with each batch; NODE_NAME or the hostname when empty")
	f.DurationVar(&poll, "poll", 2*time.Second, "how often to scan for new log lines")
	f.StringSliceVar(&namespaces, "namespaces", nil, "namespaces to tail; empty means all")
	return cmd
}
