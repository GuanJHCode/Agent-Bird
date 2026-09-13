# Global delivery validation acceptance

更新时间：2026-09-13。本文只记录已完成的受控证据和剩余 gate，不授权新的 native、模型、launchd 注册或真实账号操作。

## 已确认

- 唯一受控 launchd 对象的 bootstrap/bootout 均为退出码 0；public socket 的自动清理已经由独立 postflight 验证。
- 14 条认证路径的安全摘要前后 unchanged；job、plist、lease、socket、server PID 均已 gone。
- `private-native02` 通过 inherited FD 的私有 native transport，并完成 1 条真实 native ACK。
- `launchd-native01` 为 UNKNOWN。0-RPC EOF/uid 0 只提供边界观测；没有足够证据证明 owner/helper/backend 曾启动，因此本页不作该推断。
- `private01` 目录初始化失败记录保留，作为失败证据，不被后续 case 覆盖。

## accepted-connection gate

当前 `ActivationService` 会在服务端完成 admission、backend peer、backend PID/birth/executable 和 private socket identity 复核后发布 lease-scoped `owner-connected` receipt。该 receipt 位于服务自己的 anchored receipt directory，包含 grant/profile/context/frontend/backend/socket 的安全投影，不包含 thread、epoch 或 RPC body；后续 `ready-*` receipt 再关联 owner thread/connection/epoch。

服务端 synthetic factory/UDS 正负回归已验证 receipt 先于首个 `initialize`，未知 frontend 不产生 receipt。最终 native/controller 消费该 receipt 的组合测试仍在最后复核，当前不把它标为整体通过。

## provenance 约束

Apple 的 [`getpeereid(3)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)明确区分 listen 与 connect 两侧看到的 peer credentials；client 侧得到 listener provenance，不等于实际 accept service/backend 已接管连接。XNU 参考实现见 [`bsd/sys/socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 和 [`bsd/kern/uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)。实际 service 归属必须由服务本人 receipt 加 kernel peer、process identity 和 private socket identity 共同证明。

## 后续边界

下一次真实注册/撤销需要新的明确授权。本轮不影响普通 TUI、既有持久 history 或 G1–G4 gate；G0 仍未整体通过。
