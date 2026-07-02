"use client";

import { useMemo, useState } from "react";
import {
  Panel,
  PanelHeader,
  Mono,
  EmptyState,
} from "@/components/sentinel/common";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Prism as SyntaxHighlighter } from "react-syntax-highlighter";
import { atomOneDark } from "react-syntax-highlighter/dist/esm/styles/hljs";
import {
  Send,
  Lock,
  Globe,
  Loader2,
  AlertCircle,
  Terminal,
} from "lucide-react";
import { cn } from "@/lib/utils";

// Static endpoint list — matches the routes registered in
// internal/api/server.go's Start() method.

interface ApiParam {
  name: string;
  type: string;
  required: boolean;
  description: string;
}

interface ApiEndpoint {
  method: "GET" | "POST" | "PUT" | "DELETE" | "PATCH";
  path: string;
  description: string;
  auth: boolean;
  params?: ApiParam[];
  body?: string; // example body
}

const apiEndpoints: ApiEndpoint[] = [
  // Public (no auth)
  {
    method: "GET",
    path: "/healthz",
    description: "Health check (no auth)",
    auth: false,
  },
  {
    method: "GET",
    path: "/readyz",
    description: "Readiness check (no auth)",
    auth: false,
  },
  {
    method: "GET",
    path: "/metrics",
    description: "Prometheus metrics endpoint (no auth)",
    auth: false,
  },
  {
    method: "GET",
    path: "/metrics-summary",
    description: "Aggregate metrics summary",
    auth: false,
  },
  {
    method: "GET",
    path: "/events",
    description: "SSE stream of live events (no auth)",
    auth: false,
  },
  // Incidents (auth required when apiToken is set)
  {
    method: "GET",
    path: "/incidents",
    description: "List recent incidents",
    auth: true,
    params: [
      { name: "limit", type: "integer", required: false, description: "Max results (default 50)" },
    ],
  },
  {
    method: "GET",
    path: "/incidents/:id",
    description: "Get incident detail by ID (UUID or 4+ char prefix)",
    auth: true,
    params: [{ name: "id", type: "string", required: true, description: "Incident UUID or prefix" }],
  },
  // Providers + auto-merge
  {
    method: "GET",
    path: "/providers",
    description: "List LLM providers and circuit breaker states",
    auth: false,
  },
  {
    method: "GET",
    path: "/automerge",
    description: "Auto-merge queue, rate limit, cooldown, and stats",
    auth: false,
  },
  // Settings
  {
    method: "GET",
    path: "/settings",
    description: "Get runtime configuration",
    auth: false,
  },
  {
    method: "POST",
    path: "/settings",
    description: "Save runtime configuration (requires auth)",
    auth: true,
    body: JSON.stringify({ llm_fast_model: "anthropic/claude-haiku-3.5" }, null, 2),
  },
  // Admin (auth always required)
  {
    method: "POST",
    path: "/admin/investigate",
    description: "Trigger a manual investigation",
    auth: true,
    body: JSON.stringify({ service: "checkout-api", namespace: "payments-prod", reason: "manual trigger" }, null, 2),
  },
  {
    method: "POST",
    path: "/admin/pause",
    description: "Pause autonomous detection",
    auth: true,
    params: [{ name: "duration", type: "integer", required: false, description: "Pause duration in minutes (default 30)" }],
    body: JSON.stringify({ duration_min: 30, reason: "manual pause" }, null, 2),
  },
  {
    method: "POST",
    path: "/admin/resume",
    description: "Resume autonomous detection",
    auth: true,
  },
  // Webhooks (signature-verified, not bearer-auth)
  {
    method: "POST",
    path: "/webhook/alertmanager",
    description: "Alertmanager webhook receiver",
    auth: false,
    body: JSON.stringify({ alerts: [{ status: "firing", labels: { service: "checkout-api" } }] }, null, 2),
  },
  {
    method: "POST",
    path: "/webhook/github",
    description: "GitHub webhook (PR review + chatops)",
    auth: false,
  },
  {
    method: "POST",
    path: "/slack/events",
    description: "Slack Events API callback",
    auth: false,
  },
];

const methodStyles: Record<string, string> = {
  GET: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  POST: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  PUT: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  PATCH: "border-purple-500/30 bg-purple-500/10 text-purple-400",
  DELETE: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

interface ApiResponse {
  status: number;
  statusText: string;
  body: string;
  durationMs: number;
}

function buildPath(path: string, params: Record<string, string>): string {
  let out = path;
  // Replace :param segments with provided values
  for (const [k, v] of Object.entries(params)) {
    out = out.replace(`:${k}`, encodeURIComponent(v));
  }
  // Append remaining (non-path) params as query string
  const queryParams: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (!path.includes(`:${k}`) && v) {
      queryParams.push(`${encodeURIComponent(k)}=${encodeURIComponent(v)}`);
    }
  }
  if (queryParams.length > 0) {
    out += `?${queryParams.join("&")}`;
  }
  return out;
}

export function ApiExplorerSection() {
  const [selected, setSelected] = useState<ApiEndpoint>(apiEndpoints[0]);
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [bodyValue, setBodyValue] = useState<string>("");
  const [apiToken, setApiToken] = useState<string>("");
  const [response, setResponse] = useState<ApiResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Load API token from localStorage
  useMemo(() => {
    if (typeof window === "undefined") return;
    const t = window.localStorage.getItem("sentinel_api_token");
    if (t) setApiToken(t);
  }, []);

  const handleSelect = (ep: ApiEndpoint) => {
    setSelected(ep);
    setParamValues({});
    setBodyValue(ep.body ?? "");
    setResponse(null);
    setError(null);
  };

  const onApiTokenChange = (v: string) => {
    setApiToken(v);
    if (typeof window !== "undefined") {
      if (v) window.localStorage.setItem("sentinel_api_token", v);
      else window.localStorage.removeItem("sentinel_api_token");
    }
  };

  const handleSend = async () => {
    setLoading(true);
    setError(null);
    setResponse(null);
    const start = performance.now();
    try {
      const path = buildPath(selected.path, paramValues);
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
      };
      if (selected.auth && apiToken) {
        headers["Authorization"] = `Bearer ${apiToken}`;
      }
      const init: RequestInit = {
        method: selected.method,
        headers,
      };
      if (selected.method !== "GET" && bodyValue) {
        init.body = bodyValue;
      }
      const res = await fetch(path, init);
      const text = await res.text();
      let formatted = text;
      try {
        formatted = JSON.stringify(JSON.parse(text), null, 2);
      } catch {
        // not JSON — keep as-is
      }
      setResponse({
        status: res.status,
        statusText: res.statusText,
        body: formatted,
        durationMs: Math.round(performance.now() - start),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Network request failed");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="grid grid-cols-1 gap-3 p-6 lg:grid-cols-12">
      {/* Endpoint list */}
      <Panel className="lg:col-span-4">
        <PanelHeader
          title="Endpoints"
          sub={`${apiEndpoints.length} routes available`}
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              v1
            </Badge>
          }
        />
        <div className="max-h-[640px] overflow-y-auto scrollbar-thin">
          {apiEndpoints.map((ep, i) => (
            <button
              key={`${ep.method}-${ep.path}-${i}`}
              onClick={() => handleSelect(ep)}
              className={cn(
                "flex w-full items-center gap-2 border-b border-zinc-800/50 px-3 py-2 text-left transition-colors hover:bg-zinc-800/30",
                selected.path === ep.path && selected.method === ep.method && "bg-cyan-500/5 ring-1 ring-inset ring-cyan-500/20",
              )}
            >
              <Badge variant="outline" className={cn("font-mono text-[10px] font-semibold", methodStyles[ep.method])}>
                {ep.method}
              </Badge>
              <div className="min-w-0 flex-1">
                <Mono className="block truncate text-[11px] text-zinc-200">{ep.path}</Mono>
                <span className="block truncate text-[10px] text-zinc-500">{ep.description}</span>
              </div>
              {ep.auth ? (
                <Lock className="h-3 w-3 shrink-0 text-amber-400" />
              ) : (
                <Globe className="h-3 w-3 shrink-0 text-zinc-600" />
              )}
            </button>
          ))}
        </div>
      </Panel>

      {/* Request / Response */}
      <div className="space-y-3 lg:col-span-8">
        {/* API token (only shown when endpoint requires auth) */}
        {selected.auth && (
          <Panel>
            <PanelHeader title="Auth" sub="Bearer token for /admin/* and POST /settings" />
            <div className="px-4 py-3">
              <Label className="mb-1.5 block text-[11px] uppercase tracking-wider text-zinc-500">
                API Token
              </Label>
              <Input
                type="password"
                placeholder="stl_admin_…"
                value={apiToken}
                onChange={(e) => onApiTokenChange(e.target.value)}
                className="h-8 bg-zinc-950 font-mono text-xs"
              />
              <p className="mt-1.5 text-[10px] text-zinc-600">
                Required when SENTINEL_API_TOKEN is set on the server. Stored in browser localStorage.
              </p>
            </div>
          </Panel>
        )}

        {/* Request panel */}
        <Panel>
          <PanelHeader
            title="Request"
            sub={`${selected.method} ${selected.path}`}
            right={
              <div className="flex items-center gap-2">
                {selected.auth && (
                  <Badge variant="outline" className="border-amber-500/30 bg-amber-500/10 font-mono text-[10px] text-amber-400">
                    <Lock className="h-3 w-3" /> auth required
                  </Badge>
                )}
                <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
                  application/json
                </Badge>
              </div>
            }
          />
          <div className="space-y-3 px-4 py-3">
            {/* URL bar */}
            <div className="flex items-center gap-2">
              <Badge variant="outline" className={cn("font-mono text-[11px] font-semibold", methodStyles[selected.method])}>
                {selected.method}
              </Badge>
              <Mono className="flex-1 truncate rounded border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-[11px] text-zinc-300">
                {buildPath(selected.path, paramValues)}
              </Mono>
              <Button
                onClick={handleSend}
                size="sm"
                className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400"
                disabled={loading}
              >
                {loading ? (
                  <Loader2 className="h-3 w-3 animate-spin" />
                ) : (
                  <Send className="h-3 w-3" />
                )}
                {loading ? "Sending..." : "Send"}
              </Button>
            </div>

            {/* Params */}
            {selected.params && selected.params.length > 0 && (
              <div>
                <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">
                  Path / Query Parameters
                </Label>
                <div className="space-y-1.5">
                  {selected.params.map((p) => (
                    <div key={p.name} className="grid grid-cols-12 items-center gap-2">
                      <Mono className="col-span-3 truncate text-[11px] text-cyan-400">{p.name}</Mono>
                      <span className="col-span-2 font-mono text-[10px] text-zinc-500">{p.type}</span>
                      <span className="col-span-1 text-[10px]">
                        {p.required ? (
                          <span className="font-mono text-rose-400">req</span>
                        ) : (
                          <span className="font-mono text-zinc-600">opt</span>
                        )}
                      </span>
                      <Input
                        placeholder={p.description}
                        value={paramValues[p.name] ?? ""}
                        onChange={(e) => setParamValues({ ...paramValues, [p.name]: e.target.value })}
                        className="col-span-6 h-7 bg-zinc-950 font-mono text-[11px]"
                      />
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Body */}
            {selected.body && (
              <div>
                <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">
                  Request Body
                </Label>
                <Textarea
                  value={bodyValue}
                  onChange={(e) => setBodyValue(e.target.value)}
                  className="min-h-[100px] bg-zinc-950 font-mono text-[11px]"
                />
              </div>
            )}

            {/* Headers info */}
            <div>
              <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">
                Headers
              </Label>
              <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5 font-mono text-[11px] text-zinc-400">
                <div className="flex items-center justify-between">
                  <span>Content-Type</span>
                  <span className="text-zinc-300">application/json</span>
                </div>
                {selected.auth && (
                  <div className="mt-1 flex items-center justify-between">
                    <span>Authorization</span>
                    <span className="text-amber-400">
                      {apiToken ? "Bearer ••••••••" : "(not set)"}
                    </span>
                  </div>
                )}
                <div className="mt-1 flex items-center justify-between">
                  <span>Accept</span>
                  <span className="text-zinc-300">*/*</span>
                </div>
              </div>
            </div>
          </div>
        </Panel>

        {/* Response panel */}
        <Panel>
          <PanelHeader
            title="Response"
            sub={
              response
                ? `${response.status} ${response.statusText} · ${response.durationMs}ms`
                : "Click Send to make a real request"
            }
            right={
              response ? (
                <Badge
                  variant="outline"
                  className={cn(
                    "font-mono text-[10px]",
                    response.status >= 200 && response.status < 300
                      ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                      : "border-rose-500/30 bg-rose-500/10 text-rose-400",
                  )}
                >
                  {response.status}
                </Badge>
              ) : null
            }
          />
          <div className="px-4 py-3">
            {error ? (
              <div className="flex items-center gap-2 rounded-md border border-rose-500/30 bg-rose-500/5 p-3">
                <AlertCircle className="h-4 w-4 shrink-0 text-rose-400" />
                <p className="font-mono text-[11px] text-rose-300">{error}</p>
              </div>
            ) : response ? (
              <>
                <div className="overflow-hidden rounded-md border border-zinc-800">
                  <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900 px-3 py-1.5">
                    <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
                      <Globe className="h-3 w-3" />
                      <span>{response.body.length} bytes</span>
                    </div>
                    <button
                      onClick={() => navigator.clipboard?.writeText(response.body)}
                      className="text-[10px] text-cyan-400 hover:text-cyan-300"
                    >
                      Copy
                    </button>
                  </div>
                  <SyntaxHighlighter
                    language="json"
                    style={atomOneDark}
                    customStyle={{
                      margin: 0,
                      background: "#09090B",
                      padding: "12px",
                      fontSize: "11.5px",
                      lineHeight: "1.55",
                    }}
                  >
                    {response.body || "// empty response body"}
                  </SyntaxHighlighter>
                </div>
              </>
            ) : (
              <EmptyState
                title="No response yet"
                description="Hit Send to make a real fetch request to this endpoint. The response will appear here."
                icon={Terminal}
                className="border-0 bg-transparent py-8"
              />
            )}

            {/* Curl preview */}
            <div className="mt-3">
              <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">
                cURL Preview
              </Label>
              <div className="overflow-hidden rounded-md border border-zinc-800">
                <SyntaxHighlighter
                  language="bash"
                  style={atomOneDark}
                  customStyle={{
                    margin: 0,
                    background: "#09090B",
                    padding: "12px",
                    fontSize: "11px",
                    lineHeight: "1.55",
                  }}
                >
                  {`curl -X ${selected.method} '${buildPath(selected.path, paramValues)}' \\
  -H 'Content-Type: application/json'${
    selected.auth && apiToken
      ? ` \\
  -H 'Authorization: Bearer ${apiToken.slice(0, 4)}…'`
      : selected.auth
      ? ` \\
  -H 'Authorization: Bearer $SENTINEL_TOKEN'`
      : ""
  }${selected.body ? ` \\\n  -d '${selected.body.replace(/\n/g, "")}'` : ""}`}
                </SyntaxHighlighter>
              </div>
            </div>
          </div>
        </Panel>
      </div>
    </div>
  );
}
