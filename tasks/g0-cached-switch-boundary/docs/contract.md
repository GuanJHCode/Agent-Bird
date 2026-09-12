# Cached side/primary switch: read-only evidence contract

状态：只读方案；未启动 native/model，未改全局配置、Codex 本体或 `main` 工作区。该文件服务于后继 sd08 受控 case，等待 resume07 先验收后再执行。

固定源码 provenance：`openai/codex` commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`，由 `tasks/g0-tui-proxy/docs/native-source-receipt.json` 与 `clear-source-receipt.json` 固定并核验。固定 binary 仍是既有 0.154 SHA，不在本方案中重新取源或启动。

## 结论边界

`status` 只能作为独立 corroboration。它可以说明一次明确查询时 TUI 显示的 session/thread ID 与观察到的目标相等，但不能从 status alone 创建 attachment candidate，也不能证明发生过 `thread/fork`、`thread/resume` 或切换动作。

若缓存 P↔S 切换确实产生 zero RPC，wire 上没有新的 thread 选择事实。旧 candidate 只能绑定它产生时的 connection epoch/事件序列；切换后没有新的 observer 事件把 candidate 迁移到当前屏幕，旧 candidate 应视为 stale/失效。不得用最近一次 `skills/list` 或 status-only 猜测当前 thread。

`skills/list` 没有 `threadId`，且可由启动、resume、fork 后刷新或全局 skills 配置触发；即使带 `forceReload=true`，也仍不能唯一标识当前 P/S 选择。

## 首次 P → side S 的必要前置

首次 side 创建至少要在同一完整 epoch 观察到：

```text
client thread/fork(params.threadId=P, side presentation / ephemeral shape)
server response(thread.id=S, thread.forkedFromId=P, ok=true)
server thread/started(thread.id=S)       # 固定 source 的 fork path 会发出
[若该 TUI build 实际执行]
client thread/inject_items(params.threadId=S)
server response(thread/inject_items, ok=true)
```

`thread/fork` response 的 `forkedFromId=P` 是 lineage 证据；`thread/started(S)` 只能确认 S 的建立，不能说明 S 后来是否仍是当前显示。`thread/inject_items(S)` 若出现，只能作为 side 初始化的附加证据，payload 保持 opaque，不能用注入本身代替 lineage 或当前选择。

固定 source 中 `fork_side_thread` 使用 `ForkPresentation::SideConversation`，side fork 对分页历史设置 `excludeTurns`，并返回 `ThreadForkResponse`；server fork path 将 `forked_from_id` 写为源 P 并发 `ThreadStarted`。生产 TUI 对 `thread/inject_items` 的完整调用位置不在保存 source receipt 中，因此后继 case 必须记录“出现/未出现”，不能预先把它写成必然步骤。

## 缓存切换的可观察边界

当 P、S 都已有 live `thread_event_channels` 与本地 snapshot，切换主要由 TUI 本地 receiver/channel activation 和 snapshot replay 完成。静态 source 的 `select_agent_thread`、`activate_thread_for_replay`、`replay_thread_snapshot` 支持这一点；side thread 已缓存时会跳过 liveness/resume refresh。

因此期望序列为：

```text
P active
  → explicit side toggle
  → first S setup (fork lineage above)
  → explicit side toggle
  → cached P local replay; zero RPC is allowed
  → explicit side toggle
  → cached S local replay; zero RPC is allowed
```

这些缓存切换中出现以下任意 RPC，必须按实际序列记录并重新分类：

| RPC | 含义边界 |
|---|---|
| `thread/resume(P/S)` | 缺 live attachment、session snapshot 字段不足或权限/session refresh；这不是纯 zero-RPC cached switch。response 的 thread ID 必须与目标绑定。 |
| `thread/read(P/S)` | liveness/read fallback 或历史 hydration；这不是纯 cached switch。read 只能说明读取目标，不能单独证明当前显示。 |
| `thread/fork` | 创建了新线程；必须重新检查 lineage，不能称为切回缓存。 |
| `thread/inject_items` | 对指定 thread 写入 side/setup item；只能作为该 thread 的操作证据。 |
| `thread/unsubscribe` | 关闭/拆除通道；若随切换出现，不能把该过程标为保持缓存 live channel。 |
| `skills/list` | 全局 skills refresh；永远不能单独证明 P/S 选择。 |

primary 在 snapshot session 缺 model、rollout path 或需要权限 profile refresh 时，固定 source 允许额外 `thread/resume(P, PreserveExistingThread)`；因此“看到 resume”不能自动说明用户执行了显式 resume，也不能反向证明 side toggle。

## 后继 sd08 case（等待 resume07 后执行）

目标是验证“相同 wire 形状不足以确定当前显示”的受控反例，保持零模型、无 picker/thread-list、无 raw history。

1. 使用已验收的 resume07 产物恢复原持久 P；核验 `thread/resume` response 的 `thread.id=P`、epoch、connection identity 和独立 status(P)。
2. 在 composer 为空、无 modal、无 turn 的窗口中触发 side toggle。默认键为 `Ctrl-/`，旧终端兼容 `Ctrl-7`；若 keymap 冲突，记录实际已生效的显式键。
3. 等待首次 S 建立，不提交 user prompt；记录并核验 P→S fork lineage、可选 `thread/started(S)`、实际存在时的 `thread/inject_items(S)`，以及所有同 epoch 的 `skills/list`。
4. 发一次独立 `/status` 查询，记录显示 S。status 只作 corroboration。
5. 再次 side toggle 回 P；不主动发送任何会产生 RPC 的 resume/read 操作。记录从上一个稳定边界到下一个稳定边界的完整 wire：应允许 zero RPC；若有 RPC，按上表重新分类。
6. 发独立 `/status` 查询，记录显示 P；不得据此新建 candidate。
7. 再次 side toggle 回缓存 S；同样不补发 resume/read。发独立 `/status` 查询，记录显示 S。
8. 最后安全退出，记录旧 candidate 是否仍被 runtime 保留、清除或标为 stale；若 runtime 当前实现仍保留旧 candidate，必须把它作为实现缺口记录，不能解释成当前显示已被证明。

每次 status 查询前应有可审计的屏幕边界（明确提交 `/status`、唯一 ID、完成时间），避免把旧屏幕残留当成新选择。driver 的 selected label、picker 个人列表和名称不得作为选择来源。

## 必须保存的 metadata

只保存白名单结构，不保存 raw history、完整 prompt 或 picker 列表：

- binary SHA、source commit、case nonce、workspace/cwd、model 固定为 Luna、connection/epoch、两端 peer identity；
- `p_thread_id=P`、`s_thread_id=S`、首次 fork request/response 的 request ID、local sequence、`forkedFromId`、`thread/started` 是否出现；
- `thread/inject_items` 是否出现、目标 ID、request/response ID、成功/失败、local sequence；不保存 items body；
- 每次切换前后 `active_thread_before/after`（仅作为独立观测字段）、status query 的唯一 ID、status 时间窗和 screen hash；status 不写入 candidate；
- 每个切换边界的 RPC 方法集合和顺序摘要、是否 zero-RPC、observer gap/eof/sequence 状态；
- candidate 的 `(epoch, thread_id, session_id, source)` 及其产生/失效事件。若 zero-RPC 后仍显示旧 candidate，写 `candidate_stale=true`，禁止改写为当前选择；
- model turn/start 计数必须为 0；cleanup、hash protection、退出码及是否有未完成 native worker。

## 假设与否定条件

待验证假设 H：缓存 P↔S 切换可能不产生选择相关 RPC，因此相同 wire 仅能证明连接仍健康，不能唯一确定当前显示；只有独立 status 能 corroborate 当前显示，且 status alone 不足以建立 candidate。

以下任一真实 native 输出会否定或收窄 H，需原样记录而不是放宽推断：

- 每次 side toggle 都稳定出现带 `threadId` 的官方选择/attach/switch notification，且 ID 明确为 P 或 S；
- 每次缓存切换都稳定出现目标绑定的 `thread/resume`/`thread/read`/`thread/subscribe` 序列，并由 response/notification 明确当前 attach；
- app-server 在 cached switch 中发送带目标 thread ID 的事件，且 source/receipt 能证明该事件是 TUI 选择动作而非后台状态或 skills refresh；
- status 查询本身通过固定官方 RPC 返回结构化 `selectedThreadId`，并能与显示切换事件建立 request/response 因果关系。

反例不会被以下事实推翻：同一 epoch 继续收到 `skills/list`、`thread/status/changed` 或其他后台通知；这些都没有足够的 current-display identity。

## 文件责任

- 后继 runtime/observer 仅记录上述白名单和 stale barrier；不得增加“最近 skills”或 status-only candidate 路径。
- transport 只负责 relay、epoch、顺序和 peer identity，不解释选择。
- native case 记录真实操作键、实际生效 keymap、RPC 轨迹和独立 status；不修改全局配置、不安装常驻服务、不执行 picker 采集。
