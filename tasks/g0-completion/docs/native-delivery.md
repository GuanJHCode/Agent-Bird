# 私有 owner/helper/Go 组合夹具（离线冻结）

基线 `85d0c24`，已串行继承 helper 兼容 delta `da406aa`/`9e5a948`（本 WT 对应 `8ea649f`/`779b8bf`）。本交付只增加新文件，不修改 service、adapter、owner-helper 或旧 case。尚未运行真实 native，下一原生准入始终由 root 决定。

`prepare_delivery` 接受 PreparedSyntheticFixture 和显式固定 native/Python/Go/source pins，使用实际 Go 编译器在 prepare 阶段构建 g0-return-lab。复用 root 明确批准的任务 GOCACHE/GOMODCACHE，GOENV=off、GOPROXY=off、GOSUMDB=off、GOTOOLCHAIN=local，使用隔离 HOME。cache 和共同父目录按 canonical/dev/ino/mode/uid 验证前后不变且无 group/world write；构建上限独立 120 秒，不占用随后 `run_delivery` 的原 120 秒窗口。case/plan/attempt/result 都采用新 owner-only 路径；attempt O_EXCL 阻止重复运行。

执行链是实际任务私有 public UDS listener → ActivationService → guardian/native private UDS。owner 和 helper 是两个独立、固定源码的 Python -I -B cleanenv 进程，明确不是 native TUI；native backend 才使用原 auth-isolation SBPL。这是 Python exec 后、首 RPC 前登记的 admission，不是 native TUI pre-exec gate。client 在 RPC 前读取并核对 case grant/manifest/source pins、public socket inode，以及 actual proxy peer PID/birth/UID/executable；public peer 是 controller，不冒认 native backend。

owner 经登记后 initialize、thread/start，等待 thread/started 与真实 service OwnerLease 发布。controller 对照真实 service record 后允许初始 G0_SYNTHETIC_READY，owner 等第一 turn/completed 并保持连接。controller 随后使用真实 Go CLI start/wait/inspect 生成 task/control.json 与 producer result；没有手写这些文件。声明 event_result 的 payload hash 来自 Go inspect。

helper 使用独立 PID grant 经 public owner-helper role 复用同一 lease/backend。Go prepare/claim、一次 adapter toolOutput、原服务 thread/read 审计和 ACK 全部发生在 helper 的同一个 DeliveryAdapter 实例，capability/proof 不跨进程。helper 从已验证 grant 读取 owner_context_sha256，交给同 adapter 在 claim/send 前写 delivery-owner-binding.json；实际测试核对文件 digest 与 service lease 一致。delivery_id 含 G0_SYNTHETIC_TOOL_RESULT 标记。controller 核对 helper proof、实际服务绑定、owner 仍活跃和两次 synthetic HTTP 后，写独立 controller-handled-decision.json 并显式发送 handled 决定；只有此时 helper 调真实 Go batch-ack。原 Go ACK/control/claim 文件全部由 Go 代码生成。

结果保留 native/backend PID/birth、独立 owner/helper/public peer、lease、source/config hashes、safe envelope summaries、Go job/inspect/ACK/status 与服务原始安全 snapshot；没有设备/账号/RPC body 原文。清理证据使用每条实际 service record.private_socket（b-lease.sock）及真实 backend PID/birth；不把未使用的 spec.backend_socket 不存在当清理证据。`ordinary_tui_verified=false`、`durable_history_verified=false`。adapter HistoryProof 自身 `source_service_verified=false` 不被改写；组合层另用实际 service/peer/lease 证据判断。该实验不代表真实 AI 智能、普通 TUI 附着或完整 G0 通过。

已覆盖真实 dummy service/process/UDS/WS/HTTP + 实际 Go build/CLI：正常两轮→1 个 ACK(effect_count=1)；history mismatch 与丢 helper receipt 均 UNKNOWN/0ACK/不重发。初始实现的失败收尾曾被即时 service.close 打断，现新 driver 在原 cleanup 窗内先等待实际 EOF backend 收尾，再关闭服务；不手填 process_stopped/socket_removed。

最终增量保留原 10/120 窗：控制 stdin.write 前、Go 每步前后、helper 连接与 history 循环均核对原 deadline；status/ACK 各次交互只计算一次 local end。helper EOF 带同 RAW 时钟时间，必须晚于 controller 发出 owner close；提前 EOF 时保留已观察到的 ACK(effect_count=1)，整体 UNKNOWN。停止 client 前核实记录的 PID/birth/exe/SHA，未知身份不发 signal。public socket 经 held parent dirfd 移入独立 0700 quarantine，核实移动后的 inode 才删除；替换物保留 quarantine 路径并 UNKNOWN。所有异常均不重发，restart_allowed=false。

使用同一 controller 进程 prepare/run，持续持有 PreparedSyntheticFixture endpoint 和 private listener；不能跨进程序列化 plan 后冒认原 proxy peer。root 应先建立独立短 root 的 IsolationSpec（task-private public_socket、隔离 HOME/CODEX_HOME/workspace、受保护路径、owned_loopback_port 来自 endpoint），再调用：

```python
from pathlib import Path
from synthetic_responses import SyntheticEndpoint
from synthetic_native_fixture import prepare as prepare_synthetic
from native_delivery_case import prepare_delivery, run_delivery, source_pins
from synthetic_native_transport import NATIVE_SHA256

safe_python = Path('/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python')
go = Path('/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/go/bin/go')
# endpoint = SyntheticEndpoint(); spec 使用该 endpoint.port，并由 root 固定。
prepared = prepare_synthetic(endpoint, spec, executable=fixed_native)
plan = prepare_delivery(prepared, new_case_path,
    python_executable=safe_python, go_executable=go,
    expected_native_sha256=NATIVE_SHA256, expected_source_pins=source_pins())
# 到此只有 prepare；root 完成准入后，在此同一进程显式执行一次：
result = await run_delivery(plan, prepared)
```

case 必须新路径，mkdir/plan/attempt/result 均拒绝覆盖；不能自动换 case 重试。run 前后复核冻结源码/profile/config/manifest/Python/native/Go/public peer。endpoint 至 run finally 才关闭；隔离 profile 和记录保留。未执行真实 native/launchctl/真实 auth，真实原生准入由 root 单独编排。

最后一次源码修改后，旧新同进程 **63 passed in 16.00s**（其中新组合/边界 8 项），详见 native-delivery-combined-green.log：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_native_delivery_case.py \
  tasks/g0-completion/tests/test_owner_helper.py \
  tasks/g0-completion/tests/test_delivery_adapter.py --tb=short
```

初始缺执行链 RED：3 failed；边界 RED：恢复 binding 缺文件、真实提前 EOF 误 PASS、错误身份停止接口/隔离删除/原预算接口缺失。首次 quarantine 测试路径过长，改用自有 /private/tmp 短目录后单独重现缺接口。控制 write-before-deadline 回归也先实际观察到过期仍写 1 帧，再修为零写。日志保存初始 RED、边界 RED 与最终 GREEN；所有 Go 命令实际执行，无 skipGo。只写入受控短目录和 root 授权构建 cache；自动测试 tmp 已清理，真实旧 case 完全不变。
