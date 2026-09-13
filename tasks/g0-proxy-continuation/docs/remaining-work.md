# 后继工作状态

更新于 2026-09-13。授权边界见 [`contract.md`](../../g0-auth-preserving-activation/plans/contract.md)。本页区分已完成的离线/私有证据、一次真实 launchd 对象实验和仍未覆盖的整体 G0；没有把任一局部样本提升为完整 G0。

| 工作 | 当前状态与边界 |
| --- | --- |
| 原持久任务重新打开且不自动执行 | `proxy-resume-02` 已通过其独立验收；只覆盖固定合成任务和该样本的恢复时序。 |
| 部分任务退出、记录保留、明确继续 | `proxy-partial-02` 已通过其独立验收；初次退出保持 0 turn，明确继续后完成后缀步骤。它不证明立即停止、任意进程树清理或控制器崩溃恢复。 |
| 强制断连后的重新附着 | `proxy-reconnect-01` 已通过固定样本；不能替代持久任务恢复、缓存主/侧切换或完整 G0。 |
| 旧的直接 TUI runtime | `tasks/g0-tui-proxy/scripts/proxy_native_runtime.py` 直接管理受控 PTY、TUI 和 private backend，已有正常 quit/EOF 样本。它不是 launchd 注册器，也不拥有当前 socket-activation 服务的 guardian；其样本不能推导用户级安装或崩溃后的进程树清理。 |
| 当前 ActivationService 与 accepted-connection receipt | `ActivationService` 已有服务本人发布的 `owner-connected-<lease_id>-<frontend.pid>.json`：在 backend peer、私有 socket inode 和 grant 通过后、首个 `initialize` 前发布，后续 `ready-*` 再关联 thread/connection/epoch。真实合成 factory/UDS 回归和 `private-native-03` 的 owner/helper 前置 gate 已覆盖正例；这仍不等于 ordinary TUI 或 durable history。 |
| launchd 真实对象实验 | `launchd-native-01` 的唯一受控 bootstrap 与 bootout 均返回 0，自动 public socket 清理已由独立收尾检查验证；14 条认证路径摘要前后未变，job、plist、lease、socket 和 server PID 均已 gone。但该整体 case 仍为 UNKNOWN：观测到 0-RPC EOF/uid 0，不能宣称 owner、helper 或 backend 已启动。`private-native-01` 的目录初始化 failed 记录保留，不覆盖。下一次真实注册/永久安装/真实账号权限需要新的明确授权。 |
| 私有 native transport 与 ACK | `private-native-02` 已通过继承 FD 的私有 transport 和 1 条真实 native ACK；`private-native-03` 进一步通过实际 inherited-FD native gate、单 backend、2 次 synthetic HTTP 200、Go ACK 1/effect 1、pending 0、`send_allowed=false`，auth14 unchanged，owner/helper/service/backend 身份 gone，双 socket absent，结果为 `REVOKED(fake)`。这些样本不证明 ordinary TUI 或 durable history。 |
| 全局整合回归 | root 提供的 main `a671d65` 全套回归为 601 passed、116 subtests、116.42s。该结果不把 UNKNOWN 的 `launchd-native-01` 或未覆盖的 durable history/ordinary TUI 提升为 G0 通过。 |
| 完整 G0 与 G1–G4 | G0 尚未全部通过；G1–G4 保持暂停，不把局部 native、TUI、helper、Go ACK 或注册样本合并成整体结论。 |

## peer provenance 边界

Apple 的 [`getpeereid(3)` 说明](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)区分了 listen 端和 connect 端看到的 peer credentials：client 在自己的 socket 上看到的是执行 `listen(2)` 的对端，而这只能证明 listener provenance，不能单独证明某个 accept handler、ActivationService 或 backend 已经接管连接。XNU 的 socket API/Unix-domain 实现可参见 [`bsd/sys/socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 与 [`bsd/kern/uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)。因此本项目把 client 侧观察作为边界信号，实际 service 归属以服务本人发布的 accepted-connection receipt、kernel peer、backend PID/birth/executable 和 private socket identity 共同证明。

项目内修复和私有预检仍在本轮批准范围内；下一次真实注册、永久安装或真实账号权限操作需要新的明确授权。不得影响已有 CLI 账号的 auth。真实账号登录、登出、切换、Keychain 操作、认证写入和全局账号配置修改均不在本页范围内。
