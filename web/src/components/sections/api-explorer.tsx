"use client";

import { useEffect, useMemo, useState } from "react";
import { Panel, PanelHeader, Mono, EmptyState, ErrorState } from "@/components/sentinel/common";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { getToken } from "@/lib/api";
import { useSentinelStore } from "@/lib/store";
import { Send, Lock, Globe, Loader2, AlertCircle, Terminal, RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";

// v5 hardcoded the endpoint list + descriptions. v1 fetches the endpoint
// paths from /api/meta and uses a small local map for the
// descriptions/methods/example bodies (the API doesn't serve those yet —
// they're presentation metadata, not API contract). If the API doesn't
// return an endpoint we expect, we don't fabricate it.

interface EndpointMeta {
  method: "GET" | "POST" | "PUT" | "DELETE" | "PATCH";
  description: string;
  auth: boolean;
  params?: Array<{ name: string; type: string; required: boolean; description: string }>;
  body?: string;
  pathParam?: string;
}

const ENDPOINT_META: Record<string, EndpointMeta> = {
  "/healthz": { method: "GET", description: "Health check (no auth)", auth: false },
  "/readyz": { method: "GET", description: "Readiness check (no auth)", auth: false },
  "/metrics": { method: "GET", description: "Prometheus metrics endpoint (no auth)", auth: false },
  "/metrics-summary": { method: "GET", description: "Aggregate metrics summary", auth: false },
  "/events": { method: "GET", description: "SSE stream of live events (token via ?token=)", auth: false },
  "/incidents": {
    method: "GET",
    description: "List recent incidents",
    auth: false,
    params: [{ name: "limit", type: "integer", required: false, description: "Max results (default 50)" }],
  },
  "/incidents/{id}": {
    method: "GET",
    description: "Get incident detail by ID (UUID or 4+ char prefix)",
    auth: false,
    params: [{ name: "id", type: "string", required: true, description: "Incident UUID or prefix" }],
    pathParam: "id",
  },
  "/providers": { method: "GET", description: "List LLM providers and circuit breaker states", auth: false },
  "/automerge": { method: "GET", description: "Auto-merge queue, rate limit, cooldown, and stats", auth: false },
  "/settings": { method: "GET", description: "Get runtime configuration", auth: false },
  "/admin/investigate": {
    method: "POST",
    description: "Trigger a manual investigation",
    auth: true,
    body: JSON.stringify({ service: "checkout-api", namespace: "payments-prod", reason: "manual trigger" }, null, 2),
  },
  "/admin/pause": {
    method: "POST",
    description: "Pause autonomous detection",
    auth: true,
    body: JSON.stringify({ duration_min: 30, reason: "manual pause" }, null, 2),
  },
  "/admin/resume": { method: "POST", description: "Resume autonomous detection", auth: true },
  "/settings/github-app": {
    method: "POST",
    description: "Configure the GitHub App (POST) / get status (GET) / remove (DELETE)",
    auth: true,
    body: JSON.stringify({ app_id: "12345", private_key: "-----BEGIN RSA PRIVATE KEY-----\n…" }, null, 2),
  },
  "/settings/github-app-install": {
    method: "POST",
    description: "Set the GitHub App installation ID",
    auth: true,
    body: JSON.stringify({ install_id: "9876543" }, null, 2),
  },
  "/api/meta": { method: "GET", description: "Endpoint + chatops command catalog (this endpoint)", auth: false },
};

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
  for (const [k, v] of Object.entries(params)) {
    out = out.replace(`{${k}}`, encodeURIComponent(v));
  }
  const queryParams: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (!path.includes(`{${k}}`) && v) {
      queryParams.push(`${encodeURIComponent(k)}=${encodeURIComponent(v)}`);
    }
  }
  if (queryParams.length > 0) out += `?${queryParams.join("&")}`;
  return out;
}

interface EndpointEntry {
  path: string;
  meta: EndpointMeta;
}

export function ApiExplorerSection() {
  const apiMeta = useSentinelStore((s) => s.apiMeta);
  const refreshApiMeta = useSentinelStore((s) => s.refreshApiMeta);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [bodyValue, setBodyValue] = useState<string>("");
  const [apiToken, setApiToken] = useState<string>("");
  const [response, setResponse] = useState<ApiResponse | null>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);

  useEffect(() => {
    if (apiMeta) {
      setLoading(false);
      setError(null);
      return;
    }
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        await refreshApiMeta();
        if (!cancelled) {
          setError(null);
          setLoading(false);
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Failed to fetch /api/meta");
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [apiMeta, refreshApiMeta]);

  // Build the endpoint list from /api/meta. Only include endpoints we have
  // presentation metadata for; others are listed in a "raw" panel below.
  const endpoints: EndpointEntry[] = useMemo(() => {
    const paths = apiMeta?.endpoints ?? [];
    return paths
      .map((path) => {
        // /incidents/{id} matches /incidents/ in /api/meta.
        const meta =
          ENDPOINT_META[path] ??
          (path === "/incidents/" ? ENDPOINT_META["/incidents/{id}"] : undefined);
        return meta ? { path, meta } : null;
      })
      .filter((e): e is EndpointEntry => e !== null);
  }, [apiMeta]);

  const [selected, setSelected] = useState<EndpointEntry | null>(null);

  // Auto-select the first endpoint once the list loads.
  useEffect(() => {
    if (!selected && endpoints.length > 0) {
      setSelected(endpoints[0] ?? null);
    }
  }, [endpoints, selected]);

  // Reset param/body when selection changes.
  useEffect(() => {
    setParamValues({});
    setBodyValue(selected?.meta.body ?? "");
    setResponse(null);
    setSendError(null);
  }, [selected]);

  // Load API token from localStorage.
  useEffect(() => {
    const t = getToken();
    if (t) setApiToken(t);
  }, []);

  const handleSend = async () => {
    if (!selected) return;
    setSending(true);
    setSendError(null);
    setResponse(null);
    const start = performance.now();
    try {
      const path = buildPath(selected.path, paramValues);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (selected.meta.auth && apiToken) headers["Authorization"] = `Bearer ${apiToken}`;
      const init: RequestInit = { method: selected.meta.method, headers };
      if (selected.meta.method !== "GET" && bodyValue) init.body = bodyValue;
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
      setSendError(err instanceof Error ? err.message : "Network request failed");
    } finally {
      setSending(false);
    }
  };

  if (loading) {
    return (
      <div className="grid grid-cols-1 gap-3 p-6 lg:grid-cols-12">
        <div className="h-96 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50 lg:col-span-4" />
        <div className="h-96 animate-pulse rounded-lg border border-zinc-800 bg-zinc-900/50 lg:col-span-8" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="p-6">
        <ErrorState message={error} onRetry={() => refreshApiMeta()} />
      </div>
    );
  }

  return (
    <div className="grid grid-cols-1 gap-3 p-6 lg:grid-cols-12">
      <Panel className="lg:col-span-4">
        <PanelHeader
          title="Endpoints"
          sub={`${endpoints.length} routes from /api/meta`}
          right={
            <button
              onClick={() => refreshApiMeta()}
              className="text-[10px] text-cyan-400 hover:text-cyan-300"
              title="Refresh from /api/meta"
            >
              <RefreshCw className="h-3 w-3" />
            </button>
          }
        />
        <div className="max-h-[640px] overflow-y-auto scrollbar-thin">
          {endpoints.length === 0 ? (
            <div className="px-4 py-12">
              <EmptyState
                title="No endpoints in /api/meta"
                description="The /api/meta endpoint returned an empty endpoints list. Ensure Sentinel is running."
                icon={Terminal}
                className="border-0 bg-transparent py-0"
              />
            </div>
          ) : (
            endpoints.map((ep) => (
              <button
                key={`${ep.meta.method}-${ep.path}`}
                onClick={() => setSelected(ep)}
                className={cn(
                  "flex w-full items-center gap-2 border-b border-zinc-800/50 px-3 py-2 text-left transition-colors hover:bg-zinc-800/30",
                  selected?.path === ep.path && selected?.meta.method === ep.meta.method && "bg-cyan-500/5 ring-1 ring-inset ring-cyan-500/20",
                )}
              >
                <Badge variant="outline" className={cn("font-mono text-[10px] font-semibold", methodStyles[ep.meta.method])}>
                  {ep.meta.method}
                </Badge>
                <div className="min-w-0 flex-1">
                  <Mono className="block truncate text-[11px] text-zinc-200">{ep.path}</Mono>
                  <span className="block truncate text-[10px] text-zinc-500">{ep.meta.description}</span>
                </div>
                {ep.meta.auth ? <Lock className="h-3 w-3 shrink-0 text-amber-400" /> : <Globe className="h-3 w-3 shrink-0 text-zinc-600" />}
              </button>
            ))
          )}
        </div>
      </Panel>

      <div className="space-y-3 lg:col-span-8">
        {selected?.meta.auth && (
          <Panel>
            <PanelHeader title="Auth" sub="Bearer token for /admin/* and POST /settings" />
            <div className="px-4 py-3">
              <Label className="mb-1.5 block text-[11px] uppercase tracking-wider text-zinc-500">API Token</Label>
              <Input
                type="password"
                placeholder="stl_admin_…"
                value={apiToken}
                onChange={(e) => setApiToken(e.target.value)}
                className="h-8 bg-zinc-950 font-mono text-xs"
              />
              <p className="mt-1.5 text-[10px] text-zinc-600">
                Required when SENTINEL_API_TOKEN is set on the server. Pulled from browser localStorage.
              </p>
            </div>
          </Panel>
        )}

        <Panel>
          <PanelHeader
            title="Request"
            sub={selected ? `${selected.meta.method} ${selected.path}` : "Select an endpoint"}
            right={
              <div className="flex items-center gap-2">
                {selected?.meta.auth && (
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
            <div className="flex items-center gap-2">
              {selected && (
                <Badge variant="outline" className={cn("font-mono text-[11px] font-semibold", methodStyles[selected.meta.method])}>
                  {selected.meta.method}
                </Badge>
              )}
              <Mono className="flex-1 truncate rounded border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-[11px] text-zinc-300">
                {selected ? buildPath(selected.path, paramValues) : "—"}
              </Mono>
              <Button onClick={handleSend} size="sm" className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400" disabled={!selected || sending}>
                {sending ? <Loader2 className="h-3 w-3 animate-spin" /> : <Send className="h-3 w-3" />}
                {sending ? "Sending..." : "Send"}
              </Button>
            </div>

            {selected?.meta.params && selected.meta.params.length > 0 && (
              <div>
                <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">Path / Query Parameters</Label>
                <div className="space-y-1.5">
                  {selected.meta.params.map((p) => (
                    <div key={p.name} className="grid grid-cols-12 items-center gap-2">
                      <Mono className="col-span-3 truncate text-[11px] text-cyan-400">{p.name}</Mono>
                      <span className="col-span-2 font-mono text-[10px] text-zinc-500">{p.type}</span>
                      <span className="col-span-1 text-[10px]">
                        {p.required ? <span className="font-mono text-rose-400">req</span> : <span className="font-mono text-zinc-600">opt</span>}
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

            {selected?.meta.body && (
              <div>
                <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">Request Body</Label>
                <Textarea
                  value={bodyValue}
                  onChange={(e) => setBodyValue(e.target.value)}
                  className="min-h-[100px] bg-zinc-950 font-mono text-[11px]"
                />
              </div>
            )}

            <div>
              <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">Headers</Label>
              <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5 font-mono text-[11px] text-zinc-400">
                <div className="flex items-center justify-between">
                  <span>Content-Type</span>
                  <span className="text-zinc-300">application/json</span>
                </div>
                {selected?.meta.auth && (
                  <div className="mt-1 flex items-center justify-between">
                    <span>Authorization</span>
                    <span className="text-amber-400">{apiToken ? "Bearer ••••••••" : "(not set)"}</span>
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

        <Panel>
          <PanelHeader
            title="Response"
            sub={response ? `${response.status} ${response.statusText} · ${response.durationMs}ms` : "Click Send to make a real request"}
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
            {sendError ? (
              <div className="flex items-center gap-2 rounded-md border border-rose-500/30 bg-rose-500/5 p-3">
                <AlertCircle className="h-4 w-4 shrink-0 text-rose-400" />
                <p className="font-mono text-[11px] text-rose-300">{sendError}</p>
              </div>
            ) : response ? (
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
                <pre className="max-h-80 overflow-auto bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed text-zinc-300 scrollbar-thin">
                  {response.body || "// empty response body"}
                </pre>
              </div>
            ) : (
              <EmptyState
                title="No response yet"
                description="Hit Send to make a real fetch request to this endpoint. The response will appear here."
                icon={Terminal}
                className="border-0 bg-transparent py-8"
              />
            )}

            {selected && (
              <div className="mt-3">
                <Label className="mb-2 block text-[11px] uppercase tracking-wider text-zinc-500">cURL Preview</Label>
                <pre className="overflow-x-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[11px] leading-relaxed text-zinc-400 scrollbar-thin">
{`curl -X ${selected.meta.method} '${buildPath(selected.path, paramValues)}' \\
  -H 'Content-Type: application/json'${
    selected.meta.auth && apiToken
      ? ` \\\n  -H 'Authorization: Bearer ${apiToken.slice(0, 4)}…'`
      : selected.meta.auth
        ? ` \\\n  -H 'Authorization: Bearer $SENTINEL_TOKEN'`
        : ""
  }${selected.meta.body ? ` \\\n  -d '${selected.meta.body.replace(/\n/g, "")}'` : ""}`}
                </pre>
              </div>
            )}
          </div>
        </Panel>
      </div>
    </div>
  );
}
