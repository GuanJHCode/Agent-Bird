# sd-qual-02：首次轮次成立，helper 在本地 binding 校验拒绝

本次没有发送 standalone toolOutput，不能判定原生回传接口成功或失败。普通 TUI 的初始输入已被真实接收，root 结束首次工具执行并输出 READY 时，合成作业仍在运行；后续 `prepare` 因双方对 `comm` 字段的合同不同而在 RPC 前拒绝。没有重发、修改旧 binding 或启动第二个样本。

运行采用主树冻结 `8eeb456`，普通 `codex app-server --listen unix://` 与普通 `codex`，native 0.153.4、Python 3.13.7、websockets 16.0。六份 Python 源与 native/Go 二进制在启动前逐一验 SHA；[启动记录](native-sd-qual-02-launch.json)保留同 PID exec 和限时 caffeinate 信息。未改模型、权限、认证、Hook、全局配置或 native argv。

| 事实 | 本次证据 |
|---|---|
| 精确 root | `01a0932d-1998-7221-8897-5822328be877` |
| 首次 turn | `01a0932d-1b16-71c3-b4ef-5982a8510db8` |
| 原生身份 | service 75573、TUI 75599，二者出生 `Sat Sep 12 09:13:21 2026`；初始 socket dev 16777234 / inode 160949842 |
| 初始窗口 | RAW `4098169531827916` → `4098289531827916`；墙钟参考 01:13:21.835540Z → 01:15:21.835540Z |
| 真正用户输入 | 独立 bracketed-paste / Enter 后，恰好一条 root `UserPromptSubmit`，01:13:25.669137Z，Hook PPID 75573 |
| 原生首次完成 | typed `event_msg.task_complete`，01:13:34.385Z，精确 turn 和 `SD_QUAL_02_READY` |
| 在途先后证明 | 真实 Go read pending → 重读精确 task_complete → 真实 Go read pending，01:13:34.541150Z–01:13:34.554183Z |
| 作业完成 | 后续真实 Go read completed / count 1，01:14:12.247923Z–01:14:12.263010Z |
| helper | prepare 01:14:12.336430Z–01:14:12.547157Z，0.210742125 秒，exit 2；未生成 producer、intent 或 send-attempt |

[控制器原记录](native-sd-qual-02-controller.json)与[独立 Hook / thread / 原生 connection 元数据](native-sd-qual-02-controller-evidence.json)保留上述链路。本次实际 SQLite source 为 `vscode`，与已核共享服务路径一致；它自身不证明通用 current-UI 附着。控制器固定阶段为 `prepare`，错误码 `evidence_validation_rejected`，source frame 落在处理 helper 非零退出处；没有超出初始 RAW 窗口补算通过。

失败发生在本地合同边界。[原 binding](native-sd-qual-02-binding.json)中的 service/tui `comm` 均为实际 `ps` 返回的裸 `codex`；controller 明确接受该值。helper `validate_binding` 第 91 行却要求 `absolute(service.comm)`。对未修改原 binding 做离线验证，稳定得到 `invalid_data`，frames 为 91 → 40。运行时 stdout 只保留 116 字节及 SHA；固定源中的该外层错误 envelope 重建后与长度、SHA 精确匹配。校验位于 `asyncio.run` / RPC 之前，因此这个 helper 调用未到原生接口；这是一条源码顺序结合实际拒绝的推断。[诊断记录](native-sd-qual-02-diagnosis.json)分别标明运行摘要、离线复现和推断，没有把它写成原生 RPC 运行证据。

这也不回答 sd01 的 `-32601` 历史接口疑问：sd02 的真实 turn 已执行并持久化，但 helper 尚未越过本地 binding。后续若修复，应分别保留实际 `ps comm` 与独立核验的可执行文件路径，不可把旧字段静默改成绝对路径后声称一致；本报告没有实施该后继变更。

本次发出 `/quit` 后，控制器 10 秒窗口内仍记录 TUI exit_code=null；随后 OS 已无 TUI PID，不能称已证明正常 `/quit` 退出。controller 75568 与 caffeinate 75570 也已退出。Go worker 76288 不再存活，真实 inspect 为 completed / count 1 / ack=null。仅保留的本任务服务经出生身份、native SHA、socket inode 再核验；自身 Unix FD 的内部闭合 pair 与剩余 peer 地址分开记录，global lsof 仅筛本服务 kernel 端点，未发现具体其他 peer 进程。01:17:57.472321Z 仅向 owned service 75573 发 SIGINT，RAW 126,590,750 ns 内服务及 socket 消失，没有停止任何 peer 进程。[收尾记录](native-sd-qual-02-cleanup.json)保留实际字段和动作。

原 case/job 保留为证据，均不复用；没有本任务存活 TUI、服务、Go worker 或 caffeinate。归档保留五份核心 JSON 的原字节与 SHA，见[清单](native-sd-qual-02-artifacts.json)；空 case.lock 留在原目录。本次使用 `-B`，没有生成待清理的任务 pyc。未保存模型推理、任意 UI 原文、其他会话历史或凭据。
