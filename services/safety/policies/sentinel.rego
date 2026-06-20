package sentinel

# Sentinel governance policies.
#
# These policies are loaded by the OPA sidecar deployed alongside the
# safety service. The safety service POSTs the fix proposal + context to
# /v1/data/sentinel/<decision> and gets back a verdict.
#
# Verdicts:
#   allow                    — go ahead autonomously
#   require_human_approval  — post to approval channel, wait for click
#   deny                     — hard block (e.g. rollback blocked by schema migration)

# --- Default decisions ---

default allow_fix := false
default requires_human_approval := false
default rollback_blocked := false

# --- Inputs ---
# Expected input shape:
# {
#   "fix_type": "restart|scale|code_fix|rollback|roll_forward|infrastructure",
#   "target_service": "payment-api",
#   "target_namespace": "prod",
#   "confidence": 0.93,
#   "has_schema_migration": false,
#   "is_high_risk_service": false,
#   "replica_change_pct": 10,
#   "is_production_namespace": true,
#   "actor": "sentinel"
# }

# --- Constants (overridable via data.sentinel.config) ---

production_namespaces := {"prod", "production", "payments"}
high_risk_services := {"payment-api", "billing-api", "auth-service"}
confidence_threshold_autonomous := 0.92
confidence_threshold_human_review := 0.70
max_replica_change_pct := 20

# --- Rule: is this a production namespace? ---

is_production_namespace if {
    input.target_namespace in production_namespaces
}

# --- Rule: is this service considered high-risk? ---

is_high_risk_service if {
    input.target_service in high_risk_services
}

# --- Rule: would this fix touch more than 20% of replicas? ---

exceeds_replica_blast_radius if {
    input.replica_change_pct > max_replica_change_pct
}

# --- Rule: schema-migration blocks rollback (Layer 6 edge case #1) ---

rollback_blocked if {
    input.fix_type == "rollback"
    input.has_schema_migration == true
}

# --- Main decision: allow autonomously? ---

allow_fix if {
    not rollback_blocked
    is_production_namespace
    input.confidence >= confidence_threshold_autonomous
    not is_high_risk_service
    not exceeds_replica_blast_radius
    input.fix_type in {"restart", "scale", "code_fix", "roll_forward"}
}

# Also allow non-production namespaces at a lower bar
allow_fix if {
    not is_production_namespace
    input.confidence >= confidence_threshold_human_review
    not rollback_blocked
}

# --- Main decision: require human approval? ---

requires_human_approval if {
    not allow_fix
    not rollback_blocked
    input.confidence >= confidence_threshold_human_review
    some_condition_that_warrants_review
}

some_condition_that_warrants_review if {
    is_high_risk_service
}

some_condition_that_wants_review if {
    exceeds_replica_blast_radius
}

some_condition_that_warrants_review if {
    input.fix_type == "infrastructure"
}

some_condition_that_warrants_review if {
    input.fix_type == "rollback"
    is_production_namespace
}

# --- Decision: what's the final verdict? ---

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

# --- Helper: rule trace for audit log ---

rules_evaluated := [
    {"rule": "is_production_namespace", "result": is_production_namespace},
    {"rule": "is_high_risk_service", "result": is_high_risk_service},
    {"rule": "exceeds_replica_blast_radius", "result": exceeds_replica_blast_radius},
    {"rule": "rollback_blocked", "result": rollback_blocked},
    {"rule": "allow_fix", "result": allow_fix},
    {"rule": "requires_human_approval", "result": requires_human_approval},
]

# --- The decision the safety service calls ---

decision := {
    "verdict": verdict,
    "rules_evaluated": rules_evaluated,
    "confidence_at_decision": input.confidence,
    "rollback_blocked": rollback_blocked,
    "rollback_blocked_reason": merge.rollback_blocked_reason,
}

merge.rollback_blocked_reason := "schema_migration_present" if {
    rollback_blocked
}

merge.rollback_blocked_reason := null if {
    not rollback_blocked
}
