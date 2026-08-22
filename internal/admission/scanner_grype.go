// Package admission — Grype CVE scanner implementation.
//
// Grype is an alternative to Trivy. It's SBOM-first and focused narrowly on
// vulnerability detection (no IaC / secret scanning). Some operators prefer
// it for its speed and accuracy on specific ecosystems.
//
// This implementation shells out to the `grype` binary, which can run
// in-process or as a sidecar.
package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// GrypeScanner scans images using the Grype CLI.
type GrypeScanner struct {
	binaryPath string
	cacheTTL   time.Duration
	cache      sync.Map
}

// NewGrypeScanner creates a Grype scanner.
func NewGrypeScanner(binaryPath string, cacheTTL time.Duration) *GrypeScanner {
	if binaryPath == "" {
		binaryPath = "grype"
	}
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Minute
	}
	return &GrypeScanner{
		binaryPath: binaryPath,
		cacheTTL:   cacheTTL,
	}
}

// ScanImage runs `grype <image> -o json` and parses the result.
func (g *GrypeScanner) ScanImage(ctx context.Context, image string) (CVEScanResult, error) {
	if cached, ok := g.cache.Load(image); ok {
		if cr, ok := cached.(cachedScanResult); ok && time.Now().Before(cr.expiresAt) {
			return cr.result, nil
		}
	}

	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(scanCtx, g.binaryPath, image, "-o", "json")
	output, err := cmd.Output()
	if err != nil {
		if len(output) == 0 {
			return CVEScanResult{}, fmt.Errorf("grype scan failed: %w", err)
		}
		logging.Debug("grype non-zero exit (vulnerabilities found)", "image", image)
	}

	var grypeResp struct {
		Matches []struct {
			Vulnerability struct {
				Severity string `json:"severity"`
				Fix      struct {
					Versions []string `json:"versions"`
				} `json:"fix"`
			} `json:"vulnerability"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(output, &grypeResp); err != nil {
		return CVEScanResult{}, fmt.Errorf("parse grype output: %w", err)
	}

	var result CVEScanResult
	for _, m := range grypeResp.Matches {
		sev := strings.ToUpper(m.Vulnerability.Severity)
		if sev == "CRITICAL" {
			result.CriticalCount++
			if len(m.Vulnerability.Fix.Versions) == 0 {
				result.UnfixedCritical++
			}
		} else if sev == "HIGH" {
			result.HighCount++
		}
	}

	g.cache.Store(image, cachedScanResult{
		result:    result,
		expiresAt: time.Now().Add(g.cacheTTL),
	})

	return result, nil
}
