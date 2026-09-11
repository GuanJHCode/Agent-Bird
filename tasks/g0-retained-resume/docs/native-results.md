# 原生退出保留与明确续接：当前证据

**quit02、quit03、quit04 三次正常退出，以及一次带父守卫的 kill01，均观察到“停止执行、保留进度、同一 root 明确续接”的完整受控链路。** 四次停止都在 10 秒内确认，重新打开后至少 10 秒没有自动执行，明确继续后的 ACK 都在原定 120 秒内。此前的 quit01 排除，无守卫强杀探针仍为 FAIL。本结果只覆盖这组单进程合成工具，不代表完整 G0 通过。

全部使用普通 Codex CLI 0.153.4，已审查的固定 Go 二进制 SHA 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。没有 Goal、MCP 回调、源码/权限修改或旧工具 future 恢复。汇总见 [最终核对](native-final-checks.json)。

## quit01：准备登记竞争，排除中断基线

首个新 root 为 `01a08f2e-acf4-7572-9232-a99710828fbf`。模型窗口固定为 06:51:01.085–06:53:01.085 UTC；观察器在首次 turn 前查询 SQLite，此时 root 尚未登记，入口拒绝。controller 的后续恢复观察没有及时连贯推进，Go 已于 06:52:00.752 完成 12/12 步，无法再提供在途停止证据。仅于 06:54:36.694 正常清理 TUI，exit 0；不重置窗口、复用目录或把已经完成后关闭算作中断成功。[生命周期与最终 prefix](native-retained-quit-01-lifecycle.json)、[原始 120 秒范围的原生记录](native-retained-quit-01-initial-thread.json)。

主 Agent 明确授权仅新增固定 `rr-retained-quit-04` 替代这次缺失的正常中断样本；collector/watch 注册收紧为 quit01–04 与 kill01 五个确定 nonce，quit01 永久保留为 excluded。注册回归先有两个红灯，修后 collector 的六项测试通过，不开放任意其他 nonce。

## quit02：第一个完整正常退出样本

root 为 `01a08f42-d580-7211-b526-28a87969d42d`。初始模型窗口为 06:58:41.358–07:00:41.358；参数 20 步 × 4 秒。此次提交、等待首次 root 登记、ready、armed、controller 再核验、实际 `/quit` 与停止观察全部在同一持续 controller 内推进。

| UTC 时间 | 事实 |
|---|---|
| 06:58:53.566 | root 仍 busy；1/20 步已持久化；Go PID 22700 的直接父为 Codex PID 22223，稳定锁 held |
| 06:58:55.959 | controller 读取 armed proof/hash，再核对双方创建身份、父链、锁和未完成进度，然后真实提交 `/quit` |
| 06:59:01.283 | **5.324547 秒**内确认双方原创建身份消失，同一 device/inode 的锁可取；TUI exit 0；segment 1 / step 1 与字节 hash 保留 |
| 07:02:23.463 | 无 prompt 普通 `codex resume <确切 root>`；新 TUI PID 23523，`/status` 同一 root |
| 随后 10.128 秒 | 旧 worker 不存在，锁可取，checkpoint/prefix 完全不变，原生 task_started=0；该段原生采集 events=0 |
| 07:06:39.244 | controller 按已授权实验明确输入“继续这一合成任务 rr-retained-quit-02”；新的处理窗口截止 07:08:39.244 |
| 07:07:01.175 / 05.183 | 真正 `retained-resume` 新建 segment 2 / worker 25231，直接父为新 TUI；从旧 step 1 前缀继续，step 2 已完成且旧 hash 相同 |
| 07:08:17.485 | segment 2 completed，20/20 / effect_count 20；所有旧 prefix 字节不变 |
| 07:08:24.779 | 原生 ACK/task_complete，在明确继续后的 **105.535 秒**内；随后正常 `/quit`，exit 0 |

初始只有一次真正 retained-start。继续阶段是一次 retained-inspect、一次 retained-resume、一次本次进程 session 的阻塞 join、最后再一次 retained-inspect；真实 completed 数据来自 root 的 `custom_tool_call_output`，用户继续文本不承载伪造结果。正常退出时 cached status 仍为 running；PID/创建身份及锁证明其已停止，不能把缓存当作存活证据。没有捕获具体 worker 终止信号，不据此断言 SIGTERM 或 SIGKILL。

证据：[检查汇总](native-retained-quit-02-checks.json)、[生命周期](native-retained-quit-02-lifecycle.json)、[ready](native-retained-quit-02-ready.json)、[armed](native-retained-quit-02-stopped-armed.json)、[controller 动作及前置复核](native-retained-quit-02-action.json)、[停止观察](native-retained-quit-02-stopped.json)、[恢复静默](native-retained-quit-02-idle.json)、[新执行父链](native-retained-quit-02-resume-ready.json)、[最终步骤 hash](native-retained-quit-02-final.json)、[继续阶段原生工具结果与角色](native-retained-quit-02-continued-thread.json)。

## quit03、quit04、kill01：其余固定样本

后续参数为 12 步 × 4 秒。每次都是独立 root、nonce 与 0700 目录，首次至少完成一个持久步骤后，controller 才接受 armed proof，并在真实动作前再核对双方 PID/出生身份、直接父链与同 inode 锁。退出、停止观察、普通无 prompt 精确 resume、静默观察和明确继续均由同一持续 controller 连贯推进。

| 样本 / root | 动作后双方原创建身份消失、锁释放 | 恢复静默 | 明确继续 → ACK | 最终结果 |
|---|---:|---:|---:|---|
| quit03 / `01a08f57-d3a8-7e72-8683-0058c9b9c667` | 3.544911 秒 | 10.050900 秒 | 07:22:13.857 → 07:23:23.940；70.083 秒 | segment 2，12/12，effect_count 12 |
| quit04 / `01a08f63-3899-75d3-a0ea-00fc3323ddc0` | 5.731129 秒 | 10.096726 秒 | 07:34:41.845 → 07:35:52.625；70.780 秒 | segment 2，12/12，effect_count 12 |
| kill01 / `01a08f65-0a8d-7b53-aab2-7714c0f59720` | 0.060753 秒 | 10.037441 秒 | 07:36:33.178 → 07:37:40.850；67.672 秒 | segment 2，12/12，effect_count 12 |

以上时间均为 2026-09-11 UTC。三个样本的退出前 prefix 均为 step 1；停止后、恢复静默中和最终结果中的原文件 hash 完全相同。恢复只增加新 segment、新 worker，从旧 prefix 继续，各步骤序号完整且 effect_count 等于完成步数。静默期间 `task_started=0`，精确 root 的原生采集 `events=[]`；每次只有一条已授权的用户明确继续输入、一次新的 `retained-resume`，没有再次 `retained-start`。

kill01 仅向已核对创建身份的初始 Codex PID 32230 发送 SIGKILL，TUI 实际 exit 1；Go 在观察结束时为 interrupted，原 worker 已消失且锁可取。正常 quit03/04 的 TUI exit 0，缓存仍可能是 running；停止判定来自进程身份、锁和冻结进度。所有恢复后的 TUI 最后均正常 `/quit`，exit 0。未捕获正常关闭时 worker 的具体终止信号，也不把 Go 单进程父守卫外推为第三方后代进程树保证。

各样本包含完整 ready、armed、controller action、stopped、idle、resume-ready、final、原生角色及 lifecycle 文件，入口为 [quit03 核对](native-retained-quit-03-checks.json)、[quit04 核对](native-retained-quit-04-checks.json)、[kill01 核对](native-retained-kill-01-checks.json)。quit03 的 lifecycle 预记截止由于两次时钟读取相差 1 毫秒；原始记录保留，实际初始观察不足 21 秒、续接 ACK 70.083 秒，判定未使用额外毫秒扩窗。

## 原生消息角色与采集边界

明确继续是实际 `response_item` / `message` / `role=user`，文本绑定同一 nonce；它不包含外部结果。完成数据来自 root 真正执行最后一次 `retained-inspect` 后的 `custom_tool_call_output`，严格 14 字段、controller_thread、nonce、segment 和 effect_count 与磁盘最终状态相符；ACK 是之后的 assistant final message / task_complete。原生读取入口：[quit03](native-retained-quit-03-continued-thread.json)、[quit04](native-retained-quit-04-continued-thread.json)、[kill01](native-retained-kill-01-continued-thread.json)。

quit03/04/kill01 的本次进程 `write_stdin` 带前置 `// @exec:`，冻结的窄 literal parser 未识别该形式，因此相应 join payload 保持 hash-only，**没有标注可信 retained_source，也没有据其导出 completed 快照**。单次 session ID、空输入和等待时间只作为有界结构元数据保留，见各 `native-wait-shape.json`；最终 literal inspect 已提供独立的真实完成来源。quit02 还有一次原生 code-mode `wait` 的结构记录，不视作任务结果来源。没有为这些返回形态扩大解析器或重跑模型。

## 收尾

[清理核对](native-cleanup.json) 重新检查了本轮 22 个已记录的 TUI/worker 身份或 PID，均已结束，五个 retained 任务的稳定锁均可取。没有在收尾发送信号，没有残留的本任务 Python 缓存需要删除。七个合成任务目录保留必要 checkpoint、步骤与失败探针记录；源码、证据、已审查的持久二进制以及原有配置/Hook/会话历史保持保留。独立 worktree 由主 Agent 整合后清理。

此前无守卫强杀的失败事实不受新样本影响；见 [前置探针](native-probe-results.md)。
