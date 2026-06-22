"""Schema validation tests — every Pydantic model must reject malformed input."""

from uuid import uuid4

import pytest

from sentinel_common import (
    AgentState,
    Anomaly,
    AnomalyCluster,
    FixProposal,
    FixType,
    Hypothesis,
    RewindEvent,
    Severity,
    Signal,
    SignalSource,
)


class TestSignal:
    def test_minimal_signal(self):
        s = Signal(source=SignalSource.PROMETHEUS, service="payment-api")
        assert s.service == "payment-api"
        assert s.namespace == "default"
        assert s.schema_version == "1.0.0"

    def test_rejects_empty_service(self):
        with pytest.raises(Exception):
            Signal(source=SignalSource.PROMETHEUS, service="")

    def test_rejects_invalid_score(self):
        with pytest.raises(Exception):
            Anomaly(
                signal_id=uuid4(),
                service="test",
                namespace="default",
                detector="test",
                score=1.5,  # > 1.0 not allowed
                dimensions={},
            )


class TestAnomalyCluster:
    def test_cluster_with_anomalies(self):
        a = Anomaly(
            signal_id=uuid4(),
            service="api",
            namespace="default",
            detector="isolation_forest",
            score=0.9,
            dimensions={"cpu": 95.0},
        )
        c = AnomalyCluster(
            service="api",
            namespace="default",
            anomalies=[a],
            summary="high cpu",
        )
        assert len(c.anomalies) == 1
        assert c.severity == Severity.WARNING  # 0.7..0.95 -> WARNING


class TestHypothesis:
    def test_hypothesis_defaults(self):
        h = Hypothesis(h_id="H1", statement="test")
        assert h.confidence == 0.0
        assert h.falsified is False
        assert h.source_files == []


class TestFixProposal:
    def test_code_fix_proposal(self):
        fp = FixProposal(
            fix_type=FixType.CODE_FIX,
            target_service="api",
            target_namespace="default",
            description="fix the bug",
            files_to_change=[{"path": "src/main.py", "new_content": "print('hello')"}],
            proposed_confidence=0.95,
        )
        assert fp.fix_type == FixType.CODE_FIX
        assert fp.requires_sandbox is True
        assert len(fp.files_to_change) == 1

    def test_rejects_invalid_confidence(self):
        with pytest.raises(Exception):
            FixProposal(
                fix_type=FixType.RESTART,
                target_service="api",
                target_namespace="default",
                description="restart",
                proposed_confidence=2.0,  # > 1.0
            )


class TestAgentState:
    def test_state_rewind_preserves_audit_trail(self):
        """Layer 6 edge case #3 — rewind must preserve audit trail."""
        state = AgentState(
            incident_id=uuid4(),
            cluster=AnomalyCluster(
                service="api",
                namespace="default",
                anomalies=[],
            ),
            hypotheses=[Hypothesis(h_id="H1", statement="old")],
            agent_trace=[{"agent": "investigator", "iter": 1}],
        )
        # Simulate rewind
        state.rewind_events.append(
            RewindEvent(
                raised_by="evidence_gatherer",
                reason="upstream is down",
                new_context={"upstream": "billing-api"},
            )
        )
        state.hypotheses = []  # wiped
        state.agent_trace.append({"agent": "evidence_gatherer", "action": "rewind"})

        # Audit trail preserved
        assert len(state.rewind_events) == 1
        assert len(state.agent_trace) == 2
        assert len(state.hypotheses) == 0  # wiped

    def test_state_max_iterations(self):
        state = AgentState(
            incident_id=uuid4(),
            cluster=AnomalyCluster(service="api", namespace="default", anomalies=[]),
        )
        assert state.max_iterations == 5
        assert state.iterations == 0


class TestRewindEvent:
    def test_rewind_serializes_to_json(self):
        ev = RewindEvent(
            raised_by="analyst",
            reason="all falsified",
            new_context={"foo": "bar"},
        )
        d = ev.model_dump(mode="json")
        assert d["raised_by"] == "analyst"
        assert d["new_context"]["foo"] == "bar"
