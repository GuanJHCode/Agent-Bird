# 后继工作状态

更新于 2026-09-13。授权边界见 [`contract.md`](../../g0-auth-preserving-activation/plans/contract.md)。本页区分当前 socket-activation 服务、旧的直接 TUI runtime 和一次性用户级注册实验；没有把任一局部样本提升为完整 G0。

| 工作 | 当前状态与边界 |
| --- | --- |
| 原持久任务重新打开且不自动执行 | `proxy-resume-02` 已通过其独立验收；只覆盖固定合成任务和该样本的恢复时序。 |
| 部分任务退出、记录保留、明确继续 | `proxy-partial-02` 已通过其独立验收；初次退出保持 0 turn，明确继续后完成后缀步骤。它不证明立即停止、任意进程树清理或控制器崩溃恢复。 |
| 强制断连后的重新附着 | `proxy-reconnect-01` 已通过固定样本；不能替代持久任务恢复、缓存主/侧切换或完整 G0。 |
| 旧的直接 TUI runtime | `tasks/g0-tui-proxy/scripts/proxy_native_runtime.py` 直接管理受控 PTY、TUI 和 private backend，已有正常 quit/EOF 样本。它不是 launchd 注册器，也不拥有当前 socket-activation 服务的 guardian；其样本不能推导用户级安装或崩溃后的进程树清理。 |
| 当前 ActivationService | `tasks/g0-proxy-continuation/cold-start/scripts/projectproxy_launchd_entrypoint.py` 从 manifest 接收 Listener FD（离线测试可接收 inherited FD），由 `ActivationService` 做 profile、peer、private backend、lease 和 ready/final receipt 管理；backend 使用 `GuardedChild`。entrypoint 本身不拥有 public socket，注册/撤销由 `ActivationTransaction` 单独负责。 |
| 一次真实用户级注册/撤销 | `native-launchd-03` 已执行过唯一一次真实 bootstrap/bootout；job、plist、lease、child 和 private socket 已撤回。bootout 后曾留下 public socket，用户随后单独批准并完成了同一对象的身份复核和清理。该历史过程保留原始失败/UNKNOWN 记录，不能表述为自动撤回或永久安装已经通过。当前没有新的注册，也没有常驻安装。 |
| 私有真实 native 合成运输 | `native-transport-03` 的两轮 Codex 0.154 私有运输通过：synthetic provider 两次 HTTP 200、同一 thread、history read、14 条 auth 元数据前后不变、无外部模型、无 global registration。`durable_history_verified=false`、`business_ack_verified=false` 保持不变；这不是 helper + Go ACK 的组合验收。 |
| native + owner-helper + Go ACK 样本 | `native-delivery-03` 已局部通过：单 backend、独立 owner-helper、owner-context lease、history proof、显式 handled decision、1 条 ACK/1 个 effect，owner EOF、helper EOF、guardian 回收和 socket 清理均有 receipt。它使用 synthetic provider，`ordinary_tui_verified=false`、`durable_history_verified=false`；不代表普通 TUI、真实模型或完整 G0。 |
| delivery adapter 与恢复对账 | 正常 Go → owner-helper → history → ACK 闭环已有 production evidence。`reconcile_claimed` 已补齐原持久 owner-context binding、`send_disabled` 和旧 revision/cancelled 拒绝；恢复只允许读 history 后显式 ACK，不重新发送。最终组合回归仍在等待整合。 |
| 完整 G0 与 G1–G4 | G0 尚未全部通过；不启动 G1–G4 正式功能，不把局部 native、TUI、helper、Go ACK 或注册样本合并成整体结论。 |

现有授权允许项目内实现和一次已完成的用户级注册/撤销实验；不得影响已有 CLI 账号的 auth。没有新的注册/永久安装授权。真实账号登录、登出、切换、Keychain 操作、认证写入和全局账号配置修改均不在本页范围内。
