# Global delivery validation acceptance

更新时间：2026-09-13。本文只记录已完成的受控证据和剩余 gate。项目内修复和私有预检仍在本轮批准范围内；再次 bootstrap/bootout、永久安装或真实账号权限操作需要新的明确授权。

## 已确认

- `launchd-native-01` 的唯一受控 bootstrap/bootout 均为退出码 0；public socket 的自动清理已经由独立 postflight 验证。job、plist、lease、socket、server PID 均已 gone。
- 14 条认证路径的安全摘要前后 unchanged。
- `private-native-01` 的目录初始化 failed 记录保留。
- `private-native-02` 通过 inherited FD 的私有 native transport，并完成 1 条真实 native ACK。
- `private-native-03` 通过实际 inherited-FD native gate、owner/helper 前置服务 receipt gate、单 backend、2 次 synthetic HTTP 200、Go ACK 1/effect 1/pending 0/`send_allowed=false`；owner/helper/service/backend 原身份 gone，双 socket absent，结果为 `REVOKED(fake)`。
- `launchd-native-01` 为 UNKNOWN。0-RPC EOF/uid 0 只提供边界观测；没有足够证据证明 owner/helper/backend 曾启动，因此本页不作该推断。
- root 提供的 main `a671d65` 全套整合回归为 601 passed、116 subtests、116.42s。

## accepted-connection gate

当前 `ActivationService` 会在服务端完成 admission、backend peer、backend PID/birth/executable 和 private socket identity 复核后发布 lease-scoped `owner-connected-<lease_id>-<frontend.pid>.json`。该 receipt 位于服务自己的 anchored receipt directory，包含 grant/profile/context/frontend/backend/socket 的安全投影，不包含 thread、epoch 或 RPC body；后续 `ready-*` receipt 再关联 owner thread/connection/epoch。服务端 synthetic factory/UDS 与 `private-native-03` 的 owner/helper 前置 gate 已覆盖 receipt 先于首个 `initialize` 及未知 frontend 不产生 receipt。

## provenance 约束

Apple 的 [`getpeereid(3)`](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpeereid.3.html)明确区分 listen 与 connect 两侧看到的 peer credentials；client 侧得到 listener provenance，不等于实际 accept service/backend 已接管连接。XNU 参考实现见 [`bsd/sys/socket.h`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/socket.h) 和 [`bsd/kern/uipc_usrreq.c`](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/uipc_usrreq.c)。实际 service 归属必须由服务本人 receipt 加 kernel peer、process identity 和 private socket identity 共同证明。

## 后续边界

本轮不影响普通 TUI、既有持久 history 或 G1–G4 gate；G0 仍未整体通过。下一次真实注册、永久安装或真实账号权限操作需要新的明确授权。
