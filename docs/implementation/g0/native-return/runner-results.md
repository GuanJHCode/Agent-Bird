# 原生 CLI 返回通道实测

实验起点 `6169c5d`，日期 2026-09-11。按 `plans/g0-native-return-experiments.md` 的新授权执行。这里仅报告原生通道实验，不宣称 G0 或完整产品通过。

## nr-g0-idle-01：子 Agent 完成不自动启动空闲主控

**固定 120 秒窗口内未成立。** 子 Agent 正常完成且 TUI 显示 `Completed /root/nr_g0_idle_01`，主控已经结束的响应没有自动续轮。没有限流、模型失败、权限提示或服务错误。

启动仅使用 `TERM=xterm-256color codex`，cwd 为已信任专用仓库 `tasks/g0-macos/data/test-repo`；未改配置、认证或权限。原生 CLI 为 `0.153.4`，原生 `/status` 显示模型 `gpt-6-astra` / `max`、既有 `Full Access`。精确原生 root thread 是 `01a08e38-508a-76b2-bc17-09fd5dbe80ae`；同次 SessionStart / UserPromptSubmit Hook 均匹配该 ID，PPID 为 46093。原生进程创建时间是 `Fri Sep 11 10:07:29 2026`（机器时区 Asia/Shanghai），创建父 PID 38821。此进程由本次 `exec_command(tty=true)` 启动，工具句柄为 `87693`。

| UTC 时间 | 可验证行为 |
|---|---|
| 02:09:18 | 通过本次 TUI 提交实验请求，固定观察窗口到 02:11:18 |
| 02:09:19.013 | 原生 root `task_started`，turn `01a08e39-f851-7030-a0f8-addf7c0f24c5` |
| 02:09:27.222 | root 仅调用原生 `spawn_agent`，返回 `/root/nr_g0_idle_01` |
| 02:09:29.926 / 29.999 | root 输出 `nr-g0-idle-01-WAITING`，随后 `task_complete` |
| 02:09:35.887 | child 执行一次 `sleep 15` 后输出固定 nonce 的命令 |
| 02:09:51.186 | child 自己的工具读取到 exit 0 和 `nr-g0-idle-01-DONE` |
| 02:09:52.855 / 52.967 | child 输出 DONE，随后 `task_complete` |
| 至 02:11:18 | root 没有第二个 `task_started`，没有 ACK，没有新的返回 `response_item` |
| 02:11:25 | 对同一 PTY 句柄只读轮询仍存活、无新输出；没有因观察超时重启 |

原生 SQLite `source.subagent.thread_spawn.parent_thread_id` 将 child `01a08e3a-18bc-7a33-8007-adaba1807d4f` 精确绑定到上述 root。父会话结束响应后没有发送任何用户输入、`wait`、`wait_agent`、人工继续或 Agent 状态查询。外部实验观察者对 PTY/精确自有 rollout 的只读观察不构成原生主控轮询。

身份结论分开记录：root 的 `spawn_agent` 初始回执是 `function_call_output`，内容仅为 child task_name；真实执行结果出现在 child 的 `custom_tool_call_output`，随后是 child 的 `message` / `assistant` 最终消息。观察窗口内 root 没有接收到新的工具结果条目，不能把 TUI 的 Completed 提示当成主控已消费的工具结果。

白名单证据：[runner-idle-01.json](runner-idle-01.json)。采集器只选择明确 root 及原生 parent_thread_id 匹配的直接子会话，省略所有 reasoning，消息文本仅保留本实验 nonce；不读取或保存其他个人会话正文。采集命令：

```sh
python3 tasks/g0-native-return/scripts/runner_collect.py \
  --root-thread 01a08e38-508a-76b2-bc17-09fd5dbe80ae \
  --nonce nr-g0-idle-01 --pid 46093 \
  --since 2026-09-11T02:09:18Z --through 2026-09-11T02:11:18Z \
  --output docs/implementation/g0/native-return/runner-idle-01.json
```

## nr-g0-busy-01：忙碌主控能接收原生 Agent 消息

**忙碌时投递成立，但返回类型是 `agent_message`，没有 `role` 字段。** 该诊断不证明 idle 自动唤醒，不等同 `function_call_output`。

使用同一精确 root，于 02:13:46Z 主动提交一条明确标识的新实验请求，固定模型预算 120 秒，45.349 秒即完成。新 child 只运行 `sleep 10; echo nr-g0-busy-01-DONE`；root 在派发后运行自己唯一的有限合成工作 `sleep 30; echo nr-g0-busy-01-LOCAL`，不调用 Agent 等待或状态查询。

| UTC 时间 | 原生证据 |
|---|---|
| 02:13:46.256 | 新用户 turn `01a08e3e-0c7d-7a52-a2b9-8ed7b1c6333e` 启动 |
| 02:13:53.313 | 前一个 idle child 的 DONE 才进入 root；`type=agent_message`、`author=/root/nr_g0_idle_01`、`recipient=/root`，文本为原生 `Message Type: FINAL_ANSWER` 包装 |
| 02:13:58.625–02:14:28.695 | root 的独立 30 秒命令执行并返回 exit 0 |
| 02:14:28.699 | 新 child 的 DONE 进入 root；同为 `agent_message`、`author=/root/nr_g0_busy_01`、`recipient=/root`，没有 `role` 字段 |
| 02:14:31.518 / 31.605 | root 输出 `nr-g0-busy-01-RECEIVED`，随后 `task_complete` |

这证明原生 child 完成可以排队，并在主控仍处于活动 turn 的工具边界消费。前一个 idle 消息在新实验用户输入后才写入父 rollout，不能倒算为此前自动唤醒。证据分别为 [busy 原生消息](runner-busy-01.json) 和 [idle 通知之后才交付](runner-idle-01-later-delivery.json)。

## 进程与后续范围

目前原生 TUI 句柄 `87693` / PID `46093` 保持存活，供同一精确 root 的独立 Host 退出与恢复实验使用。正常关闭和本任务资源清理将在该实验后记录。尚未调用合成 Host；等待其独立审查，避免将本报告的在线实验当作离线交付证据。
