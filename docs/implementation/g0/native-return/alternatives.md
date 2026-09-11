# 原生返回候选与 Goal 边界核查

日期：2026-09-11。研究基线为已安装的 `codex-cli 0.153.4`；公开源码固定到官方 `rust-v0.153.4` 对应提交 [`3d2ee51ca2d5db578f328aa75e20aa22c0197c9a`](https://github.com/openai/codex/commit/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a)。本报告区分源码推导、原生实测和未知，不把公开源码等同于本机二进制的全部扩展。

**当前最有依据的路线是原生持久 Goal 配合独立 Host。** 首个样本已观察到普通 `codex resume <原 thread ID>` 不附 prompt 自动发起 Goal 新轮次，再由真实工具读取离线完成的任务并完成 Goal。它解决的是恢复后的重新协调；普通 final→idle 的子 Agent 完成消息仍没有因此获得自动续轮能力。样本总数和最新结论以 runner 报告为准，本报告不宣称 G0 通过。

## 候选取舍

| 候选 | 证据与最小下一步 | 当前边界 |
|---|---|---|
| 原生 Goal + 独立 Host + 原生等待 | 优先。`nr-g0-goal-01` 已有正常退出、Host 独立完成、普通冷恢复、真实 read、Goal complete、ACK 后停止的完整单样本；继续既定独立样本、强杀和 steer 屏障。 | 依赖明确创建的 active Goal；原生 Goal 控制消息与业务结果必须分别记录。不能推广为旧 child/future 得到恢复。 |
| 原生 child + 单次 `clock.sleep` | `nr-g0-active-sleep-01` 中 sleep 被消息提前打断，随后收到独立 `agent_message`。可验证用户新输入也能中断，及主控真实读取结果。 | 是活动 turn 的阻塞等待，区别于 C05 的真正 idle。sleep 回执不是 child 的工具执行结果。 |
| 原生 child 完成后唤醒 final→idle 主控 | `nr-g0-idle-01` 的 120 秒窗口内没有新 turn；后续用户 turn 才接到排队消息。v2 completion 路径使用 `trigger_turn=false`。 | 本机样本不成立。主动 `send_message` 是排队路径，`followup_task` 不允许 root 目标，不能用普通 CLI queue 冒充原生回调。 |
| 后台 exec / 原 child 的旧等待任务直接跨 TUI 退出恢复 | 无 Goal 样本正常 resume 后没有自动补交；现有正向样本由 Goal 开新轮次读取 Host。 | 未取得恢复旧进程工具 future/旧 child watcher 的正向证据，不把离线 result 落盘等同于父会话已消费。 |
| `thread/injectItems` / 官方 queue | 本地 schema 的 inject 是追加 model-visible history；queue 是用户输入控制通道。只有真正接通目标运行时的 pending tool request 响应才能证明补交语义。 | 任意附加 Response item、用户文本或假 assistant 文本均不构成合法 executor completion。未取得普通已启动 TUI 的受支持补交入口证据，不作为当前主路线。 |
| 受控 app-server + TUI 显式连接 / 包装启动器 | 可在自己拥有的服务上观察 `thread/start`、`thread/resume`、订阅及原生请求/事件，并把响应交回真正 pending request；前置是 TUI 从启动时使用该服务。 | 普通已启动 TUI 使用 in-process 通道；预先另起服务不等于 attach 现有连接。显式远程连接或包装启动器改变原 C01 启动合同，只是需另行评估的候选，不按原合同计通过。 |

原生实测见 [runner-results.md](runner-results.md) 和 [runner-goal-results.md](runner-goal-results.md)。v2 root 限制见 [message_tool.rs:72](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/tools/handlers/multi_agents_v2/message_tool.rs#L72)，自动完成通知见 [session/mod.rs:2259](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/mod.rs#L2259)。in-process 使用内存通道的合同见 [in_process.rs:1](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L1)。

没有把源码中出现的 `DurableSleep` / `SleepItem` 名称当成 CLI 默认暴露的耐久接口；未找到明确公开启用路径的部分保持未知。也不再用 v1 `send_input` 的普通 user-input 语义作为回调身份的正向证据。

## CLI Goal 的实际链路与取消边界

本报告已直接审阅 `nr-g0-goal-01` 的 runner 报告。原 root 的 `create_goal` 返回 active；正常 `/quit` 后，进程监视器记录 TUI 已消失而 Host worker 仍活着，随后才 completed/count 1。无 prompt 的普通 resume 自动产生新 turn，原生控制项为 `message` / `role=user`，包含 `codex_internal_context source="goal"`；之后主控执行 Host read，真正的 `custom_tool_call_output` 返回 completed/count 1，再调用 `update_goal complete` 和输出 ACK。ACK 后记录了约 98 秒无额外 turn；这不是把内部 Goal 控制文本包装成执行结果。[该样本与直接进程证据](runner-goal-results.md)

公开实现中，正常退出关闭会话运行时并终止该运行时持有的 exec；Goal 的 abort/stop 回调做记账或注销 runtime，不等于用户暂停 Goal。Esc/Ctrl-C 的运行中取消路径则显式请求暂停 Goal。强杀是否保留 active 取决于此前 Goal 写入是否已经提交，必须用独立样本证明；不要用 SessionEnd Hook 代替实际进程终止边界。[退出处理](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/handlers.rs#L402)、[Goal abort 回调](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/extension.rs#L332)、[用户中断暂停 Goal](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/chatwidget/interaction.rs#L636)

Goal 只负责在符合条件时续轮。要证明没有模型忙轮询，应观察等待窗口内的真实 turn/tool 记录；要证明停止，应在业务 ACK 与 `status=complete` 后观察无后续 Goal turn。预算和合成 objective 必须绑定当前实验任务及 nonce，不能从“Goal 名字类似”推导它会恢复任意应用任务。

## Goal 不提供外部业务 revision

`get_goal`、`create_goal`、`update_goal` 的原生工具以各自 runtime 的 `self.thread_id` 操作 Goal。模型工具的状态更新向数据库传 `expected_goal_id: None`；公开 `ThreadGoalSetParams` 没有预期 revision，公开 `ThreadGoal` 也没有 revision。数据库内部虽有 `goal_id`，修改 objective、pause/resume/complete 不会因此换一个新 ID。[tool.rs:175](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/tool.rs#L175)、[tool.rs:265](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/tool.rs#L265)、[数据库条件更新](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/state/src/runtime/goals.rs#L277)

外部 Goal API 的 `goal_state_permit` 负责串行协调 Goal 状态与 idle 续轮；更新时内部读取 expected goal ID 能防一类数据库替换竞争，但不是模型旧动作携带的业务版本。暂停或清除 Goal 也不自动撤销已发出的 Host 工作。[api.rs:176](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/api.rs#L176)、[runtime.rs:181](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/runtime.rs#L181)

因此，旧结果读取与后续 Goal 更新之间发生用户取消/改目标时，不能仅凭同一个 active Goal 接受旧 ACK 或派发。应按原合同验证 Host 的具体 controller thread、workflow revision、delivery revision；并以屏障实验检验旧动作是否被拒绝。这是源码揭示的待测竞争窗口，尚未宣称本机已复现误完成新 Goal。

## fork、新会话、子 Agent 与 compact

| 操作 | 固定源码支持的结论 | 关键实测 |
|---|---|---|
| 普通 `/fork` / `codex fork` | TUI 使用 `StartIfIdle`，从而 `defer_goal_continuation=false`；服务端只在 defer=true 的分支复制 Goal 快照。普通 fork 不应自动获得自己的 inherited Goal。 | 核实新 thread ID、原生 get_goal；即使历史含旧 nonce，也不能读取/ACK 旧工作流为控制者。之后可显式建立 fork 自己的独立工作流。 |
| 显式 API fork 且 defer=true | 复制到新 thread 的快照保留相同内部 `goal_id`；持久 deferral 使 fork 及首次无输入冷恢复不自动续 Goal。首次显式新 turn 清除 deferral，之后允许续轮。 | 不是普通 CLI fork 的同一个行为；不能把相同 `goal_id` 视为同一个 controller。 |
| `/new` / `/clear` | 都启动新 thread；`/clear` 不等于 `/goal clear`。切换旧 thread 的 TUI 路径只取消订阅；无订阅且 inactive 时服务端才卸载旧 thread。 | 分别记录旧/新身份与 Goal，观察允许切换时旧 Goal 是否仍有后台动作；不能只看到新 Goal 为空就推断旧运行时已停止。 |
| 原生子 Agent | Goal runtime 使用 child 自己的 thread ID；父/根共享的字段用于使用量记账。Review 子 Agent被排除；普通 child 的原生 Goal工具操作自己的 Goal。 | full-history child 先只调用 get_goal 和白名单身份采样，确认它没有因父历史、nonce 或 root 记账而成为旧工作流控制者。 |
| 成功 compact | CompactTask 操作同一 Session；Goal 在按 thread ID 存储的独立状态中，源码没有把成功 compact 当成 fork 或新 Goal。 | 原 ID、Goal 和 Host 任务绑定均保留，不能因摘要中重复指令再次 start。此项仍需原生样本。 |

普通 fork 开关见 [app_server_session.rs:797](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app_server_session.rs#L797) 与 [开关转换:870](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app_server_session.rs#L870)；复制条件见 [thread_processor.rs:5043](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/thread_processor.rs#L5043)。defer 的官方集成测试覆盖 fork、冷恢复、首次输入及源 Goal 保持原状：[thread_fork.rs:746](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/tests/suite/v2/thread_fork.rs#L746)。

新会话与 clear 见 [event_dispatch.rs:68](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/event_dispatch.rs#L68)、[event_dispatch.rs:200](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/event_dispatch.rs#L200)；旧 thread 处理见 [thread_routing.rs:18](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L18) 和 [卸载条件](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/thread_lifecycle.rs#L31)。子 Agent Goal 归属见 [extension.rs:99](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/ext/goal/src/extension.rs#L99)；compact 见 [tasks/compact.rs](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/tasks/compact.rs)。

`/new`、`/clear`、`/fork`、`/compact` 均被原生 TUI 标为 task busy 时不可用。runner 应在普通 UI 允许时执行；若先 pause Goal，须明确记录为 paused 样本，不通过非普通 API 绕过限制。[slash_command.rs:207](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/slash_command.rs#L207)

## 原生 exec 身份与双 TUI 条件

原生 unified exec 在环境策略之后插入 `CODEX_THREAD_ID=context.session.thread_id`，再注入 `CODEX_SESSION_ID`。前者对应当前实际 thread，后者可以被根及子 Agent 共享；不能把共享 session ID 当成子 Agent 的父 thread ID，或当成 workflow controller。所核查的标准环境构造器没有注入一个独立的 immediate-parent thread 环境变量。[process_manager.rs:1348](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/unified_exec/process_manager.rs#L1348)、[exec_env.rs:28](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/exec_env.rs#L28)

实验可只采样 `CODEX_THREAD_ID`、`CODEX_SESSION_ID`、helper PID/PPID、祖先 PID 创建时间/二进制路径/UID，并与已经独占记录的原生 TUI 和原生 parent_thread_id 事件核对。shell、sandbox、zsh-fork、exec-server 会改变进程层数，不应硬编码“helper 的固定第 N 级父亲就是 TUI”。root 与 child 还可能共享同一个 TUI 祖先；环境值也可被命令自行覆盖。这些是待交叉核实的来源信息，不是不可伪造的授权凭证。[进程启动分流](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/unified_exec/process_manager.rs#L1230)

**默认持久 Local store 的 writer lock 调用链已核清，legacy history 不绕过该锁。** in-process 构建 `MessageProcessor`，后者按配置建立进程级 store；默认配置为 Local。恢复路径调用 `LiveThread::resume`，无论 Legacy/Paginated 都进入 `LocalThreadStore::resume_thread`；`live_writer` 在识别 history mode 前即取得同 thread 的文件写锁。后台 rollout migration feature 只控制迁移，不是该锁的启用条件。

- 入口与默认值：[in_process.rs:473](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/in_process.rs#L473)、[message_processor.rs:291](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/message_processor.rs#L291)、[config/mod.rs:2422](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/config/mod.rs#L2422)。
- Local 创建与普通恢复：[thread_manager.rs:380](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/thread_manager.rs#L380)、[session/session.rs:900](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/session.rs#L900)、[live_thread.rs:147](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/thread-store/src/live_thread.rs#L147)。
- 实际加锁：[local/mod.rs:461](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/thread-store/src/local/mod.rs#L461)、[live_writer.rs:40](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/thread-store/src/local/live_writer.rs#L40)、[writer_lock.rs:39](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/thread-store/src/local/writer_lock.rs#L39)。

这是同一 `codex_home` 锁根下的 OS 文件锁，不是到期 Goal lease；不能扩展到另一个 store 根、InMemory 或未核实的二进制扩展。官方集成测试明确用 Legacy、Paginated 两种模式验证竞争 create/resume 失败以及原 writer shutdown 后恢复成功。[local/mod.rs:1456](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/thread-store/src/local/mod.rs#L1456)

同一个服务内已有 thread 则复用 Session，`start_if_idle` 在 active-turn mutex 下保留启动位置，避免同一运行时重复启动；这仍不替代业务 ACK 幂等性。[thread_manager.rs:1896](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/thread_manager.rs#L1896)、[turn_input.rs:357](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/turn_input.rs#L357)

本机双 TUI 仍待 runner 实测：保持第一 TUI 持有确切 thread，第二普通 `codex resume <同 ID>` 不附 prompt；观察是否拒绝可写恢复或只读，以及是否出现第二个 Goal turn。记录双方 PID/创建时间，第一实例真正退出后再验证合法恢复。不能只比较 `goal_id` 或 lock 文件是否存在就宣布协调成立。

## 接续屏障与版本界限

建议在主 Agent 已安排的基础恢复、强杀和 steer 样本后执行以下最小屏障：

| 合同 | 最小屏障 | 失败判据 |
|---|---|---|
| C02/C09 | 同 cwd 的独立 root、普通 fork、full-history child 分别取得真实 thread 身份和 get_goal；使用同一旧 nonce 只尝试受控 Host 身份校验。 | 新 thread 或 child 因复制的 nonce、Goal 历史、共享 session/root ID 获得旧 controller 权限。 |
| C03 | 同一持久 thread 的双普通 TUI resume，以及原 owner 退出后的再次恢复。 | 两个独立可写运行时同时推进同一 Goal/交付，或旧 owner 动作越过新所有权。 |
| C04/C10 | 旧任务 read 前设屏障；提交用户取消/改目标并记录新 revision；释放旧结果。 | 旧 ACK、旧派发或旧 `update_goal complete` 被错误当作新目标的完成。 |
| C09 | 按原生 busy 限制做 `/new`、`/clear`、成功 compact；比较前后实际身份、Goal、Host start/read 次数。 | 新 thread 自动继承旧工作流，或 compact 重复 start；不要把暂停样本泛化为 active 切换语义。 |

核查时官方最新稳定版为 [0.154.0](https://github.com/openai/codex/releases/tag/rust-v0.154.0)，发布于 2026-09-09；其中明确包含另一应用占用会话时显示只读 transcript/retry 的改动 [#43253](https://github.com/openai/codex/pull/43253)。这是双客户端恢复 UX 的具体候选证据，不是 native idle completion、Goal 外部 revision 或旧 child future 恢复的修复证明。未发现明确修复所缺能力的 release 证据时，不盲试旧版/nightly；若后续需要版本实验，只使用隔离的私有二进制与专用状态，不自动升级全局安装或迁移个人状态。

本轮 schema 生成命令为 `codex app-server generate-json-schema --experimental --out tasks/g0-native-return/tmp/schema-alternatives`，exit 0，共 416 个 JSON 文件；聚合 v2 schema SHA256 为 `e5f798fd1343c539f01fedea0e8a84a43c080fcca4615c80eb04a5edab4f7d0a`。仅保留与候选相关的摘要：inject 是 history append，Goal API 按 thread 标识且无业务 revision；不在报告附整份 schema。该 generated 目录由主 Agent 在 runner 不再需要后清理。本报告 worktree 只新增本文件，没有修改产品、配置或运行真实模型任务。
