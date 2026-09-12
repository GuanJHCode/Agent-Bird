# sd-qual-03：真实 prepare 成功，发送阶段因摘要合同错配中止

本次回传资格未通过。普通 TUI 的唯一初始输入被真实接收，主控完成初始轮次时 Go 作业仍 pending；原服务的 full history、family/session 与业务 owner 检查通过，helper `prepare` 成功。随后生成了 durable `send-attempt.json`，controller 却用原 binding 文件的 raw SHA 校验 helper 的 canonical JSON SHA，立即拒绝。是否已经发送到原生服务仍不确定；没有重发或复用本 case。

运行采用主树冻结 `318da87`（code `c2aca6c`），Python 3.13.7、websockets 16.0、native 0.153.4。新鲜前检通过后，同 PID exec controller 93153；限时 caffeinate 93155 使用 `-i -t 300 -w 93153`。六份 Python 源与 native/Go 的 SHA、普通入口 argv 见[启动记录](native-sd-qual-03-launch.json)。没有 `/status`、Goal、subagent、resume 或第二次初始提交。

| 事实 | 本次记录 |
|---|---|
| 精确 root / session | `01a09385-af58-7d51-bc08-86449c36b537`；本次两者相同，合同不要求相同 |
| 首次 turn | `01a09385-b32d-78f2-b546-2c6280842022` |
| 原生进程 | service 93158、TUI 93188，出生均 `Sat Sep 12 10:50:06 2026`；实际 comm=`codex`，libproc 实际 executable_path 独立记录 |
| 原初始窗口 | RAW `4103975614843250` → `4104095614843250`，墙钟参考 02:50:07.939562Z 起 |
| 初始输入 | 一次 bracketed-paste + 一次独立 Enter；唯一 UserPromptSubmit 在 02:50:10.861852Z，Hook PPID=93158 |
| 真实业务归属 | job controller_thread=root、revision=1、worker_pid=93919 |
| 初始完成 | 精确 typed `task_complete`，02:50:20.601Z，最后标记 `SD_QUAL_03_READY` |
| 在途屏障 | 真实 Go pending → 重读该 task_complete → 真实 Go pending，02:50:20.715102Z–02:50:20.729065Z |
| prepare | 02:50:58.711216Z–02:50:59.132161Z，RAW 0.420969 秒，exit 0 |
| 唯一发送意图 | RAW `4104027034350250`，原截止 `4104147034350250`；墙钟 02:50:59.359266Z |
| 中止 | 02:50:59.364058Z，`evidence_validation_rejected` / `send-once`，receiver 第 101 行 |

[controller evidence](native-sd-qual-03-controller-evidence.json)保存同 service 的 Hook、SQLite source/cwd/version、成功执行且零行的 parent-edge 查询及有限 connection 元数据。helper 的[prepare-meta](native-sd-qual-03-prepare-meta.json)保存真实 full read 的 session 与 family 资格；`parentThreadId` / `forkedFromId` 显式 null、ephemeral=false、source=vscode。它们仅支持本次固定、独占 PTY 的受控样本，不是通用 current-UI 自动附着证明。

原 [binding](native-sd-qual-03-binding.json)的 raw SHA 为 `18a053a895178db5472b3ea49cdf71da00f60213cc60bc26788a6fb8875da122`；按已固定 `delivery_audit.canonical` 计算为 `dab2c091901e473b5783ea1a43c5a11600f9541a2d308d95dedc18deeb73d15c`。后者与 prepare 和 [send-attempt](native-sd-qual-03-send-attempt.json)一致。实际 controller 在 helper 调用前取 `c.sha(binding.json)`，receiver 要求与 attempt 相等，构成具体的跨组件合同错误。这个离线字节核对定位了实际拒绝边界，没有把 absent receipt 当作“未发请求”的证据。

controller 没有得到 send helper 的完整返回，也没有确认当前 native turn 完成，因此按合同跳过新的 quit 输入；[原 controller 记录](native-sd-qual-03-controller.json)仍保留当时进程未全部退出的事实。随后已超过原 callback 120 秒，另做一次明确标注的同原服务只读诊断：diagnostics PID=93158；精确 root full history 当时只有首次 completed turn，root idle，工具全部终态，没有 exact intent 项或 ACK marker；真实 Go inspect completed/count=1/ack=null。该观测不证明先前请求从未上 wire，也不补算原窗口通过。

[独立诊断与收尾](native-sd-qual-03-post-window.json)记录 TUI 93188、Go 93919、controller 93153、caffeinate 93155 当时均已不存在。自己的只读连接关闭后，再验 service 的 PID、出生身份、实际 executable_path/SHA 和 socket inode；未发现具体其他客户端，仅对本任务 service 93158 发 SIGINT，RAW 65,643,042 ns 内退出，socket 消失。没有把这个收尾算作 normal quit 成功，也没有停止任何 peer 进程。

原 case、job、attempt 全部保留，不修改、不复用。九份核心 JSON 按原字节归档，见[SHA 清单](native-sd-qual-03-artifacts.json)；保护文件摘要未变，没有任务 pyc 或待清理临时文件。本次没有记录模型推理、任意 UI/history 正文或凭据；观察到的真实角色与固定标记按受限投影保存。
