package sentinel

# Standalone confidence-gate policy — used by the safety service to decide
# whether a fix can proceed autonomously in a non-production namespace.
#
# This is intentionally a separate file from sentinel.rego so that the
# confidence threshold can be updated independently of the high-risk-service
# list.

default allow := false

allow if {
    input.confidence >= data.sentinel.config.confidence_threshold_autonomous
    not input.has_schema_migration
}
