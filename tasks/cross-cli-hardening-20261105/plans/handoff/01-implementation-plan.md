# Agent-Bird 跨 CLI subagent 完整实施方案

版本：2026-11-05，面向本地 macOS Codex 接手。本文是待实施方案，不是已完成实现或已通过验收的声明。

## 1. 目标、基线与适用范围

仓库：https://github.com/GuanJHCode/Agent-Bird
目标分支：feat/cross-cli-subagent-20261104
审查基线：29691e9f08468b1de56cb0c42977084b58c91975
对照基线：v0.1 / 4b2b4916232c9d7526f51cf0a73204748c62faf8
2026-11-05 再次查询远端，目标分支 HEAD 仍为上述审查基线。接手时仍须 fetch 并重核，不把本文行号或结论当成永久事实。

用户目标：在一个主 CLI 会话中自然语言委派另一个 CLI 执行任务；子任务生成候选和回执；主会话检查实际代码、结构和测试；不满足原始要求时提交具体反馈；子任务基于上轮候选继续修改；主会话复审通过后显式验收。主会话可以继续工作，支持宿主能力的组合应能在后台结果到达时继续审查。

第一交付范围为 macOS 上 Codex 与 Claude 的双向 managed coding；AGY/Grok 保持当前已验证的只读边界。MCP 工具入口是本轮实现项；各宿主的主动唤醒是独立能力与验收项。遇到不支持的宿主，提供明确的 wait/collect 回退，继续完成其余工作。

保留 Go Coordinator、SQLite、Host、独立候选工作区、revision、持久化回执和现有安装收据。按职责收敛接口和修复具体问题。

### 1.1 已经具备的能力

- controller/routing/dispatch/wait/reconcile 已有实现和生成入口。
- Codex 原生 owner 与 Claude/Grok/AGY 的受控进程 owner 已分离。
- managed coding 的精确准入：Codex 0.166.0；Claude gateway 2.1.267 / 2.1.316。真实双向证据中的 Claude 为 2.1.316、既有兼容网关与指定模型。
- 独立 owner request-changes v2 已实现；下一 revision 以前一候选为 base，fresh invocation，保留原 provider lock/profile/paths。
- accept/reject/rework 的事务互斥、command 幂等和旧事件拒绝已有实现。
- 最后一次 attempt 即使不可继续返工，summary 仍能恢复真实 candidate OID。

依据：[当前入口与边界](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/README.md#L52-L71)；[现行实施合同](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tasks/cross-cli-subagent/plans/implementation-contract.md#L15-L39)。接手者先验证这些能力，禁止按旧审查结论重复重建。

### 1.2 当前证据边界

Codex→Claude 使用 .4 包，实际模型候选与返工成功，最终有人工可信复核收尾；Claude→Codex 使用 .7 包，真实父 CLI 通过公开入口完成闭环。最终 240 项是认证、桥接和相关回归的历史结果，不能外推为同一最终包的双向全自动验收。现有加法试验的 rev2 是新增规格，未证明主审自主发现原始规格不满足。

本轮云端审查核验了相关源文件摘要、导出证据与 JUnit 计数；未在云端重新运行 macOS 原生 CLI 或 Go/pytest。所有新修改必须产生本地的新证据。

依据：[验收记录](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tasks/cross-cli-subagent/docs/acceptance.md#L48-L83)；[初始任务与新增要求](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tasks/cross-cli-subagent/scripts/managed-coding-trial.py#L211-L216)。

## 2. 交付原则

1. 主会话负责基线、任务合同、集成和最终验收；实现 agent 负责分配范围；每个功能由未参与该功能实现的 agent 独立审查。
2. 功能只有在独立审查 PASS、主会话读取实际 diff 与证据并签收后，才能标 done。缺工具或未覆盖真实能力不能用“看起来没问题”代替。
3. 已修复项可以按 verified_existing 关闭，但仍需当前基线核验、相关测试、独立审查与主会话签收。
4. 独立任务可并行。每个 agent 使用独立 worktree 或明确互不重叠的文件归属；协议、schema 和 Go 集成冻结由主会话串行管理。
5. 保留旧数据库、句柄、版本路由、v1/v2 digest 和安装收据的语义。需要协议升级时增加版本和能力门禁，并覆盖 queue 与 launch 两处。
6. result_ready、传输送达、ACK、accept、集成分别记录。子进程 exit 0 不等于业务验收。
7. 保持私有运行目录 0700、能力和请求文件 0600；不把 owner/control/auth 文件、token、原始含密日志放入 Git 或交接包。
8. 子 CLI 的输出和反馈是任务数据；不能改变 owner、权限、provider lock、模型配置或委派层级。Worker 不递归派发。
9. unknown 状态保留资源和原证据；取消需确认进程树终止，不能把“已请求取消”直接记成“已停止”。
10. 本轮可创建任务分支、worktree、任务内私有安装、限额测试与本地提交。远端 push、正式分支合并、公开 release、覆盖全局插件均按用户在本地会话的现有授权执行；本提示词本身不授权这些发布动作。
11. 使用用户既有、已允许用于本项目的登录和模型配置；不新建账号、充值、更换网关、打印密钥或修改全局认证来制造通过。
12. 若原生能力因宿主版本、账号或系统缺失而不可验，记录 BLOCKED/NOT_EXERCISED 与精确原因，完成所有不受影响的工作。

## 3. 目标架构与合同

### 3.1 三类职责

| 层 | 责任 | 复用现有模块 |
| --- | --- | --- |
| ParentAdapter | 主会话身份、入口、回执路由和宿主能力 | current_owner/controller、Skill 入口、可选 MCP façade |
| WorkerAdapter | 启动 CLI、最小环境、工作区、事件、取消、精确准入 | adapter/profile、Host Codex/Claude/AGY/Grok |
| TaskController | 持久化状态、预算、候选绑定、返工与验收 | Go Coordinator + 同一个 SQLite Store |

DeliveryAdapter 是 ParentAdapter 的回执通道实现。它消费同一事件账本；不得拥有另一套业务完成状态或第二个任务数据库。MCP façade 只做输入输出适配、身份绑定和受控调用。优先以现有 controller_thread/epoch 表示逻辑 owner，只有现有字段无法表达时再升级，避免平行身份账本。

### 3.2 任务与回执状态分开

| 业务阶段 | 允许的后续动作 |
| --- | --- |
| preparing / submitting | 提交或按原 checkpoint 核对结果 |
| submit_uncertain | 原 request/control 绑定的 reconcile；禁止盲目重派 |
| rejected | 重放原拒绝及错误；显式重新申请需另有命令与合同 |
| running | status/wait/collect/stop；停止必须观察终止 |
| result_ready | 主审读取候选和验证证据；accept、reject 或 request-changes |
| rework_queued | revision + 1，以上轮 candidate 为 base，启动新 invocation |
| accepted / rejected / interrupted | 终态或明确可恢复路径；不隐式加预算 |

回执通道沿用现有 ledger 的 pending、sending、delivery_uncertain、transport_accepted、history_confirmed、controller_acked。awaiting_history 优先作为持久化 driver phase，避免首轮修改底层 wire 状态；同时检查 launcher/status 等全部消费者，不能让新 phase 被误认为 bootstrap 失败。

- 发送前持久化 delivery ID 与精确 envelope。
- 写入后超时属于可能已发送，保留 ID；只查同一 ID 的历史，不自动再次 turn/start。
- 历史匹配必须包含原 thread/epoch、delivery ID 和完整绑定。匹配不到不能推断业务失败。
- ACK 不撤销继续审查同一候选的权利；accept/rework 仍须绑定原事件并在写时复核。
- 普通 MCP dispatch 在 durable handle 建立后立即返回。可选 Tasks 绑定单个 revision 的 wait_review；其 completed 表示该 revision 的候选或终态结果可取，此时业务 run 可以仍在等待主审。不能等 accept 才把候选交回父会话，也不能让旧 wait_review 自动追随返工后的新 revision。

### 3.3 身份

继续使用 PID + birth + executable/socket 身份验证进程。在现有 controller_thread/epoch 上明确逻辑 owner；必要时版本化扩展 owner_session_id/conversation_generation，用于回执路由与幂等命名空间。

原生 conversation ID 只在宿主能核验时绑定；不能信任 worker 可伪造的环境变量或工具参数。不能捕获 /clear、/resume 切换的 adapter，明确报告 managed_process scope，不声称原生聊天隔离。

controller 重启不能自动认领旧任务。使用现有显式 rebind/recovery，并核验旧控制权和新 owner。默认仍是父 owner 丢失后停止 worker、保留现场；detached 持续执行暂不在本轮默认生命周期中。

### 3.4 对外工具与 review token

建议 MCP 工具直接映射现有 dispatch、inspect、wait、collect、request_changes、accept、ack、stop，降低两套名称产生语义漂移的风险。以下是 façade 合同；内部调用现有 CLI/IPC，不改造为另一套调度器。

- dispatch：任务规格、provider/profile、目录、授权文件、验收标准、预算、幂等 ID；返回 durable 句柄及真实状态。
- inspect/wait/collect：有界输出、分页/cursor、allowed_actions、blocked_reason、预算、证据引用。
- request_changes：review_token、反馈、原验收标准或明确版本化的新增要求、稳定 command ID。
- accept：review_token、稳定 command ID；内部仍校验完整 tuple。
- ack：按原回执处理规则确认已读取事件，保持与业务决定分离；包装层不得自动调用。
- stop：绑定观察过的 task/revision，返回 requested 与 confirmed 的区别。
- 可选 wait_review Tasks 的取消只取消等待；真实 worker 停止使用显式 stop，并观察退出。
- review_token 为不透明、不可任意构造的当前候选引用，绑定 owner、run/task/revision/event/hash/action_slot/segment/candidate；不取代 owner capability。旧 token 和旧 command 的重放按原决定处理。
- 限制文件与日志读取范围；MCP 参数不可传入任意 control 文件覆盖身份。
- MCP 关闭或不被宿主支持时，现有 Skill + wrapper 入口继续可用。

### 3.5 调用策略

保持每轮 fresh invocation，使用上一候选和当前反馈恢复任务连续性。session resume 未验证时继续拒绝。优先减少元数据探测、重复 prompt 和等待开销，不建立跨 owner 共享的模型进程池。

Claude 当前受管写入关闭 Bash/delegation/web/MCP，测试由受控验证路径承担。对外清楚区分“修改候选”和“验证候选”；构建命令、测试代码也属于执行，需要沿用现有 Host/验证隔离边界。不得为了 Maven/npm 等方便而静默开放网络、全局 HOME 或原项目写权限。

## 4. 任务分解与依赖

状态：pending → in_progress → awaiting_review → changes_requested 或 awaiting_owner_acceptance → done。blocked 是独立未完成状态。受条件限制的原生能力可列未验收，不能混入全量 PASS。

### T00 基线、现场保护与任务合同

依赖：无。完成后才能进入实现。

- 读取根及作用目录 AGENTS.md、现行实施合同、运行/Git 协议、测试文档、验收记录和本交接包。
- 核对 remote、分支、HEAD、工作树与未提交修改；fetch 目标分支，核对审查 SHA 是否仍为当前或祖先。
- 有后续提交时逐项重新确认本方案问题是否仍存在。保留不相关用户修改；必要时新建隔离 worktree。
- 建议任务分支 feat/cross-cli-hardening-20261105；若已存在，核验归属后继续或取唯一新名，不 force 重置。
- 目标分支与旧 v0.1 的历史无共同祖先是已知情况；对比内容，按需移植明确文件/补丁，不执行 allow-unrelated-histories 合并。
- 实际实施基线须在 baseline.md 固定：默认以上述审查提交为已知起点；若本地/远端有后续提交，先归因并记录采用哪一已核对的源树，保留后续改动，不静默回退。
- 建立 tasks/cross-cli-hardening-20261105/{plans,docs,data,reviews,scripts}，复制并更新 TASKS.json；为协议/schema 变更先写 ADR。
- 记录 Go/Python/pytest、OS/arch、CLI version/hash、安装版本和可用能力；不记录认证值。
- 初始测试与已知失败分开登记，失败先定位根因，不靠删除测试或改预期过关。

验收：BASE-01、BASE-02。

### T01 授权文件缺失父目录的准备

依赖：T00。
主要文件：internal/host/managed_policy.go、host/claude.go、internal/gitops/freeze.go 及相关测试。

当前 managedWritePaths 允许路径不存在，claudeAtomicWriteRules 随后 ReadDir(parent) 失败；PrepareWorkspace 没有创建授权文件父目录。

- 先验证全部声明文件及清单内部 foo 与 foo/bar 等文件/目录冲突，再由可信 Host 逐级创建必要父目录，限制在新建的所属 worktree 内；不预建空文件。
- 逐级核验已有组件为真实目录；拒绝 symlink、禁止组件、路径逃逸、大小写/规范化碰撞及文件占位。
- 准备阶段与启动间避免目录替换导致检查失效；优先复用项目已有的安全目录操作，不只调用裸 MkdirAll。
- 继续精确文件写授权和 Claude 原子临时文件规则；冻结时范围检查保持不变。
- Codex 与 Claude 共用必要的准备逻辑，分别实测。

验收：WS-01～WS-04。
依据：[路径与原子写规则](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/host/managed_policy.go#L48-L100)。

### T02 dispatch 拒绝结果的持久化与幂等重放

依赖：T00。会修改 task_dispatch/task_entry，主会话与 T03 协调文件归属。

- 规范区分 accepted、rejected、submit_uncertain、preparing，不用 existing 代替业务状态。
- 保存可公开的规范错误码、原决定状态和必要的可重试分类；日志详情不包含敏感路径或能力。
- 同 request_id、同规范请求重复调用返回相同业务结果；已明确拒绝返回原拒绝，CLI 退出码/工具错误语义一致。
- 未知结果仍通过原 control/checkpoint reconcile，保留原 request hash；不同内容使用同 ID 必须拒绝。
- 若提供 retry-admission，先定义“已明确未接受”的条件和独立命令 ID；首轮可先修重放，不自动增加重试功能。
- 旧 handle 的 rejected 若没有原错误字段，返回明确的历史拒绝状态与 unavailable 原因，不能伪造原错误或把它当新提交。

验收：DIS-01～DIS-04。
依据：[拒绝落盘](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/cmd/orchestrator/task_entry.go#L275-L286)；[已有请求返回](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/cmd/orchestrator/task_dispatch.go#L255-L266)。

### T03 统一输入限制与预算合同

依赖：T02 的状态合同；schema/公共类型由主会话串行集成。

- 将 CLI、IPC、Store、MCP 使用的 attempts、字符串字节数、路径数、超时范围收敛到同一合同或由能力投影提供。
- 分开定义输入文件、文本字段、adapter、owner decision、IPC envelope、artifact 的大小限制，按实际 JSON 序列化的 UTF-8 bytes 预检，包含 escaping 与信封开销；不把所有层粗暴统一成一个数或调大 IPC frame 掩盖重复内联。
- 先统一当前实际 1–3 attempts 限制，消除外层 1–64 的误导；如需扩大，另做预算/schema 论证，不直接改常量。
- 新版本明确单段 max_active_ms_per_segment 与总 max_active_ms_total，并展示 used/reserved/remaining。总 attempts/active 以 budget_group_id 为权威作用域，同组任务、重试、拆分共享不可变 policy 与已用/预占账本，不能换 task_id 或 revision 获得新额度。
- v1 的 max_active_ms 当前是每段额度，组默认 120 分钟；旧合同保持原语义并标记 legacy policy，不能偷偷改成总额度。
- retry 和 rework 分别计数且共同消耗总额度；unknown segment 继续占用预留；rebind 不重置预算。
- 新总预算合同遇到不支持的 coordinator 必须拒绝，不能自动降级或提示后继续使用更宽的旧预算；另选 legacy 合同须由调用方明确选择并获得对应预算授权。
- 总预算权威在 coordinator，Host 若继续按既有 GrantedActiveMS 执行，不必新增无关 wire 字段；仅 Host 必须理解的新语义才增加 Host capability。
- 必要迁移可重复、原子；旧数据库与旧 receipt 继续可读取。拒绝解析失败/越界时不得产生 task 或消耗预算。

验收：BUD-01～BUD-04、LIM-01。
依据：[外层限制](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/cmd/orchestrator/task_dispatch.go#L49-L55)；[Store 限制](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/store/admission.go#L78-L86)；[额度发放](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/store/scheduling.go#L102-L111)。

### T04 返工输入去重与有界反馈引用

依赖：T03 的大小/版本合同；与 T06B 串行修改 owner_rework。

- 保存不可变原始任务 brief，避免继续在上轮完整 prompt 上拼接所有历史 context。
- 完整反馈只保留权威版本；执行 payload 使用 decision ID + digest + 受控引用。Host 按确定性规则组装原始 brief、当前反馈与仍有效的历史条件，保留全部当前有效验收约束；不调用模型自动压缩合同、不静默截断，超出输入预算明确失败；旧正文、决定与 digest 不回写。
- 不用正则从旧 prompt 猜测删除历史内容；旧任务保持原解释，新增格式使用新版本/能力。
- 兼容旧 ReworkContext v1/v2 的字节与 digest；若新 context 改变绑定语义，明确升级并由新 Host 能力门禁保护。
- 引用绑定 owner/run/task/revision，限制大小与授权路径，摘要不符、丢失、截断均失败关闭。由 Host 有界读入同一份 bytes 完成 hash/解析/输入组装，worker 不能直接读取控制目录。
- 优先复用 artifact/quota/pin：先有界 staging、校验、同步并发布不可变正文，再在同一 DB transaction 登记/pin、保存决定、推进 revision、更新 payload/ready sequence、撤销旧 report capability；提交后才可调度。staging 与已发布未 commit 的正文计入有界配额并具备在途保护，GC 不得令即将提交的决定指向已删除文件。覆盖发布前、发布后 commit 前、commit 后 reply 前 crash；未引用 staging 可识别，不能删除仍被旧决定/恢复引用的正文。
- 同 command 重放先读取原决定；provider 当前不可用或 Host 离线不抹去已发生决定，执行资格由后续调度门禁检查。新格式在 queue、claim/launch、pending replay/reconnect 均检查能力。
- 在 CLI 预检报告 UTF-8 字节限制和最终 payload 超限；不得裁剪导致验收标准丢失。
- accept/reject/rework 的互斥、command 幂等、前候选 base 和完整 source tuple 保留。

验收：REW-01～REW-04、LIM-01。
依据：[当前上下文重复内联](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/store/owner_rework.go#L304-L326)。

### T05 回执桥超时、历史确认与恢复

依赖：T02 的状态规则；可与 T01/T03 的不重叠实现并行。

主要文件：runtime/native/delivery_adapter.py、owner_helper.py、plugins/codex-orchestrator/scripts/native-product-bridge.py 及 ledger/测试。

- 给 send/drain、响应读取、历史确认分别定义有名的截止时间，使用 monotonic deadline 和剩余预算。
- deadline 覆盖连接仍打开却永不返回、持续通知但目标响应缺失等情况；不因进度包无限续命。
- history 确认按总 deadline 等待，不被固定 20 次快速查询过早耗尽；保持完整 proof 和 thread/turn/item 匹配。
- awaiting_history 优先实现为 driver phase，保留底层 ledger 状态；monotonic 值不跨进程持久化比较，重启时由持久化事实重建本进程 deadline；等待中保留的 packets 数量与字节有界。
- 发送前保存唯一 intent，发送开始后出现歧义记 uncertain/awaiting_history；恢复先查 ledger + 同 ID 的历史，禁止新发同一回执。
- 正常忙碌主会话未完成 turn 不当成业务失败；过期后可恢复继续核查。是否允许仅 item 持久化即认定 delivered，须有协议证据和专项测试。
- helper 与 standalone driver 使用一致错误分类；保留原 failed/unknown 的兼容解析。
- 连接关闭、父进程退出、清理异常有界；不吞取消，不错误释放 unknown。
- 接受发送、历史确认、审查决定、ACK 之间分别故障注入并验证幂等。

验收：DEL-01～DEL-05。
依据：[发送等待](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/runtime/native/delivery_adapter.py#L430-L468)；[历史确认](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/plugins/codex-orchestrator/scripts/native-product-bridge.py#L264-L299)。

### T06A 降低等待与能力探测开销

依赖：T05 状态/生命周期、T07 身份合同稳定。

- 改进等待决策文件的 50ms 循环；文件事件或逐步退避需有 fallback，取消和 owner lost 仍能及时响应。
- 完整 executable hash 与频繁存活检查分层；初始绑定和关键动作边界保留强验证。
- 缓存以已验证 artifact identity/digest、版本、协议/profile 与安装收据为键；不能仅以路径或版本字符串缓存。
- 同路径替换、symlink、文件内容变化和安装升级触发失效；保留 held-FD identity 与重要边界完整复核，不能仅凭 mtime 维持信任。
- provider help/version 探测可以复用结果，但执行前仍验证实际 artifact/权限合同。
- 增加阶段计时与调用计数，使用同机同样本比较 p50/p95、CPU/I/O；把模型推理时间与本地启动开销分开。
- 优化 progress 展示时区分进度摘要与存活；当前每 15 秒最多 20 次的摘要上限不能被当成 5 分钟后 worker 失联。

验收：PERF-01～PERF-03。
依据：[重复摘要与等待](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/plugins/codex-orchestrator/scripts/native-product-bridge.py#L544-L595)；[摘要计算](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/plugins/codex-orchestrator/scripts/native-product-bridge.py#L229-L233)。

### T06B inspect 锁粒度与状态快照

依赖：T03/T04 的数据合同。与 T04 不并行改 owner_rework。

- 锁内读取 owner 已授权的状态快照和不可变 tuple；完成 Rows/transaction 并释放连接后，再在锁外校验 artifact/Git。
- Store 当前 SetMaxOpenConns(1)。只提前 d.mu.Unlock 而保留 tx，会继续阻塞其他操作；必须一并处理。
- 锁外验证后，再以短锁/短事务重核 source tuple、revision、adapter digest 和动作资格；变化时丢弃旧动作投影或有限重读，不无限重试。第一轮只调整 inspect 读路径，保留 QueueRequestChanges/ReviewResult 的既有原子写路径与证据检查。
- 返回 allowed_actions 时说明是观察值，真正 mutation 重新短事务检查 tuple/版本/slot 与必要证据，不能把 summary 当授权。
- OID 内容校验缓存只缓存不可变对象事实；private ref、目录身份、owner 和当前 revision 在写时复核。
- 可区分轻量 status 与显式 verify-result；保证旧 inspect 输出兼容或版本化，不破坏调用方审查链。
- 故意延迟 Git 验证时，其他 run 的 stop、退出结算和状态读取不应等待外部 Git 持有全库资源；并发 accept/rework 仍互斥。

验收：READ-01～READ-03。
依据：[summary 锁与检查](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/store/summary.go#L47-L49)；[单连接配置](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/store/store.go#L95-L96)。

### T07 逻辑 owner session 与恢复合同

依赖：T02/T03；协议和 identity ADR 主会话审查后实施。

- 在 managed owner 的现有 controller_thread/epoch 上明确逻辑会话，与 PID/birth 分开；必要时新增版本化字段，明确发起者、持久化位置、如何验证、何时轮换。
- 提供 scope=managed_process 或 verified_native_session 等明确能力；实际不能观察宿主切换时保留前者。
- PID 复用、父进程重启、旧 epoch 和跨逻辑 owner 的使用始终拒绝，不能借此认领旧权限。
- 对 verified_native_session 或经显式 owner 轮换的会话，验证 /clear、/resume 后旧回执不作用到新会话。managed_process 无法观察的宿主内聊天切换不保证聊天隔离；保留显式 collect/rebind，并关闭尚未证明正确路由的主动推送。
- 宿主没有稳定回调时，提供显式 new-owner-session/rebind 操作或继续 collect，不伪造自动识别。
- 保留默认父退出停止与显式恢复；detach 不作为隐含变化。
- worker 继承父环境仍不能递归 dispatch 或获取父会话凭据。

验收：OWN-01～OWN-04。
依据：[当前进程 scope](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/cmd/orchestrator/current_owner.go#L15-L25)；[owner 丢失停止](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/host/source.go#L342-L374)。

### T08 本地 MCP façade 与统一 review token

依赖：T02/T03/T04/T05/T07；先写工具 schema 与错误映射。

- 优先本地 stdio，使用当前正式维护且适合仓库语言的官方 SDK/协议；冻结依赖版本。
- 复用当前 Coordinator/SQLite 与 owner 授权；明确普通 shell/Skill 与 MCP 的相同业务结果。
- 实现第 3.4 节工具面与有界结果，不为 MCP 单开业务数据库或自动规划模型。
- 工具调用不能覆盖 control/state/owner；review token 绑定原候选，必须同时验证当前 owner。
- 生成任务内 MCP 配置和入口供加载验收；不写全局配置或自动关闭审批。
- 子 worker 的 empty MCP/禁止递归限制保持；MCP 仅作为父会话入口。
- 默认普通工具返回句柄与 collect 路径；异步 Tasks 只有 per-request 能力成立才启用。
- dispatch 返回 durable handle 即完成；可选 wait_review Tasks 绑定一个 revision，其 completed 仅表示该 revision 的候选/终态结果可取。request-changes 后新 revision 创建新等待；accept 与 ack 是各自显式调用，不能形成等待 accept 才给结果的死锁。
- wait_review 的 cancellation 只取消等待；真实执行由 stop 请求并确认。调用超时不自动产生另一份委派。

验收：MCP-01～MCP-05。
官方依据：见文末 O1～O3。API 字段以本地实现时核验的协议为准。

### T09 父 CLI 主动回调能力

依赖：T05/T07/T08。分开记录代码完成和宿主真实验收。

- Codex：完善现有受控 App Server 连接与 turn/start.toolOutput 路径，不假设新 server 可以接管任意已打开 TUI；实测主会话空闲和忙碌两种回执场景。
- Claude：检测实际客户端、协议、每请求 Tasks 能力、会话形态与可用功能；满足条件才使用异步 Tasks；输入关闭和会话切换按宿主行为单独验证。
- 其他 CLI 或第三方网关不满足条件时回退 wait/collect，显示原因。
- /clear 后宿主可能把任务结果送入当前会话；本层必须按 owner 路由合同验证，不能把有通知当成送回原始会话的证明。
- 仅普通 MCP notification、后台进程退出、日志出现回执，都不足以证明父模型已收到并继续审查。
- 缺少宿主能力可将对应 native 验收置 BLOCKED，接口和 fallback 仍完成并测试；不宣称全部 CLI 等价原生 subagent。
- T09 代码若纳入最终包，T11 前必须完成代码冻结、必要非 live 测试、独立审查和主会话代码范围签收。native gate 未通过则相关能力保持关闭，整个 T09 不得标 done；非 live 审查也无法完成时，相关改动留在最终包外，不阻塞核心交付。

验收：PUSH-01～PUSH-04。

### T10 兼容性基底合流

依赖：T00，可独立准备；与 profile/adapter 修改合并时串行复核。

- 对比 4b2b491 的 Claude 405 版本窗口与当前 46 版本窗口，确认差异原因。
- 若无有意收窄的当前决定，按文件移植经过审核的只读 catalog、cohort/flag 逻辑及相应测试/证据；若保留较窄窗口，形成明确 ADR 和迁移提示。
- 保留 21 个 Codex native pin 及摘要；AGY/Grok 原边界不扩大。
- 只读准入数量不写成逐版真实模型验收数；coding exact gate 与 read catalog 独立。
- 不以运行时抓“最新版本”代替冻结目录，不让自动升级改变既有 handle 路由。

验收：COMP-01～COMP-03。
依据：当前与旧提交 tools/orchestrator/internal/adapter/compatibility_catalog.json，及当前 admit/profile/compatibility 测试。

### T11 当前文档、私有包与入口

依赖：所有准备交付的生产改动；生产冻结后打最终包。

- 统一当前设计、支持矩阵、安装/使用入口；历史文档加替代关系，保留失败记录。
- 当前设计写明进程/会话 scope、父退出行为、认证投影与 refresh 限制、精确写权限、resume、MCP/回调条件。
- support-matrix 每一项包含：parent/provider、version+artifact、scope、read/write、fresh/resume、delivery mode、平台、fixture/native/live 证据、包/源码 SHA、未验证边界。
- README 第一屏区分当前可下载版本与源码/候选包；不得把旧 preview 当作本次新能力安装包。
- 打包、私有安装、doctor、入口生成在全新任务目录完成。保留绝对 wrapper、runtime receipt、解释器与必要脚本绑定。
- 收据绑定脚本可能位于 tasks 目录；迁移前检查打包依赖，不能当历史垃圾删除。
- 最终包有唯一版本、source tree/commit、binary/manifest/package SHA、依赖说明和复现步骤。
- 生产代码或打包材料发生变化，重新冻结和构建；保留旧证据为历史，按影响重跑相应最终包验收。

验收：PKG-01～PKG-04、DOC-01。

### T12 同一最终包的完整验收

依赖：T11；包含全部必做静态/进程/包验证，原生宿主子项单独标记。

分层执行：

A. Go 与 Python：先相关回归，再最终冻结源码全量普通/race/vet/build 和相关 Python 套件。真实 provider opt-in、skip 与平台条件逐项列明。
B. 真实 macOS 沙箱/进程：新目录、精确写范围、unknown、取消、认证守卫、实际工作区与候选绑定。
C. 同一最终包 Codex→Claude 和 Claude→Codex：真实父会话和真实 worker，公开入口，候选与测试绑定，返工与 accept/ACK。
D. 主动回调：仅对实际支持组合执行空闲/忙碌/切会话/重连测试；不与 C 的 wait/collect 结果混算。
E. 对照性能：相同机器与参数，测本地桥接阶段；无基线则仅报告现状，不虚构收益。

真实任务建议使用 2–3 个代码文件和一个新目录：初始合同包含结构要求、行为边界及测试，全部在首次 dispatch 前冻结并交给 worker。可先用无外部依赖的小 Python/Go 项目保证可复验；若本机已有 JDK/Maven 与离线依赖，再加 Spring Boot/Java 小模块补充，不让首次目标变成下载复杂依赖。

真实父会话必须读取原始合同、源码/diff 和实际测试输出，自行形成审查发现与决定；试验脚本提供工具和记录，不代替主模型预写“通过/返工”。

若真实模型首次全部满足标准，记 happy-path PASS、原始缺陷返工场景 NOT_EXERCISED；不得暗改候选、诱导模型违反要求或把 rev2 新增要求当成首轮缺陷。可另做公开标注 fault-injection 的负向端到端测试，并让真实父会话审查，但这证明的范围必须独立标记。

预先记录每场景 attempts/time/token 或可获得的费用上限；优先沿用现有授权。自然缺陷场景不能无限消耗模型额度，有限尝试未触发则如实交接。账号不可用时保留错误与缺失前提，继续其它验证，不替换账号/模型制造结果。

验收：LIVE-01～LIVE-05、REG-01～REG-03、PUSH-*。

### T13 最终独立审查、主会话签收与本地交接

依赖：T00 已明确现场与基线；汇总所有任务当时的准确状态及证据。T12 或其它核心项受阻时，T13 仍可生成受阻交接报告；生成报告不免除任何产品验收 gate。

- 独立最终 reviewer 审查完整冻结 diff、迁移、版本准入、打包、真实证据和文档是否一致。
- 主会话逐项复核任务状态、差异和原始报告；独立 reviewer 的 PASS 是输入，主会话仍须签收。
- 为已验收功能形成范围清晰的本地提交；不要把原始机密证据混入提交。
- 输出实际分支/HEAD、已完成/blocked/未覆盖、测试计数、最终包版本/SHA、复现方式、已知风险、待用户授权的发布动作。
- 给出可继续的恢复提示词与现场索引；避免依赖聊天摘要作为唯一状态。
- 分别记录 handoff_status 和 core_status/native_push_status。受阻交接文件可达到 PREPARED，但上游核心阻塞时 T13 的产品签收及 task 状态保持 BLOCKED，只有最终产品签收 PASS 后 T13 才能 done；最终独审无法执行也保留其 gate 未完成。
- 只完成核心部分时写“核心范围通过，主动回调某组合未验收”，不能写无条件全部完成。

验收：FIN-01～FIN-03。

## 5. 并行安排

第一组：T00 后先开展 T01、T02、T10；T02 状态合同冻结后，T05 可与 T03 并行。共享 schema/公共类型由主会话串行集成。
第二组：T04→T06B 依次修改 Store；T06A 在桥接稳定后进行；T07 按主会话审定的身份合同实施。
第三组：T08→T09；T10 合入并回归；统一 T11 文档和最终包。
第四组：冻结生产代码，T12 验收，T13 最终审查签收。

每个功能单独 review。审查 agent 不与实现 agent 为同一身份，审查时读取已冻结的提交或 diff。Go 全量 CLI 套件可能中途 go build，执行期间停止修改该 worktree；不要在持续变化的树上声称测试绑定最终代码。

## 6. 本地验证命令与证据

命令以实际仓库最新 AGENTS/脚本参数为准，先查看依赖和 opt-in 条件。

在 tools/orchestrator 目录：

~~~sh
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
~~~

在仓库根：

~~~sh
python3 -m pytest -p no:cacheprovider tools/native-product-bridge tools/real-user-trial tasks/cross-cli-subagent/tests/test_delivery_scripts.py -q -rs
~~~

最终私有包 smoke 入口已存在：

~~~sh
sh tasks/cross-cli-subagent/scripts/smoke-installed-package.sh ABS_NEW_WORK_ROOT ABS_PINNED_PYTHON UNIQUE_VERSION
~~~

该入口要求新的目录与绝对解释器路径；它使用 fixture，不证明真实模型。其它打包/本机测试依当前仓库文档补齐，不盲跑所有历史任务脚本。

每份测试记录包括 command、cwd、start/end、exit code、passed/failed/errors/skipped、source commit/tree hash、平台、安装包版本/摘要、provider/version/artifact、fixture/native/live 类型、输出路径和 SHA256。

保存失败原始报告；修复后使用新文件名和新记录。JUnit/日志不能只引用聊天中“全通过”，必须可重算计数。生产代码变更影响先前真实证据时，将其降为历史并重跑对应项。

## 7. 最终交付结构

建议在任务目录形成：

- plans/current-plan.md：最终采用方案与 ADR 索引。
- TASKS.json：机器可读任务状态与依赖、审查与签收。
- docs/baseline.md：基线/现场/环境/能力。
- docs/current-capabilities.md：实际支持矩阵。
- reviews/Txx-review.md：每功能独立审查。
- docs/test-report.md：测试命令、计数和类型。
- docs/live-acceptance.md：同包双向与主动回调的逐项结论。
- data/public-evidence/：可公开且脱敏的验证、摘要、候选 diff 与 manifest。
- docs/delivery.md：分支、提交、包/源码 SHA、安装方法、限制。
- docs/resume.md：精确恢复入口、下一步和阻塞项。
- 私有运行现场索引：仅本地安全保存，不进入公开 Git。

## 8. 最终完成标准

核心范围验收 PASS：所有必做实现与对应必需验证实际通过，独审 PASS 且主会话签收；最终包 REG-01～REG-03、LIVE-01、LIVE-02、LIVE-04、LIVE-05 通过，返工机制确定性测试通过，最终集成审查与主会话产品签收通过。任何必需核心项 FAIL/BLOCKED/NOT_RUN 时，核心状态不能写 PASS；仅列出阻塞不构成通过。

LIVE-03 原始缺陷主审场景的自然 live、fault-injection 与 NOT_EXERCISED 单独记录。它未自然触发时，在确定性返工测试和其它核心门槛通过的前提下可交付范围受限的核心版本；不能宣称“原始缺陷由真实主会话发现后的返工”已验收。核心机制范围与“全部目标场景验证完毕”的结论分别输出。

交接资料准备完成不等于核心验收通过。存在环境或授权阻塞时仍完成可做的实现、构建、报告和可继续交接，产品结论保持 BLOCKED/未验收。

完整主动回调体验：再加 T09 的目标宿主真实验收通过。某 CLI 不具备必要能力时保持能力关闭与可靠回退，不能为了“全部 done”放宽边界。

发布准备完成与公开发布分开。只有存在对应用户授权时才 push/merge/release；否则交付本地提交与可审查的发布候选。

## 9. 资料与来源

- S1：[仓库规范](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/AGENTS.md)；[分层测试](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/docs/testing.md)。
- S2：[实施合同](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tasks/cross-cli-subagent/plans/implementation-contract.md)。
- S3：[历史验收与范围](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tasks/cross-cli-subagent/docs/acceptance.md)。
- S4：[当前支持矩阵](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/docs/support-matrix.md)；[Provider 运行约束](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/docs/provider-runtime.md)。
- S5：[候选 Git/清理协议](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/docs/git-cleanup-protocol.md)；[运行协议](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/docs/runtime-protocols.md)。
- S6：[旧兼容目录](https://github.com/GuanJHCode/Agent-Bird/blob/4b2b4916232c9d7526f51cf0a73204748c62faf8/tools/orchestrator/internal/adapter/compatibility_catalog.json)；[当前兼容目录](https://github.com/GuanJHCode/Agent-Bird/blob/29691e9f08468b1de56cb0c42977084b58c91975/tools/orchestrator/internal/adapter/compatibility_catalog.json)。
- O1：[Codex App Server](https://learn.chatgpt.com/docs/app-server)：当前正式文档包含 turn/start.toolOutput；活跃 turn 可排队。实现前以本地实际 artifact/schema 再核验。
- O2：[Claude MCP](https://code.claude.com/docs/en/mcp)：Tasks 是条件性能力，文档给出 2.1.304+、协议/功能开关/会话形态等条件；一次性 -p 与持续 stream-json/交互会话不能混同。
- O3：[MCP Tasks 2026-07-28](https://tasks.extensions.modelcontextprotocol.io/specification/2026-07-28/tasks)：按请求能力协商；异步调用结果与业务验收分别管理。
- O4：[Claude headless](https://code.claude.com/docs/en/headless)：指定 session ID 的 resume 是 CLI 能力，不等于本项目已验证安全恢复。

来源核验日 2026-11-05。官方协议和本地版本可能继续变化，接手者应核验实际能力，不仅比较版本字符串。

