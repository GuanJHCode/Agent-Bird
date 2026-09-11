# 单次 standalone 原生资格 helper

本工具只覆盖已登记服务、TUI、root、prior turn 和 `sd-qual-01` 合成任务的一次样本。尚未进行原生运行；需要独立源码审查及 controller 的同次身份/PTY/时间线证据。`source_service_verified:false`、`current_display_verified:false` 始终保留，不把本地检查器或 caller 提供的字段升级成通用附着证明。

```sh
python3 tasks/g0-pending-resolution/scripts/native_standalone.py prepare --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-01
python3 tasks/g0-pending-resolution/scripts/native_standalone.py send-once --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-01
# 仅已有 send-attempt 且同 boot/原 120 秒尚未结束时，显式只读补证：
python3 tasks/g0-pending-resolution/scripts/native_standalone.py observe --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-01
```

参数顺序固定。macOS、现有 Python 3.13/websockets 16.0；不安装依赖。脚本同目录需要既有 `delivery_audit.py`（仅复用严格 JSON/envelope 函数，不修改它）。普通原生入口及受控服务由 controller 创建；helper 不创建 listener、不改任何配置/权限/账号，也不调用模型提供商。

case 必须为当前 UID 所有的 0700 目录。controller 先写 0600 的 `binding.json` 与 `controller-evidence.json`，后者字节 SHA 在前者登记；后续不变更这两个文件。binding 精确字段：

```text
version:1, nonce, controller_thread_id, controller_epoch:1,
expected_cwd, prior_turn_id, native_cli_version:"0.153.4", boot_id,
service:{pid,uid,birth,comm,socket_path,socket_dev,socket_ino,native_binary_sha256},
tui:{pid,uid,birth,comm,native_binary_sha256},
go_binary, go_binary_sha256, job_dir, controller_evidence_sha256
```

`boot_id` 为本次 `kern.bootsessionuuid` 小写 UUID；`birth` 为 `/bin/ps -p PID -o pid=,uid=,lstart=,comm=` 中 lstart 的五词，用单空格连接；`comm` 为实际可执行路径。服务与 TUI 必须不同 PID、同当前 UID；原生二进制 SHA 固定为 `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`，Go 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。controller 独立证明 socket 与服务、TUI/root/当前显示的关联；helper 只机验登记项和诊断 PID。

`prepare` 要求 Go 已完成。它验证原生 `thread/read` 的 root/source=cli/cliVersion/cwd、显式 legacy/full、exact prior completed/READY final/root idle/所有观察工具终态，再实际执行白名单 Go `read` 和 `inspect`，只接受固定 count=1、owner/revision/hash 和未 ACK 结果。写入 0600 的 `producer.json`、`intent.json`、`prepare-meta.json`。Go event hash 绑定 envelope 的单事件 payload_hash；客户端不新增业务 ACK。

`send-once` 再核机验身份、原生 idle 屏障与 Go 结果。先 O_EXCL+fsync 写 `send-attempt.json`（version/status=send_intent/nonce/controller_thread_id/binding_sha256/payload_sha256/boot_id/clock_impl/mono_ns/wall_ns），随后重新核服务和 TUI 的实际身份才发送。即使意图写完但实际发送前失败，同 case 也不重发；`send_intent` 不宣称已发出。只允许一次 `turn/start{threadId,input:[],toolOutput:{name:"g0_delivery",namespace:"orchestration",output:固定canonical envelope}}`。

RPC 仅 initialize、initialized 通知、server/diagnostics、remoteControl/status/read、thread/read 和上述一次 turn/start。initialize clientInfo 为 `{name:"g0-native-standalone",version:"0.1.0"}`。只连接已登记 AF_UNIX socket，预连接 socket 禁止重定向重连，关闭代理/压缩/心跳；没有订阅、resume 或自动重连。每次 RPC 最多 10 秒；原发送意图起最多 120 秒，跨 boot、单调时钟变化或墙钟偏差超过 2 秒均拒绝/uncertain。连接最多 256 帧、每帧 64 KiB、合计 4 MiB；每次观察最多 64 次 read，优先同 root 通知唤醒，无通知时约 2 秒只读 fallback。不会替原生 TUI 应答审批/用户输入请求。

`send-once` 保持连接观察；`observe` 仅在原窗口内另作只读采样，不重置 deadline，不重发。`receipt.json` 只含 turn ID/status 与 `history_delivery_proven:false`。`send-observation.json` 或 `read-observation.json` 保存有限 facts、Go inspect/ACK、RPC 计数和最后帧 hash。只有原生 `thread/read` 同时满足精确 functionCallOutput envelope、新匹配轮 completed/ACK final、root idle、观察到的全部工具/轮次终态、匹配轮是最后轮，且真实 Go ACK handled/effect_count=1，才返回 `status:observed`/exit 0；prepare 完成也 exit 0。发送后不确定为 exit 3，前置拒绝 exit 2。

`audit-input.json` 是单次 `thread/read` 的受限投影：保留 type/id、函数名称/namespace、已知 envelope；不保存任意命令/模型正文。它可喂既有 `delivery_audit.py`，但不是原始无损响应。`acks:[]` 保持原生运输与真实 Go 业务 ACK 分离；Go ACK 原记录见 observation，不伪造 delivery-bound ACK。未见标记不能证明未交付，absence/resend 均为 false。分页/compact 的完整对账不在本样本范围。

`prior_started_at`、`prior_completed_at` 来自原生 Turn.startedAt/completedAt，仅 nullable Unix 秒。初始 root 已完成且 Go 尚 pending 的先后屏障由 controller 的固定 root task_complete 及两侧实际 Go read pending 独立采集，不用 UI prompt 回显或同秒时间戳推断。

验证：最终 14 项离线测试通过，0.006s；相关 RED/GREEN 日志同目录 `standalone-helper-*.log`。新增 RPC response ID/恶意 namespace 及 root active 边界均先 assertion RED 再修复。CLI 缺参实际 exit 2，仅固定错误 JSON；两个文件 compile 成功。发送与观察测试保留真实 Case/O_EXCL/fsync/过滤路径，仅替换不可在单元测试中启动的原生网络、机器身份与 Go 接口；不把这些 fixture 当原生证据。

原生 shape 来源为本机 0.153.4 生成的实验 schema；[固定官方 thread_data.rs](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs) 中 history_mode/items_view 的 serde(default) 无 skip_serializing_if，故要求显式字段。[websockets 16.0 官方接口](https://websockets.readthedocs.io/en/16.0/reference/asyncio/client.html)及已安装源码用于核验 unix_connect 参数。
