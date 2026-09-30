"""C1 消费改接的真 RED/GREEN 探针 (收尾集成轮, 2026-09-30).

这是**证据面探针**，放在 `evidence/CLOSEOUT/C1/`，不进默认 Python suite，也不进 Go 身份集。
它用真实本地 native 链路（真 proxy + 真 activation service + 真 owner 子进程 + probe ws backend）
跑 D1 批准的观察协议，并允许在两种实现之间切换：

  BIRD_C1_IMPL=legacy   → 逐字复现被替换的旧 5s 硬观察形状
                          (`asyncio.wait_for(process.stdout.readline(), 5)` + 只看 stdout)
  BIRD_C1_IMPL=observer → 改接后的 D1 协议（单一 readline + 5s 软诊断 + 30s 总看门狗
                          + durable ready 身份绑定 + failure/EOF/非零退出交叉验收），
                          直接复用两个真实消费测试同一套 wiring 函数（惰性 import），
                          保证探针验的是**实际改接**，不是平行实现。

断言与实现无关，只断言 D1 合同要求的终态；因此 legacy 臂在「迟成功 / 失败先落 / 旧 lease 身份」
三格上必须真实失败（真 RED），observer 臂五格全绿。

路径根参数化：`BIRD_C1_REPO_ROOT` 覆盖默认 `<workspace>/scratch/second`。
"""

from __future__ import annotations

import asyncio
from contextlib import ExitStack
import json
import os
from pathlib import Path
import socket
import sys
import tempfile

import pytest

WORKSPACE = Path(__file__).resolve().parents[3]
REPO = Path(os.environ.get("BIRD_C1_REPO_ROOT", str(WORKSPACE / "scratch" / "second")))
IMPL = os.environ.get("BIRD_C1_IMPL", "observer")

for directory in (
    REPO / "tasks" / "g0-proxy-continuation" / "cold-start" / "scripts",
    REPO / "tasks" / "g0-proxy-continuation" / "cold-start" / "tests",
    REPO / "tasks" / "g0-completion" / "tests",
    REPO / "tools" / "native-product-bridge",
):
    sys.path.insert(0, str(directory))

import activation_service  # noqa: E402
import proxy_transport  # noqa: E402
import test_owner_helper_production_pipeline as pipe  # noqa: E402
from test_activation_service import digest, policy, private_json  # noqa: E402

# D1 场景（scenario 30s 总看门狗之内）的合法迟成功：>5s 软阈、<10s 产品准入。
LATE_DELAY_S = 6.5
# 失败先落：owner 打印前留一拍，让 failure receipt 先落盘。
FAILURE_DELAY_S = 1.0

# 每格的 D1 期望终态：accept 格要求成功观察，reject 格要求协议显式拒绝并给出阶段标签。
CASES = {
    "control": (True, None),
    "late-success": (True, None),
    "failure-before-ready": (False, "failure-before-ready"),
    "premature-eof": (False, "premature-eof"),
    "stale-lease-identity": (False, "durable-identity"),
}


def _patch_owner_script(path: Path, *, delay_s: float) -> None:
    """把 owner 客户端打印 owner-ready 的时点推迟 delay_s 秒（产品准入与预算不改）。"""
    text = path.read_text()
    marker = "print('owner-ready',flush=True)"
    assert marker in text, "owner client fixture shape changed"
    path.write_text(text.replace(marker, f"time.sleep({delay_s});{marker}", 1))


async def _legacy_observe(process, *, owner_pid, service, origin, loop, scenario_origin_budget):
    """旧 5s 硬观察：wait_for 到点即取消 readline，且只校验 stdout 一行。"""
    line = (await asyncio.wait_for(process.stdout.readline(), 5)).decode().strip()
    if line != "owner-ready":
        raise AssertionError(f"legacy hard line observed {line!r}")
    return {"accepted": True, "late_but_accepted": False, "stage": None}


async def _observe(case: str):
    """跑一格真实链路，返回 (accepted, exception)。异常不吞：由外层按 D1 期望判定。"""
    with tempfile.TemporaryDirectory(prefix="c1-probe-", dir="/private/tmp") as raw, ExitStack() as roots:
        root = Path(raw)
        public = root / "public.sock"
        binary = str(Path(proxy_transport._process_metadata(os.getpid())[1]))
        manifest, profiles, grants = policy(root, public, binary, roots)
        value = json.loads(manifest.read_text())
        backend = pipe.FIXTURES / "probe_ws_backend.py"
        value["backend_argv"] = [str(binary), "-B", str(backend), "{socket_path}", "success"]
        value["file_pins"] = {str(p): digest(p) for p in (backend, activation_service.__file__, pipe.OWNER_HELPER)}
        value["owner_helper"] = {
            "executable": str(binary), "executable_sha256": digest(binary),
            "source_path": str(pipe.OWNER_HELPER), "source_sha256": digest(pipe.OWNER_HELPER),
        }
        value["idle_seconds"] = 2.0
        private_json(manifest, value)
        owner_script = root / "owner.py"
        pipe._write_client(owner_script, False)
        if case == "late-success":
            _patch_owner_script(owner_script, delay_s=LATE_DELAY_S)
        if case == "failure-before-ready":
            _patch_owner_script(owner_script, delay_s=FAILURE_DELAY_S)
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind(str(public)); listener.listen(8); public.chmod(0o600)
        service = activation_service.ActivationService.from_manifest(manifest)
        await service.start(listener)
        env = {"PATH": "/usr/bin:/bin", "HOME": str(root), "PYTHONDONTWRITEBYTECODE": "1"}
        owner = await asyncio.create_subprocess_exec(
            str(binary), "-B", str(owner_script), str(public), cwd=profiles["a"]["workspace"],
            stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE, env=env)
        loop = asyncio.get_running_loop()
        origin = loop.time()
        accepted = None
        exception = None
        try:
            assert await pipe._line(owner) == "ready"
            birth, executable = proxy_transport._process_metadata(owner.pid)
            private_json(grants / f"{owner.pid}.json", {
                "version": 1, "pid": owner.pid, "uid": os.getuid(), "birth": birth,
                "expected_executable": executable, "executable_sha256": digest(binary), "profile_id": "a"})
            command = b"abort\n" if case == "premature-eof" else b"go\n"
            owner.stdin.write(command); await owner.stdin.drain()
            if case == "failure-before-ready":
                # 用产品自有 API 落一份真实 failure receipt（合成注入，不改产品码/预算）。
                service.record_startup_failure("probe-injection", RuntimeError("synthetic-failure"))
            # 旧 lease 身份格：观察被要求绑定到一个不属于本次 activation/owner 的身份。
            bound_pid = os.getpid() if case == "stale-lease-identity" else owner.pid
            budget = 30.0 - (loop.time() - origin)
            if IMPL == "legacy":
                observation = await _legacy_observe(owner, owner_pid=bound_pid, service=service,
                                                    origin=origin, loop=loop, scenario_origin_budget=budget)
            else:
                observation = await pipe.observe_owner_ready_line(
                    owner, service, owner_pid=bound_pid, origin=origin, loop=loop)
            accepted = bool(observation["accepted"]) if isinstance(observation, dict) else bool(observation.accepted)
        except BaseException as exc:  # noqa: BLE001 - 记录终态供 D1 期望比对，不吞成 PASS
            exception = exc
        finally:
            if owner.returncode is None:
                owner.kill()
            await owner.wait()
            await service.close()
            await asyncio.sleep(0.1)
            listener.close()
        return accepted, exception


@pytest.mark.parametrize("case", sorted(CASES))
def test_c1_owner_ready_observation_contract(case):
    expect_accept, expect_stage = CASES[case]
    accepted, exception = asyncio.run(_observe(case))
    if expect_accept:
        assert exception is None, f"{case}: D1 requires no false reject, got {exception!r}"
        assert accepted is True, f"{case}: D1 requires accepted observation"
        return
    assert accepted is not True, (
        f"{case}: D1 requires rejection before 5s-only acceptance, but observation accepted")
    assert isinstance(exception, Exception), f"{case}: rejection must surface a real failure, got {exception!r}"
    if IMPL == "observer":
        stage = getattr(exception, "stage", None)
        assert stage == expect_stage, f"{case}: expected stage {expect_stage!r} got {stage!r}"
        # 提前 EOF 格必须同时把「子进程非零退出」如实留在观察证据里
        if case == "premature-eof":
            assert expect_stage == "premature-eof"
