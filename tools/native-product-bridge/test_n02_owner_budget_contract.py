"""Observer budget contracts; helper only, no product/Provider/TUI claims."""
import asyncio
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import n02_owner_readiness_observer as observer


def test_short_remaining_budget_is_valid_late_start():
    async def run():
        calls = []
        async def read():
            calls.append(1)
            return b"owner-ready\n"
        result = await observer.observe_owner_ready(
            readline=read, durable_ready=lambda: True, failure_present=lambda: False,
            soft_s=5.0, watchdog_s=30.0, remaining_s=0.1)
        assert result.accepted is True
        assert result.soft_diagnostic_fired is False
        assert result.late_but_accepted is False
        assert len(calls) == 1
    asyncio.run(run())


def test_short_remaining_never_ready_is_watchdog_not_configuration_error():
    async def run():
        calls = []
        before = set(asyncio.all_tasks())
        async def read():
            calls.append(1)
            await asyncio.Event().wait()
        with pytest.raises(observer.OwnerReadinessError) as caught:
            await observer.observe_owner_ready(
                readline=read, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=5.0, watchdog_s=30.0, remaining_s=0.03)
        assert caught.value.stage == "scenario-watchdog"
        assert len(calls) == 1
        assert set(asyncio.all_tasks()) == before
    asyncio.run(run())


@pytest.mark.parametrize("remaining", [0.0, -1.0])
def test_exhausted_remaining_is_explicit_unobserved_timeout(remaining):
    async def run():
        calls = []
        async def read():
            calls.append(1)
            return b"owner-ready\n"
        with pytest.raises(observer.OwnerReadinessError) as caught:
            await observer.observe_owner_ready(
                readline=read, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=5.0, watchdog_s=30.0, remaining_s=remaining)
        assert caught.value.stage == "scenario-watchdog"
        assert "before observation" in caught.value.detail
        assert calls == []
    asyncio.run(run())


@pytest.mark.parametrize("arguments", [
    {"soft_s": 0}, {"soft_s": -1}, {"soft_s": float("nan")}, {"soft_s": float("inf")},
    {"watchdog_s": 0}, {"watchdog_s": -1}, {"watchdog_s": float("nan")}, {"watchdog_s": float("inf")},
    {"soft_s": 10, "watchdog_s": 5},
    {"remaining_s": float("nan")}, {"remaining_s": float("inf")}, {"remaining_s": 31},
])
def test_invalid_configuration_is_rejected_before_read(arguments):
    async def run():
        calls = []
        async def read():
            calls.append(1)
            return b"owner-ready\n"
        with pytest.raises(ValueError):
            await observer.observe_owner_ready(
                readline=read, durable_ready=lambda: True, failure_present=lambda: False,
                **arguments)
        assert calls == []
    asyncio.run(run())


def test_caller_cancel_propagates_and_converges_single_read():
    async def run():
        entered = asyncio.Event()
        canceled = asyncio.Event()
        calls = []
        before = set(asyncio.all_tasks())
        async def read():
            calls.append(1)
            entered.set()
            try:
                await asyncio.Event().wait()
            finally:
                canceled.set()
        observation = asyncio.create_task(observer.observe_owner_ready(
            readline=read, durable_ready=lambda: True, failure_present=lambda: False))
        await entered.wait()
        observation.cancel()
        with pytest.raises(asyncio.CancelledError):
            await observation
        assert canceled.is_set()
        assert len(calls) == 1
        assert set(asyncio.all_tasks()) == before
    asyncio.run(run())


def test_durable_checks_must_not_accept_after_watchdog(monkeypatch):
    async def run():
        clock = SimpleNamespace(now=0.0)
        facade = SimpleNamespace(
            get_running_loop=lambda: SimpleNamespace(time=lambda: clock.now),
            ensure_future=asyncio.ensure_future, wait_for=asyncio.wait_for,
            shield=asyncio.shield, TimeoutError=asyncio.TimeoutError,
            CancelledError=asyncio.CancelledError)
        monkeypatch.setattr(observer, "asyncio", facade)
        async def read():
            return b"owner-ready\n"
        def durable():
            clock.now = 0.3
            return True
        with pytest.raises(observer.OwnerReadinessError) as caught:
            await observer.observe_owner_ready(
                readline=read, durable_ready=durable, failure_present=lambda: False,
                soft_s=0.1, watchdog_s=0.2)
        assert caught.value.stage == "scenario-watchdog"
    asyncio.run(run())


def test_real_clock_old_five_seconds_rejects_legal_six_point_five_ready():
    async def run():
        counts = {"old": 0, "new": 0}
        async def read(which):
            counts[which] += 1
            await asyncio.sleep(6.5)  # Controlled protocol event, not a retry/green padding sleep.
            return b"owner-ready\n"
        async def old():
            with pytest.raises(asyncio.TimeoutError):
                await observer._legacy_hard_line(lambda: read("old"), hard_s=5.0)
        async def new():
            result = await observer.observe_owner_ready(
                readline=lambda: read("new"), durable_ready=lambda: True,
                failure_present=lambda: False, soft_s=5.0, watchdog_s=8.0)
            assert result.accepted and result.soft_diagnostic_fired and result.late_but_accepted
            assert 6.5 <= result.elapsed_s < 8.0
        await asyncio.gather(old(), new())
        assert counts == {"old": 1, "new": 1}
    asyncio.run(run())
