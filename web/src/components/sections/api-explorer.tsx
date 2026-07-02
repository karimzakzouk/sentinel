"use client";

import { useState } from "react";
import { Panel, PanelHeader, Mono } from "@/components/sentinel/common";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { apiEndpoints, apiEndpointsPublic, type ApiEndpoint } from "@/lib/mock-data";
import { Prism as SyntaxHighlighter } from "react-syntax-highlighter";
import { atomOneDark } from "react-syntax-highlighter/dist/esm/styles/hljs";
import { Send, Lock, Globe, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

const methodStyles: Record<string, string> = {
  GET: "border-cyan-500/30 bg-cyan-500/10 text-cyan-400",
  POST: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
  PUT: "border-amber-500/30 bg-amber-500/10 text-amber-400",
  PATCH: "border-purple-500/30 bg-purple-500/10 text-purple-400",
  DELETE: "border-rose-500/30 bg-rose-500/10 text-rose-400",
};

export function ApiExplorerSection() {
  const [selected, setSelected] = useState<ApiEndpoint>(apiEndpoints[0]);
  const [response, setResponse] = useState<string>(apiEndpoints[0].response);
  const [status, setStatus] = useState<number>(200);
  const [loading, setLoading] = useState(false);
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [bodyValue, setBodyValue] = useState<string>(apiEndpoints[0].body ?? "");

  const handleSend = () => {
    setLoading(true);
    setTimeout(() => {
      setResponse(selected.response);
      setStatus(selected.method === "POST" ? 201 : 200);
      setLoading(false);
    }, 600);
  };

  const handleSelect = (ep: ApiEndpoint) => {
    setSelected(ep);
    setResponse(ep.response);
    setStatus(ep.method === "POST" ? 201 : 200);
    setParamValues({});
    setBodyValue(ep.body ?? "");
  };

  const allEndpoints = [...apiEndpoints, ...apiEndpointsPublic];

  return (
    <div className="grid grid-cols-1 gap-3 p-6 lg:grid-cols-12">
      {/* Endpoint list */}
      <Panel className="lg:col-span-4">
        <PanelHeader
          title="Endpoints"
          sub={`${allEndpoints.length} routes available`}
          right={
            <Badge variant="outline" className="border-zinc-700 bg-zinc-900 font-mono text-[10px] text-zinc-400">
              v1
            </Badge>
          }
        />
        <div className="max-h-[640px] overflow-y-auto scrollbar-thin">
          {allEndpoints.map((ep, i) => (
            <button
              key={`${ep.method}-${ep.path}-${i}`}
              onClick={() => handleSelect(ep)}
              className={cn(
                "flex w-full items-center gap-2 border-b border-zinc-800/50 px-3 py-2 text-left transition-colors hover:bg-zinc-800/30",
                selected.path === ep.path && selected.method === ep.method && "bg-cyan-500/5 ring-1 ring-inset ring-cyan-500/20"
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
                {selected.path}
              </Mono>
              <Button
                onClick={handleSend}
                size="sm"
                className="h-8 bg-cyan-500 text-xs text-zinc-950 hover:bg-cyan-400"
                disabled={loading}
              >
                <Send className="h-3 w-3" />
                {loading ? "Sending..." : "Send"}
              </Button>
            </div>

            {/* Params */}
            {selected.params && selected.params.length > 0 && (
              <div>
                <Label className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">Path / Query Parameters</Label>
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
                <Label className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">Request Body</Label>
                <Textarea
                  value={bodyValue}
                  onChange={(e) => setBodyValue(e.target.value)}
                  className="min-h-[100px] bg-zinc-950 font-mono text-[11px]"
                />
              </div>
            )}

            {/* Headers info */}
            <div>
              <Label className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">Headers</Label>
              <div className="rounded-md border border-zinc-800 bg-zinc-950 p-2.5 font-mono text-[11px] text-zinc-400">
                <div className="flex items-center justify-between">
                  <span>Authorization</span>
                  <span className="text-amber-400">Bearer ••••••••</span>
                </div>
                <div className="mt-1 flex items-center justify-between">
                  <span>Content-Type</span>
                  <span className="text-zinc-300">application/json</span>
                </div>
                <div className="mt-1 flex items-center justify-between">
                  <span>X-Sentinel-Version</span>
                  <span className="text-zinc-300">2.4.1</span>
                </div>
              </div>
            </div>
          </div>
        </Panel>

        {/* Response panel */}
        <Panel>
          <PanelHeader
            title="Response"
            sub={`${status} ${status === 200 ? "OK" : status === 201 ? "Created" : ""} · 142ms`}
            right={
              <Badge
                variant="outline"
                className={cn(
                  "font-mono text-[10px]",
                  status >= 200 && status < 300
                    ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                    : "border-rose-500/30 bg-rose-500/10 text-rose-400"
                )}
              >
                {status}
              </Badge>
            }
          />
          <div className="px-4 py-3">
            <div className="overflow-hidden rounded-md border border-zinc-800">
              <div className="flex items-center justify-between border-b border-zinc-800 bg-zinc-900 px-3 py-1.5">
                <div className="flex items-center gap-2 text-[10px] uppercase tracking-wider text-zinc-500">
                  <Globe className="h-3 w-3" />
                  <span>JSON · 142 bytes</span>
                </div>
                <Button variant="outline" size="sm" className="h-6 border-zinc-700 bg-zinc-900 text-[10px] text-zinc-400">
                  Copy
                </Button>
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
                {loading ? "// awaiting response..." : response}
              </SyntaxHighlighter>
            </div>

            {/* Curl preview */}
            <div className="mt-3">
              <Label className="mb-2 text-[11px] uppercase tracking-wider text-zinc-500">cURL Preview</Label>
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
                  {`curl -X ${selected.method} 'https://sentinel.acme.io${selected.path}' \\
  -H 'Authorization: Bearer $SENTINEL_TOKEN' \\
  -H 'Content-Type: application/json'${selected.body ? ` \\\n  -d '${selected.body.replace(/\n/g, "")}'` : ""}`}
                </SyntaxHighlighter>
              </div>
            </div>
          </div>
        </Panel>
      </div>
    </div>
  );
}
