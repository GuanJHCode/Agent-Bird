# 直接交给本地 Codex 的主执行提示词

将以下“提示词正文”复制给本地 Codex。若已解压交接包，请同时让 Codex 能读取同目录的 01-implementation-plan.md、03-review-and-resume-prompts.md、04-acceptance-matrix.md 和 TASKS.json。没有附件时，正文也包含启动与实施的完整要点。

## 提示词正文

你是 Agent-Bird 的本地主实施负责人。请实际检查代码、实施改动、运行验证、形成本地提交和交接记录，持续推进所有已授权且不受阻的任务。主会话负责拆解、集成和最终验收；可并行委派子 agent，但每项功能完成后必须由未参与该功能实现的另一 agent 独立审查，通过后主会话还要核对实际 diff、测试和证据，才能关闭该任务。

### 一、仓库与基线

仓库：https://github.com/GuanJHCode/Agent-Bird.git
目标分支：feat/cross-cli-subagent-20261104
已审查基线：29691e9f08468b1de56cb0c42977084b58c91975
兼容性对照：v0.1 / 4b2b4916232c9d7526f51cf0a73204748c62faf8
建议本轮分支：feat/cross-cli-hardening-20261105；同名存在时先核验归属，不强制覆盖。

从当前工作区开始确认仓库。若当前目录不是目标仓库，在用户给定或常用代码目录做有界定位；不存在时按本地约定克隆到新的项目目录，报告实际路径。不要假设云端审查路径存在于这台 Mac。

先读取根及涉及目录 AGENTS.md，以及：
- tasks/cross-cli-subagent/plans/implementation-contract.md
- tasks/cross-cli-subagent/docs/acceptance.md
- docs/runtime-protocols.md
- docs/git-cleanup-protocol.md
- docs/provider-runtime.md
- docs/support-matrix.md
- docs/testing.md
- README.md 与实际安装/入口脚本
- 本交接包的完整方案、审查提示词、验收矩阵和任务模板（如可用）。

检查 git status、remote、HEAD、目标分支、现有 worktree；fetch 后核对远端。已知 2026-11-05 目标 HEAD 为上述 SHA。如果接手时分支前进，记录新旧差异并逐项复核；已修复项按当前代码验证，不重复实现。实施基线的选择必须写入 baseline.md，不能静默换树。历史无共同祖先的分支按内容比较，禁止自动 allow-unrelated-histories 合并、reset、clean、force push 或覆盖用户修改。

优先使用隔离 worktree。每个实现 agent 有明确文件或独立 worktree 归属，主会话是集成树唯一写入者。不要多个 agent 同时修改 task_dispatch、owner_rework、schema 等共享文件。Go 全量测试期间冻结其工作树，避免套件中途 go build 时源码变动。

### 二、目标与保留能力

实现并稳定以下体验：
主 CLI 会话派发另一 CLI → 受管工作区执行 → 候选/回执返回 → 主会话读取真实代码、结构与测试 → 不满足原始条件则反馈返工 → 新 revision 继承前一候选 → 复审后显式 accept，ACK 单独处理。

保留现有 Go Coordinator + SQLite、Host、候选冻结、revision、durable receipt、权限隔离和安装收据。已经实现的 controller/routing/dispatch/wait/reconcile、通用 owner、owner request-changes v2 和 exact managed coding 要先复核，不能按旧结论重写。

当前编码精确门禁：Codex 0.166.0；Claude gateway 2.1.267/2.1.316；AGY/Grok 保持只读。以当前源码和已验证 artifact 为准，不能根据只读 catalog 或 --help 自动扩大 coding 权限。

主审直接返工继续限制在当前已验证的独立 managed implementer 范围。每轮使用 fresh invocation，previous candidate 作新 base；typed session resume、递归 worker、多任务 DAG 联动返工暂不顺带开放。

### 三、按依赖实施任务

编号是任务标识，不是严格执行顺序：T00 后可并行 T01/T02/T10；T02 冻结后 T03 与 T05 可并行；T04 在 T03 后，T06B 在 T03/T04 后；T07 在 T02/T03 后；T06A 在 T05/T07 后；T08 在 T02/T03/T04/T05/T07 后，T09 再接 T08；所有纳包生产材料冻结后做 T11/T12。以 TASKS.json 的同一依赖图为准。

T00 基线与合同：
记录实际基线、用户修改、环境、CLI/安装版本、已知失败和能力；建立 tasks/cross-cli-hardening-20261105 下的 plans/docs/data/reviews/scripts 与 TASKS.json。先为协议、身份、预算变更写简短 ADR。

T01 新目录：
修复 managedWritePaths 允许父目录不存在、claudeAtomicWriteRules 随后 ReadDir 失败的链路。Host 在 worker 启动前，先验证全部声明文件，再逐级创建必要父目录。拒绝 symlink、目录替换、越界、禁止组件、foo 与 foo/bar 冲突；保持 exact file 与原子临时文件授权，不开放整个父目录。Codex/Claude 分别验证；准备失败 worker 启动数应为零。

T02 dispatch 拒绝重放：
当前 rejected handle 清空 ControlFile 后，同 request_id 重放可能成功返回 status=existing。保存并重放真实 rejected 状态和规范错误码；CLI/工具错误语义一致。同 ID 不同内容拒绝。submit_uncertain 继续走原 control/checkpoint reconcile，绝不盲重发。以 public CLI + fake coordinator 测第二次仍失败、Submit RPC 总计一次。旧 rejected 记录缺错误时明确标 unavailable，不伪造。

T03 限制与预算：
统一 CLI/IPC/Store/MCP 的真实范围。先让 attempts 与实际 1–3 一致；统一序列化后 payload/frame 的字节预检，包括中文与 JSON 转义开销。新版本明确单段和总活动预算，展示 used/reserved/remaining。旧 v1 max_active_ms 是每段额度，组上限默认 120 分钟；保留旧语义并标 legacy policy，不偷改为总额度。总 attempts/active 以 budget_group_id 为权威，同组任务、重试和拆分共享同一不可变 policy/账本；换 task/revision 不获新额度。retry/rework 共同耗用总预算；unknown 保留预留；rebind 不重置。新总预算遇旧 coordinator 不支持必须拒绝，不自动降级扩大额度。迁移与必要能力门禁完整；Host 若沿用 GrantedActiveMS，不无谓新增 Host 协议。

T04 返工上下文：
完整反馈持久化一份，使用 decision ID/digest/授权引用传递。Host 按确定性规则组装原始 brief、当前反馈与仍有效的历史条件，保留全部有效验收约束；不调用模型压缩合同、不静默截断，超出输入预算明确失败。保存不可变原始 brief，不从历史 prompt 用正则猜删内容。不继续累加重复 OwnerSpec/context。旧 v1/v2 字节和 digest 不改；新格式用显式版本/Host 门禁，queue 与 launch 两处检查。引用校验 owner/task/revision、大小和摘要；Host 对同一份有界 bytes 校验后组装。staging/已发布未 commit 正文计入配额并有在途保护，GC 不得删除即将提交的引用；测试发布 artifact 与提交 DB 各崩溃点、丢失/篡改和重放。accept/reject/rework 原子互斥保持。

T05 回执桥：
给 connect、send/drain、匹配响应、history read/confirmation、close 设置有名的单调时钟截止时间；持续无关通知不能无限续命，缓冲有界。history 按 deadline 等待，修复固定 20 次快速耗尽。优先把 awaiting_history 加为 driver phase，保留 ledger 的既有状态兼容。
一旦 sending 已持久化，超时/断连/崩溃都按可能已发送处理：只核对原 delivery ID、hash、owner/epoch 的历史，不重发、不换 ID、不自动 ACK。主会话忙碌造成历史暂未确认不改变 worker 业务状态。保留严格 history proof；清理与取消有界；恢复覆盖发送前后、响应后、proof 后和 ACK 成功落盘前。

T06A 性能与观测：
减少 50ms 决策循环反复完整 hash 三个 executable；用事件或退避，存活检查与完整校验分层。初次绑定、重要动作和执行前保留强验证。缓存先做进程内，key 包含真实 artifact identity/digest、协议、profile、平台及影响探测的隔离环境；同路径替换/修改/权限或策略变化失效。不能缓存动态 owner、认证、权限、workspace 或预算授权。记录 probe/hash/spawn/first_event/history 等耗时和计数，同机比较，不虚构收益。进度摘要停止不等于进程失联。

T06B inspect：
锁内读授权快照，结束 Rows/transaction 并释放唯一 SQLite 连接，再在锁外做 Git/文件验证。当前 SetMaxOpenConns(1)，只移动 Unlock 不够。锁外验证后用短锁/短事务重核 source tuple、revision、adapter digest 和动作资格；变化则丢弃旧投影或有限重读，不无限循环。先优化只读路径，保留 QueueRequestChanges/ReviewResult 的原子写路径和证据检查；mutation 再核验 tuple/CAS、ref/目录身份。用可阻塞 verifier 注入证明：验证尚未释放时，另一 run 的真实 DB 写已完成；避免只凭毫秒阈值。旧 summary/inspect 兼容或明确版本化。

T07 owner：
在现有 controller_thread/epoch 上区分逻辑 owner、PID/birth 进程绑定和经过核验的 native conversation。需要新字段才增，避免平行身份账本。返回 owner_scope 与可用 delivery/continuation 能力。无法观察 /clear 或会话切换时继续明确 managed_process；不把 MCP transport session ID 当原生聊天 ID。旧 epoch、PID 复用、跨 owner handle、worker 继承环境都不能获得新权限。默认父退出停止 worker、保留现场，恢复走显式 rebind；不隐式改成 detach。

T08 MCP 普通工具：
增加本地薄 MCP façade，复用现有 Go 状态与授权，建议直接映射 dispatch/inspect/wait/collect/request_changes/accept/ack/stop。dispatch 立即返回 durable handle；结果读取有界/可分页。review_token 绑定原候选完整 tuple 且不能替代 owner capability。CLI 与 MCP 同请求结果一致：过期 revision 的新 mutation 拒绝；同 command/同规范正文、且当前 owner 权限有效的重放返回原决定，即使 revision 已前进；同 command 不同内容拒绝。不能自动 accept 或 ACK。只生成任务内加载配置，不覆盖全局配置；worker 的 empty MCP 与禁止递归保持。
如实现 Tasks，用单 revision 的 wait_review：其 completed 表示该 revision 结果可取，业务仍可 result_ready 等主审；返工新 revision 建新等待，旧等待不追 latest。取消等待只结束等待，停止真实 worker 走显式 stop。

T09 原生主动回调：
按精确宿主能力实现与验收。Codex 继续受控 App Server/turn/start.toolOutput，不假设新 server 能接管任意现有 TUI；Claude Tasks 必须核验实际 per-request 能力、协议、会话形态和开关。第三方网关或一次性 -p 不自动认定支持。分别证明空闲/忙碌主会话收到结果且继续审查。无宿主能力则保持关闭、wait/collect 回退并列 BLOCKED，不影响其它任务完成；managed_process 下未证明原聊天路由的主动推送保持关闭。若 T09 代码纳入最终包，T11 前先完成代码冻结、必要非 live 测试、独立审查与主会话代码签收；native 门禁不足时能力关闭、T09 保持 BLOCKED，不把整个任务标 done。非 live 审查也无法完成时，相关改动留包外。typed worker resume 仍不开放。

T10 兼容性：
对照旧 Claude 405 版本目录与当前 46 版本目录，确认缩窄原因。移植只读 catalog/cohort/flag 与相应测试须一起审核；有意收窄则写 ADR。保留 Codex 21 pins，coding exact gate 独立。版本目录不等于逐版本 live 验收，旧 handle 不改路由。

T11 文档和最终包：
统一当前设计、安装、支持矩阵、生命周期、认证投影和真实能力；历史文件标替代关系并保留失败。README 区分旧公开 preview 与新候选包。所有生产改动审查后冻结，生成唯一版本，记录 source/binary/manifest/package SHA，在全新任务私有目录安装、doctor、生成并实际加载所需入口。不得覆盖 .7 或全局插件，不删除仍被包引用的 tasks 脚本。

T12 验收：
相关测试通过后，在冻结树上运行必要 Go 普通/race/vet/build、Python、包 smoke；实际命令遵照仓库规范。报告 fixture、OS-native-local、live-provider 及 PASS/FAIL/BLOCKED/NOT_RUN/NOT_EXERCISED，skip 不算 pass。
用同一个最终包、相同 runtime SHA 分别做 Codex→Claude 与 Claude→Codex；任务包含多文件、新目录、固定原始验收标准和真实测试。真实父会话读原合同、候选 diff、测试，再自行形成 accept 或返工意见；脚本只供工具，不代写业务判断。rev2.base 必须等于 rev1.candidate。
当前 .4 人工收尾、.7 反向成功和 240 回归是历史证据，不能拼成当前包全通过。真实模型首轮全对就记 happy-path PASS，原始缺陷返工 NOT_EXERCISED；不篡改候选、诱导违反要求、无限追加付费试验。可另做明确标注 fault-injection 的已知坏候选试验，不冒充自然 live 失败。新要求返工与原始要求失败返工分开。
真实调用只使用本地会话已授权的账户、模型与额度，预先设有限 attempts/time/cost 范围；历史文档曾用过的凭据/模型授权不自动沿用。缺前提时完成代码、fixture、包与其它验证后列精确阻塞项。

T13 最终交付：
T13 可在 T12 或其它核心任务 BLOCKED 时生成准确的受阻交接，不必等验收通过才记录现场。另起独立 reviewer 检查完整冻结 diff、迁移、包、证据和文档；主会话逐项签收。形成范围清晰的本地提交、完整交接报告、可继续提示词和发布候选。报告实际分支/HEAD、包版本/SHA、通过与未覆盖、已知限制及下一步。handoff_status 与产品 core_status/native_push_status 分开：受阻交接可 PREPARED，但 T13 产品签收和任务状态保持 BLOCKED。核心 PASS 必须有核心实现/独审/主签收、最终包必要回归、LIVE-01/02/04/05 和最终集成审查通过；必需项 FAIL/BLOCKED/NOT_RUN 不得聚合 PASS。LIVE-03 原始缺陷 live 未触发可单列 NOT_EXERCISED，在确定性返工及其它门槛通过时交付范围受限核心；不宣称该场景或全部目标场景已验收。

### 四、审查与推进规则

每项功能：
1. 主会话明确任务合同、文件归属、验收项和预算。
2. 实现 agent 修改并跑直接相关的必要测试。
3. 另一只读 reviewer 根据实际 diff 和证据审核，给 PASS/CHANGES_REQUESTED/BLOCKED 与文件/行号。
4. 发现问题交回实现者修复，再复核。
5. 主会话检查代码、报告与关键行为，签收后才 done。

独立 agent 不可用时，不伪称已独审；可继续实现、测试和交付，但对应 review gate 保持未完成。已修复项可以 verified_existing 关闭，也必须有当轮核验和审查。每个任务记录实现者、reviewer、审查基线/目标、测试、发现与整改、主会话签收和提交 ID。

遇到具体失败先定位原因，区分产品 bug、fixture bug、环境、能力不支持和授权缺失。不要靠跳过、放宽权限、换账号或重置预算制造通过。普通可逆实施选择自行完成；只有确实缺权限/必要信息时说明最小阻塞，先完成其它工作。

### 五、证据与交付

任务文件放在 tasks/cross-cli-hardening-20261105。
至少维护 baseline.md、current-plan/ADR、TASKS.json、每任务 review、test-report、live-acceptance、脱敏 public-evidence/manifest、delivery.md 和 resume.md。

记录每次验证的源码/包身份、命令/cwd、退出码、测试计数、环境、provider/model/认证路由类型、报告路径与 SHA。原始含密日志、auth/control/bootstrap/owner capability 只在安全私有现场；不进入 Git、公开报告或交接包。

允许本地 task branch/worktree、代码、文档、任务内安装、已授权的有限测试与本地提交。push、合并正式分支、公开发布、替换全局插件须有本地会话对应授权；本提示词不自动授权。不要全量 git add 用户已有修改，不删除旧运行时/工作区/refs/失败证据。

生产代码、桥、入口或权限策略在验收中改变，必须形成新构建身份，按影响重跑对应验收；不要混用不同包记录证明一个最终包。

开始执行 T00，随后按依赖持续推进，不要只返回计划或每一步询问是否继续。每完成一项功能给出简短进度：改了什么、验证了什么、独审结论、剩余阻塞。最终清楚区分核心范围和各宿主主动回调范围。

## 提示词正文结束

