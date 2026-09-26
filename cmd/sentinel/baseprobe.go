// Package main — base-build probe (v1.7.14).
//
// Run #28 post-mortem: search-api's Dockerfile copies Cargo.toml but not
// Cargo.lock onto a rust:1.78 builder. Without the lockfile, every image
// build resolves dependencies FRESH — and today's crate ecosystem contains
// packages that require the 2024 edition, which cargo 1.78 cannot build.
// The result: EVERY branch (including the base) fails the kaniko build
// with "edition2024 not stabilized", while GitHub CI stays green on its
// modern toolchain. Sentinel burned three fix retries trying to fix an
// error its fix did not cause, and opened the PR unverified 4 minutes
// before the run ended.
//
// The probe builds the BASE branch (no fix overlay) once per repo per
// process. If the base also fails with a BUILD-class error, the fix
// proposer's feedback says so explicitly — the corrected fix must then
// ALSO repair the build (copy lockfiles, pin/bump the builder image),
// which is the only path to a deployable fix when the base is rotten.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/executors"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// baseBuildBroken runs the one-time base-branch build probe. Returns true
// when the base branch fails to build with a BUILD-class error (rot), false
// when the base builds fine (the failure IS the fix's fault) or the probe
// is disabled/undeterminable. The result is cached per repo for the
// process lifetime.
func (w *wiring) baseBuildBroken(ctx context.Context, fp *schemas.FixProposal, prodImage string) bool {
	if !w.Config().BaseBuildProbe {
		return false
	}
	if fp == nil {
		return false
	}
	repoName := ""
	if fp.Repo != nil {
		repoName = *fp.Repo
	}
	if repoName == "" || prodImage == "" {
		return false
	}
	baseBranch := "main"
	if fp.BaseBranch != nil && *fp.BaseBranch != "" {
		baseBranch = *fp.BaseBranch
	}

	w.baseProbeMu.Lock()
	if w.baseProbeCache == nil {
		w.baseProbeCache = map[string]bool{}
	}
	if v, ok := w.baseProbeCache[repoName]; ok {
		w.baseProbeMu.Unlock()
		return v
	}
	// Reserve the slot so a concurrent failure for the same repo does not
	// start a second probe pod.
	w.baseProbeCache[repoName] = false
	w.baseProbeMu.Unlock()

	imageName := strings.SplitN(prodImage, ":", 2)[0]
	tag := fmt.Sprintf("sentinel-bp%d", time.Now().UnixNano())
	// Empty fixedFiles: the pod clones the BASE branch and builds it as-is.
	_, err := w.kaniko.Build(ctx, repoName, baseBranch, map[string]string{}, imageName, tag, "Dockerfile")

	broken := false
	if err != nil {
		var be *executors.BuildError
		if errors.As(err, &be) && be.Kind == "build" {
			broken = true
		}
	}

	w.baseProbeMu.Lock()
	w.baseProbeCache[repoName] = broken
	w.baseProbeMu.Unlock()

	if broken {
		logging.Error("base-build probe: BASE branch also fails to build — environment rot, not the fix",
			"repo", repoName, "base_branch", baseBranch)
	} else {
		logging.Info("base-build probe: base builds fine — failure is attributable to the fix",
			"repo", repoName, "base_branch", baseBranch)
	}
	return broken
}
