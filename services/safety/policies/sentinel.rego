package sentinel

# Sentinel governance policies — all in one file to avoid rule conflicts.
#
# The safety service POSTs the fix proposal + context to
# /v1/data/sentinel/decision and gets back a verdict.

import rego.v1

# --- Constants ---

production_namespaces := {"prod", "production", "payments"}
high_risk_services := {"payment-api", "billing-api", "auth-service"}
confidence_threshold_autonomous := 0.92
max_replica_change_pct := 20

# --- Derived facts ---

is_production_namespace if {
    input.target_namespace in production_namespaces
}

is_high_risk_service if {
    input.target_service in high_risk_services
}

exceeds_replica_blast_radius if {
    input.replica_change_pct > max_replica_change_pct
}

rollback_blocked if {
    input.fix_type == "rollback"
    input.has_schema_migration == true
}

# --- Main decision rules ---

# Production namespace: require high confidence + safe fix type + not high risk
allow_fix if {
    is_production_namespace
    input.confidence >= confidence_threshold_autonomous
    not is_high_risk_service
    not exceeds_replica_blast_radius
    not rollback_blocked
    input.fix_type in {"restart", "scale", "code_fix", "roll_forward"}
}

# Non-production namespace: ALLOW everything except rollbacks with schema migrations.
# This is intentionally permissive for testing — in production, set this to
# require a minimum confidence threshold.
allow_fix if {
    not is_production_namespace
    not rollback_blocked
}

# --- Final verdict ---

verdict := "deny" if {
    rollback_blocked
}

verdict := "allow" if {
    allow_fix
}

verdict := "deny" if {
    not allow_fix
    not rollback_blocked
}

# --- Audit trail ---

rules_evaluated := [
    {"rule": "is_production_namespace", "result": is_production_namespace},
    {"rule": "is_high_risk_service", "result": is_high_risk_service},
    {"rule": "exceeds_replica_blast_radius", "result": exceeds_replica_blast_radius},
    {"rule": "rollback_blocked", "result": rollback_blocked},
    {"rule": "allow_fix", "result": allow_fix},
]

rollback_blocked_reason contains "schema_migration_present" if {
    rollback_blocked
}

decision := {
    "verdict": verdict,
    "rules_evaluated": rules_evaluated,
    "confidence_at_decision": input.confidence,
    "rollback_blocked": rollback_blocked,
}
