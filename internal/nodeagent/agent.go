// Package nodeagent is the tier-1 DaemonSet: it tails the kubelet's pod
// logs through a read-only hostPath, redacts on the node, optionally reads
// cgroup v2 memory.peak and pressure, and posts JSON batches to the
// controller. It runs as root without capabilities.
package nodeagent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/karimzakzouk/sentinel/internal/redact"
)

// Config is the agent's runtime settings.
type Config struct {
	// LogRoot is where the kubelet writes pod logs, mounted at
	// /host/var/log/pods in the DaemonSet.
	LogRoot string
	// CgroupRoot, when set, is where cgroup v2 lives, mounted at
	// /host/sys/fs/cgroup. Empty skips cgroup samples.
	CgroupRoot string
	// Endpoint is the controller's base URL, for example
	// http://sentinel-node-agent.sentinel.svc:4319
	Endpoint string
	Token    string
	Node     string
	// Namespaces limits which pods are tailed; empty means all.
	Namespaces map[string]bool
	// Poll is how often to scan for new lines; defaults to 2s.
	Poll time.Duration
	// BatchSize is the maximum lines per POST; defaults to 200.
	BatchSize int
	// FromStart reads existing file content on first sight (tests).
	FromStart bool
	Log       *slog.Logger
}

// Run tails until ctx is cancelled. It returns ctx.Err() on cancel.
func Run(ctx context.Context, cfg Config) error {
	if cfg.LogRoot == "" {
		cfg.LogRoot = "/host/var/log/pods"
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	if cfg.Node == "" {
		cfg.Node, _ = os.Hostname()
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	redactor := redact.New()
	sender := &Sender{Endpoint: cfg.Endpoint, Token: cfg.Token}
	states := map[string]*tailState{}
	tick := time.NewTicker(cfg.Poll)
	defer tick.Stop()
	log.Info("node agent started", "logs", cfg.LogRoot, "cgroup", cfg.CgroupRoot, "endpoint", cfg.Endpoint)
	for {
		if err := pollOnce(ctx, cfg, redactor, sender, states); err != nil && ctx.Err() == nil {
			log.Warn("node agent poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func pollOnce(ctx context.Context, cfg Config, r *redact.Redactor, sender *Sender, states map[string]*tailState) error {
	files, err := listLogFiles(cfg.LogRoot)
	if err != nil {
		return err
	}
	var batch Batch
	batch.Node = cfg.Node
	seenPods := map[string]PodLog{}
	for _, path := range files {
		pl, ok := ParsePodLogPath(cfg.LogRoot, path)
		if !ok {
			continue
		}
		if len(cfg.Namespaces) > 0 && !cfg.Namespaces[pl.Namespace] {
			continue
		}
		seenPods[pl.UID] = pl
		st := states[path]
		if st == nil {
			st = &tailState{}
			states[path] = st
		}
		raw, err := readNewLines(path, st, cfg.FromStart)
		if err != nil {
			if os.IsNotExist(err) {
				delete(states, path)
				continue
			}
			return err
		}
		for _, line := range raw {
			at, text := stripCRIPrefix(line)
			if at.IsZero() {
				at = time.Now().UTC()
			}
			text, _ = r.String(text)
			batch.Lines = append(batch.Lines, Line{
				At: at, Namespace: pl.Namespace, Pod: pl.Pod, UID: pl.UID,
				Container: pl.Container, Text: text,
			})
			if len(batch.Lines) >= cfg.BatchSize {
				if err := sender.Send(ctx, batch); err != nil {
					return err
				}
				batch.Lines = nil
			}
		}
	}
	for path := range states {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			delete(states, path)
		}
	}
	if cfg.CgroupRoot != "" {
		pods := make([]PodLog, 0, len(seenPods))
		for _, p := range seenPods {
			pods = append(pods, p)
		}
		batch.Cgroups = readCgroupSamples(cfg.CgroupRoot, pods)
	}
	return sender.Send(ctx, batch)
}

// Abs joins root-relative paths the way tests build fake trees.
func Abs(root string, parts ...string) string {
	return filepath.Join(append([]string{root}, parts...)...)
}
