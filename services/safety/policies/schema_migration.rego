package sentinel

# Schema-migration policy — the database-aware governance edge case.
#
# If a deployment included a schema migration, we HARD-BLOCK rollbacks. The
# system must roll-forward instead, or escalate to a human.
#
# This implements Layer 6 edge case #1 from the architecture spec.

default allow_rollback := true

allow_rollback if {
    not input.has_schema_migration
}

deny_rollback_reason if {
    input.has_schema_migration
    input.fix_type == "rollback"
}

deny_rollback_reason contains "schema_migration_blocks_rollback" if {
    input.has_schema_migration
    input.fix_type == "rollback"
}
