# 当前验收边界

更新于 2026-09-13。本页记录当前可审查的局部事实，不是 G0 总验收，也不授权再次运行 native、模型、launchd 或真实账号操作。

## 已确认的本轮事实

- 本轮唯一受控 launchd 对象的 bootstrap 与 bootout 返回码均为 0；独立收尾验证 public socket 已自动清理。job、plist、lease、socket 和 server PID 均已 gone。
- 14 条认证路径只保留安全摘要，前后未变。该事实证明本轮没有改动登记的 auth 文件，不证明真实账号认证可用于业务。
- `launchd-native01` 保留为 UNKNOWN：观测边界是 0-RPC EOF/uid 0，不能把它解释为 owner、helper 或 backend 已启动，也不能从 client listener provenance 推断实际服务进程归属。
- 目录初始化 `private01` 的 failed 记录保留，不能用后续成功的私有样本覆盖它。
- `private-native02` 已通过继承 FD 的私有 native transport 和 1 条真实 native ACK；该结果不包含 launchd 注册，不证明普通 TUI、持久 history 或完整模型流程。

## 当前服务接线

`ActivationService` 位于 `tasks/g0-proxy-continuation/cold-start/scripts/activation_service.py`，由 `projectproxy_launchd_entrypoint.py` 以 manifest 启动。服务在首次 `initialize` 或其他 RPC 前发布 `owner-connected-<lease_id>-<frontend.pid>.json`，绑定 activation、manifest、grant、profile/context、frontend peer、backend peer、backend PID/birth/executable 和 private socket identity；`ready-*` 后续再补 thread/connection/epoch。该 accepted-connection pre-RPC gate 已有真实合成 factory/UDS 正负回归，最终 native runner 组合仍在最后测试，不能提前称为完整通过。

`proxy_native_runtime.py` 是旧的直接 PTY/TUI 控制器，负责受控样本中的 PTY、TUI、private backend、观察和正常 quit/EOF。它不是当前 ActivationService 的 guardian，也不提供 parent-death 或任意进程树清理保证。旧 TUI 样本与 socket-activation 样本不能互相替代。

## 仍未通过或正在复核

- `launchd-native01` 的 0-RPC EOF/uid 0 结果仍是 UNKNOWN；本轮不虚构 owner/helper/backend 启动证据。
- accepted-connection pre-RPC gate 需要最后的 native 接线与 receipt 消费回归；服务端 receipt 本身不能替代 client/controller 的 kernel/process 验证。
- `private-native02` 的 ACK 是私有 transport 边界证据，不自动升级为 durable history、ordinary TUI 或完整 G0 业务验收。
- G0 的完整范围仍包括持久任务、缓存主/侧切换、重连/恢复边界、正常退出和注册撤销的整体时序；已有局部样本不构成整体通过。

所有失败、UNKNOWN、前置实验和清理记录均保留在各自 case 目录。下一次真实注册、永久安装、native 或模型运行必须重新形成明确的受控实验入口和授权；不影响普通 TUI、既有持久 history 或 G1–G4 边界。

## 依据

Apple [`getpeereid(3)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)说明 client 侧得到的是 listener 对端 credentials；XNU 的 [`socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 和 [`uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)是本项目复核 Unix-domain socket peer 语义时使用的实现来源。listener provenance 与 actual accepted service 归属因此分开记录。
