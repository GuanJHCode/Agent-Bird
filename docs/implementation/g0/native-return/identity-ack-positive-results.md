# 原生 root 的 owned start / inspect / ACK 正向集成

**一个新原生 root 用自己的真实环境完成了固定计数任务的 owned start、读取结果、inspect、ACK 和 ACK 后复核。** 持久记录为 `decision_count=1`、`effect_count=1`。本组只验证这个正向闭环；未执行 child/fork 的负向 ACK，不能据此宣布跨 thread 拒绝或完整 C09 通过。

原生 `/status` ID 为 `01a08e8d-97cd-7f01-adb7-5a51990d8d81`，TUI PID `85391`、创建时间 11:40:38 +0800、PTY `82298`。正常入口 `codex-cli 0.153.4`；只有 `TERM` 显示变量覆盖。metadata 的精确 ID、`source=cli`、专用 cwd 与实际工具输出中的两项环境字段一致：`CODEX_THREAD_ID`、`CODEX_SESSION_ID` 都是本 root ID。未创建 Goal、child 或 fork，未更改原生身份字段、权限、认证或配置。

使用已审查 ACK 变体 `65d8311` 的二进制 `g0-return-lab-ack`，实测前核对 SHA-256 为 `8ed9567aaf95867204f448aa5bd498a7ce5895790a0f24149419d9b460c962db`。数据目录为本次预建的 `test-repo/tasks/native-return/data/identity-ack-root-01`（0700）；nonce 为 `nr-g0-identity-ack-root-01`。Host 只产生本目录内固定合成 JSON，不分发业务程序。

| UTC 时间 | 实际原生工具证据 |
|---|---|
| 03:41:31.136 | owned start 返回 `controller_thread` 等于本 root、`task_revision=1`，worker PID `86064`，delay 60s |
| 03:41:44.835 | 独立 `ps` 记录 worker PID 86064、ppid 1、OS 创建时间 11:41:31 +0800；这与 job 的元数据时间分别记录 |
| 03:42:31.180 | Host wait 返回固定 `completed/count=1` |
| 03:42:37.177 | 首次 inspect 返回本 root owner、revision 1、实际 event_hash，`ack=null` |
| 03:45:44.016 | 正向 ACK 阶段重新 inspect，仍为同一事件且 `ack=null` |
| 03:45:53.305 | 本 root 使用已有 `$CODEX_THREAD_ID` 与刚读取的实际 hash 执行 ACK；返回 `acknowledged`、handled、decision/effect_count 各 1 |
| 03:45:59.981 | 再次 inspect 返回相同的已提交 ACK |

原生 producer 轮耗时 86.383 秒，ACK 轮耗时 32.013 秒，均在各自 120 秒模型上限内完成；采集窗口各增加 10 秒本地观察且仍小于 120 秒。没有依赖扩大模型窗口取得结果。Host ACK 的判定依据是实际 `custom_tool_call_output` 的固定 JSON，以及相同的 `ack-r1.json` 持久记录。

事件 hash 为 `addcdfda9e28799b4bf5e02a4ace5693d7a067c6e12234b7f8f42b2f4bbb4814`。按已审查合同的字段顺序，用 nonce、实际 root、producer revision 1 和四字段 result 重新计算，和三次 inspect、ACK 工具返回及磁盘 ACK 相同。command ID 为 `nr-g0-identity-ack-root-01-cmd01`。这些计数仅描述合成记录，不是第三方执行的 exactly-once 证明。

证据：[原生类型与严格固定 envelope](identity-ack-positive-events.json)、[磁盘固定记录](identity-ack-persisted.json)、[进程创建身份及退出清理](identity-ack-processes.json)。[专用采集器](../../../../tasks/g0-native-return/scripts/identity_ack_collect.py) 只读取这个硬编码自有 root，复用已提交的完整 JSON 解码器；每个 envelope 必须匹配精确键集合、类型、nonce、owner、hash、revision 和固定值才导出。额外字段及 bool 冒充数字不匹配；任意工具正文、推理及其他环境字段均不导出。

最后核对正常 `/quit` 的 PTY exit 0，并确认 PID 85391、86064 都已消失。仅删除本次创建且仍为空的 `identity-ack-fork-01` 目录；保留 root 任务的 job/control/result/ACK 作为必要证据。未向任何进程发送信号。尚未验证 child/fork 对旧 owner 的拒绝，这组正向结果也不能修复此前已复现的原生 Goal 编辑竞态。
