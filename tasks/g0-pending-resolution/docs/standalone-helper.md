# 单次 standalone 原生资格 helper

本工具当前只准入已登记服务、TUI、root、prior turn 和新的 `sd-qual-02` 合成样本。原 `sd-qual-01` 失败记录保留，不能用此版本重跑；helper 尚未取得原生资格阳性。需要独立源码审查及 controller 的同次身份/PTY/时间线证据。`source_service_verified:false`、`current_display_verified:false` 始终保留，不把本地检查器或 caller 提供的字段升级成通用附着证明。

```sh
python3 tasks/g0-pending-resolution/scripts/native_standalone.py prepare --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-02
python3 tasks/g0-pending-resolution/scripts/native_standalone.py send-once --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-02
# 仅已有 send-attempt 且同 boot/原 120 秒尚未结束时，显式只读补证：
python3 tasks/g0-pending-resolution/scripts/native_standalone.py observe --case ABS0700DIR --root ROOT_UUID --nonce sd-qual-02
```

参数顺序固定。macOS、现有 Python 3.13/websockets 16.0；不安装依赖。脚本同目录需要既有 `delivery_audit.py`（仅复用严格 JSON/envelope 函数，不修改它）。普通原生入口及受控服务由 controller 创建；helper 不创建 listener、不改任何配置/权限/账号，也不调用模型提供商。

case 必须为当前 UID 所有的 0700 目录。controller 先写 0600 的 `binding.json` 与 `controller-evidence.json`，后者字节 SHA 在前者登记；后续不变更这两个文件。binding 精确字段：

```text
version:1, nonce, controller_thread_id, controller_epoch:1,
expected_cwd, prior_turn_id, native_cli_version:"0.153.4", boot_id,
service:{pid,uid,birth,comm,executable_path,socket_path,socket_dev,socket_ino,native_binary_sha256},
tui:{pid,uid,birth,comm,executable_path,native_binary_sha256},
go_binary, go_binary_sha256, job_dir, controller_evidence_sha256
```

`boot_id` 为本次 `kern.bootsessionuuid` 小写 UUID；`birth` 为 `/bin/ps -p PID -o pid=,uid=,lstart=,comm=` 中 lstart 的五词，用单空格连接；`comm` 保留真实 ps 输出（本次为 `codex`），只作有界字符串与实时 ps 精确比较；必需的 `executable_path` 另由 libproc.proc_pidpath 实读并在服务/TUI两端实时精确核验，固定 SHA 核验使用该独立路径。旧缺字段记录拒绝，不把 comm 改写成路径，也不迁移旧 binding。服务与 TUI 必须不同 PID、同当前 UID；原生二进制 SHA 固定为 `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`，Go 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。controller 独立证明 socket 与服务、TUI/root/当前显示的关联；helper 只机验登记项和诊断 PID。

`prepare` 要求 Go 已完成。它验证原生 `thread/read` 的 root/source=vscode/cliVersion/cwd、显式 historyMode 为 legacy 或 paginated 且各 turn 的 itemsView 为 full、exact prior completed/READY final/root idle/所有观察工具终态，再实际执行白名单 Go `read` 和 `inspect`，只接受固定 count=1、owner/revision/hash 和未 ACK 结果。写入 0600 的 `producer.json`、`intent.json`、`prepare-meta.json`。Go event hash 绑定 envelope 的单事件 payload_hash；客户端不新增业务 ACK。

此后继精确要求 `source:vscode`：固定版本的普通 [`codex app-server` 启动分支](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/cli/src/main.rs#L1287)将 SessionSource::VSCode 传给共享 ThreadManager，远程 TUI 创建新 thread 不覆盖它。该值不证明用户运行了 VSCode，也不等于当前 TUI 附着；仍依靠 controller 独立证据。cli、appServer、unknown、subAgent 与缺失来源全部拒绝，不改原生来源设置，不宣称与 embedded 或完整 C13 等价。这个条件的 17 项回归已先 RED 后 GREEN（0.007s），见 source-red/source-green 日志。原 sd-qual-01 未取得真实持久化 user turn 的阳性证据，其失败及当时的 -32601 只读事实保留；本修正不假定后续历史读取一定可用。

固定 0.153.4 普通 TUI 默认请求 paginated 历史，旧 helper 的 legacy-only 限制会误拒真实新 root。原生 `thread/read(includeTurns:true)` 在此模式仍有[完整历史兼容路径](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/thread_processor.rs#L3281)：服务端以 Asc/Full 遍历到 cursor 结束，loaded 路径先 persist 再物化，并发送 deprecation 通知。因此本 helper 仅接受该有界完整响应，不新增 RPC 或客户端分页；summary、notLoaded、缺失 itemsView、超出原有大小限制仍拒绝。原始 historyMode 在 audit 投影中保留，不翻译成 legacy，也不将离线检查器的完整分页标志提升为 absence proof。

`send-once` 再核机验身份、原生 idle 屏障与 Go 结果。先 O_EXCL+fsync 写 `send-attempt.json`（version/status=send_intent/nonce/controller_thread_id/binding_sha256/payload_sha256/boot_id/clock_impl/mono_ns/wall_ns），随后重新核服务和 TUI 的实际身份才发送。即使意图写完但实际发送前失败，同 case 也不重发；`send_intent` 不宣称已发出。只允许一次 `turn/start{threadId,input:[],toolOutput:{name:"g0_delivery",namespace:"orchestration",output:固定canonical envelope}}`。

RPC 仅 initialize、initialized 通知、server/diagnostics、remoteControl/status/read、thread/read 和上述一次 turn/start。initialize clientInfo 为 `{name:"g0-native-standalone",version:"0.1.0"}`。只连接已登记 AF_UNIX socket，预连接 socket 禁止重定向重连，关闭代理/压缩/心跳；没有订阅、resume 或自动重连。每次 RPC 最多 10 秒；原发送意图起最多 120 秒。两者均以 `time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)` 的整数纳秒构造绝对期限，`clock_impl` 精确为 `clock_gettime_ns(CLOCK_MONOTONIC_RAW)`，`mono_ns` 是该时钟原始值。模型截止固定为原 mono_ns + 120000000000，observe 不重新相加一个“剩余窗口”。跨 boot、旧 mach_absolute_time() 时钟记录或时钟倒退均拒绝；wall_ns 仅记录诊断，墙钟跳变不延长、不重置预算。连接最多 256 帧、每帧 64 KiB、合计 4 MiB；每次观察最多 64 次 read，优先同 root 通知唤醒，无通知时约 2 秒只读 fallback。不会替原生 TUI 应答审批/用户输入请求。

发出 RPC 前、连接/握手/读取返回后均核连续时钟；系统睡眠后才收到的超窗响应不能算成功。asyncio 的唤醒本身可能受系统睡眠影响，故返回路径必须再次核绝对期限。旧 prepare/send-attempt 时钟记录不转换，不复用为新 case 的起点。

`send-once` 保持连接观察；`observe` 仅在原窗口内另作只读采样，不重置 deadline，不重发。`receipt.json` 只含 turn ID/status 与 `history_delivery_proven:false`。`send-observation.json` 或 `read-observation.json` 保存有限 facts、Go inspect/ACK、RPC 计数和最后帧 hash。只有原生 `thread/read` 同时满足精确 functionCallOutput envelope、新匹配轮 completed/ACK final、root idle、观察到的全部工具/轮次终态、匹配轮是最后轮，且真实 Go ACK handled/effect_count=1，才返回 `status:observed`/exit 0；prepare 完成也 exit 0。发送后不确定为 exit 3，前置拒绝 exit 2。

`audit-input.json` 是单次 `thread/read` 的受限投影：保留 type/id、函数名称/namespace、已知 envelope；不保存任意命令/模型正文。它可喂既有 `delivery_audit.py`，但不是原始无损响应。`acks:[]` 保持原生运输与真实 Go 业务 ACK 分离；Go ACK 原记录见 observation，不伪造 delivery-bound ACK。未见标记不能证明未交付，absence/resend 均为 false。分页/compact 的完整对账不在本样本范围。

`prior_started_at`、`prior_completed_at` 来自原生 Turn.startedAt/completedAt，仅 nullable Unix 秒。初始 root 已完成且 Go 尚 pending 的先后屏障由 controller 的固定 root task_complete 及两侧实际 Go read pending 独立采集，不用 UI prompt 回显或同秒时间戳推断。

验证：368d146 的历史基线为 14 项通过，0.006s；5f2bd86 审查后为 15 项通过，0.005s。本次 paginated 完整响应先因 unsupported_history 出现 assertion RED，最小修改后最新 16 项通过，0.007s（`standalone-helper-paginated-green.log`），并保留三种不完整视图的拒绝。单份历史的重复 turn ID 拒绝也有明确 assertion RED；集合只在一次读取内，不阻止下次读取同 ID 正常更新。非目标 namespace 必须 nullable/有界安全 ID，恶意对象泄出也已有独立 RED→GREEN。相关日志同目录 `standalone-helper-*.log`。RPC response ID 与 root active 边界同样先 RED 再修复。CLI 缺参实际 exit 2，仅固定错误 JSON；两个文件 compile 成功。发送与观察测试保留真实 Case/O_EXCL/fsync/过滤路径，仅替换不可在单元测试中启动的原生网络、机器身份与 Go 接口；不把这些 fixture 当原生证据。

原生 shape 来源为本机 0.153.4 生成的实验 schema；[固定官方 thread_data.rs](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server-protocol/src/protocol/v2/thread_data.rs) 中 history_mode/items_view 的 serde(default) 无 skip_serializing_if，故要求显式字段。[websockets 16.0 官方接口](https://websockets.readthedocs.io/en/16.0/reference/asyncio/client.html)及已安装源码用于核验 unix_connect 参数。

此次 source 后继独立通过 17 项；连续时钟的 5 条行为先 assertion RED，覆盖原始 raw 记录、墙钟前后跳、旧 uptime 记录、睡眠后发出请求、超窗响应。另两个边界 RED 覆盖旧 case 拒绝和超时前不创建未等待协程。最新 23 项通过，0.007s，无 warning，见 `standalone-helper-clock-boundary-green.log`；模拟时钟测试未修改系统时间或请求机器休眠。时钟依据为本机 SDK `clock_gettime(3)` 与 [Apple 官方说明](https://github.com/apple-oss-distributions/Libc/blob/main/gen/clock_gettime.3)：RAW 连续时钟含系统 sleep，不受墙钟/频率调整影响。

审查返工：prepare 现在于 `prepare-meta.json` 写入/fsync 完成后再次检查原 10 秒绝对期限，跨窗不能返回 prepared。新增一个定点回归包含 producer 与最后 prepare-meta 两个真实文件提交屏障，均先 RED 后 GREEN（1 项、2 个子场景，0.016s）；同 case 重跑因 O_EXCL 拒绝，已写字节保持不变。审查者原复现脚本也已由实现者重跑转绿（1 项，0.002s）。上述 23 项为上一固定版本已审历史基线，本次未重跑无关测试；最新证据为 `standalone-helper-prepare-{final,review}-{red,green}.log`。

进程字段后继：实际 sd02 原 binding 在 RPC 之前因 comm=codex 被原 absolute(comm) 拒绝，原 SHA/错误已离线复现保留。新字段合同和两端实时 executable 不匹配的检查均先 RED，最小实现后 26 项通过（0.025s）。服务/TUI在 hashes=false 的观察阶段也会重新核实际 exe 路径；仅重哈希省略。真实当前 Python 进程的只读 proc_pidpath 返回绝对普通文件路径，证明本机调用可用，不是原生服务身份证据。完整 collector/controller→helper prepare 的 sd02 离线克隆回归仍在后继交付，尚不准入新原生样本。
