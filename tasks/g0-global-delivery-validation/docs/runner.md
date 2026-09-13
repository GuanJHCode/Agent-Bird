# 独立服务注册与回传验收 runner

用户已批准一次用户级注册/撤销，由 root 单独执行。本交付仅离线实现和 dummy 验证；没有运行真实 native、launchctl、真实 auth/Keychain 或账号模型。实际入口以主线 `plans/authorized-run.md` 为授权依据。

`prepare_global` 只构建真实 Go CLI、写新 case/manifest/plan 并冻结 source/native/Python/Go/config/profile/credential inventory；不注册。必须在同一个固定 safePython controller 进程持有 PreparedSyntheticFixture 与 endpoint，之后由 root 显式一次 `run_global`。case 名限定 ASCII 字母数字下划线连字符、最长38字符，新目录/attempt/阶段/result均 O_EXCL。程序没有自动换 case 或重发。

两种模式共享真实 ActivationTransaction 文件事务与独立服务/owner/helper/native 进程链：

- `scheduler_mode='launchd'`：真实系统 UID 的 HOME，固定 `/bin/launchctl` 与原 projectproxy entrypoint。ProgramArguments 为绝对 safePython `-I -B ENTRYPOINT --manifest MANIFEST`，没有 `--listener-fd`；拒绝 scheduler 实例/type/spec 或真实 HOME 错配。
- `scheduler_mode='inherited_fd'`：只允许新 `/private/tmp` HOME/socket，私有 InheritedFdScheduler 创建真实 listener 并把 FD 传给原 entrypoint。job inventory 明确是模拟；`real_launchctl_used=false`、`global_registration_verified=false`。root 可先以相同固定 native 做私有预检，本作者测试仅通过独立测试 entrypoint 将 backend 替换为实际 Python dummy。

注册/verify 使用独立原30秒，native阶段原120秒（业务110、保留10清理），撤销使用独立原30秒，不宣称整个事务120秒。launchctl 同步执行，每调用上限 min(5秒, 阶段remaining)，不存在取消 to_thread 后继续 bootstrap。阶段 start/final receipt 在异常和timeout时也落盘，不丢注册/撤销证据。

先由未授 grant 的 controller 做0RPC UDS唤醒并等待EOF；确认无backend lease目录。实际dummy此时peer只有UID，PID/birth/executable不可用，如实保留。service-start+launchctl job PID+kernel PID/birth/executable/SHA先冻结服务；后续 owner/helper 的持久WS必须有完整OS peer并匹配该服务。Python子进程 parent仍是controller，独立核PID/UID/birth/executable/SHA，不把service当parent。

只从稳定FD读取服务写入的service-start、owner-ready、helper-ready、backend-final、activation-final。目录identity冻结，O_NOFOLLOW/O_NONBLOCK、owner0600、nlink1、长度/fstat前后不变；绑定目录消失/替换、重复JSON key、NaN/Infinity拒绝。owner lease逐层核profile/context/service/front/backend/socket/connection/epoch；helper-ready核完整双peer；final核单backend+单helper、匹配lease及所有cleanup状态。最终结果只投影必要安全字段及原文件SHA，不导出任意service JSON正文。

owner初始固定输入首turn完成；helper同一adapter真实prepare/claim→receipt-only一次toolOutput→同thread只读完整history审计→controller明确handled决定→真实GoACK(effect_count=1)。不新增订阅/resume/RPC。正常PASS要求owner/helper exit0、helper EOF晚于ownerclose、backend/guardian安全final及原PID/socket消失、service安全final/原PID消失、对称revoke和auth unchanged。`ordinary_tui_verified=false`、`durable_history_verified=false`，不冒称完整G0或模型智能。

正常服务可自然idle退出；已有稳定final与原PID消失时不再signal。信号前必须匹配原kernel身份。启动失败在局部保留child并有界收尾；metadata缺失且仍长驻的child不发信号，保留_job/cleanup_unproven并由finally revoke复核，无法证明仍为UNKNOWN。测试metadata缺失child是自行退出负例，不宣称任意长驻进程保证回收。未知对象不误杀、不删；失败不自动再bootstrap。

## Root调用

用已有 `SyntheticEndpoint` + `IsolationSpec` + `synthetic_native_fixture.prepare(endpoint, spec, executable=fixed_native)` 构建 fresh PreparedSyntheticFixture。spec.owned_loopback_port必须来自持续持有的endpoint，profile root必须为新直接 `/private/tmp/g0-auth-*`；protected_read_paths必须与显式14项清单一致。private模式先创建独立supervisor HOME及Library/LaunchAgents、.codex/app-server-control（0700）；真实模式不修改已有目录权限。

```python
from pathlib import Path
from global_delivery_case import prepare_global, run_global, source_pins
from synthetic_native_transport import NATIVE_SHA256

safe_python = Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
go = Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go')
plan = prepare_global(prepared, new_case_path,
    python_executable=safe_python, go_executable=go,
    activation_home=activation_home, credential_manifest=frozen_inventory,
    scheduler_mode='inherited_fd',  # root私有预检；唯一真实运行另用launchd
    expected_native_sha256=NATIVE_SHA256, expected_source_pins=source_pins())
# prepare到此不运行；root准入后，同进程明确一次：
result = await run_global(plan, prepared)
```

root运行controller可用同framework `bin/python3.13`；kernel身份必须为上面的Resources/Python。真实native SHA始终固定0.154，测试只在pytest monkeypatch Python dummy hash，生产无绕过开关。

## 新鲜验证

本任务34项 PASS/16.46s；最后源码修改后新旧同进程 **63 passed in 33.85s**，日志 `combined-green.log`。包含实际独立服务/FD/guardian/双客户端/HTTP/真实Go正例，context/helper birth/final状态篡改、history无ACK、revoke失败UNKNOWN、真实child启动失败/实际deadline子进程reap、稳定FD与JSON拒绝。RED已观察缺runner、parent/receipt缺口、helper birth/final closed误PASS、阶段失败无文件、启动失败FD未关闭；未伪造claim或服务snapshot。

safePython没有安装pytest；明确只读加入既有测试依赖。pytest9.1.1内置subtests；禁自动外部插件，无外部pytest_subtests。旧native用例只在harness把原GO_CACHE参数指向本task新cache，未改旧源码、未重建旧task缓存。可在该工作树根目录复制：

```sh
PYTHONDONTWRITEBYTECODE=1 PYTEST_DISABLE_PLUGIN_AUTOLOAD=1 \
GOCACHE="$PWD/tasks/g0-global-delivery-validation/tmp/build/cache" \
GOMODCACHE="$PWD/tasks/g0-global-delivery-validation/tmp/build/modcache" \
GOENV=off GOPROXY=off GOSUMDB=off \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B -c '
import sys
from pathlib import Path
sys.path[:0]=["/Library/Frameworks/Python.framework/Versions/3.13/lib/python3.13/site-packages", "/Users/guanjunhui/Library/Python/3.13/lib/python/site-packages", str(Path.cwd()/"tasks/g0-completion/scripts")]
import native_delivery_case as legacy
legacy.GO_CACHE=Path.cwd()/"tasks/g0-global-delivery-validation/tmp/build/cache"
legacy.GO_MOD_CACHE=Path.cwd()/"tasks/g0-global-delivery-validation/tmp/build/modcache"
import pytest
raise SystemExit(pytest.main(["-q","-p","no:cacheprovider","tasks/g0-global-delivery-validation/tests","tasks/g0-completion/tests/test_native_delivery_case.py","tasks/g0-completion/tests/test_owner_helper_service.py","tasks/g0-proxy-continuation/cold-start/tests/test_activation_protocol.py","--tb=short"]))'
```

只保留本task tmp/build cache供终审复测（约52MB）；测试短临时根自动清理。真实清单从未传给dummy测试。旧case、主树和账号配置均未修改。
