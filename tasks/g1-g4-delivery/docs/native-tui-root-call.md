# Root 私有原生调用模板

仅 root 在最终 MAIN source freeze/复审后执行一次；不使用真实 launchctl。作者未运行下面 native 调用。调用中需要持续持有 PreparedSyntheticFixture 的 endpoint；prepare 不运行 native，run 为独立明确操作。失败保留同 case，不改名重跑。

固定解释器启动模板（内核实际身份是同 framework 的 Resources/Python）：

```sh
PYTHONDONTWRITEBYTECODE=1 \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B ROOT_OWNED_CONTROLLER.py
```

controller 正文示例，保存到 root 自有语义任务目录后固定 hash；root 自行指定一次新 case ID 和报告归档位置：

```python
import asyncio, hashlib, os, sys, tempfile
from pathlib import Path

repo = Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design')
sys.path[:0] = [str(repo/'tasks/g1-g4-delivery/scripts'),
               str(repo/'tasks/g0-completion/scripts'),
               str(repo/'tasks/g0-auth-preserving-activation/scripts')]
from native_tui_case import prepare_tui_case, run_tui_case
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare
from synthetic_native_transport import NATIVE_SHA256
from auth_isolation import IsolationSpec, snapshot_auth_paths
from native_activation_fixture import _load_credential_inventory

inventory = Path('/private/tmp/g0-next-delivery-auth-inventory/credential-paths.txt')
assert hashlib.sha256(inventory.read_bytes()).hexdigest() == '78a5f826242bc439387a8972bddf587383dc7f45ff4dac19118b7a47cbc996a5'
paths, _ = _load_credential_inventory(inventory)
native = Path('/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex')
assert hashlib.sha256(native.read_bytes()).hexdigest() == NATIVE_SHA256
python = Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
go = repo/'tasks/g0-macos/data/go/bin/go'
profile = Path(tempfile.mkdtemp(prefix='g0-auth-tuir-', dir='/private/tmp'))
supervisor = Path(tempfile.mkdtemp(prefix='g0-auth-tuis-', dir='/private/tmp'))
activation_home = supervisor/'h'
public = activation_home/'.codex/app-server-control/app-server-control.sock'
endpoint = SyntheticEndpoint()  # binds and holds the owned loopback listeners
spec = IsolationSpec(profile, profile/'h', profile/'c', profile/'w',
    'native-tui-controlled', public, profile/'b/b.sock',
    (Path('/Users/guanjunhui'),), tuple(paths), owned_loopback_port=endpoint.port)
before = snapshot_auth_paths(paths)
prepared = prepare(endpoint, spec, executable=native)  # claims the empty profile
# Only now create the independent supervisor directories; do not pre-populate
# the profile root that prepare must claim as empty.
for path in (activation_home, activation_home/'Library', activation_home/'Library/LaunchAgents',
             activation_home/'.codex', activation_home/'.codex/app-server-control'):
    path.mkdir(mode=0o700)
plan = prepare_tui_case(prepared, profile/'native-tui-01',
    python_executable=python, go_executable=go, activation_home=activation_home,
    credential_manifest=inventory, expected_native_sha256=NATIVE_SHA256)
# PREPARE boundary: inspect plan/source/profile pins; no service/native started.
# Root's separately authorized RUN boundary, same controller process:
result = asyncio.run(run_tui_case(plan, prepared))
after = snapshot_auth_paths(paths)
assert before.compare(after)["unchanged"]
# Root archives safe result/plan/receipts, own PID/birth and socket postflight;
# retains the synthetic profile/history. Do not blindly delete on UNKNOWN.
```

实际前后认证比较沿 root 原有 opaque metadata/hash 流程，不输出清单正文或凭据；snapshot 使用原 compare 判据。`run_tui_case` 自身也执行原 transaction auth guard 和 source/profile 后验。新 fake HOME 的默认 socket alias 在 finally 精确撤回；private listener 只由原 fake transaction 撤回。必要时 root 按实际身份做独立 postflight，不使用 unused base socket 代替 record.private_socket。

结果必须同时为 passed、原生 TUI 两次 exit0、新 service PID/activation、同 owner thread、resume_zero_turns、pending_delivery_preserved、两次 synthetic 请求、完整分页 proof、Go ACK effect1、helper EOF、backend_count2/all_backends_stopped、service_stopped、REVOKED(fake)、public absent、auth/source unchanged。此验收不代表新全局安装或第二轮结果再次启动后的 durable history。
