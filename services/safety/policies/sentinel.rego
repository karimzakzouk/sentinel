package sentinel

# Sentinel governance policies — all in one file to avoid rule conflicts.
#
# The safety service POSTs the fix proposal + context to
# /v1/data/sentinel/decision and gets back a verdict.

import rego.v1

default allow_fix := false
default requires_human_approval := false
default rollback_blocked := false

# --- Constants ---

production_namespaces := {"prod", "production", "payments"}
high_risk_services := {"payment-api", "billing-api", "auth-service"}
confidence_threshold_autonomous := 0.92
confidence_threshold_human_review := 0.70
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

allow_fix if {
    not rollback_blocked
    is_production_namespace
    input.confidence >= confidence_threshold_autonomous
    not is_high_risk_service
    not exceeds_replica_blast_radius
    input.fix_type in {"restart", "scale", "code_fix", "roll_forward"}
}

allow_fix if {
    not is_production_namespace
    input.confidence >= confidence_threshold_human_review
    not rollback_blocked
}

requires_human_approval if {
    not allow_fix
    not rollback_blocked
    input.confidence >= confidence_threshold_human_review
    is_high_risk_service
}

requires_human_approval if {
    not allow_fix
    not rollback_blocked
    input.confidence >= confidence_threshold_human_review
    exceeds_replica_blast_radius
}

requires_human_approval if {
    not allow_fix
    not rollback_blocked
    input.confidence >= confidence_threshold_human_review
    input.fix_type == "infrastructure"
}

requires_human_approval if {
    not allow_fix
    not rollback_blocked
    input.confidence >= confidence_threshold_human_review
    input.fix_type == "rollback"
    is_production_namespace
}

# --- Final verdict ---

verdict := "deny" if {
    rollback_blocked
}

verdict := "require_human_approval" if {
    requires_human_approval
    not rollback_blocked
}

verdict := "allow" if {
    allow_fix
}

verdict := "deny" if {
    not allow_fix
    not requires_human_approval
    not rollback_blocked
    input.confidence < confidence_threshold_human_review
}

# --- Audit trail ---

rules_evaluated := [
    {"rule": "is_production_namespace", "result": is_production_namespace},
    {"rule": "is_high_risk_service", "result": is_high_risk_service},
    {"rule": "exceeds_replica_blast_radius", "result": exceeds_replica_blast_radius},
    {"rule": "rollback_blocked", "result": rollback_blocked},
    {"rule": "allow_fix", "result": allow_fix},
    {"rule": "requires_human_approval", "result": requires_human_approval},
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
