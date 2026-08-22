// Package admission — Trivy CVE scanner implementation.
//
// Trivy is the recommended scanner (broadest coverage: OS packages, language
// dependencies, IaC misconfigurations, secrets). This implementation shells
// out to the `trivy` binary, which can run either:
//   - In-process (Trivy installed in the Sentinel container)
//   - As a sidecar (Trivy in a separate container in the same pod)
//
// The scanner has a TTL-based cache to avoid re-scanning the same image on
// every admission request (Kyverno's pattern).
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

// TrivyScanner scans images using the Trivy CLI.
type TrivyScanner struct {
	binaryPath string // path to trivy binary (default "trivy")
	cacheTTL   time.Duration
	cache      sync.Map // image → cachedResult
}

// NewTrivyScanner creates a Trivy scanner. binaryPath may be empty (defaults
// to "trivy" — must be in PATH). cacheTTL of 0 disables caching.
func NewTrivyScanner(binaryPath string, cacheTTL time.Duration) *TrivyScanner {
	if binaryPath == "" {
		binaryPath = "trivy"
	}
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Minute
	}
	return &TrivyScanner{
		binaryPath: binaryPath,
		cacheTTL:   cacheTTL,
	}
}

type cachedScanResult struct {
	result    CVEScanResult
	expiresAt time.Time
}

// ScanImage runs `trivy image --format json <image>` and parses the result.
func (t *TrivyScanner) ScanImage(ctx context.Context, image string) (CVEScanResult, error) {
	// Check cache.
	if cached, ok := t.cache.Load(image); ok {
		if cr, ok := cached.(cachedScanResult); ok && time.Now().Before(cr.expiresAt) {
			return cr.result, nil
		}
	}

	// Run trivy with a timeout. The scan can take 10-60s depending on image
	// size and whether the DB is warm.
	scanCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(scanCtx, t.binaryPath, "image", "--format", "json", "--quiet", image)
	output, err := cmd.Output()
	if err != nil {
		// Trivy returns non-zero exit when vulnerabilities are found, but
		// still writes valid JSON to stdout. Only treat as error if stdout
		// is empty.
		if len(output) == 0 {
			return CVEScanResult{}, fmt.Errorf("trivy scan failed: %w", err)
		}
		logging.Debug("trivy non-zero exit (vulnerabilities found)", "image", image)
	}

	var trivyResp struct {
		Results []struct {
			Vulnerabilities []struct {
				Severity     string `json:"Severity"`
				FixedVersion string `json:"FixedVersion"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(output, &trivyResp); err != nil {
		return CVEScanResult{}, fmt.Errorf("parse trivy output: %w", err)
	}

	var result CVEScanResult
	for _, r := range trivyResp.Results {
		for _, v := range r.Vulnerabilities {
			sev := strings.ToUpper(v.Severity)
			if sev == "CRITICAL" {
				result.CriticalCount++
				if v.FixedVersion == "" {
					result.UnfixedCritical++
				}
			} else if sev == "HIGH" {
				result.HighCount++
			}
		}
	}

	// Cache the result.
	t.cache.Store(image, cachedScanResult{
		result:    result,
		expiresAt: time.Now().Add(t.cacheTTL),
	})

	return result, nil
}
