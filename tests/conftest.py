"""Pytest config — make every service importable in tests."""

import sys
from pathlib import Path

ROOT = Path(__file__).parent.parent.resolve()

# Add the common package
sys.path.insert(0, str(ROOT / "services" / "common"))

# Add each service directory so test imports like `from feedback.beta_tracker`
# or `from reasoning.context` work.
for svc in ["ingestion", "detection", "reasoning", "safety", "execution", "feedback", "operator"]:
    sys.path.insert(0, str(ROOT / "services" / svc))

# Make sure services/common is treated as the sentinel_common package's parent
sys.path.insert(0, str(ROOT / "services"))
