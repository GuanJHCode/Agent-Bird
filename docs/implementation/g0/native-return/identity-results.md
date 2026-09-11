# 原生多客户端与 Goal 身份实验

2026-09-11，macOS，正常入口 `codex-cli 0.153.4`。**同一 thread 的第二写入者被原生锁拒绝，正常退出后可以接管；普通 `/fork`、`/new`、`/clear` 生成的新 thread 未继承旧 Goal。** 这些结果验证原生身份边界，不代表 Host ACK 控制权或完整 C09 已实现。

本组只使用已有账号、模型与权限，启动命令仅增加 `TERM=xterm-256color` 显示变量。专用 cwd 为既有已信任 `tasks/g0-macos/data/test-repo`。未修改全局配置、认证、Hook 或聊天文件，未执行 Host。模型轮按 120 秒上限判定，本地行为按 10 秒窗口判定；全部实际模型轮均在上限内结束，没有通过扩大窗口解释服务失败。

## C03：同一 thread 的写入锁与正常接管

原 TUI `/status` 给出 root `01a08e69-17ab-74e0-94bb-7e83065a90b3`。进程 `63401` 的创建时间为 11:00:46 +0800；SessionStart Hook 的 `ppid=63401`、原生 SQLite 中的精确 UUID、`source=cli`、cwd、CLI 版本一致。首个最小模型轮仅调用 `get_goal` 和两个环境字段，03:02:11.462Z 完成，未设置 Goal。

| UTC 时间 | 观察 |
|---|---|
| 03:02:26 附近 | 第一次普通精确 `codex resume <ID>` 立即 exit 1、提示 active writer；进程过快退出，未捕获其 PID，因此不计入完整身份样本 |
| 03:03:14–15 | 重复验证前用 `ps` 记录 PID `64122` 和创建身份，再由同 PID 的 shell `exec` 正常 `codex resume <ID>`；立即 exit 1：`thread … already has an active writer (code -32600)` |
| 至 03:04:06 附近 | 原进程仍存活；原生记录只有原来的一个 `task_started`，无第二模型轮，也没有新 Goal 轮 |
| 03:04:08 / 03:04:16 | 原 TUI 接收正常 `/quit`；后续 PTY exit 0，`ps` 确认 PID 63401 消失 |
| 03:04:27 | 新 PID `64316` 正常精确 resume，无附加 prompt；`/status` 仍为相同 ID |
| 至 03:04:53 | 接管后的空闲观察超过所需 10 秒，原生事件仍只有原模型轮；之后明确提交的 C09 请求可以正常执行 |

本样本的原写入者为 **idle 且无 Goal**；不是两个 active Goal 竞跑的样本。它直接证明原生锁覆盖仍持有 thread 的空闲客户端，也证明正常退出释放后可接管。没有使用暂停进程、强杀或取消工具轮。

证据：[退出前原生事件](identity-c03-before-exit.json)、[正常接管后原生事件](identity-c03-after-resume.json)、[有限 PTY 字串与进程身份](identity-lifecycle.json)。

## C09：旧 Goal、fork、new 与 clear

旧 root 上通过正常 `/goal` 设置专属合成目标 `nr-g0-identity-c09-old`，其任务只是原生 `clock.sleep(60000)` 后按 Goal 状态结束。03:05:27.116Z 实际 `get_goal` 返回该 root 的 `active`。

03:06:03 附近输入原生 `/goal pause`，UI 确认 `Goal paused`，但正在等待的工具轮仍运行。03:06:17 附近尝试 `/fork`，UI 明确返回 `'/fork' is disabled while a task is in progress.`。未按 Esc；原睡眠自然返回后，03:06:32.603Z 的实际 `get_goal` 为 `paused`，随后模型正常结束。

| 操作与 thread | 首次实际 Goal 返回 | 后续结果 |
|---|---|---|
| 旧 root `01a08e69-17ab-74e0-94bb-7e83065a90b3` | 03:05:27 active，03:06:32 paused | fork 完成后，03:10:55.777Z 复核仍为 paused，`createdAt/updatedAt` 未变 |
| 普通 `/fork` → `01a08e6f-8466-7c40-8897-47aa5e30a0cb` | 03:08:21.141Z `goal=null` | 按明确新请求创建 `nr-g0-identity-c09-fork`，新 root 自己的 Goal 在 03:08:36 active、03:08:39 complete；没有操作旧 Goal |
| 从旧 paused root `/new` → `01a08e72-ea1f-7e30-904e-b6ac3d4ce427` | 03:12:13.534Z `goal=null` | 身份采样与一个直接 child 完成，未创建 Goal |
| 再次从旧 paused root `/clear` → `01a08e75-cae3-7043-832e-20ebf886aef7` | 03:15:07.201Z `goal=null` | 身份采样完成，未创建 Goal |

所有新 ID 均来自相应 TUI 的 `/status`，随后由相同 UUID 的原生 metadata/Hook 和真实工具环境输出交叉核对。采集 fork 时将时间下界放在新 fork 创建之后，排除复制来的旧工具返回；复制的 transcript 不能算新 thread 的执行。

**paused Goal 的恢复有交互边界。** 两次正常精确 resume 旧 root 时，原生 TUI 都出现 `Resume paused goal?`，提供 `Resume goal` 与 `Leave paused`。实验明确选择 `Leave paused` 后继续。该选择是本次实验的真实用户操作，不能将其写成“paused Goal 无输入自动恢复”。这与 active Goal 的自动续跑样本不同。

证据：[旧 root 原生事件](identity-c09-old-events.json)、[旧 Goal 最小快照](identity-c09-old-snapshots.json)、[fork 原生事件](identity-c09-fork-events.json)、[fork 快照](identity-c09-fork-snapshots.json)、[new 原生事件](identity-c09-new-events.json)、[new 快照](identity-c09-new-snapshots.json)、[clear 原生事件](identity-c09-clear-events.json)、[clear 快照](identity-c09-clear-snapshots.json)。

## 原生环境中的 root 与 child 身份

各 root 的 `CODEX_THREAD_ID` 和 `CODEX_SESSION_ID` 都等于自身的 `/status` ID；普通 fork/new/clear 后，两项都更新为新 root ID。

唯一直接 child `01a08e73-bcd0-7313-9921-618e3d860dbf` 的原生 `source.parent_thread_id` 明确指向 new root `01a08e72-ea1f-7e30-904e-b6ac3d4ce427`。它实际读取到：

```json
{
  "CODEX_THREAD_ID": "01a08e73-bcd0-7313-9921-618e3d860dbf",
  "CODEX_SESSION_ID": "01a08e72-ea1f-7e30-904e-b6ac3d4ce427"
}
```

因此 `CODEX_SESSION_ID` 在同一 root 与 child 之间共享，不能单独区分主控和执行者。控制归属需要保留实际 native thread/root 关系。此结论是身份观察；当前 Host 没有 owner/revision ACK 守卫，不能据此声称新 fork 已被阻止操作旧 nonce。证据：[child 最小快照](identity-c09-child-snapshots.json)，其所属关系由 [new 原生事件](identity-c09-new-events.json) 的 direct-child metadata 验证。

官方文档说明 `/goal pause/resume/clear` 管理当前 chat 的 Goal，并说明普通 `/fork` 创建新的 chat ID；本报告关于不继承、写入锁与 paused 恢复弹窗的结论来自本机实测。[OpenAI 原生命令文档](https://learn.chatgpt.com/docs/developer-commands?surface=cli)

## 采集、验证与清理

复用已审查的 `runner_collect.py`，未修改它。新增一次性 `identity_snapshot.py` 只补充：匹配实验 nonce 的 Goal 是否存在、thread/status/创建更新时间，以及严格限定为两个 UUID 键的环境 JSON。原始 prompt、objective、推理、任意工具正文均不导出；原生调用与输出通过相同 call_id 对齐，正文只保留原采集器的 hash。child 文件只在 metadata 确认指定 root 的直接父子关系后打开。

最后一次修改后，用真实的 old/fork/new/child/clear 文件重新采集，核对了 Goal 序列与两项身份字段；Python 语法检查通过。错误父子关系、未来观察窗口和错误 nonce 三项拒绝检查均非零退出且没有写出文件。随后还核对 JSON/schema、固定字串范围和 `git diff --check`。这些是只读实验采集器的操作验证，没有增加形式化产品测试。

本组自有 TUI PID `63401`、`64122`、`64316`、`68693`、`73211` 在 03:16:03Z 的精确 `ps` 查询中全部消失；可运行的四个 TUI 都正常 `/quit` 后 exit 0，锁拒绝客户端 exit 1。无信号发送，未操作其他运行者。旧合成 Goal 最后原生观察为 paused、新 fork Goal 为 complete，原生历史保留以便复核；无自有 Host/业务进程。任务产生的 Python 缓存与空临时目录已列明并清理。

未在本组验证：Host 结果跨 root 隔离、ACK owner/revision 拒绝、Goal 编辑竞态、普通 compact、特殊 defer fork，以及 Windows。完整 G0 与 C09 的业务控制权结论仍需独立证据。
