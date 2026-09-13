# 后继工作状态

更新于 2026-09-13。授权边界见 [`contract.md`](../../g0-auth-preserving-activation/plans/contract.md)。本页区分已完成的离线/私有证据、一次真实 launchd 对象实验和仍待最终 native 接线的 gate；没有把任一局部样本提升为完整 G0。

| 工作 | 当前状态与边界 |
| --- | --- |
| 原持久任务重新打开且不自动执行 | `proxy-resume-02` 已通过其独立验收；只覆盖固定合成任务和该样本的恢复时序。 |
| 部分任务退出、记录保留、明确继续 | `proxy-partial-02` 已通过其独立验收；初次退出保持 0 turn，明确继续后完成后缀步骤。它不证明立即停止、任意进程树清理或控制器崩溃恢复。 |
| 强制断连后的重新附着 | `proxy-reconnect-01` 已通过固定样本；不能替代持久任务恢复、缓存主/侧切换或完整 G0。 |
| 旧的直接 TUI runtime | `tasks/g0-tui-proxy/scripts/proxy_native_runtime.py` 直接管理受控 PTY、TUI 和 private backend，已有正常 quit/EOF 样本。它不是 launchd 注册器，也不拥有当前 socket-activation 服务的 guardian；其样本不能推导用户级安装或崩溃后的进程树清理。 |
| 当前 ActivationService 与 accepted-connection receipt | `ActivationService` 已有服务本人发布的 `owner-connected-<lease_id>-<frontend.pid>.json`：在 backend peer、私有 socket inode 和 grant 通过后、首个 `initialize` 前发布，后续 `ready-*` 再关联 thread/connection/epoch。真实合成 factory/UDS 回归已覆盖正例和未知 frontend 无 receipt；accepted-connection pre-RPC gate 的最终 native 接线测试仍待最后复核，不能据此宣称完整 native 通过。 |
| launchd 真实对象实验 | 本轮唯一受控 bootstrap 与 bootout 均返回 0，自动 public socket 清理已由独立收尾检查验证；14 条认证路径摘要前后未变，job、plist、lease、socket 和 server PID 均已 gone。`launchd-native01` 整体结果仍为 UNKNOWN：只观察到 0-RPC EOF/uid 0 边界，不能宣称 owner、helper 或 backend 已启动。目录初始化的 `private01` 失败记录保留，不覆盖。下一次真实注册/撤销需要新的明确授权。 |
| 私有 native transport 与 ACK | `private-native02` 已通过继承 FD 的私有 transport 和 1 条真实 native ACK；这不等于 launchd 注册、普通 TUI、持久 history 或完整 G0。 |
| 全局交付与恢复对账 | delivery adapter 的正常发送、history proof、显式 ACK 和 send-disabled 恢复语义已有项目内证据；accepted-connection gate 的最终 native 组合仍待复核。 |
| 完整 G0 与 G1–G4 | G0 尚未全部通过；不启动 G1–G4 正式功能，不把局部 native、TUI、helper、Go ACK 或注册样本合并成整体结论。 |

## peer provenance 边界

Apple 的 [`getpeereid(3)` 说明](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)区分了 listen 端和 connect 端看到的 peer credentials：client 在自己的 socket 上看到的是执行 `listen(2)` 的对端，而这只能证明 listener provenance，不能单独证明某个 accept handler、ActivationService 或 backend 已经接管连接。XNU 的 socket API/Unix-domain 实现可参见 [`bsd/sys/socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 与 [`bsd/kern/uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)。因此本项目把 client 侧观察作为边界信号，实际 service 归属以服务本人发布的 accepted-connection receipt、kernel peer、backend PID/birth/executable 和 private socket identity 共同证明。

现有授权允许项目内实现和一次已完成的用户级注册/撤销实验；不得影响已有 CLI 账号的 auth。没有新的注册/永久安装授权。真实账号登录、登出、切换、Keychain 操作、认证写入和全局账号配置修改均不在本页范围内。
