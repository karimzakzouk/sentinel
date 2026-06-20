"use client";

import { useIncidents } from "@/lib/api";
import { statusBadgeClass, severityBadgeClass, timeAgo } from "@/lib/utils";
import Link from "next/link";

export default function IncidentsPage() {
  const { data: incidents, isLoading } = useIncidents();
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">All Incidents</h1>
        <div className="text-xs text-[var(--color-text-dim)]">
          {(incidents || []).length} total
        </div>
      </div>
      <div className="panel">
        <div className="overflow-x-auto max-h-[calc(100vh-200px)] overflow-y-auto">
          {isLoading ? (
            <div className="p-8 text-center text-[var(--color-text-dim)]">Loading...</div>
          ) : (
            <table className="dense-table">
              <thead>
                <tr>
                  <th>Created</th>
                  <th>Severity</th>
                  <th>Service</th>
                  <th>Namespace</th>
                  <th>Status</th>
                  <th>Root Cause</th>
                  <th>Fix Type</th>
                  <th>Confidence</th>
                  <th>PR</th>
                </tr>
              </thead>
              <tbody>
                {(incidents || []).map((inc) => (
                  <tr key={inc.incident_id}>
                    <td className="whitespace-nowrap text-[var(--color-text-dim)] text-xs">
                      {timeAgo(inc.created_at)}
                    </td>
                    <td>
                      <span className={`badge ${severityBadgeClass(inc.severity)}`}>{inc.severity}</span>
                    </td>
                    <td>
                      <Link href={`/incidents/${inc.incident_id}`} className="font-mono text-[var(--color-accent)] hover:underline">
                        {inc.service}
                      </Link>
                    </td>
                    <td className="font-mono text-xs text-[var(--color-text-dim)]">{inc.namespace}</td>
                    <td>
                      <span className={`badge ${statusBadgeClass(inc.status)}`}>
                        {inc.status.replace(/_/g, " ")}
                      </span>
                    </td>
                    <td className="max-w-md truncate text-[var(--color-text-dim)] text-xs">
                      {inc.state?.root_cause?.statement || "—"}
                    </td>
                    <td className="text-xs font-mono">
                      {inc.state?.fix_proposal?.fix_type || "—"}
                    </td>
                    <td className="font-mono text-xs">
                      {inc.state?.root_cause ? (
                        <span className={inc.state.root_cause.confidence >= 0.92 ? "text-[var(--color-accent)]" : "text-[var(--color-warning)]"}>
                          {(inc.state.root_cause.confidence * 100).toFixed(1)}%
                        </span>
                      ) : "—"}
                    </td>
                    <td>
                      {inc.state?.execution_result?.pr_url ? (
                        <a
                          href={inc.state.execution_result.pr_url}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="text-xs text-[var(--color-accent)] hover:underline font-mono"
                        >
                          #{inc.state.execution_result.pr_number}
                        </a>
                      ) : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}
