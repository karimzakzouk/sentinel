// Sentinel — Mock data for visual prototype. Realistic SRE-shaped data.

export type IncidentStatus =
  | "investigating"
  | "verified"
  | "failed"
  | "escalated"
  | "resolved";

export type SandboxLevelStatus = "pass" | "fail" | "pending" | "skip";

export interface Incident {
  id: string;
  service: string;
  namespace: string;
  status: IncidentStatus;
  errorType: string;
  confidence: number; // 0-100
  prNumber: number | null;
  timeToFix: string | null;
  created: string; // ISO
  crashFile: string;
  crashLine: number;
  crashFunction: string;
  errorExcerpt: string;
  rootCause: string;
  rootCauseConfidence: number;
  hypotheses: {
    id: string;
    statement: string;
    confidence: number;
    falsified: boolean;
  }[];
  fixProposal: {
    description: string;
    filesChanged: {
      path: string;
      additions: number;
      deletions: number;
      diff: string;
    }[];
  };
  sandboxLevels: SandboxLevelStatus[];
  agentTrace: AgentNode;
  events: { time: string; phase: string; message: string }[];
}

export interface AgentNode {
  id: string;
  name: string;
  agent: string;
  duration: string;
  tokens: number;
  cost: number;
  status: "completed" | "running" | "failed";
  llmInput?: string;
  llmOutput?: string;
  children?: AgentNode[];
}

export interface Provider {
  name: string;
  priority: number;
  status: "healthy" | "tripped" | "degraded";
  model: string;
  circuitState: "closed" | "open" | "half-open";
  calls: number;
  successRate: number;
  avgLatency: number; // ms
  trips: number;
  inputTokens: number;
  outputTokens: number;
  cost: number;
}

export interface PullRequest {
  pr: number;
  repo: string;
  title: string;
  status: "open" | "merged" | "closed" | "pending";
  sandboxVerified: boolean;
  autoMerge: "merged" | "queued" | "blocked" | "reverted" | "skipped";
  opened: string;
  merged: string | null;
  gates: { name: string; status: "pass" | "fail" | "pending" }[];
  incidentId: string;
}

export interface SandboxRun {
  id: string;
  incidentId: string;
  service: string;
  levels: SandboxLevelStatus[];
  duration: string;
  infraError: string | null;
  timestamp: string;
  canaryImage: string;
  namespace: string;
  namespaceAge: string;
  levelDetails: {
    level: number;
    name: string;
    status: SandboxLevelStatus;
    podLogs: string[];
    probeResults: { name: string; status: "pass" | "fail" }[];
    testOutput: string;
    reproducerOutput?: string;
  }[];
  retries: { attempt: number; feedback: string; time: string }[];
}

export interface ChatopsCommand {
  command: string;
  syntax: string;
  description: string;
  permission: "Read" | "Write" | "Destructive";
}

export interface ChatopsAuditEntry {
  user: string;
  command: string;
  args: string;
  time: string;
  result: "success" | "error" | "denied";
}

export interface ApiEndpoint {
  method: "GET" | "POST" | "PUT" | "DELETE" | "PATCH";
  path: string;
  description: string;
  auth: boolean;
  params?: { name: string; type: string; required: boolean; description: string }[];
  body?: string;
  response: string;
}

const now = Date.now();
const iso = (minutesAgo: number) => new Date(now - minutesAgo * 60_000).toISOString();

export const incidents: Incident[] = [
  {
    id: "INC-7A3F9B2C",
    service: "checkout-api",
    namespace: "payments-prod",
    status: "investigating",
    errorType: "KeyError",
    confidence: 87,
    prNumber: null,
    timeToFix: null,
    created: iso(4),
    crashFile: "app/services/pricing.py",
    crashLine: 142,
    crashFunction: "compute_totals",
    errorExcerpt: `Traceback (most recent call last):
  File "app/services/pricing.py", line 142, in compute_totals
    tax_rate = region_meta["tax_rate"]
KeyError: 'tax_rate'

During handling of the above exception, another exception occurred:
  File "app/api/checkout.py", line 88, in post
    order = await checkout_service.create(payload)
  File "app/services/checkout.py", line 211, in create
    raise PricingError("missing region metadata")`,
    rootCause:
      "Region metadata cache was evicted under memory pressure; downstream consumers assume the key always exists. The pricing service does not handle cache misses gracefully.",
    rootCauseConfidence: 91,
    hypotheses: [
      { id: "H1", statement: "Region metadata cache evicted under memory pressure", confidence: 91, falsified: false },
      { id: "H2", statement: "Upstream region service returned 503 causing cache miss", confidence: 22, falsified: true },
      { id: "H3", statement: "Schema migration dropped tax_rate column", confidence: 8, falsified: true },
      { id: "H4", statement: "Network partition between pricing and region pods", confidence: 14, falsified: true },
      { id: "H5", statement: "Bad deploy introduced default value removal", confidence: 33, falsified: false },
    ],
    fixProposal: {
      description:
        "Add a defensive cache-miss handler with a fallback to the region service, plus a circuit breaker around region_meta lookups. Adds structured logging for cache miss events.",
      filesChanged: [
        {
          path: "app/services/pricing.py",
          additions: 28,
          deletions: 4,
          diff: `@@ -138,12 +138,36 @@
 def compute_totals(cart, region_meta):
-    tax_rate = region_meta["tax_rate"]
-    subtotal = sum(item.price for item in cart.items)
-    return subtotal * (1 + tax_rate)
+    if not region_meta or "tax_rate" not in region_meta:
+        logger.warning("region_meta miss", extra={"region": cart.region})
+        region_meta = region_client.get(cart.region, fallback=True)
+        if region_meta is None:
+            raise PricingError("region unavailable", region=cart.region)
+    tax_rate = region_meta["tax_rate"]
+    subtotal = sum(item.price for item in cart.items)
+    return {
+        "subtotal": subtotal,
+        "tax": subtotal * tax_rate,
+        "total": subtotal * (1 + tax_rate),
+    }`,
        },
        {
          path: "app/services/region_client.py",
          additions: 14,
          deletions: 0,
          diff: `@@ -0,0 +1,14 @@
+class RegionClient:
+    def __init__(self, cache, breaker):
+        self._cache = cache
+        self._breaker = breaker
+
+    def get(self, region, fallback=False):
+        if self._breaker.open:
+            return self._cache.get_stale(region) if fallback else None
+        try:
+            return self._cache.get_or_set(region, lambda: self._fetch(region))
+        except UpstreamError:
+            self._breaker.record_failure()
+            return self._cache.get_stale(region) if fallback else None`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pass", "pending", "pending"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "4m 12s",
      tokens: 18420,
      cost: 0.027,
      status: "running",
      children: [
        {
          id: "n1",
          name: "Log Analysis",
          agent: "evidence-collector",
          duration: "1m 08s",
          tokens: 9120,
          cost: 0.011,
          status: "completed",
          llmInput: "Analyze pod logs from checkout-api-7d4f for KeyError traces in the last 30m...",
          llmOutput: `Found 47 instances of KeyError: 'tax_rate' across 3 pods.
Pattern: all errors occur in compute_totals() at pricing.py:142.
First occurrence: 2026-07-02T16:38:12Z
Last occurrence: 2026-07-02T16:42:51Z
Affected regions: us-east-1, eu-west-1, ap-southeast-2
Correlated events:
  - region-cache eviction at 16:37:58
  - pod memory pressure warning at 16:35:00
Recommend: investigate cache layer and region_meta population path.`,
          children: [
            {
              id: "n1a",
              name: "StackTrace Parse",
              agent: "evidence-collector",
              duration: "0m 18s",
              tokens: 1840,
              cost: 0.002,
              status: "completed",
              llmInput: "Parse stacktrace JSON from alertmanager webhook payload",
              llmOutput: 'Parsed: KeyError at pricing.py:142 in compute_totals. Frame chain depth=4.',
            },
          ],
        },
        {
          id: "n2",
          name: "Hypothesis Generation",
          agent: "analyst",
          duration: "1m 47s",
          tokens: 14320,
          cost: 0.019,
          status: "completed",
          llmInput: "Generate ranked hypotheses for KeyError: 'tax_rate' given evidence bundle EB-7A3F9B2C",
          llmOutput: `Generated 5 hypotheses:
H1 (0.91): Region metadata cache evicted under memory pressure
H2 (0.22): Upstream region service returned 503
H3 (0.08): Schema migration dropped column
H4 (0.14): Network partition
H5 (0.33): Bad deploy removed default

Confidence via Bayesian update with prior weight=0.3.
Falsified H2-H4 against pod logs and metrics.`,
          children: [],
        },
        {
          id: "n3",
          name: "Fix Proposal",
          agent: "fix-proposer",
          duration: "0m 57s",
          tokens: 11200,
          cost: 0.018,
          status: "running",
          llmInput: "Propose fix for H1 (cache eviction). Constraint: must not break existing API contract.",
          llmOutput: "Generating patch...",
        },
      ],
    },
    events: [
      { time: iso(4), phase: "Detection", message: "CrashLoopBackOff detected on checkout-api-7d4f" },
      { time: iso(4), phase: "Detection", message: "Alertmanager webhook received (fingerprint=7a3f9b2c)" },
      { time: iso(3), phase: "Investigation", message: "Investigator agent spawned" },
      { time: iso(3), phase: "Investigation", message: "Evidence collection: pod logs, metrics, deploy history" },
      { time: iso(2), phase: "Analysis", message: "5 hypotheses generated, 3 falsified" },
      { time: iso(1), phase: "Governance", message: "Confidence 0.91 ≥ autonomous threshold 0.85" },
      { time: iso(0), phase: "Execution", message: "Fix proposal in progress..." },
    ],
  },
  {
    id: "INC-2B8E1D7A",
    service: "auth-service",
    namespace: "identity-prod",
    status: "investigating",
    errorType: "panic: nil pointer dereference",
    confidence: 64,
    prNumber: null,
    timeToFix: null,
    created: iso(9),
    crashFile: "src/handler/oidc.go",
    crashLine: 411,
    crashFunction: "handleCallback",
    errorExcerpt: `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x18 pc=0x1a3f4c8]

goroutine 247 [running]:
main.handleCallback(...)
    /workspace/src/handler/oidc.go:411 +0x4a8
main.(*Server).serveOIDC(...)
    /workspace/src/handler/oidc.go:88 +0x1c0
net/http.(*ServeMux).ServeHTTP(...)
    /usr/local/go/src/net/http/server.go:2536 +0x1d4`,
    rootCause:
      "Provider config struct is nil when the OIDC callback handler is invoked before the initialization goroutine completes. Race condition between lazy provider registration and the first inbound callback.",
    rootCauseConfidence: 68,
    hypotheses: [
      { id: "H1", statement: "Provider config nil due to lazy init race", confidence: 68, falsified: false },
      { id: "H2", statement: "Bad config rollout removed provider entry", confidence: 41, falsified: false },
      { id: "H3", statement: "Memory corruption from cgo bindings", confidence: 4, falsified: true },
      { id: "H4", statement: "Goroutine leak exhausting stack", confidence: 11, falsified: true },
      { id: "H5", statement: "Provider registry never loaded for new region", confidence: 47, falsified: false },
    ],
    fixProposal: {
      description: "Add nil-check guard with sync.Once-based provider initialization. Returns 503 with retry-after when providers not yet loaded.",
      filesChanged: [
        {
          path: "src/handler/oidc.go",
          additions: 22,
          deletions: 3,
          diff: `@@ -408,9 +408,28 @@
 func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
+    s.providersOnce.Do(s.loadProviders)
+    if s.providers == nil {
+        w.Header().Set("Retry-After", "2")
+        http.Error(w, "providers not loaded", http.StatusServiceUnavailable)
+        return
+    }
     provider := s.providers.Get(r.URL.Query().Get("p"))
-    if provider == nil {
-        panic("provider not found")
-    }
+    if provider == nil {
+        logger.Error("provider missing", "provider", r.URL.Query().Get("p"))
+        http.Error(w, "unknown provider", http.StatusBadRequest)
+        return
+    }
     token, err := provider.Exchange(r.Context(), r.URL.Query().Get("code"))`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pending", "pending", "pending"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "8m 41s",
      tokens: 22180,
      cost: 0.034,
      status: "running",
      children: [
        {
          id: "n1",
          name: "Goroutine Dump Analysis",
          agent: "evidence-collector",
          duration: "2m 14s",
          tokens: 12400,
          cost: 0.015,
          status: "completed",
          llmInput: "Analyze pprof goroutine dump from auth-service-5x2k. Look for races in OIDC handler.",
          llmOutput: "Identified 3 goroutines in handleCallback with nil provider config. Race window: 0-200ms after pod start.",
          children: [],
        },
      ],
    },
    events: [
      { time: iso(9), phase: "Detection", message: "panic detected in auth-service logs" },
      { time: iso(9), phase: "Detection", message: "Pod restarted (CrashLoopBackOff, 7 restarts)" },
      { time: iso(8), phase: "Investigation", message: "Investigator agent spawned" },
      { time: iso(6), phase: "Analysis", message: "Confidence 0.68 — below autonomous threshold" },
      { time: iso(5), phase: "Governance", message: "Holding for human review (15m window)" },
      { time: iso(2), phase: "Investigation", message: "Additional evidence collected: deploy diff" },
    ],
  },
  {
    id: "INC-9C1F4E8D",
    service: "search-indexer",
    namespace: "search-prod",
    status: "verified",
    errorType: "TypeError",
    confidence: 93,
    prNumber: 4821,
    timeToFix: "12m 04s",
    created: iso(74),
    crashFile: "src/indexer/pipeline.ts",
    crashLine: 89,
    crashFunction: "transformDocument",
    errorExcerpt: `TypeError: Cannot read properties of undefined (reading 'toLowerCase')
    at transformDocument (/app/src/indexer/pipeline.ts:89:34)
    at Pipeline.process (/app/src/indexer/pipeline.ts:42:18)
    at IndexerWorker.handle (/app/src/indexer/worker.ts:124:22)
    at MessagePort.<anonymous> (/app/src/indexer/worker.ts:58:42)`,
    rootCause:
      "Documents from the legacy feed do not include a 'title' field. The transform pipeline assumes title is always present and calls .toLowerCase() without a null check.",
    rootCauseConfidence: 95,
    hypotheses: [
      { id: "H1", statement: "Legacy feed omits title field", confidence: 95, falsified: false },
      { id: "H2", statement: "Schema migration broke title extraction", confidence: 12, falsified: true },
      { id: "H3", statement: "Search adapter returns wrong shape", confidence: 18, falsified: true },
      { id: "H4", statement: "JSON parse drops field on encoding", confidence: 6, falsified: true },
      { id: "H5", statement: "Worker pool deserialization bug", confidence: 9, falsified: true },
    ],
    fixProposal: {
      description: "Add optional chaining and fallback to empty string for missing title field. Adds a schema validator that logs malformed documents.",
      filesChanged: [
        {
          path: "src/indexer/pipeline.ts",
          additions: 12,
          deletions: 2,
          diff: `@@ -86,8 +86,18 @@
 function transformDocument(doc: RawDocument): IndexedDocument {
-  const title = doc.title.toLowerCase();
-  const body = doc.body ?? "";
+  const title = (doc.title ?? "").toString().toLowerCase();
+  const body = doc.body ?? "";
+  if (!doc.title) {
+    logger.warn("document missing title", { id: doc.id, source: doc.source });
+    metrics.increment("indexer.missing_title", { source: doc.source });
+  }
   return {
     id: doc.id,
     title,
     body,
     tokens: tokenize(title + " " + body),
   };
 }`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "9m 22s",
      tokens: 19840,
      cost: 0.029,
      status: "completed",
      children: [
        {
          id: "n1",
          name: "Log Analysis",
          agent: "evidence-collector",
          duration: "1m 52s",
          tokens: 8200,
          cost: 0.009,
          status: "completed",
          llmInput: "Analyze TypeError traces in search-indexer logs.",
          llmOutput: "318 occurrences in last 60m. All from legacy-feed source. Field 'title' undefined in 100% of cases.",
          children: [],
        },
      ],
    },
    events: [
      { time: iso(74), phase: "Detection", message: "TypeError spike detected" },
      { time: iso(72), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(68), phase: "Analysis", message: "5 hypotheses generated" },
      { time: iso(65), phase: "Governance", message: "Confidence 0.95 — autonomous" },
      { time: iso(63), phase: "Execution", message: "Patch generated" },
      { time: iso(62), phase: "Sandbox", message: "L1-L5 verification passed" },
      { time: iso(61), phase: "PR", message: "PR #4821 opened" },
      { time: iso(58), phase: "Auto-Merge", message: "All gates passed, queued for merge" },
      { time: iso(58), phase: "Auto-Merge", message: "PR #4821 merged to main" },
    ],
  },
  {
    id: "INC-4D7E2A1F",
    service: "notifications-worker",
    namespace: "notify-prod",
    status: "verified",
    errorType: "ZeroDivisionError",
    confidence: 96,
    prNumber: 4818,
    timeToFix: "8m 47s",
    created: iso(142),
    crashFile: "app/workers/sms_router.py",
    crashLine: 67,
    crashFunction: "compute_split_cost",
    errorExcerpt: `Traceback (most recent call last):
  File "app/workers/sms_router.py", line 67, in compute_split_cost
    per_segment = total_cost / segment_count
ZeroDivisionError: division by zero`,
    rootCause:
      "Segment count is computed from message length divided by 160, but for empty messages (unsubscribe confirmations) this yields 0. No guard around the division.",
    rootCauseConfidence: 97,
    hypotheses: [
      { id: "H1", statement: "Empty message yields segment_count=0", confidence: 97, falsified: false },
      { id: "H2", statement: "Encoding mismatch inflates length calc", confidence: 8, falsified: true },
      { id: "H3", statement: "Unicode chars counted as single byte", confidence: 4, falsified: true },
      { id: "H4", statement: "Carrier API returns 0 segments", confidence: 14, falsified: true },
      { id: "H5", statement: "Off-by-one in segment math", confidence: 11, falsified: true },
    ],
    fixProposal: {
      description: "Guard against zero segments by treating empty messages as a single segment with zero cost.",
      filesChanged: [
        {
          path: "app/workers/sms_router.py",
          additions: 8,
          deletions: 1,
          diff: `@@ -64,7 +64,14 @@
 def compute_split_cost(message: str, total_cost: float) -> float:
-    segment_count = max(1, len(message) // 160)
+    if not message:
+        # Empty messages (e.g. unsubscribe confirmations) consume no segments
+        # but still incur a fixed carrier fee of $0.005.
+        logger.info("empty message, applying fixed fee", extra={"cost": 0.005})
+        return 0.005
+    segment_count = max(1, math.ceil(len(message) / 160))
     per_segment = total_cost / segment_count
     return per_segment`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "6m 18s",
      tokens: 14200,
      cost: 0.021,
      status: "completed",
      children: [],
    },
    events: [
      { time: iso(142), phase: "Detection", message: "ZeroDivisionError detected" },
      { time: iso(140), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(135), phase: "Analysis", message: "Confidence 0.97" },
      { time: iso(133), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(133), phase: "PR", message: "PR #4818 opened" },
      { time: iso(133), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-1E5A8B3C",
    service: "billing-cron",
    namespace: "billing-prod",
    status: "failed",
    errorType: "AttributeError",
    confidence: 42,
    prNumber: null,
    timeToFix: null,
    created: iso(211),
    crashFile: "app/jobs/reconcile.py",
    crashLine: 233,
    crashFunction: "reconcile_invoice",
    errorExcerpt: `Traceback (most recent call last):
  File "app/jobs/reconcile.py", line 233, in reconcile_invoice
    invoice.customer.email = invoice.customer.contact.email
AttributeError: 'NoneType' object has no attribute 'email'`,
    rootCause:
      "Customer contact is None for legacy accounts created before the contact table was introduced. Investigation did not reach autonomous threshold.",
    rootCauseConfidence: 48,
    hypotheses: [
      { id: "H1", statement: "Legacy accounts lack contact rows", confidence: 48, falsified: false },
      { id: "H2", statement: "ORM session mismatch causing lazy load failure", confidence: 28, falsified: false },
      { id: "H3", statement: "Schema migration partial failure", confidence: 18, falsified: false },
      { id: "H4", statement: "Foreign key constraint disabled", confidence: 6, falsified: true },
      { id: "H5", statement: "Soft-deleted customer reappearing in queries", confidence: 22, falsified: false },
    ],
    fixProposal: {
      description: "Insufficient confidence to propose autonomous fix. Escalated to human review queue.",
      filesChanged: [],
    },
    sandboxLevels: ["skip", "skip", "skip", "skip", "skip"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "11m 04s",
      tokens: 24800,
      cost: 0.041,
      status: "failed",
      children: [],
    },
    events: [
      { time: iso(211), phase: "Detection", message: "AttributeError in billing-cron" },
      { time: iso(208), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(200), phase: "Analysis", message: "Confidence 0.48 — below threshold" },
      { time: iso(198), phase: "Governance", message: "Escalated to human review" },
    ],
  },
  {
    id: "INC-8F2D6B9E",
    service: "image-resizer",
    namespace: "media-prod",
    status: "escalated",
    errorType: "panic: slice out of range",
    confidence: 38,
    prNumber: null,
    timeToFix: null,
    created: iso(289),
    crashFile: "src/transform/crop.go",
    crashLine: 154,
    crashFunction: "cropToAspect",
    errorExcerpt: `panic: runtime error: slice bounds out of range [:12000] with capacity 8192

goroutine 91 [running]:
main.cropToAspect(...)
    /workspace/src/transform/crop.go:154 +0x2c0
main.processImage(...)
    /workspace/src/transform/pipeline.go:72 +0x140`,
    rootCause:
      "EXIF metadata reports an image dimension larger than the actual pixel buffer. Could not determine if this is a parser bug or a malicious input pattern.",
    rootCauseConfidence: 41,
    hypotheses: [
      { id: "H1", statement: "EXIF dimension mismatch with pixel buffer", confidence: 41, falsified: false },
      { id: "H2", statement: "Malicious crafted image", confidence: 35, falsified: false },
      { id: "H3", statement: "Decoder returns wrong capacity", confidence: 28, falsified: false },
      { id: "H4", statement: "Memory corruption in decoder", confidence: 12, falsified: true },
      { id: "H5", statement: "Concurrent map write corrupting buffer", confidence: 9, falsified: true },
    ],
    fixProposal: {
      description: "Escalated. Awaiting human input on whether to treat as security incident.",
      filesChanged: [],
    },
    sandboxLevels: ["skip", "skip", "skip", "skip", "skip"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "13m 41s",
      tokens: 28100,
      cost: 0.048,
      status: "failed",
      children: [],
    },
    events: [
      { time: iso(289), phase: "Detection", message: "panic in image-resizer" },
      { time: iso(285), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(278), phase: "Analysis", message: "Confidence 0.41" },
      { time: iso(275), phase: "Governance", message: "Escalated — possible security incident" },
    ],
  },
  {
    id: "INC-3A7C1E5D",
    service: "checkout-api",
    namespace: "payments-prod",
    status: "resolved",
    errorType: "ConnectionError",
    confidence: 94,
    prNumber: 4812,
    timeToFix: "18m 22s",
    created: iso(432),
    crashFile: "app/db/pool.py",
    crashLine: 88,
    crashFunction: "acquire",
    errorExcerpt: `ConnectionError: connection pool exhausted, no connection available within 5s timeout
    at acquire (/app/app/db/pool.py:88)
    at checkout (/app/app/services/checkout.py:201)
    at handler (/app/app/api/checkout.py:42)`,
    rootCause:
      "Connection pool size was set to 10 but new ingress load requires 50. No autoscaling on the pool.",
    rootCauseConfidence: 92,
    hypotheses: [
      { id: "H1", statement: "Pool size too small for ingress load", confidence: 92, falsified: false },
      { id: "H2", statement: "Connection leak in transaction handling", confidence: 18, falsified: true },
      { id: "H3", statement: "DB instance overloaded", confidence: 12, falsified: true },
      { id: "H4", statement: "Network latency causing slow returns", confidence: 8, falsified: true },
      { id: "H5", statement: "Idle timeout too aggressive", confidence: 14, falsified: true },
    ],
    fixProposal: {
      description: "Increase pool size from 10 to 50, add dynamic resizing based on queue depth.",
      filesChanged: [
        {
          path: "app/db/pool.py",
          additions: 18,
          deletions: 4,
          diff: `@@ -85,10 +85,24 @@
-def acquire():
-    return self._pool.get(timeout=5)
+def acquire():
+    self._maybe_resize()
+    return self._pool.get(timeout=5)
+
+def _maybe_resize(self):
+    depth = self._pool.qsize()
+    waiters = self._pool._waiters
+    if len(waiters) > 2 and len(self._all) < self._max:
+        self._grow(min(self._max, len(self._all) + 10))`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "14m 02s",
      tokens: 22100,
      cost: 0.033,
      status: "completed",
      children: [],
    },
    events: [
      { time: iso(432), phase: "Detection", message: "ConnectionError spike" },
      { time: iso(430), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(425), phase: "Analysis", message: "Confidence 0.92" },
      { time: iso(423), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(422), phase: "PR", message: "PR #4812 opened" },
      { time: iso(420), phase: "Auto-Merge", message: "Merged" },
      { time: iso(414), phase: "Detection", message: "Error rate returned to baseline" },
    ],
  },
  {
    id: "INC-6B9D4F2A",
    service: "search-indexer",
    namespace: "search-prod",
    status: "resolved",
    errorType: "OutOfMemoryError",
    confidence: 89,
    prNumber: 4805,
    timeToFix: "24m 11s",
    created: iso(612),
    crashFile: "src/indexer/batch.ts",
    crashLine: 42,
    crashFunction: "processBatch",
    errorExcerpt: `FATAL ERROR: Ineffective mark-compacts near heap limit Allocation failed - JavaScript heap out of memory
    at processBatch (src/indexer/batch.ts:42)`,
    rootCause:
      "Batch size was set to 10000 documents but large PDFs inflate memory per-document. No memory pressure check before processing.",
    rootCauseConfidence: 88,
    hypotheses: [
      { id: "H1", statement: "Batch size too large for PDF-heavy feeds", confidence: 88, falsified: false },
      { id: "H2", statement: "Memory leak in PDF parser", confidence: 22, falsified: true },
      { id: "H3", statement: "V8 heap limit too low", confidence: 14, falsified: true },
      { id: "H4", statement: "Stream not being garbage collected", confidence: 18, falsified: true },
      { id: "H5", statement: "Worker count too high", confidence: 11, falsified: true },
    ],
    fixProposal: {
      description: "Dynamic batch sizing based on document type, capped at 4MB total payload.",
      filesChanged: [
        {
          path: "src/indexer/batch.ts",
          additions: 24,
          deletions: 5,
          diff: `@@ -39,11 +39,30 @@
-async function processBatch(docs: Document[]) {
-  for (const doc of docs) {
-    await transform(doc);
-  }
-}
+async function processBatch(docs: Document[]) {
+  const chunks = chunkByPayloadSize(docs, MAX_PAYLOAD_BYTES);
+  for (const chunk of chunks) {
+    if (process.memoryUsage().heapUsed > HEAP_CRITICAL) {
+      global.gc?.();
+      await sleep(100);
+    }
+    for (const doc of chunk) {
+      await transform(doc);
+    }
+  }
+}`,
        },
      ],
    },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: {
      id: "n0",
      name: "Root Investigation",
      agent: "investigator",
      duration: "18m 33s",
      tokens: 26400,
      cost: 0.039,
      status: "completed",
      children: [],
    },
    events: [
      { time: iso(612), phase: "Detection", message: "OOMKilled" },
      { time: iso(610), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(602), phase: "Analysis", message: "Confidence 0.88" },
      { time: iso(598), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(597), phase: "PR", message: "PR #4805 opened" },
      { time: iso(588), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-5C1F8A3E",
    service: "auth-service",
    namespace: "identity-prod",
    status: "resolved",
    errorType: "panic: concurrent map writes",
    confidence: 91,
    prNumber: 4801,
    timeToFix: "15m 47s",
    created: iso(821),
    crashFile: "src/store/cache.go",
    crashLine: 78,
    crashFunction: "Set",
    errorExcerpt: `fatal error: concurrent map writes

goroutine 142 [running]:
main.(*Cache).Set(...)
    /workspace/src/store/cache.go:78 +0x120`,
    rootCause:
      "Cache map is accessed from multiple goroutines without a mutex. The fix introduces sync.Map.",
    rootCauseConfidence: 94,
    hypotheses: [
      { id: "H1", statement: "Unsynchronized map access across goroutines", confidence: 94, falsified: false },
      { id: "H2", statement: "Race in cache invalidation", confidence: 24, falsified: true },
      { id: "H3", statement: "Map iteration during write", confidence: 14, falsified: true },
      { id: "H4", statement: "Goroutine leak corrupting map header", confidence: 6, falsified: true },
      { id: "H5", statement: "Memory model violation from cgo", confidence: 4, falsified: true },
    ],
    fixProposal: { description: "Replace map with sync.Map.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "12m 18s", tokens: 18900, cost: 0.028, status: "completed", children: [] },
    events: [
      { time: iso(821), phase: "Detection", message: "panic: concurrent map writes" },
      { time: iso(819), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(814), phase: "Analysis", message: "Confidence 0.94" },
      { time: iso(811), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(810), phase: "PR", message: "PR #4801 opened" },
      { time: iso(805), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-7D2E9B4F",
    service: "billing-cron",
    namespace: "billing-prod",
    status: "resolved",
    errorType: "KeyError",
    confidence: 90,
    prNumber: 4798,
    timeToFix: "10m 02s",
    created: iso(1102),
    crashFile: "app/jobs/scheduler.py",
    crashLine: 51,
    crashFunction: "next_run",
    errorExcerpt: `Traceback (most recent call last):
  File "app/jobs/scheduler.py", line 51, in next_run
    return config["schedule"]["interval"]
KeyError: 'interval'`,
    rootCause: "Schedule config schema changed; legacy entries missing 'interval' key.",
    rootCauseConfidence: 90,
    hypotheses: [
      { id: "H1", statement: "Schema migration removed default interval", confidence: 90, falsified: false },
      { id: "H2", statement: "Bad deploy removed key", confidence: 18, falsified: true },
      { id: "H3", statement: "Config source changed", confidence: 12, falsified: true },
      { id: "H4", statement: "YAML parse dropping key", confidence: 8, falsified: true },
      { id: "H5", statement: "Validator allowing missing keys", confidence: 22, falsified: false },
    ],
    fixProposal: { description: "Add default interval with schema validator.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "8m 11s", tokens: 13200, cost: 0.019, status: "completed", children: [] },
    events: [
      { time: iso(1102), phase: "Detection", message: "KeyError in scheduler" },
      { time: iso(1100), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(1094), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(1092), phase: "PR", message: "PR #4798 opened" },
      { time: iso(1092), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-2E8A1F5C",
    service: "notifications-worker",
    namespace: "notify-prod",
    status: "resolved",
    errorType: "TypeError",
    confidence: 92,
    prNumber: 4795,
    timeToFix: "11m 36s",
    created: iso(1488),
    crashFile: "app/workers/email_router.py",
    crashLine: 41,
    crashFunction: "render_template",
    errorExcerpt: `TypeError: unsupported operand type(s) for +: 'NoneType' and 'str'
    at render_template (app/workers/email_router.py:41)`,
    rootCause: "Template variable not initialized for unsubscribe flow.",
    rootCauseConfidence: 93,
    hypotheses: [
      { id: "H1", statement: "Template var not initialized", confidence: 93, falsified: false },
      { id: "H2", statement: "Jinja context missing key", confidence: 14, falsified: true },
      { id: "H3", statement: "Wrong template loaded", confidence: 8, falsified: true },
      { id: "H4", statement: "Filter chain returns None", confidence: 18, falsified: true },
      { id: "H5", statement: "Locale fallback returns None", confidence: 22, falsified: false },
    ],
    fixProposal: { description: "Initialize template var with empty string.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "9m 22s", tokens: 15100, cost: 0.022, status: "completed", children: [] },
    events: [
      { time: iso(1488), phase: "Detection", message: "TypeError in email router" },
      { time: iso(1486), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(1480), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(1478), phase: "PR", message: "PR #4795 opened" },
      { time: iso(1476), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-9F3D7B1E",
    service: "image-resizer",
    namespace: "media-prod",
    status: "resolved",
    errorType: "panic: nil pointer dereference",
    confidence: 88,
    prNumber: 4791,
    timeToFix: "16m 28s",
    created: iso(1874),
    crashFile: "src/transform/resize.go",
    crashLine: 102,
    crashFunction: "resize",
    errorExcerpt: `panic: runtime error: invalid memory address or nil pointer dereference
    at resize (src/transform/resize.go:102)`,
    rootCause: "Source image not loaded before transform when called from worker pool.",
    rootCauseConfidence: 89,
    hypotheses: [
      { id: "H1", statement: "Image not loaded before transform", confidence: 89, falsified: false },
      { id: "H2", statement: "Worker pool dispatch bug", confidence: 18, falsified: true },
      { id: "H3", statement: "Decoder returns nil on truncated input", confidence: 28, falsified: false },
      { id: "H4", statement: "Channel race on dispatch", confidence: 14, falsified: true },
      { id: "H5", statement: "Context cancellation returns nil", confidence: 22, falsified: false },
    ],
    fixProposal: { description: "Add nil check after load.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "13m 12s", tokens: 19700, cost: 0.029, status: "completed", children: [] },
    events: [
      { time: iso(1874), phase: "Detection", message: "panic in resizer" },
      { time: iso(1872), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(1866), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(1864), phase: "PR", message: "PR #4791 opened" },
      { time: iso(1858), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-4A6E2D8F",
    service: "checkout-api",
    namespace: "payments-prod",
    status: "resolved",
    errorType: "TimeoutError",
    confidence: 86,
    prNumber: 4788,
    timeToFix: "13m 51s",
    created: iso(2160),
    crashFile: "app/services/payment.py",
    crashLine: 71,
    crashFunction: "charge",
    errorExcerpt: `TimeoutError: Payment gateway did not respond within 10s
    at charge (app/services/payment.py:71)`,
    rootCause: "Gateway timeout too aggressive for new 3DS flow.",
    rootCauseConfidence: 87,
    hypotheses: [
      { id: "H1", statement: "3DS flow takes longer than timeout", confidence: 87, falsified: false },
      { id: "H2", statement: "Network latency to gateway", confidence: 22, falsified: true },
      { id: "H3", statement: "Gateway degradation", confidence: 18, falsified: true },
      { id: "H4", statement: "Connection pool exhaustion", confidence: 12, falsified: true },
      { id: "H5", statement: "DNS resolution slow", confidence: 8, falsified: true },
    ],
    fixProposal: { description: "Increase timeout to 30s for 3DS flow.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "11m 09s", tokens: 16800, cost: 0.024, status: "completed", children: [] },
    events: [
      { time: iso(2160), phase: "Detection", message: "TimeoutError spike" },
      { time: iso(2158), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(2152), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(2150), phase: "PR", message: "PR #4788 opened" },
      { time: iso(2146), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-1B5F3A7C",
    service: "search-indexer",
    namespace: "search-prod",
    status: "resolved",
    errorType: "ConnectionRefusedError",
    confidence: 91,
    prNumber: 4782,
    timeToFix: "9m 14s",
    created: iso(2520),
    crashFile: "src/indexer/worker.ts",
    crashLine: 88,
    crashFunction: "connect",
    errorExcerpt: `ConnectionRefusedError: connect ECONNREFUSED 10.0.4.21:9200
    at connect (src/indexer/worker.ts:88)`,
    rootCause: "Elasticsearch endpoint moved to new IP; config not updated.",
    rootCauseConfidence: 92,
    hypotheses: [
      { id: "H1", statement: "Config points to old ES endpoint", confidence: 92, falsified: false },
      { id: "H2", statement: "ES pod evicted", confidence: 14, falsified: true },
      { id: "H3", statement: "Service mesh route stale", confidence: 18, falsified: true },
      { id: "H4", statement: "DNS cache poisoning", confidence: 4, falsified: true },
      { id: "H5", statement: "Network policy blocking", confidence: 12, falsified: true },
    ],
    fixProposal: { description: "Update config with new endpoint.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "7m 22s", tokens: 11400, cost: 0.016, status: "completed", children: [] },
    events: [
      { time: iso(2520), phase: "Detection", message: "ConnectionRefusedError" },
      { time: iso(2518), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(2514), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(2511), phase: "PR", message: "PR #4782 opened" },
      { time: iso(2511), phase: "Auto-Merge", message: "Merged" },
    ],
  },
  {
    id: "INC-8C4D1E9B",
    service: "billing-cron",
    namespace: "billing-prod",
    status: "resolved",
    errorType: "KeyError",
    confidence: 89,
    prNumber: 4776,
    timeToFix: "10m 47s",
    created: iso(2880),
    crashFile: "app/jobs/invoice.py",
    crashLine: 33,
    crashFunction: "build_invoice",
    errorExcerpt: `Traceback (most recent call last):
  File "app/jobs/invoice.py", line 33, in build_invoice
    tax = customer["tax_region"]
KeyError: 'tax_region'`,
    rootCause: "Tax region not set for EU customers post-Brexit migration.",
    rootCauseConfidence: 90,
    hypotheses: [
      { id: "H1", statement: "Brexit migration left tax_region null", confidence: 90, falsified: false },
      { id: "H2", statement: "Customer onboarding skipped field", confidence: 22, falsified: true },
      { id: "H3", statement: "Schema migration dropped column", confidence: 8, falsified: true },
      { id: "H4", statement: "ORM mapping wrong", confidence: 12, falsified: true },
      { id: "H5", statement: "Cache invalidation missing", confidence: 16, falsified: true },
    ],
    fixProposal: { description: "Default tax_region to 'EU' for legacy customers.", filesChanged: [] },
    sandboxLevels: ["pass", "pass", "pass", "pass", "pass"],
    agentTrace: { id: "n0", name: "Root Investigation", agent: "investigator", duration: "8m 47s", tokens: 13900, cost: 0.020, status: "completed", children: [] },
    events: [
      { time: iso(2880), phase: "Detection", message: "KeyError in invoice builder" },
      { time: iso(2878), phase: "Investigation", message: "Investigator spawned" },
      { time: iso(2872), phase: "Sandbox", message: "L1-L5 passed" },
      { time: iso(2870), phase: "PR", message: "PR #4776 opened" },
      { time: iso(2869), phase: "Auto-Merge", message: "Merged" },
    ],
  },
];

export const providers: Provider[] = [
  {
    name: "OpenRouter",
    priority: 1,
    status: "healthy",
    model: "anthropic/claude-sonnet-4.5",
    circuitState: "closed",
    calls: 1284,
    successRate: 99.4,
    avgLatency: 1840,
    trips: 0,
    inputTokens: 4_182_400,
    outputTokens: 894_120,
    cost: 64.18,
  },
  {
    name: "Groq",
    priority: 2,
    status: "tripped",
    model: "llama-3.3-70b-versatile",
    circuitState: "open",
    calls: 412,
    successRate: 71.2,
    avgLatency: 940,
    trips: 3,
    inputTokens: 1_284_900,
    outputTokens: 412_800,
    cost: 9.84,
  },
  {
    name: "Nemotron",
    priority: 3,
    status: "healthy",
    model: "nvidia/llama-3.1-nemotron-70b",
    circuitState: "closed",
    calls: 642,
    successRate: 97.8,
    avgLatency: 2240,
    trips: 1,
    inputTokens: 2_012_400,
    outputTokens: 521_700,
    cost: 18.42,
  },
];

export const pullRequests: PullRequest[] = [
  {
    pr: 4821,
    repo: "acme/search-indexer",
    title: "fix(indexer): handle missing title field in legacy feed",
    status: "merged",
    sandboxVerified: true,
    autoMerge: "merged",
    opened: iso(62),
    merged: iso(58),
    incidentId: "INC-9C1F4E8D",
    gates: [
      { name: "Branch protection", status: "pass" },
      { name: "CI green", status: "pass" },
      { name: "Code review (1)", status: "pass" },
      { name: "Sandbox L1-L5", status: "pass" },
      { name: "Confidence ≥ 0.85", status: "pass" },
      { name: "Diff size ≤ 500", status: "pass" },
      { name: "No new dependencies", status: "pass" },
      { name: "Static analysis", status: "pass" },
      { name: "License scan", status: "pass" },
      { name: "Rate limit available", status: "pass" },
      { name: "Cooldown clear", status: "pass" },
    ],
  },
  {
    pr: 4818,
    repo: "acme/notifications-worker",
    title: "fix(sms): guard zero-segment messages",
    status: "merged",
    sandboxVerified: true,
    autoMerge: "merged",
    opened: iso(133),
    merged: iso(133),
    incidentId: "INC-4D7E2A8F",
    gates: [
      { name: "Branch protection", status: "pass" },
      { name: "CI green", status: "pass" },
      { name: "Code review (1)", status: "pass" },
      { name: "Sandbox L1-L5", status: "pass" },
      { name: "Confidence ≥ 0.85", status: "pass" },
      { name: "Diff size ≤ 500", status: "pass" },
      { name: "No new dependencies", status: "pass" },
      { name: "Static analysis", status: "pass" },
      { name: "License scan", status: "pass" },
      { name: "Rate limit available", status: "pass" },
      { name: "Cooldown clear", status: "pass" },
    ],
  },
  {
    pr: 4825,
    repo: "acme/checkout-api",
    title: "fix(pricing): cache-miss handler for region metadata",
    status: "open",
    sandboxVerified: false,
    autoMerge: "queued",
    opened: iso(1),
    merged: null,
    incidentId: "INC-7A3F9B2C",
    gates: [
      { name: "Branch protection", status: "pass" },
      { name: "CI green", status: "pending" },
      { name: "Code review (1)", status: "pending" },
      { name: "Sandbox L1-L5", status: "pending" },
      { name: "Confidence ≥ 0.85", status: "pass" },
      { name: "Diff size ≤ 500", status: "pass" },
      { name: "No new dependencies", status: "pass" },
      { name: "Static analysis", status: "pending" },
      { name: "License scan", status: "pass" },
      { name: "Rate limit available", status: "pass" },
      { name: "Cooldown clear", status: "pass" },
    ],
  },
  {
    pr: 4812,
    repo: "acme/checkout-api",
    title: "fix(db): dynamic connection pool resizing",
    status: "merged",
    sandboxVerified: true,
    autoMerge: "merged",
    opened: iso(422),
    merged: iso(420),
    incidentId: "INC-3A7C1E5D",
    gates: Array.from({ length: 11 }, (_, i) => ({ name: `Gate ${i + 1}`, status: "pass" as const })),
  },
  {
    pr: 4805,
    repo: "acme/search-indexer",
    title: "fix(indexer): dynamic batch sizing by payload size",
    status: "merged",
    sandboxVerified: true,
    autoMerge: "merged",
    opened: iso(597),
    merged: iso(588),
    incidentId: "INC-6B9D4F2A",
    gates: Array.from({ length: 11 }, (_, i) => ({ name: `Gate ${i + 1}`, status: "pass" as const })),
  },
  {
    pr: 4800,
    repo: "acme/auth-service",
    title: "feat(auth): add WebAuthn support",
    status: "closed",
    sandboxVerified: false,
    autoMerge: "skipped",
    opened: iso(840),
    merged: null,
    incidentId: "",
    gates: Array.from({ length: 11 }, (_, i) => ({ name: `Gate ${i + 1}`, status: "fail" as const })),
  },
  {
    pr: 4798,
    repo: "acme/billing-cron",
    title: "fix(jobs): default interval for legacy schedules",
    status: "merged",
    sandboxVerified: true,
    autoMerge: "merged",
    opened: iso(1092),
    merged: iso(1092),
    incidentId: "INC-7D2E9B4F",
    gates: Array.from({ length: 11 }, (_, i) => ({ name: `Gate ${i + 1}`, status: "pass" as const })),
  },
];

export const sandboxRuns: SandboxRun[] = [
  {
    id: "SBR-9F2A",
    incidentId: "INC-9C1F4E8D",
    service: "search-indexer",
    levels: ["pass", "pass", "pass", "pass", "pass"],
    duration: "4m 12s",
    infraError: null,
    timestamp: iso(63),
    canaryImage: "registry.acme.io/search-indexer:canary-9c1f4e8d",
    namespace: "snd-9c1f4e8d",
    namespaceAge: "4m 12s",
    levelDetails: [
      { level: 1, name: "Namespace boot", status: "pass", podLogs: ["Creating namespace snd-9c1f4e8d...", "Pod scheduled", "Pod Ready ✓"], probeResults: [{ name: "readiness", status: "pass" }, { name: "liveness", status: "pass" }], testOutput: "1/1 pods ready in 8s" },
      { level: 2, name: "Reproducer run", status: "pass", podLogs: ["Loading reproducer corpus", "100 documents loaded", "Running transform pipeline"], probeResults: [{ name: "reproducer", status: "pass" }], testOutput: "Reproducer reproduced original error on unpatched image", reproducerOutput: "TypeError reproduced 47 times in 30s window" },
      { level: 3, name: "Fix verification", status: "pass", podLogs: ["Deploying canary image", "Loading reproducer corpus", "Running transform pipeline"], probeResults: [{ name: "no-crash", status: "pass" }], testOutput: "0 TypeErrors in 5m window (baseline: 47/30s)" },
      { level: 4, name: "Regression suite", status: "pass", podLogs: ["Running 248 tests"], probeResults: [{ name: "test-suite", status: "pass" }], testOutput: "248/248 passed in 1m 22s" },
      { level: 5, name: "Sustained load", status: "pass", podLogs: ["Sustained 1000 req/s for 5m"], probeResults: [{ name: "sustained-error-rate", status: "pass" }], testOutput: "Error rate 0.00% (threshold < 0.01%) over 5m window" },
    ],
    retries: [],
  },
  {
    id: "SBR-4D7E",
    incidentId: "INC-4D7E2A8F",
    service: "notifications-worker",
    levels: ["pass", "pass", "pass", "pass", "pass"],
    duration: "3m 47s",
    infraError: null,
    timestamp: iso(134),
    canaryImage: "registry.acme.io/notifications-worker:canary-4d7e2a8f",
    namespace: "snd-4d7e2a8f",
    namespaceAge: "3m 47s",
    levelDetails: [
      { level: 1, name: "Namespace boot", status: "pass", podLogs: ["Pod Ready ✓"], probeResults: [{ name: "readiness", status: "pass" }], testOutput: "1/1 pods ready" },
      { level: 2, name: "Reproducer run", status: "pass", podLogs: ["Sending empty SMS payload"], probeResults: [{ name: "reproducer", status: "pass" }], testOutput: "Reproduced", reproducerOutput: "ZeroDivisionError on empty message" },
      { level: 3, name: "Fix verification", status: "pass", podLogs: ["Deploying canary"], probeResults: [{ name: "no-crash", status: "pass" }], testOutput: "No errors" },
      { level: 4, name: "Regression suite", status: "pass", podLogs: ["Running tests"], probeResults: [{ name: "test-suite", status: "pass" }], testOutput: "120/120 passed" },
      { level: 5, name: "Sustained load", status: "pass", podLogs: ["1000 msg/s"], probeResults: [{ name: "sustained-error-rate", status: "pass" }], testOutput: "0.00% error rate" },
    ],
    retries: [],
  },
  {
    id: "SBR-7A3F",
    incidentId: "INC-7A3F9B2C",
    service: "checkout-api",
    levels: ["pass", "pass", "pass", "pending", "pending"],
    duration: "2m 18s (in progress)",
    infraError: null,
    timestamp: iso(2),
    canaryImage: "registry.acme.io/checkout-api:canary-7a3f9b2c",
    namespace: "snd-7a3f9b2c",
    namespaceAge: "2m 18s",
    levelDetails: [
      { level: 1, name: "Namespace boot", status: "pass", podLogs: ["Pod Ready ✓"], probeResults: [{ name: "readiness", status: "pass" }], testOutput: "1/1 pods ready" },
      { level: 2, name: "Reproducer run", status: "pass", podLogs: ["Loading checkout reproducer"], probeResults: [{ name: "reproducer", status: "pass" }], testOutput: "Reproduced KeyError", reproducerOutput: "KeyError: 'tax_rate' x23" },
      { level: 3, name: "Fix verification", status: "pass", podLogs: ["Deploying canary"], probeResults: [{ name: "no-crash", status: "pass" }], testOutput: "0 errors in 2m window" },
      { level: 4, name: "Regression suite", status: "pending", podLogs: ["Running 412 tests..."], probeResults: [], testOutput: "In progress: 318/412" },
      { level: 5, name: "Sustained load", status: "pending", podLogs: [], probeResults: [], testOutput: "Queued" },
    ],
    retries: [],
  },
  {
    id: "SBR-2B8E",
    incidentId: "INC-2B8E1D7A",
    service: "auth-service",
    levels: ["pass", "pass", "pending", "pending", "pending"],
    duration: "1m 47s (in progress)",
    infraError: null,
    timestamp: iso(7),
    canaryImage: "registry.acme.io/auth-service:canary-2b8e1d7a",
    namespace: "snd-2b8e1d7a",
    namespaceAge: "1m 47s",
    levelDetails: [
      { level: 1, name: "Namespace boot", status: "pass", podLogs: ["Pod Ready ✓"], probeResults: [{ name: "readiness", status: "pass" }], testOutput: "1/1 pods ready" },
      { level: 2, name: "Reproducer run", status: "pass", podLogs: ["Simulating race"], probeResults: [{ name: "reproducer", status: "pass" }], testOutput: "Reproduced panic", reproducerOutput: "panic: nil pointer in 0-200ms window" },
      { level: 3, name: "Fix verification", status: "pending", podLogs: ["Deploying canary..."], probeResults: [], testOutput: "In progress" },
      { level: 4, name: "Regression suite", status: "pending", podLogs: [], probeResults: [], testOutput: "Queued" },
      { level: 5, name: "Sustained load", status: "pending", podLogs: [], probeResults: [], testOutput: "Queued" },
    ],
    retries: [],
  },
  {
    id: "SBR-3A7C",
    incidentId: "INC-3A7C1E5D",
    service: "checkout-api",
    levels: ["pass", "pass", "pass", "pass", "pass"],
    duration: "5m 02s",
    infraError: null,
    timestamp: iso(423),
    canaryImage: "registry.acme.io/checkout-api:canary-3a7c1e5d",
    namespace: "snd-3a7c1e5d",
    namespaceAge: "5m 02s",
    levelDetails: Array.from({ length: 5 }, (_, i) => ({
      level: i + 1, name: `Level ${i + 1}`, status: "pass" as const, podLogs: ["ok"], probeResults: [{ name: "p", status: "pass" as const }], testOutput: "passed",
    })),
    retries: [],
  },
  {
    id: "SBR-6B9D",
    incidentId: "INC-6B9D4F2A",
    service: "search-indexer",
    levels: ["pass", "fail", "pass", "pass", "pass"],
    duration: "8m 41s",
    infraError: "ImagePullBackOff on first attempt",
    timestamp: iso(598),
    canaryImage: "registry.acme.io/search-indexer:canary-6b9d4f2a",
    namespace: "snd-6b9d4f2a",
    namespaceAge: "8m 41s",
    levelDetails: Array.from({ length: 5 }, (_, i) => ({
      level: i + 1, name: `Level ${i + 1}`, status: (i === 1 ? "fail" : "pass") as "fail" | "pass", podLogs: ["ok"], probeResults: [{ name: "p", status: "pass" as const }], testOutput: i === 1 ? "Failed: image pull timeout" : "passed",
    })),
    retries: [
      { attempt: 1, feedback: "ImagePullBackOff: registry.acme.io/search-indexer:canary-6b9d4f2a — backoff 2m before retry", time: iso(600) },
      { attempt: 2, feedback: "Pull succeeded after registry rate-limit cleared", time: iso(598) },
    ],
  },
  {
    id: "SBR-5C1F",
    incidentId: "INC-5C1F8A3E",
    service: "auth-service",
    levels: ["pass", "pass", "pass", "pass", "pass"],
    duration: "4m 18s",
    infraError: null,
    timestamp: iso(811),
    canaryImage: "registry.acme.io/auth-service:canary-5c1f8a3e",
    namespace: "snd-5c1f8a3e",
    namespaceAge: "4m 18s",
    levelDetails: Array.from({ length: 5 }, (_, i) => ({
      level: i + 1, name: `Level ${i + 1}`, status: "pass" as const, podLogs: ["ok"], probeResults: [{ name: "p", status: "pass" as const }], testOutput: "passed",
    })),
    retries: [],
  },
];

export const chatopsCommands: ChatopsCommand[] = [
  { command: "incident list", syntax: "incident list [--status <status>] [--service <svc>]", description: "List incidents with optional filters", permission: "Read" },
  { command: "incident show", syntax: "incident show <id>", description: "Show details for a specific incident", permission: "Read" },
  { command: "incident pause", syntax: "incident pause <id>", description: "Pause autonomous remediation for an incident", permission: "Write" },
  { command: "incident resume", syntax: "incident resume <id>", description: "Resume autonomous remediation", permission: "Write" },
  { command: "incident escalate", syntax: "incident escalate <id> [--to <user>]", description: "Escalate incident to human review", permission: "Write" },
  { command: "investigation status", syntax: "investigation status <id>", description: "Show current investigation phase", permission: "Read" },
  { command: "investigation trigger", syntax: "investigation trigger <service> [--namespace <ns>]", description: "Manually trigger investigation", permission: "Write" },
  { command: "sandbox run", syntax: "sandbox run <incident-id> [--level <1-5>]", description: "Trigger a sandbox verification run", permission: "Write" },
  { command: "sandbox status", syntax: "sandbox status <run-id>", description: "Show sandbox run status", permission: "Read" },
  { command: "pr list", syntax: "pr list [--status <status>]", description: "List recent PRs", permission: "Read" },
  { command: "pr merge", syntax: "pr merge <pr-number>", description: "Force-merge a PR bypassing gates", permission: "Destructive" },
  { command: "pr revert", syntax: "pr revert <pr-number> [--reason <text>]", description: "Revert a merged PR", permission: "Destructive" },
  { command: "provider list", syntax: "provider list", description: "List LLM providers and status", permission: "Read" },
  { command: "provider trip", syntax: "provider trip <name>", description: "Manually trip a provider circuit breaker", permission: "Write" },
  { command: "provider reset", syntax: "provider reset <name>", description: "Reset a tripped circuit breaker", permission: "Write" },
  { command: "detection pause", syntax: "detection pause [--duration <m>]", description: "Pause autonomous detection", permission: "Write" },
  { command: "detection resume", syntax: "detection resume", description: "Resume autonomous detection", permission: "Write" },
  { command: "automerge queue", syntax: "automerge queue", description: "Show current auto-merge queue", permission: "Read" },
  { command: "automerge drain", syntax: "automerge drain", description: "Drain the auto-merge queue (rejects all)", permission: "Destructive" },
];

export const chatopsAudit: ChatopsAuditEntry[] = [
  { user: "sre-oncall", command: "incident list", args: "--status investigating", time: iso(2), result: "success" },
  { user: "sre-oncall", command: "investigation status", args: "INC-7A3F9B2C", time: iso(4), result: "success" },
  { user: "platform-bot", command: "sandbox run", args: "INC-7A3F9B2C --level 4", time: iso(6), result: "success" },
  { user: "sre-oncall", command: "provider list", args: "", time: iso(12), result: "success" },
  { user: "sre-oncall", command: "provider reset", args: "Groq", time: iso(28), result: "success" },
  { user: "eng-alice", command: "pr revert", args: "4800 --reason 'flaky tests'", time: iso(45), result: "success" },
  { user: "eng-bob", command: "pr merge", args: "4821", time: iso(48), result: "denied" },
  { user: "platform-bot", command: "automerge queue", args: "", time: iso(58), result: "success" },
  { user: "sre-oncall", command: "detection pause", args: "--duration 30", time: iso(74), result: "success" },
  { user: "sre-oncall", command: "detection resume", args: "", time: iso(104), result: "success" },
  { user: "eng-carol", command: "incident escalate", args: "INC-8F2D6B9E", time: iso(180), result: "success" },
  { user: "eng-dave", command: "provider trip", args: "Groq", time: iso(212), result: "success" },
];

// Metrics — 7 days of data
export const metrics7d = Array.from({ length: 7 }, (_, i) => {
  const d = new Date(now - (6 - i) * 24 * 3600_000);
  const day = d.toISOString().slice(5, 10);
  return {
    day,
    investigating: 2 + Math.floor(Math.random() * 4),
    verified: 3 + Math.floor(Math.random() * 5),
    failed: Math.floor(Math.random() * 3),
    escalated: Math.floor(Math.random() * 2),
    resolved: 5 + Math.floor(Math.random() * 6),
    cost: 0.08 + Math.random() * 0.18,
    llmCalls: 180 + Math.floor(Math.random() * 120),
    sandboxFailures: Math.floor(Math.random() * 4),
  };
});

export const metrics30d = Array.from({ length: 30 }, (_, i) => {
  const d = new Date(now - (29 - i) * 24 * 3600_000);
  return {
    day: d.toISOString().slice(5, 10),
    cost: 0.06 + Math.random() * 0.22,
    incidents: Math.floor(Math.random() * 12),
    merged: Math.floor(Math.random() * 8),
  };
});

export const investigationDuration = [
  { bucket: "0-2m", count: 4 },
  { bucket: "2-5m", count: 11 },
  { bucket: "5-10m", count: 18 },
  { bucket: "10-15m", count: 14 },
  { bucket: "15-20m", count: 9 },
  { bucket: "20-30m", count: 6 },
  { bucket: "30m+", count: 3 },
];

export const llmCallsByModel = [
  { model: "claude-sonnet-4.5", investigator: 420, evidence: 680, analyst: 320, fixer: 280 },
  { model: "llama-3.3-70b", investigator: 180, evidence: 220, analyst: 90, fixer: 60 },
  { model: "nemotron-70b", investigator: 210, evidence: 280, analyst: 140, fixer: 110 },
];

export const llmCostByModel = [
  { model: "claude-sonnet-4.5", cost: 64.18 },
  { model: "llama-3.3-70b", cost: 9.84 },
  { model: "nemotron-70b", cost: 18.42 },
];

export const sandboxLevelFailures = [
  { level: "L1 boot", failures: 2 },
  { level: "L2 repro", failures: 5 },
  { level: "L3 fix", failures: 8 },
  { level: "L4 regress", failures: 11 },
  { level: "L5 sustained", failures: 14 },
];

export const autoMergeByState = [
  { name: "merged", value: 38, color: "#22C55E" },
  { name: "queued", value: 1, color: "#06B6D4" },
  { name: "blocked", value: 4, color: "#EAB308" },
  { name: "reverted", value: 2, color: "#EF4444" },
];

export const providerCostTrend = Array.from({ length: 7 }, (_, i) => {
  const d = new Date(now - (6 - i) * 24 * 3600_000);
  return {
    day: d.toISOString().slice(5, 10),
    openrouter: 8 + Math.random() * 4,
    groq: 0.5 + Math.random() * 2,
    nemotron: 1.5 + Math.random() * 3,
  };
});

export const apiEndpoints: ApiEndpoint[] = [
  {
    method: "GET",
    path: "/api/v1/incidents",
    description: "List incidents with optional filters",
    auth: true,
    params: [
      { name: "status", type: "string", required: false, description: "Filter by status" },
      { name: "service", type: "string", required: false, description: "Filter by service" },
      { name: "limit", type: "integer", required: false, description: "Max results (default 50)" },
    ],
    response: JSON.stringify({ incidents: [{ id: "INC-7A3F9B2C", status: "investigating" }], total: 15 }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/incidents/:id",
    description: "Get incident detail by ID",
    auth: true,
    params: [{ name: "id", type: "string", required: true, description: "Incident ID" }],
    response: JSON.stringify({ id: "INC-7A3F9B2C", status: "investigating", confidence: 0.87 }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/incidents/:id/pause",
    description: "Pause autonomous remediation for an incident",
    auth: true,
    params: [{ name: "id", type: "string", required: true, description: "Incident ID" }],
    body: JSON.stringify({ reason: "manual investigation", duration_minutes: 30 }, null, 2),
    response: JSON.stringify({ ok: true, paused_until: "2026-07-02T17:30:00Z" }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/investigations/trigger",
    description: "Manually trigger an investigation",
    auth: true,
    body: JSON.stringify({ service: "checkout-api", namespace: "payments-prod" }, null, 2),
    response: JSON.stringify({ investigation_id: "INV-9F2A1B3C", status: "queued" }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/providers",
    description: "List LLM providers and circuit breaker states",
    auth: true,
    response: JSON.stringify({ providers: [{ name: "OpenRouter", status: "healthy" }] }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/providers/:name/reset",
    description: "Reset a tripped circuit breaker",
    auth: true,
    params: [{ name: "name", type: "string", required: true, description: "Provider name" }],
    response: JSON.stringify({ ok: true, provider: "Groq", circuit_state: "closed" }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/sandbox/runs",
    description: "List recent sandbox runs",
    auth: true,
    response: JSON.stringify({ runs: [{ id: "SBR-9F2A", status: "pass" }] }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/sandbox/run",
    description: "Trigger a sandbox verification run",
    auth: true,
    body: JSON.stringify({ incident_id: "INC-7A3F9B2C", level: 4 }, null, 2),
    response: JSON.stringify({ run_id: "SBR-7A3F", status: "queued" }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/pulls",
    description: "List recent pull requests",
    auth: true,
    response: JSON.stringify({ pulls: [{ pr: 4821, status: "merged" }] }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/pulls/:pr/revert",
    description: "Revert a merged PR",
    auth: true,
    params: [{ name: "pr", type: "integer", required: true, description: "PR number" }],
    body: JSON.stringify({ reason: "flaky tests in regression suite" }, null, 2),
    response: JSON.stringify({ ok: true, revert_pr: 4822 }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/detection/status",
    description: "Get current detection state",
    auth: true,
    response: JSON.stringify({ autonomous_mode: "running", pause_timer: null }, null, 2),
  },
  {
    method: "POST",
    path: "/api/v1/detection/pause",
    description: "Pause autonomous detection",
    auth: true,
    body: JSON.stringify({ duration_minutes: 30 }, null, 2),
    response: JSON.stringify({ ok: true, paused_until: "2026-07-02T17:30:00Z" }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/automerge/queue",
    description: "Show current auto-merge queue",
    auth: true,
    response: JSON.stringify({ queue: [{ incident_id: "INC-7A3F9B2C", state: "queued" }] }, null, 2),
  },
  {
    method: "GET",
    path: "/api/v1/metrics",
    description: "Get aggregate metrics",
    auth: true,
    response: JSON.stringify({ success_rate: 0.892, total_merged: 38, total_reverted: 2 }, null, 2),
  },
];

export const apiEndpointsPublic: ApiEndpoint[] = [
  {
    method: "GET",
    path: "/healthz",
    description: "Health check (no auth)",
    auth: false,
    response: JSON.stringify({ status: "ok", version: "2.4.1" }, null, 2),
  },
  {
    method: "GET",
    path: "/metrics",
    description: "Prometheus metrics endpoint",
    auth: false,
    response: "# HELP sentinel_incidents_total Total incidents by status\n# TYPE sentinel_incidents_total counter\nsentinel_incidents_total{status=\"resolved\"} 8\n",
  },
];

// 7-day x 24-hour incident heatmap (rows=days, cols=hours)
export const incidentHeatmap: number[][] = Array.from({ length: 7 }, (_, day) =>
  Array.from({ length: 24 }, (_, hour) => {
    // Slight pattern: more incidents during business hours, fewer on weekends
    const isWeekend = day >= 5;
    const businessHour = hour >= 9 && hour <= 18;
    let base = 1;
    if (businessHour && !isWeekend) base = 4;
    if (businessHour && isWeekend) base = 2;
    return Math.floor(Math.random() * base) + (businessHour ? 1 : 0);
  })
);

export const heatmapDays = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

// Activity feed seed
export const activityFeedSeed = [
  { id: "evt-001", type: "investigation", message: "Investigator spawned for INC-7A3F9B2C", time: iso(4), icon: "Radio" },
  { id: "evt-002", type: "sandbox", message: "Sandbox L1 passed for INC-9C1F4E8D", time: iso(63), icon: "FlaskConical" },
  { id: "evt-003", type: "pr", message: "PR #4821 merged to main", time: iso(58), icon: "GitPullRequest" },
  { id: "evt-004", type: "provider", message: "Groq circuit breaker tripped (3 failures in 60s)", time: iso(28), icon: "Cpu" },
  { id: "evt-005", type: "incident", message: "INC-2B8E1D7A escalated for human review", time: iso(5), icon: "AlertTriangle" },
  { id: "evt-006", type: "detection", message: "Autonomous detection resumed", time: iso(104), icon: "Eye" },
  { id: "evt-007", type: "automerge", message: "PR #4818 auto-merged (all 11 gates passed)", time: iso(133), icon: "Merge" },
  { id: "evt-008", type: "sandbox", message: "Sandbox L3 failed for SBR-6B9D (ImagePullBackOff)", time: iso(600), icon: "FlaskConical" },
];

// Phases for live investigation pipeline
export const pipelinePhases = [
  "Detection",
  "Investigation",
  "Evidence",
  "Analysis",
  "Governance",
  "Execution",
  "Sandbox",
  "PR",
  "Auto-Merge",
];

// Console lines that appear one-by-one in live sandbox
export const sandboxConsoleLines = [
  { level: 1, text: "Creating namespace snd-7a3f9b2c..." },
  { level: 1, text: "Pulling image registry.acme.io/checkout-api:canary-7a3f9b2c" },
  { level: 1, text: "Pod scheduled to node-pool-3" },
  { level: 1, text: "Container created" },
  { level: 1, text: "Pod Ready ✓" },
  { level: 1, text: "L1: Namespace boot — PASS (8.4s)" },
  { level: 2, text: "L2: Loading reproducer corpus (100 docs)" },
  { level: 2, text: "L2: Running transform pipeline..." },
  { level: 2, text: "L2: Reproduced KeyError: 'tax_rate' x23 in 30s window" },
  { level: 2, text: "L2: Reproducer run — PASS" },
  { level: 3, text: "L3: Deploying canary image with fix..." },
  { level: 3, text: "L3: Canary pod Ready ✓" },
  { level: 3, text: "L3: Loading reproducer corpus..." },
  { level: 3, text: "L3: Running transform pipeline..." },
  { level: 3, text: "L3: 0 errors in 2m window (baseline: 23/30s)" },
  { level: 3, text: "L3: Fix verification — PASS" },
  { level: 4, text: "L4: Running regression suite (412 tests)..." },
  { level: 4, text: "L4: 318/412 passed..." },
  { level: 4, text: "L4: 380/412 passed..." },
  { level: 4, text: "L4: 412/412 passed (1m 22s)" },
  { level: 4, text: "L4: Regression suite — PASS" },
  { level: 5, text: "L5: Sustained load 1000 req/s for 5m..." },
  { level: 5, text: "L5: Error rate 0.00% (threshold < 0.01%)" },
  { level: 5, text: "L5: p99 latency 184ms (threshold < 500ms)" },
  { level: 5, text: "L5: Sustained load — PASS" },
  { level: 5, text: "All levels passed. Promoting to PR creation." },
];

// Detection state
export const detectionState = {
  autonomousMode: "running" as "running" | "paused",
  pauseTimer: null as number | null,
  activeStreams: [
    { pod: "checkout-api-7d4f", namespace: "payments-prod", service: "checkout-api", status: "streaming", errors: 47, started: iso(58) },
    { pod: "auth-service-5x2k", namespace: "identity-prod", service: "auth-service", status: "streaming", errors: 12, started: iso(98) },
    { pod: "search-indexer-3p1q", namespace: "search-prod", service: "search-indexer", status: "streaming", errors: 0, started: iso(182) },
    { pod: "notifications-worker-8r4t", namespace: "notify-prod", service: "notifications-worker", status: "streaming", errors: 3, started: iso(244) },
    { pod: "billing-cron-2w9x", namespace: "billing-prod", service: "billing-cron", status: "streaming", errors: 0, started: iso(320) },
    { pod: "image-resizer-1q7m", namespace: "media-prod", service: "image-resizer", status: "streaming", errors: 1, started: iso(412) },
  ],
  podWatcher: [
    { pod: "checkout-api-7d4f", namespace: "payments-prod", status: "CrashLoopBackOff", restarts: 7, age: "2h 14m" },
    { pod: "auth-service-5x2k", namespace: "identity-prod", status: "CrashLoopBackOff", restarts: 5, age: "1h 47m" },
    { pod: "search-indexer-3p1q", namespace: "search-prod", status: "Running", restarts: 0, age: "3h 02m" },
    { pod: "billing-cron-2w9x", namespace: "billing-prod", status: "OOMKilled", restarts: 2, age: "5h 18m" },
  ],
  alertmanager: {
    webhookStatus: "receiving",
    lastReceived: iso(0.3),
    pendingAlerts: 2,
  },
};

// Auto-merge state
export const autoMergeState = {
  rateLimit: { used: 2, limit: 3, window: "1h" },
  cooldown: { active: false, remaining: 0 },
  stats: { totalMerged: 38, totalReverted: 2, successRate: 94.7 },
  queue: [
    { incidentId: "INC-7A3F9B2C", service: "checkout-api", pr: 4825, state: "queued", queuedAt: iso(1), gates: { pass: 8, fail: 0, pending: 3 } },
    { incidentId: "INC-2B8E1D7A", service: "auth-service", pr: null, state: "blocked", queuedAt: iso(5), gates: { pass: 4, fail: 2, pending: 5 } },
  ],
};
