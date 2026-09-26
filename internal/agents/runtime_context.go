package agents

// runtime_context.go (v1.7.4): renders the pod's runtime ground truth
// (env var NAMES, config refs, resource limits) into LLM prompts.
//
// Motivation — eval run #11, user-api: the investigation saw a pg-pool
// timeout, guessed a canonical `DATABASE_URL` was "missing", and the fix
// renamed the env var the deployment actually sets (USER_DATABASE_URL),
// which would have pointed the service at localhost. The decisive fact —
// what the pod's environment really provides — was never in the prompt.
// This block puts it there, for both hypothesis-forming and fix-writing.

import (
	"fmt"
	"strings"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// renderRuntimeContext renders the runtime ground truth as a prompt
// section. Returns "" when rc is nil (pod not inspectable) so callers can
// append unconditionally. Values are never rendered — names and refs only.
func renderRuntimeContext(rc *schemas.PodRuntimeContext) string {
	if rc == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Pod Runtime Context (GROUND TRUTH from the live pod — do not contradict)\n")
	fmt.Fprintf(&b, "- pod: %s", rc.PodName)
	if rc.ContainerName != "" {
		fmt.Fprintf(&b, " (container: %s)", rc.ContainerName)
	}
	b.WriteString("\n")
	if rc.Image != "" {
		fmt.Fprintf(&b, "- image: %s\n", rc.Image)
	}
	if len(rc.EnvVarNames) > 0 {
		fmt.Fprintf(&b, "- environment variables SET on this container (names only): %s\n",
			strings.Join(rc.EnvVarNames, ", "))
	} else {
		b.WriteString("- environment variables SET on this container: (none)\n")
	}
	if len(rc.ConfigMapRefs) > 0 {
		fmt.Fprintf(&b, "- configmaps referenced: %s\n", strings.Join(rc.ConfigMapRefs, ", "))
	}
	if len(rc.SecretRefs) > 0 {
		fmt.Fprintf(&b, "- secrets referenced: %s\n", strings.Join(rc.SecretRefs, ", "))
	}
	if rc.CPULimit != "" || rc.MemoryLimit != "" {
		fmt.Fprintf(&b, "- resources (requests cpu=%s mem=%s, limits cpu=%s mem=%s)\n",
			orDash(rc.CPURequest), orDash(rc.MemoryRequest), orDash(rc.CPULimit), orDash(rc.MemoryLimit))
	}
	// Crash vitals (v1.7.16): the failure's physical signature. Restart
	// counts prove recurrence; the last termination reason is the single
	// most decisive localization fact the investigator can get (run #29:
	// five symptom patches, none informed by the OOMKilled signal).
	if rc.RestartCount > 0 || rc.LastTerminationReason != "" || rc.WaitingReason != "" {
		fmt.Fprintf(&b, "- restarts: %d\n", rc.RestartCount)
		if rc.RestartCount > 1 {
			b.WriteString("  (the failure is RECURRING — deterministic, not a one-off blip)\n")
		}
		if rc.LastTerminationReason != "" {
			exit := ""
			if rc.LastTerminationExitCode != 0 {
				exit = fmt.Sprintf(" exit_code=%d", rc.LastTerminationExitCode)
			}
			fmt.Fprintf(&b, "- last termination: reason=%s%s\n", rc.LastTerminationReason, exit)
		}
		if rc.WaitingReason != "" {
			fmt.Fprintf(&b, "- current state: %s\n", rc.WaitingReason)
		}
	}
	if len(rc.Command) > 0 {
		cmd := rc.Command
		if len(cmd) > 3 {
			cmd = cmd[:3]
		}
		fmt.Fprintf(&b, "- entrypoint: %s\n", strings.Join(cmd, " "))
	}
	b.WriteString("\nUse this when a hypothesis involves configuration, environment variables,\n")
	b.WriteString("connection strings, or resources. An env var listed here IS set; one that is\n")
	b.WriteString("NOT listed here is NOT set on the pod. Never assume a canonical variable name\n")
	b.WriteString("(e.g. DATABASE_URL) — use the exact names above.\n")
	if rc.OOMKilled {
		b.WriteString("\nOOM EVIDENCE: the kernel killed this container for exceeding its memory\n")
		b.WriteString("limit. Memory grew until the cap — the traceback line is where allocation\n")
		b.WriteString("finally failed (a VICTIM, not the cause). The root cause is data that only\n")
		b.WriteString("grows: a module-level Map/Set/dict/list, a cache without TTL/eviction, tokens\n")
		b.WriteString("or sessions that are never removed. Adding validation or guards at the\n")
		b.WriteString("crash line will NOT stop an OOM; bounding the growth will.\n")
	}
	return b.String()
}

// runtimeContextGroundingRules returns the extra warning lines appended in
// the Fix Proposer's prompt, where a wrong env-var assumption turns into a
// harmful patch.
func runtimeContextGroundingRules(rc *schemas.PodRuntimeContext) string {
	if rc == nil || len(rc.EnvVarNames) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("IMPORTANT: The environment variables above are the ACTUAL names the deployment\n")
	b.WriteString("provides. When your fix reads an env var, it MUST use one of those exact names.\n")
	b.WriteString("Do NOT rename env var references in code to canonical/conventional names —\n")
	b.WriteString("that breaks the service against its real deployment configuration.\n")
	return b.String()
}

// RenderRuntimeContext is the exported form of renderRuntimeContext, used
// by the agent-loop package (internal/agent) for its system prompt.
func RenderRuntimeContext(rc *schemas.PodRuntimeContext) string {
	return renderRuntimeContext(rc)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
