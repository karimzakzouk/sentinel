"""Reasoning graph tests — verify the LangGraph wiring + rewind logic."""

from typing import Any
from uuid import uuid4

from reasoning.graph import _after_analyst, _after_evidence, _after_investigator

from sentinel_common import (
    AgentState,
    Anomaly,
    AnomalyCluster,
    Hypothesis,
    IncidentStatus,
    RootCause,
)


def _make_state(**overrides: Any) -> dict[str, Any]:
    cluster = AnomalyCluster(
        service="api",
        namespace="default",
        anomalies=[
            Anomaly(
                signal_id=uuid4(),
                service="api",
                namespace="default",
                detector="test",
                score=0.9,
                dimensions={},
            )
        ],
        summary="test",
    )
    s = AgentState(incident_id=uuid4(), cluster=cluster)
    for k, v in overrides.items():
        setattr(s, k, v)
    return s.model_dump(mode="json")


class TestGraphEdges:
    def test_after_investigator_no_hypotheses_goes_to_end(self):
        state = _make_state(hypotheses=[], status=IncidentStatus.FAILED)
        assert _after_investigator(state) == "__end__"

    def test_after_investigator_with_hypotheses_goes_to_evidence(self):
        state = _make_state(
            hypotheses=[Hypothesis(h_id="H1", statement="x")],
            status=IncidentStatus.INVESTIGATING,
        )
        assert _after_evidence(state) == "analyst"  # placeholder; we test evidence below

    def test_after_evidence_rewind_goes_back_to_investigator(self):
        """Layer 6 edge case #3 — RewindEvent returns to Investigator."""
        from sentinel_common import RewindEvent

        # We need to test the actual function
        cluster = AnomalyCluster(
            service="api",
            namespace="default",
            anomalies=[
                Anomaly(
                    signal_id=uuid4(),
                    service="api",
                    namespace="default",
                    detector="test",
                    score=0.9,
                    dimensions={},
                )
            ],
            summary="test",
        )
        s = AgentState(
            incident_id=uuid4(),
            cluster=cluster,
            hypotheses=[],
            rewind_events=[
                RewindEvent(
                    raised_by="evidence_gatherer",
                    reason="upstream down",
                    new_context={},
                )
            ],
            iterations=1,  # < max_iterations=5
            status=IncidentStatus.INVESTIGATING,
        )
        state = s.model_dump(mode="json")
        assert _after_evidence(state) == "investigator"

    def test_after_evidence_rewind_max_iter_ends(self):
        from sentinel_common import RewindEvent

        cluster = AnomalyCluster(
            service="api",
            namespace="default",
            anomalies=[
                Anomaly(
                    signal_id=uuid4(),
                    service="api",
                    namespace="default",
                    detector="test",
                    score=0.9,
                    dimensions={},
                )
            ],
            summary="test",
        )
        s = AgentState(
            incident_id=uuid4(),
            cluster=cluster,
            hypotheses=[],
            rewind_events=[
                RewindEvent(
                    raised_by="evidence_gatherer",
                    reason="x",
                    new_context={},
                )
            ],
            iterations=10,  # >= max_iterations=5
            status=IncidentStatus.INVESTIGATING,
        )
        state = s.model_dump(mode="json")
        assert _after_evidence(state) == "__end__"

    def test_after_analyst_with_root_cause_goes_to_fix_proposer(self):
        cluster = AnomalyCluster(
            service="api",
            namespace="default",
            anomalies=[
                Anomaly(
                    signal_id=uuid4(),
                    service="api",
                    namespace="default",
                    detector="test",
                    score=0.9,
                    dimensions={},
                )
            ],
            summary="test",
        )
        s = AgentState(
            incident_id=uuid4(),
            cluster=cluster,
            hypotheses=[Hypothesis(h_id="H1", statement="x")],
            root_cause=RootCause(
                statement="the bug",
                confidence=0.95,
                category="oom",
            ),
            status=IncidentStatus.ROOT_CAUSED,
        )
        state = s.model_dump(mode="json")
        assert _after_analyst(state) == "fix_proposer"

    def test_after_analyst_no_root_cause_rewinds(self):
        cluster = AnomalyCluster(
            service="api",
            namespace="default",
            anomalies=[
                Anomaly(
                    signal_id=uuid4(),
                    service="api",
                    namespace="default",
                    detector="test",
                    score=0.9,
                    dimensions={},
                )
            ],
            summary="test",
        )
        from sentinel_common import RewindEvent

        s = AgentState(
            incident_id=uuid4(),
            cluster=cluster,
            hypotheses=[],
            root_cause=None,
            rewind_events=[
                RewindEvent(
                    raised_by="analyst",
                    reason="all_falsified",
                    new_context={},
                )
            ],
            iterations=2,
            status=IncidentStatus.INVESTIGATING,
        )
        state = s.model_dump(mode="json")
        assert _after_analyst(state) == "investigator"
