# 私有原生运输 runner：离线交付

基线 fixture：`96c22a6a7044cef04f05478f92e43f9297570737`。新增 `synthetic_native_transport.py` 提供可执行的 prepare/run 函数，不自动执行真实 Codex。本次所有进程都是 Python dummy；没有 native、真实账号/凭据、外部模型、TUI、全局/public socket 或 launchctl 调用。原 fixture 与其他实现者文件未修改。

`prepare_transport(prepared, case_dir, native_executable=..., expected_native_sha256=..., expected_source_pins=...)` 校验后创建全新 owner-only case，O_EXCL 写 `plan.json`。参数 `prepared` 必须来自持续持有 listener 的 PreparedSyntheticFixture。缺源 pins、端口 proof、context/native 对应关系或固定 native SHA 都拒绝。生产固定 0.154.0 SHA 为 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`；不提供测试模式或绕过参数。native 的 canonical path、dev/ino/mode/uid、SHA 在 prepare、spawn 前和最后验收复查。配置/政策/环境摘要、完整 sandbox/native argv、source/interpreter pins 与 WebSocket 16.0 版本入 plan，环境正文不落盘。

主会话后续明确准入后才可调用 `await run_transport(plan, prepared)`。这也是执行入口，没有导入即运行、CLI 自动 run 或默认账号探测。每个 case 在执行前只创建一次 `attempt.json`，已有 attempt 拒绝再次执行。case directory 的 check/open 身份会用 fstat 复查，写入始终通过已持有的目录 FD，避免写进被替换的目录。

## 执行链与判据

运行入口先记录 CLOCK_MONOTONIC_RAW 起点，原总期限 120 秒。preflight、spawn、UDS/WebSocket connect、initialize、initialized、thread/start 共享同一个最初 10 秒 deadline；不会每个 startup RPC 重新计时。两轮 turn 完成仍受同一个原 120 秒总窗约束，后续普通 RPC 各最多 10 秒且不得越过总窗。收尾完成晚于原总期限也只能 UNKNOWN。

backend 命令固定为 `native app-server --listen unix://<owned-private-socket>`，由原 clean environment/SBPL 和 GuardedChild 启动。guard 是 backend 的实际 parent/reaper，receipt 中 native PID/birth 与 guardian PID 区分。新连接先验证 OS UDS peer PID、UID、birth、executable 都对应实际 child，再进行一次 WebSocket upgrade；使用 preconnected socket，关闭 redirect reconnection、compression、proxy、ping。不通过默认/public 端点，不使用旧 disabled controller。

RPC 顺序固定：

1. `initialize` 含 experimentalApi；返回 codexHome 必须精确匹配 lease，随后仅一次 `initialized`。
2. `thread/start` 显式 cwd、synthetic provider、Luna 标识、ephemeral=false、historyMode=legacy、approvalPolicy=never。
3. 第一次 `turn/start` 只提交固定 `G0_SYNTHETIC_READY`；收到相同 thread/turn 的 completed 且无 error 后才前进。
4. 第二次 `turn/start` 在同一 thread 提交 `input=[]` 和 `toolOutput={name:g0_delivery, namespace:orchestration, output:G0_SYNTHETIC_TOOL_RESULT}`；再次等待该独立 turn 的 completed。
5. 一次 `thread/read(includeTurns=true)`：相同 thread/cwd/legacy 模式、两个精确 turn id、completed 状态；首 turn 只有匹配 userMessage 与 READY agentMessage，第二 turn 只有精确 functionCallOutput 与 SYNTHETIC_COMPLETE agentMessage，四个 item id 必须唯一。

这是**native 运输与原服务历史读取**判据。`history_verified` 仅指该服务返回的历史；`durable_history_verified=false` 和 `business_ack_verified=false` 始终保留。不以此声称原生落盘持久历史、业务 Go ACK、真实 AI 智能或完整 G0 通过。profile/session 文件保留供 root 后续独立归档，不启动第二服务来补证，也不额外读取 rollout。

method/request id 只记录白名单方法名和整数 id；通知必须属于固定枚举，未知通知/服务器请求直接 UNKNOWN，不回答。记录的是 canonical envelope byte count/SHA、turn/item ids、home-match bool、source/config hash 与 cleanup 安全事实；不保存 request headers、params/result 正文、token 或原始 stdout/stderr。输出流只保存累计字节数和 SHA。RPC 收据不代表完成，历史不匹配、丢 receipt、超时或 source 漂移都不会重发。

finally 关闭 WS，要求 guardian 对实际 child 的停止证明，使用持有的 backend parent dirfd 校验私有 socket inode/owner/mode 和父目录身份后 unlinkat。public socket 不在此路径。endpoint.close 异常会转 UNKNOWN，仍写新 result 并关闭 case/backend dirfd。`tree_stop_unproven=true`、`restart_allowed=false` 继承原 guardian 边界；停止证明不等于任意进程树均停止。

## 固定源码证据

复用原归档 0.154 protocol：`v2/turn.rs:145` TurnToolOutput、`:156` TurnStartParams、`:509` TurnCompletedNotification；`v2/thread.rs:62` ThreadStartParams、`:1667` includeTurns；`v2/thread_data.rs:78` legacy/paginated；`v2/item.rs:234` user/agent/functionCallOutput item 结构。此次仅新增下载 `protocol/common.rs`，`:1879` 起确认白名单后台通知名称，避免猜字符串。全部固定官方 commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`，新文件和复用摘要在 `transport-source-receipt.json`。Responses SSE、provider、retry 源继续复用原 fixture 收据。

## 离线证据

测试用 test-only spawn 适配将固定 native SHA 常量替为已知安全 0755 Homebrew Python 3.13.5 的摘要，执行真实 sandbox 中的 Python stand-in；生产模块没有该旁路。使用实际 PID/birth/peer、UDS/WS 和 synthetic HTTP；dummy 确实将自己的历史写文件并读取，但这不外推真实 Codex 持久性。没有 chmod 实际 Python/native；权限漂移测试只修改测试自己复制的临时 Python 文件。

新 13 项包括实际完整两轮、wrong home、第二发送后无 receipt、历史错误、缺 pins/config 漂移、陌生服务器请求的安全投影、pre-spawn/共享 startup deadline、native 权限漂移、case parent 换位、未列入白名单通知、endpoint close 异常留证、总 deadline 后清理不能 PASS。

RED 证据分文件保留（仅去除 pytest 展示的行尾空格）：初始 5 failed；安全投影 1 failed/1 passed；权限漂移 1 failed；case 换位 1 failed；独立 review 三项 3 failed；late cleanup 1 failed。所有测试文件中的真实 dummy 资源在收尾停止/关闭，其独立临时根由测试删除；没有清理真实账号、历史 case、main 或其他 worktree。

最终组合命令：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_synthetic_native_transport.py \
  tasks/g0-completion/tests/test_synthetic_native_fixture.py \
  tasks/g0-completion/tests/test_lifecycle_guard.py \
  tasks/g0-auth-preserving-activation/tests/test_auth_isolation.py \
  tasks/g0-auth-preserving-activation/tests/test_native_activation_fixture.py \
  tasks/g0-proxy-continuation/cold-start/tests --tb=short
```

新鲜结果：**110 passed in 20.89s**，见 `transport-green.log`。真实 native 实验尚未执行，最终准入由 root 在固定代码独立复审后编排。
