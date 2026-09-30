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
  * 继续等待同一个 task, 受调用方 scenario 总看门狗 (默认 30s, 从原起点计) 约束;
  * 最终成功必须同时满足: stdout 恰为期望 token、durable ready 身份绑定本次
    activation/lease、期间未先出现 backend failure receipt、未在 ready 前提前 EOF;
  * 无论成功/失败, finally 收敛自己创建的等待任务, 不留孤儿。

产品准入 (proxy/activation 的 10s) 与既有 cleanup/grace 均不在此改动;
5s 之后的成功只标为 late-but-accepted, 不扩展产品预算。
"""

from __future__ import annotations

import asyncio
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
) -> Observation:
    """单一 readline + shield 软诊断 + 调用方总看门狗的观察协议。

    readline: 恰好一次 owner.stdout.readline() 的协程工厂 (只调用一次, 得到唯一 task)。
    durable_ready: 返回本次 activation/lease 绑定的 durable ready 是否已存在且身份一致。
    failure_present: 返回期间是否已出现 backend failure receipt。
    """
    if soft_s > watchdog_s:
        raise ValueError("soft diagnostic must be within the scenario watchdog")

    log: List[str] = []
    loop = asyncio.get_running_loop()
    origin = loop.time()
    # 只创建一个 readline 等待任务, 全程复用同一个, 绝不重建。
    task = asyncio.ensure_future(readline())
    soft_fired = False
    try:
        try:
            raw = await asyncio.wait_for(asyncio.shield(task), soft_s)
        except asyncio.TimeoutError:
            # 软诊断: 只记录, 不取消 task, 不判失败, 不吞成 PASS。
            soft_fired = True
            log.append(
                f"owner-ready not observed by {soft_s:.3f}s; continuing under watchdog"
            )
            remaining = max(0.0, watchdog_s - (loop.time() - origin))
            # 受总看门狗约束; 到点即失败并在此取消 (终态收敛)。
            try:
                raw = await asyncio.wait_for(task, remaining)
            except asyncio.TimeoutError:
                raise OwnerReadinessError("scenario-watchdog", f"owner-ready never arrived within {watchdog_s:.3f}s from origin")
        elapsed = loop.time() - origin

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
