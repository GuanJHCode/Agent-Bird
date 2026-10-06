"""R1/D1 N02 owner-ready 观察协议 — 确定性负例与真 RED/GREEN 对照.

分类: 观察协议返修的强制回归 (opt-in 时序测量在 run_n02 harness, 不在此)。
不依赖 pytest-asyncio: 每个 async 体用 asyncio.run 驱动, <1s 内确定收敛。
"""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent))

from n02_owner_readiness_observer import (  # noqa: E402
    OwnerReadinessError,
    _legacy_hard_line,
    observe_owner_ready,
)


def make_readline(raw: bytes, delay: float, calls: list):
    async def _factory() -> bytes:
        calls.append(1)
        await asyncio.sleep(delay)
        return raw
    return _factory


def test_legacy_hard_line_false_rejects_late_but_accepted():
    # 真 RED: 旧硬 5s 观察在「超软阈值(0.35s>0.2s)但仍在看门狗/产品准入之内」的合法准入上误拒。
    calls: list = []

    async def body():
        readline = make_readline(b"owner-ready\n", 0.35, calls)
        with pytest.raises(asyncio.TimeoutError):
            await _legacy_hard_line(readline, hard_s=0.2)
        return sum(calls)

    assert asyncio.run(body()) == 1  # 旧协议也只起一次 readline(但把它取消)


def test_new_protocol_accepts_late_but_accepted():
    # GREEN: 新协议同一场景不误拒, 标 late-but-accepted, 且全程只有一个 readline。
    calls: list = []

    async def body():
        readline = make_readline(b"owner-ready\n", 0.35, calls)
        obs = await observe_owner_ready(
            readline=readline, durable_ready=lambda: True, failure_present=lambda: False,
            soft_s=0.2, watchdog_s=2.0,
        )
        return obs, sum(calls)

    obs, call_count = asyncio.run(body())
    assert obs.accepted and obs.late_but_accepted and obs.soft_diagnostic_fired
    assert call_count == 1


def test_new_protocol_accepts_fast_ready_not_late():
    calls: list = []

    async def body():
        readline = make_readline(b"owner-ready\n", 0.01, calls)
        return await observe_owner_ready(
            readline=readline, durable_ready=lambda: True, failure_present=lambda: False,
            soft_s=0.2, watchdog_s=2.0,
        )

    obs = asyncio.run(body())
    assert obs.accepted and not obs.late_but_accepted and not obs.soft_diagnostic_fired


def test_never_ready_hits_watchdog_and_converges():
    # 永不 ready → 总看门狗失败; finally 收敛自建等待任务, 无泄漏。
    async def body():
        async def _never():
            await asyncio.sleep(5)
            return b"owner-ready\n"
        before = {t for t in asyncio.all_tasks() if t is not asyncio.current_task()}
        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=_never, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=0.15, watchdog_s=0.4,
            )
        stage = exc.value.stage
        await asyncio.sleep(0)
        after = {t for t in asyncio.all_tasks() if t is not asyncio.current_task()}
        return stage, before == after

    stage, no_leak = asyncio.run(body())
    assert stage == "scenario-watchdog"
    assert no_leak


def test_premature_eof_rejected():
    calls: list = []

    async def body():
        readline = make_readline(b"", 0.01, calls)
        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=readline, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=0.2, watchdog_s=2.0,
            )
        return exc.value.stage

    assert asyncio.run(body()) == "premature-eof"


def test_wrong_token_rejected():
    calls: list = []

    async def body():
        readline = make_readline(b"not-ready\n", 0.01, calls)
        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=readline, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=0.2, watchdog_s=2.0,
            )
        return exc.value.stage

    assert asyncio.run(body()) == "wrong-token"


def test_stale_lease_durable_identity_mismatch_rejected():
    # 只见 receipt 存在不算端到端成功; lease/身份不绑定必须失败 (错误/旧 lease)。
    calls: list = []

    async def body():
        readline = make_readline(b"owner-ready\n", 0.01, calls)
        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=readline, durable_ready=lambda: False, failure_present=lambda: False,
                soft_s=0.2, watchdog_s=2.0,
            )
        return exc.value.stage

    assert asyncio.run(body()) == "durable-identity"


def test_failure_present_before_ready_rejected():
    calls: list = []

    async def body():
        readline = make_readline(b"owner-ready\n", 0.01, calls)
        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=readline, durable_ready=lambda: True, failure_present=lambda: True,
                soft_s=0.2, watchdog_s=2.0,
            )
        return exc.value.stage

    assert asyncio.run(body()) == "failure-before-ready"


def test_late_ready_after_failure_rejected():
    # 失败先落, owner-ready 迟到: acceptance 时 failure_present() 已 True → 拒绝。

    async def body():
        flag = {"failure": False}

        async def _factory():
            await asyncio.sleep(0.05)
            flag["failure"] = True
            await asyncio.sleep(0.02)
            return b"owner-ready\n"

        with pytest.raises(OwnerReadinessError) as exc:
            await observe_owner_ready(
                readline=_factory, durable_ready=lambda: True,
                failure_present=lambda: flag["failure"], soft_s=0.2, watchdog_s=2.0,
            )
        return exc.value.stage

    assert asyncio.run(body()) == "failure-before-ready"


def test_soft_exceeding_watchdog_is_programming_error():
    async def body():
        async def _q():
            return b"owner-ready\n"
        with pytest.raises(ValueError):
            await observe_owner_ready(
                readline=_q, durable_ready=lambda: True, failure_present=lambda: False,
                soft_s=10.0, watchdog_s=5.0,
            )

    asyncio.run(body())
