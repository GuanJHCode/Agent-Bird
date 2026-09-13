# 真实注册后的后续边界

本轮单次真实授权已用于 `launchd-native-01`：bootstrap 和 bootout 均返回 0，job、plist、lease、public socket 已撤回，服务已退出，14 项认证摘要/元数据未变。样本在无 RPC 唤醒阶段因 listener UID 为 0 被拒绝，没有 owner/helper/native backend、模型请求或 ACK。整体保持 UNKNOWN，不能以成功撤销代替投递通过。

Apple `getpeereid(3)` 与 XNU 说明：客户端侧凭据可来自 `listen()` 的创建者；继承 launchd listener 时，不能把它等同于实际接收连接的服务进程。依据：[Apple getpeereid](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)、[XNU UNIX socket 实现](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)。EOF 探针也不证明持久连接会返回哪组 PID 字段。

项目内修复采用明确的 RPC 前校验：

1. 公共连接先完成无敏感数据的 HTTP Upgrade；固定源码 child 暂停，不发送 RPC，不创建 Go claim。
2. 服务按原 UID/PID/birth/executable/grant 规则接受 frontend，并自行发布不可变 owner-connected 或 helper-ready receipt。
3. controller 通过稳定文件 FD、manifest/source pins、launchd job、live service/frontend/backend 身份和 socket inode 核验同一连接，随后才通过受控 pipe 放行。
4. owner 后续 ready 必须对应同一 lease/backend，再绑定真实 connection/epoch/thread；helper 的 history、明确 handled 决定和 Go ACK 保持原合同。
5. launchd 模式将客户端 public peer 作为 listener provenance；私有 inherited-FD 模式保留额外的完整 peer 比对。模式钉在 manifest，不由 RPC 或用户输入切换。

这不是新的 server RPC 或账号权限，也不是对任意恶意同 UID 程序的通用认证保证。服务端既有准入和 helper RPC 白名单保持不变。普通 TUI、持久 native history 和完整 G0 仍未由此证明。

下一次真实重测必须取得新的单次授权；当前仅允许项目内实现、受控测试和私有原生预检。重测仍只使用同一临时 label `org.codex.orchestration.proxy`、全新隔离 profile、本地合成 provider、14 项 auth 保护和对称撤销，不永久安装、不登录/登出、不复制/恢复认证、不修改已有目录权限。不自动再次 bootstrap。

## 已就绪的重测方案（尚未执行）

修复源码 `a671d65aa9c920c620e9c71eb949e8d092b2d016` 已通过独立代码审查、主线 601 项测试和 116 项子测试；`private-native-03` 用相同固定 native 通过新的 pre-RPC gate、同一 backend 的投递与一次 Go ACK，并完成清理与 14 项 auth 前后核验。

收到下一次单次授权后，root 使用相同 `prepare_global` / `run_global` 入口、`scheduler_mode='launchd'` 和全新 `launchd-native-02` case；执行前重新冻结实际 source/native/Python/Go 与保护清单，先确认 label/plist/lease/socket 均无外来对象，再最多 bootstrap 一次。验收 owner/helper 服务回执、同 lease/backend/history、明确 handled 后 ACK 一次及最终撤销。失败只做必要收尾并保留 UNKNOWN，不自动重注册。该方案不启动 G1–G4，也不扩大账号权限。
