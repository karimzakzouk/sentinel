package sentinel

# Blast-radius policy — blocks fixes that would touch too many replicas at once.

default within_blast_radius := true

within_blast_radius if {
    input.replica_change_pct <= data.sentinel.config.max_replica_change_pct
}

# A "high blast radius" action requires human approval even if confidence is high.
requires_human_approval if {
    not within_blast_radius
}
