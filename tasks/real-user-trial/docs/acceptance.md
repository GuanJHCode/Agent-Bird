# 真实试用准备验收

截至本轮：真实试用 `NOT_READY`。一次性原生 `--remote` 参数已获允许，未修改全局配置、登录账号或注册持久服务。原 rc6 包未重打，也不把它标为真实账号版本。

## 已合入

- 刷新保护合同：固定 native 版本/摘要，检查启动和执行两条认证路径，只修改返回的子进程环境。可信元数据 source 尚未实现。
- 认证 RPC 纯策略：允许不刷新的账户读取，拒绝登录、登出、刷新等认证操作。尚未接入真实代理。
- Codex CLI 协议候选：固定 `gpt-5.6-luna` / `medium` / `read-only`，prompt 走 stdin，显式 session resume；最后一条消息作为结果候选，成功必须有非空结果，严格校验 session、terminal 和 usage。
- 通用启动入口按 provider、版本或固定摘要提前返回 `codex_trial_guard_not_ready`，在文件检查和原生版本探针之前阻断，不能换 provider 标签绕过。
- 原生 fixture 的超时路径改为有界等待，不可回收进程返回明确阻断；这只解决测试控制流，未解决 macOS 启动问题。

## 验证证据

- 主验收 `test_rpc_policy.py`：`35 passed in 0.02s`。
- 主验收关闭 native opt-in 后运行 `test_native_auth_tripwire.py`：`1 passed, 1 skipped in 0.03s`。通过的是不可回收进程模拟用例，跳过的是真实 native 用例。
- 子 Agent 在最终源码上执行 `go test ./internal/adapter ./internal/host -count=1`：adapter `0.588s`、host `4.579s`，均通过。
- 子 Agent 执行单个 `TestCodexTrialInvocationIsBlockedBeforePinReadOrVersionProbe`：通过，`0.928s`。
- 独立审查复核提交 `ab06af9369406a28a50eb467c09cae85cac74ed3`，未发现剩余实质问题。主合入为 `4af84068`，逐路径比较确认测试过的七个 Go/fixture 文件与合入结果一致；`git diff --check` 通过，没有重复长测。
- 原生 synthetic 实验未进入 Codex 主程序；详见 [启动阻断记录](native-tripwire-attempt.md)。没有真实模型、真实账号任务或 Keychain 操作。

## 尚未完成

1. 在不改动既有认证的条件下完成原生刷新保护实证；当前严格 sandbox 在 dyld 阶段阻断，具体缺失系统权限尚无证据。
2. 可信元数据来源及启动前保护、启动后 effective config 复核。
3. 真实来源环境下的私有代理端点、前端 owner/thread 绑定及 RPC 策略接线。
4. 可由用户操作的临时入口，以及真实小任务的下发、退出停止、同任务重开和显式续接全链路验收。

本轮没有交付可直接运行真实账号任务的命令，也没有解除真实入口阻断。

## 清理与保留

本轮两个开发 worktree 已删除；子 Agent 清除了任务 Go cache、公开源码审计临时目录及系统采样临时文件。保留交付源码、测试、记录和原有用户文件。

PID `39659`（birth `Mon Sep 14 07:32:04 2026`）仍为 `UE`，父测试进程已停止；两套新 synthetic 根 `/private/tmp/g0-auth-qxskc_u5`、`/private/tmp/g0-auth-g2z7tkep` 保留为 `cleanup_pending`，不删除仍可能被该进程使用的目录。未重复发信号、未重启主机。原 PID `47082`、原 worktree、stash 与用户修改未改动。
