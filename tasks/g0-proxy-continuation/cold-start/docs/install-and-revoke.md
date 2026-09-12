# 安装、撤销与批准边界

2026-09-13 用户已批准一次用户级按需注册及对称撤销，并要求保护所有现有 CLI 账号的 auth。批准持续有效；主会话在隔离测试、固定代码复审和冻结预检完成后执行。当前未注册；隔离的原生版本检查已通过，认证文件比对一致。认证硬边界见 [contract.md](../../../g0-auth-preserving-activation/plans/contract.md)。

影响范围仍是当前用户的 GUI launchd domain、`~/Library/LaunchAgents/org.codex.orchestration.proxy.plist` 和默认 UDS。新测试 frontend/backend 必须使用各自登记的全新 HOME/CODEX_HOME/workspace，clean environment、隔离 guard 和无真实凭据的配置。服务默认拒绝未登记 PID/UID/birth/executable/hash/profile；任何真实账号的 HOME/auth/token/keyring 都不作为 backend 默认值。用户仍使用普通 `codex`，固定 Python `-I -B` 仅是内部服务入口。

## 唯一事务入口

批准已覆盖一次实验及对称撤销，不重复索权。授权不允许借用或修改真实账号凭据，不能跳过实现、认证隔离、静态预检和下列验收门槛。

安装和撤销由 [activation_transaction.py](../../../g0-auth-preserving-activation/scripts/activation_transaction.py) 的 `ActivationTransaction` 执行；接口与状态定义见 [activation-transaction.md](../../../g0-auth-preserving-activation/docs/activation-transaction.md)。控制器接收完整冻结的 `ActivationSpec` 和 auth guard，顺序调用 `prepare()`、`register()`，运行受控 fixture 后调用 `revoke()`；`managed()` 提供正常退出和异常退出的对称撤销。这里不提供绕开身份检查的手工文件复制、覆盖或删除命令。

`ActivationSpec.program_arguments` 使用 renderer 的完整结果：绝对且有 hash 的 Python、`-I -B`、绝对 entrypoint、`--manifest` 和冻结 manifest。解释器、脚本和依赖通过 `artifact_hashes` 固定；manifest 单独固定 hash。Listener 名称固定，plist 不含 RunAtLoad/KeepAlive。真实注册 controller/case 仍由主会话单独准备与准入，本页不是可直接执行的注册命令。

`prepare()` 拒绝任何既有 job、plist、public socket 或外来 lease，使用 O_EXCL 形成自身 stage/lease；发布使用 no-replace link。注册后精确核对 job label、plist path 和完整 argv。撤销前重新核对自有 plist 的 dev/ino/uid/mode/size/hash 和 job 身份；不确定则保留 UNKNOWN 与证据，不自动重试、不接管外来对象。controller 与 service 都不 unlink 或 chmod launchd-owned public socket。

## 退出与验收

正常步骤是 frontend 先退出、等待自身 backend 停止及安全 receipt，再执行事务撤销。entrypoint 已离线验证 SIGTERM/SIGINT 受控清理，不能据此声称 SIGKILL 或 controller 崩溃恢复已经覆盖。auth baseline 发生变化时只报事实，不恢复凭据备份。

当前离线证据涵盖真实 inherited listening FD、独立 fake backend process/UDS、双 frontend 隔离、同 profile 串行、probe 后 grant 可复用、未知 peer 拒绝、byte-exact relay、安全摘要、EOF/TERM/INT 清理和 public inode 保留。真实 `launch_activate_socket` 调用、实际 GUI 注册/撤销、普通 Codex 的 WebSocket 初始化和 native 生命周期仍待独立单次验收。TUI/thread attachment、旧 epoch 排除、固定 owner 回传及完整 G0 的其他门槛保持各自合同，不能由本次 fakeFD 测试替代。
