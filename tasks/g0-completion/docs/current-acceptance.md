# 当前验收边界

更新于 2026-09-13。本页记录当前可审查的局部事实，不是 G0 总验收。项目内修复和私有预检仍在本轮批准范围内；再次 bootstrap/bootout、永久安装或真实账号权限操作需要新的明确授权。

## 已确认的本轮事实

- `launchd-native-01` 的唯一受控 bootstrap 与 bootout 返回码均为 0；独立收尾验证 public socket 已自动清理。job、plist、lease、socket 和 server PID 均已 gone。
- 14 条认证路径只保留安全摘要，前后未变。该事实证明本轮没有改动登记的 auth 文件，不证明真实账号认证可用于业务。
- `launchd-native-01` 整体保留为 UNKNOWN：观测边界是 0-RPC EOF/uid 0，不能把它解释为 owner、helper 或 backend 已启动，也不能从 client listener provenance 推断实际服务进程归属。
- `private-native-01` 的目录初始化 failed 记录保留，不能用后续成功的私有样本覆盖它。
- `private-native-02` 已通过继承 FD 的私有 native transport 和 1 条真实 native ACK。
- `private-native-03` 已通过实际 inherited-FD native gate、owner/helper 前置服务 receipt gate、单 backend、2 次 synthetic HTTP 200、Go ACK 1、effect 1、pending 0、`send_allowed=false`；14 条 auth 摘要 unchanged，owner/helper/service/backend 原身份 gone，双 socket absent，结果为 `REVOKED(fake)`。该结果仍不证明 ordinary TUI、durable history 或完整 G0。
- root 提供的 main `a671d65` 全套整合回归为 601 passed、116 subtests、116.42s。

## 当前服务接线

`ActivationService` 位于 `tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py`，由 `projectproxy_launchd_entrypoint.py` 以 manifest 启动。服务在首次 `initialize` 或其他 RPC 前发布 `owner-connected-<lease_id>-<frontend.pid>.json`，绑定 activation、manifest、grant、profile/context、frontend peer、backend peer、backend PID/birth/executable 和 private socket identity；`ready-*` 后续再补 thread/connection/epoch。服务端 synthetic factory/UDS 正负回归和 `private-native-03` 的 owner/helper 前置 gate 已验证这一边界；它仍不能替代 ordinary TUI 或 durable history 验收。

`proxy_native_runtime.py` 是旧的直接 PTY/TUI 控制器，负责受控样本中的 PTY、TUI、private backend、观察和正常 quit/EOF。它不是当前 ActivationService 的 guardian，也不提供 parent-death 或任意进程树清理保证。旧 TUI 样本与 socket-activation 样本不能互相替代。

## 仍未通过或正在复核

- `launchd-native-01` 的 0-RPC EOF/uid 0 结果仍是 UNKNOWN；本轮不虚构 owner/helper/backend 启动证据。
- `private-native-03` 的服务 receipt gate 和 ACK 只覆盖该受控 inherited-FD 样本；ordinary TUI、durable history 和完整 G0 仍未验收。
- G0 的完整范围仍包括持久任务、缓存主/侧切换、重连/恢复边界、正常退出和注册撤销的整体时序；已有局部样本不构成整体通过。

所有失败、UNKNOWN、前置实验和清理记录均保留在各自 case 目录。后续真实注册、永久安装或真实账号权限操作必须重新形成明确的受控实验入口和授权；不影响普通 TUI、既有持久 history 或 G1–G4 边界。

## 依据

Apple [`getpeereid(3)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)说明 client 侧得到的是 listener 对端 credentials；XNU 的 [`socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 和 [`uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)是本项目复核 Unix-domain socket peer 语义时使用的实现来源。listener provenance 与 actual accepted service 归属因此分开记录。
