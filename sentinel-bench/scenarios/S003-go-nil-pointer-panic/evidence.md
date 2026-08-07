# S003 — Go service panics on nil config pointer at startup

## What broke

The `inventory-api` Deployment in the `prod` namespace enters CrashLoopBackOff
within ~5 seconds of being scheduled. The container log shows a Go panic:

```
2025-01-14T03:21:09Z INF starting inventory-api version=v1.2.0
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x8 pc=0x6f4d2a]

goroutine 1 [running]:
main.main()
        /workspace/main.go:48 +0x2a
```

The alert that triggers Sentinel is a `KubePodCrashLooping` page with
`restarts: 11` and `severity: critical`. The annotations mention a Go panic
stack trace.

## Why it broke

The Deployment ships with a ConfigMap (`cluster/configmap.yaml`) that
intentionally omits the `server:` section. `main.go` deserializes the YAML
into a config struct and then dereferences `cfg.Server.Port` without first
checking `cfg.Server == nil`. Because the `server:` section is absent, the
YAML decoder leaves `cfg.Server` as a nil pointer, the dereference panics,
the container exits with SIGSEGV, kubelet restarts it, and after the
configured back-off the pod enters CrashLoopBackOff.

The bug is in the source repo at `main.go`. The cluster ConfigMap that
triggers the panic is part of the scenario setup.

## The fix

Add a nil check on `cfg.Server` before dereferencing. A clean failure with
a helpful message is the right behavior — the service should not panic on
a missing config section, it should refuse to start and log what's wrong.

```diff
--- a/main.go
+++ b/main.go
@@ -42,6 +42,10 @@ func main() {
 	}

 	cfg := loadConfig(configPath)
+	if cfg.Server == nil {
+		log.Fatalf("config: server section is required in %s", configPath)
+	}
+
 	port := cfg.Server.Port
 	srv := http.Server{
 		Addr:    fmt.Sprintf(":%d", port),
```

The fix exits with a non-zero status and a human-readable message; the
container will still restart (no `server:` section means no port to listen
on), but it will fail cleanly instead of producing a stack trace that an
on-call has to decode. In production the ConfigMap would be fixed too, but
the code change is what prevents the panic from recurring on the next
misconfigured deploy.

## Why this is a "moderate" scenario

- The error message (`nil pointer dereference`) tells you *what* panicked
  but not *why*. The agent has to read the stack trace (`main.go:48`),
  open the file, and recognize that `cfg.Server.Port` is the offending
  dereference.
- The fix is 3 lines, but the agent has to choose between several plausible
  fixes (add a nil check, change `loadConfig` to return an error, change
  the ConfigMap, add a default port). The ground truth is the nil check —
  it's the smallest change that turns a panic into a clean error.
- Tests the Go stack-trace parser (which extracts `main.main` at
  `/workspace/main.go:48`) and the LLM's ability to write idiomatic Go
  (`log.Fatalf` with `%s` formatting, not `panic` or `os.Exit(1)`).
- The PR diff must include the nil check AND keep the rest of the function
  intact — a diff_similarity >= 0.80 threshold rejects a fix that
  rewrites the function.

A pattern-matching agent that only knows "nil pointer = add `if x == nil`"
will get partial credit but may fail the diff-similarity gate if it also
rewrites unrelated lines. A real investigation reads the stack trace,
finds line 48, and makes the minimal fix.
