"use client";

const POLICIES = [
  {
    name: "allow_autonomous",
    file: "sentinel.rego",
    description: "Allow a fix to proceed autonomously without human approval",
    rule: "Allow if: not rollback_blocked AND is_production_namespace AND confidence >= 0.92 AND not high_risk_service AND not exceeds_replica_blast_radius AND fix_type in {restart, scale, code_fix, roll_forward}",
  },
  {
    name: "require_human_approval",
    file: "sentinel.rego",
    description: "Require human approval via Slack before proceeding",
    rule: "Require approval if: not allow AND (is_high_risk_service OR exceeds_replica_blast_radius OR fix_type == infrastructure OR (fix_type == rollback AND is_production_namespace))",
  },
  {
    name: "deny_rollback_with_schema_migration",
    file: "schema_migration.rego",
    description: "Hard-block rollback if the previous deploy included a DB schema migration (forces roll-forward)",
    rule: "Deny if: fix_type == rollback AND has_schema_migration == true",
  },
  {
    name: "blast_radius_limit",
    file: "blast_radius.rego",
    description: "Block fixes that touch more than 20% of replicas",
    rule: "Requires human approval if: replica_change_pct > 20",
  },
  {
    name: "confidence_gate",
    file: "confidence.rego",
    description: "Confidence threshold for autonomous execution in non-production namespaces",
    rule: "Allow if: confidence >= 0.92 AND not has_schema_migration",
  },
];

export default function PoliciesPage() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Governance Policies</h1>
        <p className="text-xs text-[var(--color-text-dim)] mt-1">
          All policies are written in <a href="https://www.openpolicyagent.org/docs/latest/policy-language/" target="_blank" rel="noopener noreferrer" className="text-[var(--color-accent)] hover:underline">Rego</a> and evaluated by Open Policy Agent.
        </p>
      </div>
      <div className="space-y-3">
        {POLICIES.map((p) => (
          <div key={p.name} className="panel">
            <div className="panel-header">
              <h2>{p.name}</h2>
              <span className="font-mono text-xs text-[var(--color-text-dim)]">{p.file}</span>
            </div>
            <div className="panel-body space-y-2">
              <div className="text-sm text-[var(--color-text-dim)]">{p.description}</div>
              <pre className="text-xs font-mono p-2 bg-[var(--color-bg)] rounded border border-[var(--color-border)] whitespace-pre-wrap">
                {p.rule}
              </pre>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
