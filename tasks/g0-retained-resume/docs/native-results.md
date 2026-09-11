# 原生退出保留与明确续接：当前证据

**quit02 单次受控正常退出／明确续接已观察到完整链路；尚未达到三个基础样本门槛，也不代表完整 G0 通过。** 全部使用普通 Codex CLI 0.153.4，已审查的固定 Go 二进制 SHA 为 `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`。没有 Goal、MCP 回调、源码/权限修改或旧工具 future 恢复。

## quit01：准备登记竞争，排除中断基线

首个新 root 为 `01a08f2e-acf4-7572-9232-a99710828fbf`。模型窗口固定为 06:51:01.085–06:53:01.085 UTC；观察器在首次 turn 前查询 SQLite，此时 root 尚未登记，入口拒绝。controller 的后续恢复观察没有及时连贯推进，Go 已于 06:52:00.752 完成 12/12 步，无法再提供在途停止证据。仅于 06:54:36.694 正常清理 TUI，exit 0；不重置窗口、复用目录或把已经完成后关闭算作中断成功。[生命周期与最终 prefix](native-retained-quit-01-lifecycle.json)、[原始 120 秒范围的原生记录](native-retained-quit-01-initial-thread.json)。

主 Agent 明确授权仅新增固定 `rr-retained-quit-04` 替代这次缺失的正常中断样本；collector/watch 注册收紧为 quit01–04 与 kill01 五个确定 nonce，quit01 永久保留为 excluded。注册回归先有两个红灯，修后 collector 的六项测试通过，不开放任意其他 nonce。

## quit02：完整单样本

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

quit03、quit04 与带父守卫的 kill01 仍待实测。此前无守卫强杀的失败事实不受本页正常退出结果影响；见 [前置探针](native-probe-results.md)。
