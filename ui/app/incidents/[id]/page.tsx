"use client";

import { useIncident } from "@/lib/api";
import { statusBadgeClass, severityBadgeClass, timeAgo } from "@/lib/utils";
import { useParams } from "next/navigation";
import Link from "next/link";
import { ArrowLeft, GitPullRequest, AlertTriangle, CheckCircle2, XCircle, Clock } from "lucide-react";

export default function IncidentDetailPage() {
  const params = useParams<{ id: string }>();
  const { data: inc, isLoading } = useIncident(params.id);

  if (isLoading) {
    return <div className="text-center text-[var(--color-text-dim)] p-8">Loading...</div>;
  }
  if (!inc) {
    return <div className="text-center text-[var(--color-danger)] p-8">Incident not found</div>;
  }

  return (
    <div className="space-y-6">
      <div>
        <Link href="/incidents" className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-accent)] flex items-center gap-1 mb-3">
          <ArrowLeft size={12} /> All incidents
        </Link>
        <div className="flex items-start justify-between">
          <div>
            <h1 className="text-xl font-semibold flex items-center gap-3">
              <span className={`badge ${severityBadgeClass(inc.severity)}`}>{inc.severity}</span>
              {inc.service}
              <span className={`badge ${statusBadgeClass(inc.status)}`}>
                {inc.status.replace(/_/g, " ")}
              </span>
            </h1>
            <div className="text-xs text-[var(--color-text-dim)] mt-1 font-mono">
              incident_id: {inc.incident_id}
            </div>
          </div>
          <div className="text-xs text-[var(--color-text-dim)]">
            <div>Created: {timeAgo(inc.created_at)}</div>
            <div>Updated: {timeAgo(inc.updated_at)}</div>
          </div>
        </div>
      </div>

      {/* Root cause + Fix */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div className="panel">
          <div className="panel-header"><h2>Root Cause</h2></div>
          <div className="panel-body space-y-3">
            {inc.state.root_cause ? (
              <>
                <div>
                  <div className="text-[11px] uppercase tracking-wider text-[var(--color-text-dim)] mb-1">Statement</div>
                  <div>{inc.state.root_cause.statement}</div>
                </div>
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <div className="text-[11px] uppercase text-[var(--color-text-dim)]">Category</div>
                    <div className="font-mono">{inc.state.root_cause.category}</div>
                  </div>
                  <div>
                    <div className="text-[11px] uppercase text-[var(--color-text-dim)]">Confidence</div>
                    <div className={`font-mono ${(inc.state.root_cause.confidence || 0) >= 0.92 ? "text-[var(--color-accent)]" : "text-[var(--color-warning)]"}`}>
                      {(inc.state.root_cause.confidence * 100).toFixed(1)}%
                    </div>
                  </div>
                  <div>
                    <div className="text-[11px] uppercase text-[var(--color-text-dim)]">Schema Migration</div>
                    <div className="font-mono">
                      {inc.state.root_cause.has_schema_migration ? (
                        <span className="text-[var(--color-danger)]">YES (blocks rollback)</span>
                      ) : (
                        <span className="text-[var(--color-text-dim)]">no</span>
                      )}
                    </div>
                  </div>
                  <div>
                    <div className="text-[11px] uppercase text-[var(--color-text-dim)]">Source Files</div>
                    <div className="font-mono text-xs">
                      {inc.state.root_cause.source_files.length} file(s)
                    </div>
                  </div>
                </div>
                {inc.state.root_cause.source_files.length > 0 && (
                  <div>
                    <div className="text-[11px] uppercase text-[var(--color-text-dim)] mb-1">Files Identified</div>
                    <div className="font-mono text-xs space-y-1">
                      {inc.state.root_cause.source_files.map((f) => (
                        <div key={f} className="text-[var(--color-info)]">{f}</div>
                      ))}
                    </div>
                  </div>
                )}
              </>
            ) : (
              <div className="text-[var(--color-text-dim)] text-sm">No root cause identified yet.</div>
            )}
          </div>
        </div>

        <div className="panel">
          <div className="panel-header"><h2>Fix Proposal</h2></div>
          <div className="panel-body space-y-3">
            {inc.state.fix_proposal ? (
              <>
                <div className="flex items-center gap-2">
                  <span className="badge badge-info">{inc.state.fix_proposal.fix_type}</span>
                  <span className="text-xs text-[var(--color-text-dim)]">
                    proposed confidence: {(inc.state.fix_proposal.proposed_confidence * 100).toFixed(1)}%
                  </span>
                </div>
                <div>{inc.state.fix_proposal.description}</div>
                {inc.state.execution_result?.pr_url && (
                  <a
                    href={inc.state.execution_result.pr_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-2 text-sm text-[var(--color-accent)] hover:underline"
                  >
                    <GitPullRequest size={14} />
                    PR #{inc.state.execution_result.pr_number}
                  </a>
                )}
                {inc.state.execution_result?.sandbox_verified !== undefined && (
                  <div className="flex items-center gap-2 text-xs">
                    {inc.state.execution_result.sandbox_verified ? (
                      <><CheckCircle2 size={12} className="text-[var(--color-accent)]" /> Sandbox verified</>
                    ) : (
                      <><XCircle size={12} className="text-[var(--color-danger)]" /> Sandbox FAILED</>
                    )}
                  </div>
                )}
                {inc.state.execution_result?.error && (
                  <div className="text-xs text-[var(--color-danger)] font-mono">
                    {inc.state.execution_result.error}
                  </div>
                )}
              </>
            ) : (
              <div className="text-[var(--color-text-dim)] text-sm">No fix proposed yet.</div>
            )}
          </div>
        </div>
      </div>

      {/* Governance decision */}
      {inc.state.governance_decision && (
        <div className="panel">
          <div className="panel-header"><h2>Governance Decision</h2></div>
          <div className="panel-body space-y-3">
            <div className="flex items-center gap-3">
              <span className={`badge ${
                inc.state.governance_decision.verdict === "allow" ? "badge-ok" :
                inc.state.governance_decision.verdict === "require_human_approval" ? "badge-warn" :
                "badge-danger"
              }`}>
                {inc.state.governance_decision.verdict.replace(/_/g, " ")}
              </span>
              <span className="text-xs text-[var(--color-text-dim)] font-mono">
                confidence at decision: {(inc.state.governance_decision.confidence_at_decision * 100).toFixed(1)}%
              </span>
            </div>
            {inc.state.governance_decision.rollback_blocked_reason && (
              <div className="flex items-center gap-2 text-sm text-[var(--color-danger)]">
                <AlertTriangle size={14} />
                Rollback blocked: {inc.state.governance_decision.rollback_blocked_reason}
              </div>
            )}
            <div>
              <div className="text-[11px] uppercase text-[var(--color-text-dim)] mb-1">Rules Evaluated</div>
              <div className="font-mono text-xs space-y-1">
                {inc.state.governance_decision.rules_evaluated.map((r, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <span className={r.result ? "text-[var(--color-accent)]" : "text-[var(--color-text-dim)]"}>
                      {r.result ? "✓" : "✗"}
                    </span>
                    <span>{r.rule}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Hypotheses */}
      <div className="panel">
        <div className="panel-header"><h2>Hypotheses ({inc.state.hypotheses?.length || 0})</h2></div>
        <div className="panel-body space-y-2">
          {(inc.state.hypotheses || []).map((h) => (
            <div key={h.h_id} className={`p-2 rounded border ${h.falsified ? "border-[var(--color-danger)] opacity-50" : "border-[var(--color-border)]"}`}>
              <div className="flex items-start gap-2">
                <span className="font-mono text-xs text-[var(--color-text-dim)]">{h.h_id}</span>
                <div className="flex-1">
                  <div className="text-sm">{h.statement}</div>
                  <div className="flex items-center gap-3 mt-1 text-xs">
                    <span className="font-mono">confidence: {(h.confidence * 100).toFixed(1)}%</span>
                    {h.falsified && <span className="text-[var(--color-danger)]">FALSIFIED</span>}
                  </div>
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Agent trace timeline */}
      <div className="panel">
        <div className="panel-header"><h2>Agent Timeline</h2></div>
        <div className="panel-body">
          <div className="space-y-3">
            {(inc.state.agent_trace || []).map((entry, i) => (
              <div key={i} className="flex items-start gap-3 text-xs font-mono">
                <div className="w-2 h-2 rounded-full bg-[var(--color-accent)] mt-1.5 flex-shrink-0" />
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-[var(--color-accent)]">{String(entry.agent ?? "")}</span>
                    {entry.action && <span className="text-[var(--color-text-dim)]">/ {String(entry.action)}</span>}
                  </div>
                  <pre className="text-[var(--color-text-dim)] mt-1 whitespace-pre-wrap">
                    {JSON.stringify(entry, null, 2)}
                  </pre>
                </div>
              </div>
            ))}
            {(inc.state.rewind_events || []).length > 0 && (
              <div className="mt-4 p-3 border border-[var(--color-warning)] rounded">
                <div className="text-xs text-[var(--color-warning)] mb-2 flex items-center gap-2">
                  <Clock size={12} /> Rewind Events
                </div>
                {(inc.state.rewind_events || []).map((e, i) => (
                  <div key={i} className="text-xs font-mono text-[var(--color-text-dim)]">
                    {String(e.raised_by ?? "")}: {String(e.reason ?? "")}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Evidence */}
      <div className="panel">
        <div className="panel-header"><h2>Evidence ({inc.state.evidence?.length || 0})</h2></div>
        <div className="overflow-x-auto max-h-96 overflow-y-auto">
          <table className="dense-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Source</th>
                <th>Supports</th>
                <th>Contradicts</th>
                <th>Query</th>
                <th>Result</th>
              </tr>
            </thead>
            <tbody>
              {(inc.state.evidence || []).map((e) => (
                <tr key={e.e_id}>
                  <td className="font-mono text-xs">{e.e_id}</td>
                  <td className="text-xs">{e.source}</td>
                  <td className="font-mono text-xs text-[var(--color-accent)]">{e.supports_hypothesis_id || "—"}</td>
                  <td className="font-mono text-xs text-[var(--color-danger)]">{e.contradicts_hypothesis_id || "—"}</td>
                  <td className="font-mono text-xs max-w-xs truncate">{e.query}</td>
                  <td className="font-mono text-xs max-w-md truncate text-[var(--color-text-dim)]">
                    {JSON.stringify(e.result).slice(0, 200)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* Postmortem */}
      {inc.postmortem && (
        <div className="panel">
          <div className="panel-header"><h2>Postmortem</h2></div>
          <div className="panel-body">
            <pre className="text-xs font-mono whitespace-pre-wrap">{inc.postmortem}</pre>
          </div>
        </div>
      )}
    </div>
  );
}
