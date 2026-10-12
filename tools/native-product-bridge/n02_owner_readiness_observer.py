"""N02 owner-ready 观察协议返修 (R1 / D1, 2026-09-30).

背景 (逐字对应 timing-review BLOCKER 3 与 root D1):
  现有 test_owner_helper_production_pipeline.py 的 ``_line`` 用
  ``asyncio.wait_for(process.stdout.readline(), 5)`` 做硬观察。当 owner-ready 落在
  6.5s / 5.2s / 5.5s 这类「超过 5s、但仍在产品 10s 准入与场景 30s 看门狗之内」的
  合法准入时, 硬 ``wait_for`` 抛 TimeoutError 并取消该 readline, 造成 false reject
  (S2)。5s 只是测试观察上限, 不是产品准入; proxy 外层 10s 从连接处理起并覆盖
  backend_factory, activation 内层 10s 在 grant/resume 之后起, 二者不是严格区间
  关系, 因此「直接改 10」或「10+2 经验裕量」都不是合同。

本模块把 D1 批准的观察协议固化为可复用 helper:
  * 只创建一个 owner.stdout.readline() 等待任务 (task);
  * soft_s (默认 5s) 只作为 soft diagnostic: 用 ``asyncio.shield`` 等待, 超时不取消
    task、不重建第二个 readline、不判失败, 仅记诊断;
  * 继续等待同一个 task, 受调用方观察 watchdog 的剩余预算约束, 不重开预算;
  * 最终成功必须同时满足: stdout 恰为期望 token、durable ready 身份绑定本次
    activation/lease、期间未先出现 backend failure receipt、未在 ready 前提前 EOF;
  * 无论成功/失败, finally 收敛自己创建的等待任务, 不留孤儿。

pipeline 新增的是 owner-ready 观察 watchdog, 不是继承的全流程期限;
local chain 的调用者另有既有外层 30s。产品准入 (proxy/activation 的 10s)
与既有 cleanup/grace 均不在此改动;
5s 之后的成功只标为 late-but-accepted, 不扩展产品预算。
"""

from __future__ import annotations

import asyncio
import math
from contextlib import suppress
from dataclasses import dataclass, field
from typing import Awaitable, Callable, List, Optional


OWNER_READY_TOKEN = "owner-ready"


class OwnerReadinessError(AssertionError):
    """观察协议判定的失败终态; 携带阶段标签便于保留证据。"""

    def __init__(self, stage: str, detail: str = "") -> None:
        self.stage = stage
        self.detail = detail
        super().__init__(f"owner-readiness {stage}: {detail}")


@dataclass
class Observation:
    accepted: bool
    late_but_accepted: bool = False
    soft_diagnostic_fired: bool = False
    elapsed_s: float = 0.0
    diagnostics: List[str] = field(default_factory=list)


async def observe_owner_ready(
    *,
    readline: Callable[[], Awaitable[bytes]],
    durable_ready: Callable[[], bool],
    failure_present: Callable[[], bool],
    expected_token: str = OWNER_READY_TOKEN,
    soft_s: float = 5.0,
    watchdog_s: float = 30.0,
    remaining_s: Optional[float] = None,
) -> Observation:
    """单一 readline + shield 软诊断 + 原观察截止的协议。

    readline: 恰好一次 owner.stdout.readline() 的协程工厂 (只调用一次, 得到唯一 task)。
    durable_ready: 返回本次 activation/lease 绑定的 durable ready 是否已存在且身份一致。
    failure_present: 返回期间是否已出现 backend failure receipt。
    watchdog_s: 配置的完整观察预算, 不能小于配置的 soft_s。
    remaining_s: 调用时的剩余预算; 合法晚开始可以少于 soft_s。
    """
    if not math.isfinite(soft_s) or not math.isfinite(watchdog_s) or soft_s <= 0 or watchdog_s <= 0:
        raise ValueError("soft diagnostic and watchdog must be finite positive seconds")
    if soft_s > watchdog_s:
        raise ValueError("soft diagnostic must be within the scenario watchdog")
    budget = watchdog_s if remaining_s is None else remaining_s
    if not math.isfinite(budget) or budget > watchdog_s:
        raise ValueError("remaining budget must be finite and no greater than configured watchdog")
    if budget <= 0:
        raise OwnerReadinessError("scenario-watchdog", "budget exhausted before observation; readline not started")

    log: List[str] = []
    loop = asyncio.get_running_loop()
    origin = loop.time()
    deadline = origin + budget

    def watchdog_error():
        return OwnerReadinessError("scenario-watchdog", f"owner-ready not accepted within remaining {budget:.3f}s")
    # 只创建一个 readline 等待任务, 全程复用同一个, 绝不重建。
    task = asyncio.ensure_future(readline())
    soft_fired = False
    try:
        try:
            raw = await asyncio.wait_for(asyncio.shield(task), min(soft_s, max(0.0, deadline - loop.time())))
        except asyncio.TimeoutError:
            if budget <= soft_s or loop.time() >= deadline:
                raise watchdog_error() from None
            # 软诊断: 只记录, 不取消 task, 不判失败, 不吞成 PASS。
            soft_fired = True
            log.append(
                f"owner-ready not observed by {soft_s:.3f}s; continuing under watchdog"
            )
            remaining = deadline - loop.time()
            if remaining <= 0:
                raise watchdog_error()
            # 受总看门狗约束; 到点即失败并在此取消 (终态收敛)。
            try:
                raw = await asyncio.wait_for(task, remaining)
            except asyncio.TimeoutError:
                raise watchdog_error() from None
        elapsed = loop.time() - origin
        if loop.time() >= deadline:
            raise watchdog_error()

        if not raw:
            raise OwnerReadinessError("premature-eof", f"stdout EOF before ready at {elapsed:.3f}s")

        if failure_present():
            raise OwnerReadinessError("failure-before-ready", "backend failure receipt present before acceptance")

        line = raw.decode(errors="replace").strip()
        if line != expected_token:
            raise OwnerReadinessError("wrong-token", f"stdout line {line!r} != {expected_token!r}")

        if not durable_ready():
            raise OwnerReadinessError("durable-identity", "owner-ready printed but durable ready identity not bound to this lease")

        if failure_present():
            # late-but-accepted 仍要求失败不得存在; 覆盖「失败后迟到 ready」。
            raise OwnerReadinessError("failure-before-ready", "backend failure present at acceptance")

        if loop.time() >= deadline:
            raise watchdog_error()

        late = soft_fired
        return Observation(
            accepted=True,
            late_but_accepted=late,
            soft_diagnostic_fired=soft_fired,
            elapsed_s=elapsed,
            diagnostics=log,
        )
    finally:
        # 收敛自己创建的等待任务: 若仍在运行 (永不 ready / 取消路径), 取消并 await。
        if not task.done():
            task.cancel()
        with suppress(asyncio.CancelledError, Exception):
            await task


async def _legacy_hard_line(
    readline: Callable[[], Awaitable[bytes]],
    *,
    hard_s: float,
) -> str:
    """复现被替换的旧硬观察 (仅用于真 RED 对照, 不进生产/默认 suite)。"""
    raw = await asyncio.wait_for(readline(), hard_s)
    return raw.decode(errors="replace").strip()
