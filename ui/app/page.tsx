"use client";

import { useIncidents, useMetrics } from "@/lib/api";
import { statusBadgeClass, severityBadgeClass, timeAgo } from "@/lib/utils";
import Link from "next/link";
import { AlertTriangle, CheckCircle2, Clock, GitPullRequest, Cpu, Activity } from "lucide-react";

export default function DashboardPage() {
  const { data: incidents, isLoading } = useIncidents();
  const { data: metrics } = useMetrics();

  const active = (incidents || []).filter((i) =>
    ["detected", "investigating", "root_caused", "fix_proposed", "governance_review", "approved", "executing"].includes(i.status)
  );
  const resolved = (incidents || []).filter((i) =>
    ["verified", "resolved"].includes(i.status)
  );
  const failed = (incidents || []).filter((i) =>
    ["failed", "rejected", "escalated"].includes(i.status)
  );

  return (
    <div className="space-y-6">
      {/* Top stats */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        <StatCard
          icon={Activity}
          label="Active Incidents"
          value={active.length}
          accent="text-[var(--color-info)]"
        />
        <StatCard
          icon={CheckCircle2}
          label="Resolved (24h)"
          value={resolved.length}
          accent="text-[var(--color-accent)]"
        />
        <StatCard
          icon={AlertTriangle}
          label="Failed / Escalated"
          value={failed.length}
          accent="text-[var(--color-danger)]"
        />
        <StatCard
          icon={GitPullRequest}
          label="PRs Opened (24h)"
          value={(incidents || []).filter((i) => i.state?.execution_result?.pr_url).length}
          accent="text-[var(--color-warning)]"
        />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Active incidents list */}
        <div className="lg:col-span-2 panel">
          <div className="panel-header">
            <h2>Active Incidents</h2>
            <Link href="/incidents" className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-accent)]">
              View all →
            </Link>
          </div>
          <div className="overflow-x-auto max-h-[500px] overflow-y-auto">
            {isLoading ? (
              <div className="p-8 text-center text-[var(--color-text-dim)] text-sm">Loading...</div>
            ) : active.length === 0 ? (
              <div className="p-8 text-center text-[var(--color-text-dim)] text-sm">
                <CheckCircle2 className="mx-auto mb-2 text-[var(--color-accent)]" size={32} />
                No active incidents. All systems nominal.
              </div>
            ) : (
              <table className="dense-table">
                <thead>
                  <tr>
                    <th>Severity</th>
                    <th>Service</th>
                    <th>Status</th>
                    <th>Root Cause</th>
                    <th>Confidence</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {active.slice(0, 20).map((inc) => (
                    <tr key={inc.incident_id} className="cursor-pointer hover:bg-[var(--color-panel-2)]">
                      <td>
                        <span className={`badge ${severityBadgeClass(inc.severity)}`}>
                          {inc.severity}
                        </span>
                      </td>
                      <td>
                        <Link href={`/incidents/${inc.incident_id}`} className="font-mono text-[var(--color-accent)] hover:underline">
                          {inc.service}
                        </Link>
                        <div className="text-[11px] text-[var(--color-text-dim)]">{inc.namespace}</div>
                      </td>
                      <td>
                        <span className={`badge ${statusBadgeClass(inc.status)}`}>
                          {inc.status.replace(/_/g, " ")}
                        </span>
                      </td>
                      <td className="max-w-xs truncate text-[var(--color-text-dim)]">
                        {inc.state?.root_cause?.statement || "—"}
                      </td>
                      <td className="font-mono">
                        {inc.state?.root_cause ? (
                          <span className={inc.state.root_cause.confidence >= 0.92 ? "text-[var(--color-accent)]" : "text-[var(--color-warning)]"}>
                            {(inc.state.root_cause.confidence * 100).toFixed(1)}%
                          </span>
                        ) : "—"}
                      </td>
                      <td className="text-[var(--color-text-dim)] text-xs whitespace-nowrap">
                        {timeAgo(inc.updated_at)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>

        {/* Right column: live agent activity + layer health */}
        <div className="space-y-6">
          <div className="panel">
            <div className="panel-header">
              <h2>Layer Health</h2>
              <span className="live-dot" />
            </div>
            <div className="panel-body space-y-2 text-xs font-mono">
              <LayerHealth name="Ingestion" icon={Activity} status="ok" detail="3 collectors" />
              <LayerHealth name="Detection" icon={Cpu} status="ok" detail="IF + CUSUM" />
              <LayerHealth name="Reasoning" icon={Clock} status="ok" detail="4 agents" />
              <LayerHealth name="Safety" icon={CheckCircle2} status="ok" detail="OPA running" />
              <LayerHealth name="Execution" icon={GitPullRequest} status="ok" detail="ready" />
              <LayerHealth name="Feedback" icon={Activity} status="ok" detail="Weaviate ready" />
            </div>
          </div>

          <div className="panel">
            <div className="panel-header">
              <h2>Recent PRs Opened</h2>
            </div>
            <div className="panel-body space-y-2">
              {(incidents || [])
                .filter((i) => i.state?.execution_result?.pr_url)
                .slice(0, 5)
                .map((inc) => (
                  <a
                    key={inc.incident_id}
                    href={inc.state.execution_result!.pr_url!}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="block p-2 hover:bg-[var(--color-panel-2)] rounded text-xs"
                  >
                    <div className="flex items-center gap-2">
                      <GitPullRequest size={12} className="text-[var(--color-accent)]" />
                      <span className="font-mono truncate">#{inc.state.execution_result!.pr_number}</span>
                      <span className="text-[var(--color-text-dim)]">on {inc.service}</span>
                    </div>
                  </a>
                ))}
              {!incidents?.some((i) => i.state?.execution_result?.pr_url) && (
                <div className="text-xs text-[var(--color-text-dim)]">No PRs opened yet.</div>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Metrics dump */}
      {metrics && (
        <div className="panel">
          <div className="panel-header">
            <h2>System Metrics</h2>
          </div>
          <div className="panel-body">
            <pre className="text-xs font-mono text-[var(--color-text-dim)] overflow-x-auto">
              {JSON.stringify(metrics, null, 2)}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

function StatCard({ icon: Icon, label, value, accent }: {
  icon: React.ComponentType<{ size?: number; className?: string; style?: React.CSSProperties }>;
  label: string;
  value: number;
  accent: string;
}) {
  return (
    <div className="panel">
      <div className="panel-body flex items-center justify-between">
        <div>
          <div className="text-[11px] uppercase tracking-wider text-[var(--color-text-dim)]">{label}</div>
          <div className="text-3xl font-bold mt-1">{value}</div>
        </div>
        <Icon size={28} className={accent} />
      </div>
    </div>
  );
}

function LayerHealth({ name, icon: Icon, status, detail }: {
  name: string;
  icon: React.ComponentType<{ size?: number; className?: string; style?: React.CSSProperties }>;
  status: "ok" | "warn" | "danger";
  detail: string;
}) {
  const color = status === "ok" ? "var(--color-accent)" :
                status === "warn" ? "var(--color-warning)" : "var(--color-danger)";
  return (
    <div className="flex items-center justify-between py-1">
      <div className="flex items-center gap-2">
        <Icon size={12} style={{ color }} />
        <span>{name}</span>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-[var(--color-text-dim)]">{detail}</span>
        <span className="w-2 h-2 rounded-full" style={{ background: color }} />
      </div>
    </div>
  );
}
