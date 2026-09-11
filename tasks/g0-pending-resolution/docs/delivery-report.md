# G0 交付对账：协议候选成立，完整三态仍需原服务实证

日期：2026-09-11；实施基线 `cd3d054`；macOS / 现装 `codex-cli 0.153.4`。

**standalone `turn/start.toolOutput` 是合法候选；不能因为没有旧 pending tool call 就排除它。当前可独立补齐的是有界历史标记核对、分页信息检查及业务 ACK 分离。完整 C11 尚未通过。** 本轮新增的离线检查器不连接服务、不发送回调、不改历史、不实施 G1–G4；输出恒有 `source_service_verified=false`。它既不证明采集来源是原生主服务，也不证明传入的业务 ACK 是真实持久提交。

用户此前调整了退出停止、保留记录及明确续接；C11 的运输对账与业务幂等要求仍保留。参见[本轮生命周期合同](../../g0-retained-resume/plans/contract.md)、[原 C11](../../../docs/acceptance-v0.1.md)和[六故障屏障](../../../docs/g0-validation-plan.md)。本报告不重复另外两路的 idle / 当前 TUI 附着调查。

## 已核对与复现

1. 官方文档明确允许客户端已经执行的工具通过 `turn/start.toolOutput` 提交；`input=[]`，输出保留为 `functionCallOutput`，普通轮次忙碌时排队。这个接口不要求旧 pending call。[官方 App Server](https://learn.chatgpt.com/docs/app-server#start-a-turn)
2. 现装二进制新鲜生成 416 份 schema、155 个请求方法；`TurnToolOutput` 是 `name/output` 加可选 `namespace`。`TurnStartResponse` 只有 `turn`；独立工具历史项有 `id/name/namespace/output`。[最小 schema 摘要](delivery-schema-summary.json)
3. 固定官方源码 `3d2ee51ca2d5db578f328aa75e20aa22c0197c9a` 的处理器把客户端内容构造为 `FunctionCallOutput { id:None, call_id:None, ... }`，再调用 `start_or_steer_turn`。成功回执人为构造 `items=[] / itemsView=notLoaded / status=inProgress`，并非已落盘逐项收据。[turn_processor.rs:509](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/turn_processor.rs#L509)、[回执构造:675](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/app-server/src/request_processors/turn_processor.rs#L675)
4. Core 明确接受 `call_id:None` 的 standalone 输出；进入 pending 数据时赋原生 item id。后续消费路径记录 conversation、发送 item 通知、调用 rollout materialization。回执和历史观察之间因此需要实测屏障，不能假定同步完成。[turn_input.rs:252](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/turn_input.rs#L252)、[pending 转换:664](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/session/turn_input.rs#L664)、[hook_runtime.rs:726](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/core/src/hook_runtime.rs#L726)
5. 现有 Go 的单事件 ACK、修改 UUID 后复用、版本/取消、真实响应丢失，以及 retained 完成态对账共 9 个针对性测试重新运行，exit 0 / 5.602s。[命令输出](delivery-existing-tests.log)

上述源码只说明接收/记录路径，不是服务故障后的持久性实验。原生 message 级 exactly-once 或 idempotency key **不是额外准入要求**：原设计允许重复展示；需要避免的是重复业务派发、回答、续接、整合。

## 三种记录分别能证明什么

| 记录 | 现有能力 | C11 仍缺的内容 |
| --- | --- | --- |
| Go `ack-r1.json` | owner、固定 producer revision、event hash、单个语义决定/效果槽；新 command UUID 不增加槽 | 没有 delivery id/controller epoch、原服务回执或历史覆盖；只有一个事件，没有部分 ACK/问题序列 |
| retained `inspect` | 读取经过连续产物校验的完成进度；明确续接前取得执行锁 | 没有运输状态或业务 ACK；固定 revision=1，不是取消/合同更新 API |
| 旧 retained 原生样本 | 主控真实 `custom_tool_call_output` 读取结果，后有 assistant final/task_complete | final 中的 ACK 标记不是逐事件持久 ACK；其类型不是 standalone `functionCallOutput`，不能转换类型冒充新通路阳性 |
| standalone 历史项 | 经原服务来源核验后，精确 envelope + hash 可作为已进入历史的阳性 | 单看发送响应、一次空查询、turn 状态或普通队列为空，无法证明未接收 |

旧原生样本仅用于本脚本的“拒绝误认”负例；没有新增原生 standalone 发送样本。[旧样本类型与明确边界](../../g0-retained-resume/docs/native-results.md)

## 需要采集的最小字段

发送端应在允许写请求之前持久化：`delivery_id`、目标 `controller_thread_id`、`controller_epoch`、有序事件 `event_id/event_revision/payload_hash/action_slot`、整个 envelope 的 `payload_hash`；另记录发送者实例身份、排他所有权及所用连接身份。JSON-RPC request id 只用于关联这一次请求，不能替代业务 slot。

原服务观察需记录请求/回执关联、`turn.id`、`item.id/type/name/namespace/output`；历史查询保留原 `method/params`、响应 id、`itemsView/historyMode` 和正反游标。保留的是固定 envelope / 摘要及必要索引，不导出完整个人历史。

| 状态 | 可接受证据 | 当前限制 |
| --- | --- | --- |
| 已交付到历史 | 来源已核验的历史读取中，目标 thread 的完整 standalone 项与已持久化 envelope 完全一致；保存原 turn/item id | 离线检查器能核对内容与形状，不能自行认证原服务。回执空 items 不足以替代历史 |
| 已证明未接收 | 例如唯一发送者在可审计的发送前屏障终止，屏障从未释放；或经该版本实证的旧请求终结、队列清空和历史覆盖组合 | 本轮没有可输入检查器的可信原生否定证明。不是断言永远无法实现 |
| uncertain | 请求可能已写出，但没有精确历史阳性或充分否定证据 | 保留记录且不盲重发；这不因所有业务 ACK 已存在而自动变成历史确认 |

公开 `thread/queue/list` 确实存在，但其 `QueuedSubmission` 只有 `clientUserMessageId/id/input:UserInput[]`。这不是 standalone `FunctionCallOutput` 的 pending-input 队列观察，因此它为空不能证明本批次未接收。检查到的 schema 没有提供本批次接收水位、flush 截点或废止旧 sender 的证明；需要对真实接收路径进一步实验，不能只按字段缺失宣判全部方案不可能。[schema 摘要](delivery-schema-summary.json)

分页默认 `summary` 不足以核对完整 envelope；应请求 `itemsView=full`，必要时遍历 `thread/items/list`。后者取决于 store 是否支持。`nextCursor=null` 只说明该遍历结束，不排除之后的队列落盘；`backwardsCursor` 可用于回看更新，仍不是消息接收水位。compact 后必须实测原 item/envelope 是否仍可通过该 store 的历史接口完整定位，不能把摘要中的 delivery 字符串当原始项。[官方历史接口](https://learn.chatgpt.com/docs/app-server#list-thread-turns)

取消/修改合同需要协调器自己的持久 revision 和既定 action slot；原服务无需实现业务 CAS。旧事件可以保留运输历史，同时被当前业务 revision 拒绝。当前 ACK 夹具已有单事件版本拒绝，仍欠批次部分 ACK 与下一问题到达时的真实事务/原生样本；本轮没有把离线 ACK 数组检查冒充这个事务。

## 已落地的最小 G0 检查器

[delivery_audit.py](../scripts/delivery_audit.py) 接受一个最多 64 KiB 的显式采样文件，核对有限的历史读取形状；不会调用网络、服务、Go 命令或模型，也不查找 latest 会话。只支持三种读取：`thread/read`、`thread/turns/list`、`thread/items/list`。输出不回显原始 output 或任意错误文本。

它输出 `exact_history_marker` 或 `uncertain`，且恒输出 `source_service_verified=false / absence_proof=false / resend_authorized=false`。前者是**采样内容匹配**，还需外部采集器证明来源后才能作为原生运输阳性。分页信息、重复展示计数和逐事件业务 ACK 单独列出：缺少 ACK 不抹去已经匹配的历史项，ACK 完整也不补造历史阳性。UUID 换新但语义 slot/决定相同按一个输入槽计数；这不是证明外部副作用恰好执行一次。

初版 13 项测试曾通过；初始未实现 CLI 的 9 个行为断言失败，随后通过。追加的真实旧样本负例、同原生 item id 内容冲突和空 summary 页核对中，后两项先失败再修复。

独立 review 又复现两处精度问题：过滤无关项前未记录 item 身份，导致同一原生 ID 改成其他类型/工具时仍可能保留阳性；遍历结束后再次读取首页，被错误算作同一完整页链。已增加稳定 type/name/namespace 检查与结束页判断；不会冻结无关 agentMessage 的正常文本增长。新增测试先出现 6 个失败子情形，最小修复后最新 **15 项测试全部通过**。该修复待独立复核，尚未宣称已审。接口与限制见[使用说明](delivery-audit.md)，证据见[验证记录](delivery-validation.json)。

## 下一步可直接执行的六屏障采样方案

先满足既有原服务身份与接入准入，再实现独立、白名单 sender：只允许真实客户端工具结果的 `turn/start`，参数只含 `threadId/input:[]/toolOutput`；不带账号、model、cwd、permission 覆盖。probe 仅位于自身新连接与已证明的原服务之间。sender/逐事件持久事务尚未在本轮开发。

| 屏障 | 受控动作与必须保留的观测 | 通过条件 |
| --- | --- | --- |
| 1 意图提交后、发送前 | sender 固定本地允许发送屏障；记录唯一所有者，杀掉尚未放行的 sender，核验原进程及连接不再可写 | 证明本批未发送；新 sender 可在同事件合同下发送。不能仅凭 PID 消失 |
| 2 已写请求、回执前 | 故障代理放行完整请求后截断返回；停止旧 sender；从另一个经验证连接读取历史 | 精确阳性则不重发；没有阳性且没有旧请求/队列终结证据时 uncertain |
| 3 服务接受/排队、历史未读到 | 使用真实忙轮次的接收边界及 item 记录时序；不得用 `thread/queue/list` 的用户队列替代 | 等到可解释的历史阳性，或建立真实否定屏障；空查询不算未接收 |
| 4 回执后、本地回执提交前 | 捕获 turn id 后终止 probe，保留原 immutable intent | 以历史 envelope 对账；收到过 turn id 不能直接标 history_confirmed |
| 5 历史确认后、模型处理前 | 留存完整 item；业务 ACK 仍缺失，随后同一声明批次重复展示 | 运输保持确认；未处置事件仍 pending，新问题不被普通进展的未 ACK 状态阻塞 |
| 6 决定/逐事件 ACK 已提交、响应丢失 | 固定最多三事件（进展、问题、结果），事务只 ACK 一部分；丢响应，再用新 UUID 提交相同语义槽；另提交冲突/旧 revision | 持久决定和 outbox 槽不增加；其余事件仍待处理；冲突、取消和旧版本不能创建新副作用 |

每个支持组合仍按原规格运行屏障 20 次，并另测分页、compact、服务崩溃、部分 ACK 和 waiting_user 后新问题。本轮仅锁定有意义的检查逻辑与后续采样合同；没有把 Go 本地幂等、离线 fixture 或只读 schema 计为这些原生验收通过。
