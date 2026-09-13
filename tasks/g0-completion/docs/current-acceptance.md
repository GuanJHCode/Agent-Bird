# 当前验收边界

更新于 2026-09-13。本页记录当前可审查的局部事实，不是 G0 总验收，也不授权再次运行 native、模型、launchd 或真实账号操作。

## 已完成的局部证据

- `native-transport-03` 使用固定 Codex 0.154 二进制、全新 synthetic HOME/CODEX_HOME/workspace、private UDS 和 synthetic provider 完成两轮运输。receipt 报告 `history_verified=true`、两次 HTTP 200、`external_model_calls=0`、`initialize_home_match=true`、child 已回收、private socket 已清理。
- 同一 receipt 的 14 条认证路径只保存 opaque 元数据和摘要；前后 `unchanged=true`，合成 profile 没有生成 `auth.json`。这证明本次隔离实验没有改变已登记 auth 路径，不证明真实账号认证可用于业务。
- 该样本明确保留 `durable_history_verified=false` 和 `business_ack_verified=false`。它证明的是受控 native transport 与服务返回的 history read，不是原生持久历史、Go business ACK 或完整模型流程。
- 用户级 `native-launchd-03` 的一次真实 bootstrap/bootout 及后续经授权的残留 public socket 清理已有独立记录。job、plist、lease 和 private backend 已不存在；该记录不等于永久安装，也不构成再次注册授权。
- `native-delivery-03` 已完成一次受控的真实 native + owner-helper + Go ACK 样本：单 backend、独立 owner-helper PID、持久 owner-context lease、两次 synthetic provider HTTP 200，history proof 后由显式 controller decision 产生 1 条 ACK、1 个 effect。14 条 auth 元数据前后不变，source/fixture postflight 通过，owner close→helper EOF→guardian 回收顺序成立。该样本仍是 synthetic provider，`ordinary_tui_verified=false`、`durable_history_verified=false`、外部模型和 global registration 均为 0。

## 两条 runtime 边界

`ActivationService` 位于 `tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py`，由 `projectproxy_launchd_entrypoint.py` 以 manifest 启动。它校验 profile 配置、file pins、peer 身份和 private backend，并使用 `GuardedChild` 保存 backend 的 start/stop receipt；它不自行安装 LaunchAgent，也不拥有 public socket。安装和撤销属于独立的 `ActivationTransaction`。

`proxy_native_runtime.py` 是旧的直接 PTY/TUI 控制器，负责受控样本中的 PTY、TUI、private backend、观察和正常 quit/EOF。它不是当前 ActivationService 的 guardian，也不提供 parent-death 或任意进程树清理保证。旧 TUI 样本与 socket-activation 样本不能互相替代。

## 仍未通过或正在复核

- `native-delivery-03` 只证明一个新的 synthetic provider 组合样本；普通 TUI、真实模型、原生持久 history 和更广泛 G0 生命周期仍未验收。
- `reconcile_claimed` 的 owner-context 持久绑定、send-disabled、history proof 和 claim 文件身份已实现测试；修复后的恢复语义已通过真实 Go adapter case，组合回归结果仍需按最终合并基线复核。
- G0 的完整范围仍包括持久任务、缓存主/侧切换、重连/恢复边界、正常退出和注册撤销的整体时序；已有局部样本不构成整体通过。

所有失败、UNKNOWN、前置实验和残留清理记录均保留在各自 case 目录，不用新结果覆盖历史。后续如需新的 global registration、永久安装、native 或模型运行，必须重新形成明确的受控实验入口和验收记录。
