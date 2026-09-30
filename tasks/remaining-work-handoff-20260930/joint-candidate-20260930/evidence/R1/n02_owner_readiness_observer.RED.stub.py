from __future__ import annotations
import asyncio
from contextlib import suppress
from dataclasses import dataclass, field
from typing import Awaitable, Callable, List
OWNER_READY_TOKEN = "owner-ready"
class OwnerReadinessError(AssertionError):
    def __init__(self, stage, detail=""):
        self.stage=stage; self.detail=detail; super().__init__(f"owner-readiness {stage}: {detail}")
@dataclass
class Observation:
    accepted: bool
    late_but_accepted: bool=False
    soft_diagnostic_fired: bool=False
    elapsed_s: float=0.0
    diagnostics: List[str]=field(default_factory=list)
async def observe_owner_ready(*, readline, durable_ready, failure_present, expected_token=OWNER_READY_TOKEN, soft_s=5.0, watchdog_s=30.0):
    # RED STUB: OLD hard-cancel behavior + no cross-validation of durable identity
    if soft_s > watchdog_s: raise ValueError("soft diagnostic must be within the scenario watchdog")
    raw = await asyncio.wait_for(readline(), soft_s)
    return Observation(accepted=True)
async def _legacy_hard_line(readline, *, hard_s):
    raw = await asyncio.wait_for(readline(), hard_s)
    return raw.decode(errors="replace").strip()
