# 私有 owner/helper/Go 组合夹具（离线审查快照）

基线 `85d0c24`，已串行继承 helper 兼容 delta `da406aa`/`9e5a948`（本 WT 对应 `8ea649f`/`779b8bf`）。本交付只增加新文件，不修改 service、adapter、owner-helper 或旧 case。尚未运行真实 native，下一原生准入始终由 root 决定。

`prepare_delivery` 接受 PreparedSyntheticFixture 和显式固定 native/Python/Go/source pins，使用实际 Go 编译器在 prepare 阶段构建 g0-return-lab。复用 root 明确批准的任务 GOCACHE/GOMODCACHE，GOENV=off、GOPROXY=off、GOSUMDB=off、GOTOOLCHAIN=local，使用隔离 HOME。构建不占用随后 `run_delivery` 的原 120 秒窗口。case/plan/attempt/result 都采用新 owner-only 路径；attempt O_EXCL 阻止重复运行。

执行链是实际任务私有 public UDS listener → ActivationService → guardian/native private UDS。owner 和 helper 是两个独立、固定源码的 Python -I -B cleanenv 进程，明确不是 native TUI；native backend 才使用原 auth-isolation SBPL。client 在 RPC 前读取并核对 case grant/manifest/source pins、public socket inode，以及 actual proxy peer PID/birth/UID/executable；public peer 是 controller，不冒认 native backend。

owner 经登记后 initialize、thread/start，等待 thread/started 与真实 service OwnerLease 发布。controller 对照真实 service record 后允许初始 G0_SYNTHETIC_READY，owner 等第一 turn/completed 并保持连接。controller 随后使用真实 Go CLI start/wait/inspect 生成 task/control.json 与 producer result；没有手写这些文件。声明 event_result 的 payload hash 来自 Go inspect。

helper 使用独立 PID grant 经 public owner-helper role 复用同一 lease/backend。Go prepare/claim、一次 adapter toolOutput、原服务 thread/read 审计和 ACK 全部发生在 helper 的同一个 DeliveryAdapter 实例，capability/proof 不跨进程。delivery_id 含 G0_SYNTHETIC_TOOL_RESULT 标记。controller 核对 helper proof、实际服务绑定、owner 仍活跃和两次 synthetic HTTP 后，写独立 controller-handled-decision.json 并显式发送 handled 决定；只有此时 helper 调真实 Go batch-ack。原 Go ACK/control/claim 文件全部由 Go 代码生成。

结果保留 native/backend PID/birth、独立 owner/helper/public peer、lease、source/config hashes、safe envelope summaries、Go job/inspect/ACK/status 与服务原始安全 snapshot；没有设备/账号/RPC body 原文。`ordinary_tui_verified=false`、`durable_history_verified=false`。adapter HistoryProof 自身 `source_service_verified=false` 不被改写；组合层另用实际 service/peer/lease 证据判断。该实验不代表真实 AI 智能、普通 TUI 附着或完整 G0 通过。

已覆盖真实 dummy service/process/UDS/WS/HTTP + 实际 Go build/CLI：正常两轮→1 个 ACK(effect_count=1)；history mismatch 与丢 helper receipt 均 UNKNOWN/0ACK/不重发。初始实现的失败收尾曾被即时 service.close 打断，现新 driver 在原 cleanup 窗内先等待实际 EOF backend 收尾，再关闭服务；不手填 process_stopped/socket_removed。

新鲜命令：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_native_delivery_case.py --tb=short
```

**3 passed in 6.84s**，`native-delivery-green.log`；初始缺执行链 RED **3 failed in 0.04s**，`native-delivery-red.log`。所有 Go 命令都实际执行，没有 skipGo。仅受控临时根和 root 允许的构建 cache 被写入。

此为可运行审查快照，尚在补充严格 helper EOF 与 owner close 的时间对应、客户端失去身份时的停止拒绝，以及各阶段原 deadline 后的零后继动作检查。修订冻结与独立审查完成前不作 native 准入声明。
