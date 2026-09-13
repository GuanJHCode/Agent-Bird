# 原生 TUI、服务重启与固定 owner 回传

本实现供 root 单次私有原生验收；作者只运行真实 Python dummy 的 PTY/UDS/HTTP/Go 测试，没有运行 Codex、launchctl、真实认证或账号模型。继续使用固定 Codex 0.154 源码与二进制，默认 TUI 设置不变。

唯一组合顺序：普通空 argv codex 建 P → /status=P → 固定 G0_SYNTHETIC_READY 输入和 READY → 真实 Go 任务完成、结果仍未 claim → /quit exit0 → backend/frontend/service 原身份停止且 immutable receipts 保留 → 原私有 listener FD 下固定 argv 启动一个新 service → 新 PID grant 后执行 codex resume P → 新 resume response/skills barrier 与 /status=P，0 新模型 turn → 检查旧 Go 记录逐字节不变 → controller 明确允许投递 → 新 lease 上独立 helper 先读取唯一完整 READY 初始 turn，再真实 prepare/claim → 一次 toolOutput → 同 thread 完整历史和分页审计 → 明确 handled 决定、真实 Go ACK(effect_count=1) → SYNTHETIC_COMPLETE 与 /status=P → 正常 /quit、helper EOF、完整清理和 fake revoke。模型端只有本任务持续持有的合成 endpoint 两次响应。

## 接线与安全边界

- `native_tui_case.prepare_tui_case` 复用原 prepare/build/ActivationTransaction；prepare 不启动服务或 native。`run_tui_case` 是独立调用且只允许 inherited_fd。真实 launchd scheduler/默认 Python owner 入口不变。
- manifest/config 固定 native_tui_policy 三字段；只加入预先指定的 runner/session/probe/runtime 源码，禁止任意 extra pins。普通 TUI 不具备 Python owner 的 pipe 门闸，使用既有 pre-exec PID/birth/native SHA grant 与服务端 accepted peer 检查；controller 后验独立读取 owner-connected/ready。helper 继续既有 HTTP 后 parent admission 和实际 receipt 比对，在 admitted 前零 RPC/claim。
- resume grant 固定旧 activation/lease/ready SHA/backend-final SHA。服务稳定 FD 读取原 immutable 文件，核同 manifest/profile/context/thread、完整旧 lifecycle 与 guardian receipt、原 frontend/backend/service 身份已停止及旧 socket 缺席；不依赖内存 backend_records。明确不同 birth 的 PID 复用表示原身份消失，元数据不完整仍 UNKNOWN，不发信号给复用者。
- 私有 scheduler 只在原 service 已结束、固定原 activation-final 完整验收后，以仍持有的同 listener FD/同 argv 启动一次 successor，不新建或重复 bootstrap。controller 只接受原 activation 集合之外唯一新增目录，随后用实际 successor PID、kernel、job、manifest、启动窗口相互绑定，再显式选择该 activation；不按时间挑最近目录。
- 新 TUI reader 可选等待 publisher 的 nlink=2 过渡，不读取正文或记 pin；只有 owner0600/nlink1/稳定 FD 才读取。持续 nlink2 到原期限仍 UNKNOWN。旧默认 reader 严格行为不变。
- 原本地 10 秒、业务 110/总 120 秒、清理保留 10 秒，注册/撤销独立原上界保持。prepare/Go build 在运行窗口外。UNKNOWN 不自动重发、重跑或新增模型输入。

## 0.154 固定源码依据

均为仓库已归档官方 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`，没有再次下载：

- `tasks/g0-sessionstart-mcp-diagnostic/data/source/codex-rs/tui/src/app_server_session.rs:2038`：非 ephemeral 普通 TUI 请求 Paginated，故不能用旧 Python owner 的 legacy fixture 替代。
- `tasks/g0-native-0154-contract/data/source/codex-rs/app-server/src/request_processors/thread_processor.rs:3958`、`:4076`：resume 自动 attach 原 thread 并返回对应 response；该路径没有 thread/started。复用真实 `proxy-resume-02` 的 request5 response → 新 skills request9 response barrier（`tasks/g0-proxy-continuation/docs/proxy-resume-02-acceptance.md`），不新增客户端 RPC。
- 同文件 `:3038`、`:3317`：thread/read(includeTurns=true) 为 paginated 提供完整历史兼容路径。仅用于初始 READY 资格和完成观察；业务证明仍走真实分页。
- `tasks/g0-native-0154-contract/data/source/codex-rs/app-server-protocol/src/protocol/v2/thread.rs:1701`：thread/turns/list 的 threadId/cursor/limit/sortDirection/itemsView 和 nextCursor。helper 限原 grant thread、full、asc、bounded cursor/limit；本 client 固定 limit2/max8页/同10秒。原 delivery_audit 不改，必须完整页链与精确 envelope。DeliveryAdapter 直接复用 auditor 已确认的完整 envelope 事件集合，修复原二次 legacy-only 扫描丢失分页事件的问题。
- `.../protocol/v2/turn.rs:395`：UserInput.Text 的 text_elements 默认空；初始 marker 核验接受该实际字段且只允许空 spans。

此 case 验证任务结果等待回传期间的退出/跨服务恢复；不等同任意运行中任务树中断恢复、不证明第二轮结果再次重启后的 durable history，也不证明未经预登记的任意个人账号 CLI 已产品化安装。结果固定 P，不用 UI 缓存猜 owner。

## 测试与已知限制

最新相称组合见 native-tui-green.log：真实 controlling PTY、两个独立服务进程、跨服务读取保留历史、两页 full 返回、实际 Go claim/ACK；坏旧 proof 和歧义 initial history 均在 claim 前失败。另含 resume 请求/响应/后置 skills/零 turn 门禁、publisher nlink2 受控暂停、PID 复用/unknown、只读分页目标/shape 和原默认 global 路径兼容。

沿用已审 `native_activation_probe._spawn_pty_gate` 的 fork，Python 3.13 对多线程进程 fork 发出 DeprecationWarning；本次真实 dummy 全部正常退出，未隐藏 warning，未据此声明一般 fork 安全。sandboxed exec shim 在导入控制器依赖前只使用 os/sys 等待 release 再 exec，避免 ctypes import 的被禁 uname sysctl，不扩大 SBPL。

只保留本 WT 的 Go build cache 供 review；中间诊断日志删除，必要 RED/GREEN 在 docs。原 case、MAIN、实际凭据和其他 agent 文件未改。


复现命令（工作树根，safePython 原库依赖只读加入；pytest 9.1.1 内置 subtests，禁自动外部插件，不安装依赖）：

```sh
PYTHONDONTWRITEBYTECODE=1 PYTEST_DISABLE_PLUGIN_AUTOLOAD=1 \
/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13 -I -B -c '
import sys
sys.path[:0]=["/Library/Frameworks/Python.framework/Versions/3.13/lib/python3.13/site-packages","/Users/guanjunhui/Library/Python/3.13/lib/python/site-packages"]
import pytest
raise SystemExit(pytest.main(["-q","-p","no:cacheprovider","tasks/g1-g4-delivery/tests","tasks/g0-proxy-continuation/cold-start/tests/test_activation_protocol.py","tasks/g0-global-delivery-validation/tests/test_receipt_store.py","tasks/g0-global-delivery-validation/tests/test_inherited_fd_scheduler.py","tasks/g0-global-delivery-validation/tests/test_global_delivery_case.py::test_actual_external_service_transaction_and_go_delivery","--tb=short"]))'
```
