# v0.1 运行协议补充

本文保留运行协议约定；具体实现与已验证范围以 [支持矩阵](support-matrix.md) 为准。

## 1. 版本、持久化与进程角色

- 一个 Go 二进制提供协调器、来源 Run Host、控制命令、任务报告和 Hook 角色。Go 工具链按发行版锁定；如使用 `os.Root.RemoveAll`，不得低于 Go 1.25，实际最低版本由依赖和两平台测试共同确定。
- SQLite 驱动优先选择支持两平台的纯 Go 实现，具体包和版本在原型中锁定；不能把选择 Go 写成不存在 C 依赖或构建问题的保证。数据库位于本地持久化目录，网络文件系统不准入。
- SQLite 采用单写者、外键/唯一约束、明确事务和 `synchronous=FULL`；WAL/驱动/文件系统的实际断电保证要验证。完整配置随 schema 版本记录。
- Host 不写主 DB；每个 Host 独立的追加账本/spool 先持久化再发事件。条目有长度、sequence、hash 和提交边界；恢复截断未提交尾部，不能把未确认尾部当成功事件。
- DB schema 迁移在唯一实例锁下执行；活动不兼容 Host 不被强制升级。迁移意图与备份可对账，失败保留旧库；禁止运行更旧程序猜读新 schema。

## 2. 单实例、Host 与重启

协调器通过本机用户作用域的 OS 互斥选主，取得锁后开启 DB，并原子递增 `coordinator_epoch`。锁被持有时不因心跳超时再起第二个协调器。所有 Host 控制消息绑定该 epoch；旧连接断开后重新握手，只接收当前 epoch。

`ensure-running` 是 submit、collect、Hook 和活动 Host 共用的启动入口；仅启动本工具进程，不启动模型或执行 CLI。首次启动者竞争同一锁，其他调用等待带版本的 ready 握手。

活动 Host 发现 IPC 中断后保存事件，并以 1、2、4、8、16 秒退避竞争恢复协调器；这轮恢复次数落在账本中，程序重启不清零。再次失败标记 `coordinator_unavailable`，保留事件等待后续明确触发，不无限循环。恢复协调器不会重新构造未知执行者或读取认证环境。

Run Host 按来源工作流启动和复用，绑定 `origin_context_id/host_generation`，先从来源 PATH 解析并固定 CLI 路径与版本再切换 worktree；不把认证环境给协调器。协调器即使由 Host 拉起，也只使用项目自己的非认证状态；禁止以协调器继承的环境直接发起执行任务。来源上下文丢失进入 `launch_context_unavailable`，恢复原会话后重新绑定。Windows shim 使用明确解释器及保真的参数，不加 ExecutionPolicy Bypass。

工作流的 Git 准备/整合与验证命令也在其来源 Host 中运行，协调器只管理意图、配额与仓库互斥；不能借协调器来源的 PATH/Git配置/签名环境执行别的工作流。整合进程即使不是模型CLI也要有独立进程所有权账本和仓库互斥，不能和其他整合并发。

初始每用户最多 4 个来源 Host、每 run 64 个未结束 task、全用户 256 个未结束 task。只剩结果交付且无后续执行/恢复用途的 Host 在其事件 durable ACK 后退出；协调器保留待回调，不为保存无用环境保留 Host。达到来源 Host 上限时拒绝新的工作流启动并返回 `admission_deferred`，不偷偷接受后丢启动上下文，不消耗 attempt；用户/Codex 可在已有 run 结束后从原来源重新提交。

来源 submit 在创建 Host 前先经协调器事务预占 `host_launch_id/host_slot`，创建使用已登记的启动意图及屏障，存活身份确认后才返回成功。启动中/Host身份UNKNOWN都占上限，确认退出或证明从未启动才释放；并发submit不能先spawn后计数。来源submit崩溃则对该意图查证，协调器不能借自己的环境补开Host。

显式 `Resume` 对 supervised Owner 的恢复要求稳定 `CommandID`、当前控制能力、精确 task/revision、剩余预算和已确认退出的旧执行树。普通继任仍要求原 Owner 已完成 Applied。特例仅允许已有入站继任授权的 current bound Owner：从未有任何 control Applied、当前 Owner 与 Host 的精确 PID/birth 均为 typed `IdentityExited`，来源仍存活，固定 executable 与原预留匹配。

该特例先在同一事务写不可变死亡证明与完整计费的相邻 scope handoff intent，随后在 existing-only 文件锁内完成 scope-only CAS、DB Applied 标记，最后原子提交下一代授权、退休旧 Owner 和 Host offline fence。完整文件摘要、原 authority、pending/不确定 start 门禁保留；不能跳过中间 incarnation。恢复复用入站授权的原 ready sequence、attempt 和预算，重放保留首次死亡观察时间，同时重新核验当前权限与进程状态。typed 退出不是原 `Cmd.Wait` 回执，不能用它清理未确认的后代或 OwnerPending；初代 Bind-before-ledger、pre-Bind 丢失原 handle 和 pending 授权后的 origin rebind 仍不在本特例范围内。

初代 Bind-before-ledger 使用独立 schema29 协议，不套用 existing-only 继任路径：只接受当前 inc1、Bound 且从未 Applied、整 run 零 attempt 的精确双死亡。先固定原 Store root/数据库身份、完整有界 task/queue/budget snapshot、不可变文件事实与授权模板，并计入实际 run/global control-byte escrow；prepared proof 持久化后才能发布 canonical zero ledger。发布在原私有目录/锁下采用唯一 stage、精确前缀恢复、file fsync→无覆盖 rename→身份内容复核→directory fsync，拒绝外来节点或身份漂移。随后提交 initial_ready 授权，保留原 ready/queued 与 FIFO；marker 经 never-Applied 继任链传播，首次 signed Applied 与 proof activated 在同一事务完成。未激活时 stop/新预算 revision 受 run fence 约束；重放不换 command/proof/授权身份。root pin 在固定物理连接后、事务前及提交后检查，不能解释为同 UID 恶意 ABA 或断电保证。pre-Bind 丢失原 child handle 仍待处理。

完全空闲定义为无运行/可执行任务、待决策问题、待回调、在途交付、清理到期动作和 UNKNOWN 所有权。空闲 30 秒防抖后退出，启动请求与退出在单实例状态机中对账，不能丢掉退出窗口的新任务。

## 3. 启动账本与全局配额

每次模型 CLI 启动是一个 segment，同一问答续接 attempt 可以有多个 segment。主控任务 retries 才产生新 attempt。重试键与执行键不可混用。

```text
segment_id（协调器生成，不可变）
attempt_id / work_revision / execution_epoch
host_instance / host_process_identity
launch_intent_hash / slot_token
state / cli_process_identity / budget
```

正常顺序：

1. 协调器事务验证计划、依赖和预算，分配持久化 slot token 与 segment 时间额度，写 `launch_requested` 和 outbox。
2. Host 验证命令 epoch/segment/hash，将 `prepared` 持久化。重复请求返回同一条状态，不再次 spawn。
3. Host 创建处于启动屏障后的执行进程，记录操作系统身份。Windows 使用 suspended process + Job；macOS 使用经验证的 launcher 握手。屏障必须先登记才能允许执行者写代码。
4. Host 登记 `spawned`，释放屏障，记录 `running` 并 ACK。释放屏障到记 running 仍有崩溃窗口，因此 spawned 的恢复不能简单重跑。
5. 协调器持久化 ACK。正常进程退出与已拥有执行树完成后，Host 写 `exited` 和实际活跃用时；协调器事务结算预算、释放槽并处理结果。

若系统无法实现可靠的该平台启动屏障，则显式保留 `launch_unknown` 的恢复路径：有可能已经启动就占槽，只查证、不重跑，直到已证实没有旧写入者。lease 失效、缺 PID、没有日志、ACK 超时都不是“尚未启动”的证据。

v0.1 默认用户总槽数 2，工作流配额不超过它。所有 ready segment 使用持久化 FIFO 序号；retry/resume 重新排到队尾，避免一个工作流垄断。全局配额修改不杀现有任务，下调后等待活动数降到新上限才再启动。

等待答案时，执行树存活/UNKNOWN 仍占槽；只在已确认执行树退出后释放。v0.1 不做暂停进程释放槽。后续问答续接要申请新 segment 和槽，不能原地恢复而超额并发。

## 4. 事件 outbox、语义决定与消费

Host 每个事件先写本地 spool 并 flush，再发送。协调器验证 producer/sequence/hash/epoch，事务提交事件及其派生状态后才 ACK；Host 只能删除该 durable ACK 覆盖的前缀。重复事件 ID 相同内容返回旧 ACK，不同内容为协议冲突。普通结构化诊断可以截断，问题/结果/状态变化不能悄悄丢弃。

运输去重键是 `command_id`。业务决定键由协调器维护，例如：

| 动作 | action slot |
|---|---|
| 回答问题 | question_id + question_revision + answer_slot |
| 接受/拒绝候选 | candidate_oid + review_revision + review_slot |
| retry/resume | work_id + work_revision + failure/question_id + next_attempt/segment_slot |
| 派发计划 | run_id + plan_revision + declared_task_slot |
| 最终整合 | repo_id + target_ref + target_base_oid + final_candidate_oid + integration_slot |

Codex 收到回调后只填写现有 slot。协调器在单事务内校验当前 revision、写决定、逐事件处置 ACK 和副作用 outbox。重复回调即使生成新 command UUID，也不能占用同一 slot 两次。改变决定必须提交显式 plan/answer revision；同一工作以不可变 work_id/budget_group_id 关联，失败后改名/拆分继承预算组，不能通过换 task_id 洗掉 attempt 预算。确实新增目标需主控显式提出新合同，不当成旧失败的自动续接。

Question 状态为 `open → answer_committed → resume_queued → resume_started → resolved`，绑定 task revision、attempt 和原 segment；同答案返回旧结果，冲突答案、旧版本和已取消问题拒绝。保存答案不等于已经恢复；续接由 segment 启动账本独立对账。原生权限事件单独分型，不能伪装为技术 question。

每个 attempt/worktree 在协调器和 Host 两侧都只能有一个未确认退出的写 segment，包含 prepared/running/stopping/UNKNOWN；全局还有空槽不意味着该 attempt 可并发。改答案只允许在旧启动 outbox 尚未被发送者领取、未交给 Host 时通过同事务 CAS 替换并废止旧意图。一旦请求可能已发送，就先撤销并取得 Host“未启动且以后拒绝旧请求”的确认；若已 prepared/可能运行/UNKNOWN，则走停止协议，确认旧树退出后才能提交后继决定。新 answer revision 或新 command UUID 不能绕过这一所有权屏障。

已终结/被后继取代的segment晚到事件只补历史、ACK和对应段的时间/进程退出对账，不能推进current_segment、重新开问题或改变当前候选；业务更新必须同时匹配task/attempt revision和current_segment_id。

取消/撤回在事务中推进 run/task revision 并使旧 action slot 失效。旧执行者晚到的结果保存为历史，不重新使取消任务 ready。

## 5. DAG 与输入版本

整张计划提交时事务校验：唯一任务 slot、节点存在、无环、依赖类型和固定 artifact、修改范围冲突、预算、目标版本。任一失败则整次更新拒绝，不部分派发。

依赖分为主控审查认可、或计划明确声明的客观产物条件；进程退出 0 不自动满足。协调器 pin 指定不可变产物，再按 [Git 协议](git-cleanup-protocol.md) 材料化实际执行基线，成功后才 ready。

上游 failed/cancelled/budget_exhausted 使必要的传递下游进入 `blocked_dependency`，独立节点继续。新计划可以明确解除/替换依赖；不能偷偷将下游输入改到另一个 HEAD。

运行中下游使用固定快照；生产者被撤销或替换后标记 `dependency_stale`，旧结果不得自动认可或整合。Codex 可以显式维持原合同或者重做；主控离线时需要语义判断的节点等待。

`cancel task` 只停止该任务及其必需下游的进一步释放，独立任务保留；`cancel run` 撤销所有未来派发/续接/重试/整合入口，并停止现有执行。停止结果未知时保留槽和现场，不用假完成解除占用。

## 6. 时间、停止与分段问答

- 预计耗时仅用于粒度与进展展示。默认诊断静默阈值 5 分钟；存在合同声明的正常静默阶段时使用该阶段预算。诊断只查宿主/进程身份、最后durable事件、字节/活动计数、已知等待原因和可安全读取的退出状态，2 秒预算，不自动调用模型。相同阶段关注事件至少间隔 10 分钟，状态改变才提前报告；诊断不更新 last_progress。
- 每 attempt 活跃执行初始上限 60 分钟、同 work 累计 120 分钟、attempt 最多 3；计划可事前指定其他时间上限。问答 segment 累计，不重新置零。
- 预算权威作用域是 budget_group_id；失败后拆分的子任务共享组内剩余attempt次数和累计active_time，不各获一份新预算。启动事务先预占本次attempt次数（正常问答段不重复占）和segment时间额度，额度不得超过attempt剩余与组内未预占余额。Host只执行获授额度，协调器离线仍按该额度停止；没有额度就等待预算/主控决定，不启动。
- 段确认退出后事务结算实际使用并退还可证明未用额度；UNKNOWN保留预占，无法可靠重建实际时间时保守计满该段额度，不凭租约退回。两段并行也不能共同超出组预算；新合同增加预算必须显式记录revision，不能伪装重试。
- 分别记录 queued_time、active_time、waiting_decision_time。问答若执行树仍运行，则仍是活动时间；只有当前段结束后才进入不计活动预算的等待。
- 使用单调时钟计活动时间；重启后根据持久账本保守恢复。休眠/系统时钟变更需单独用例，不能通过改系统时间延长预算。
- Host 自主执行 hard deadline；协调器离线不取消这个上限。停止先用经过该 CLI/平台验证的温和取消，等待最多 10 秒，再终止已拥有的执行树；仍无法证实退出则 UNKNOWN，不能新开执行者。
- 可预授权的瞬时错误 retry 初始退避为 5 秒、20 秒（最多三次attempt），`next_retry_at` 与尝试预占一起持久化；协调器重启不立即重复或清零退避。无法证明未运行的启动预占不得退回。业务失败/失败后重建上下文/审查返工消耗attempt，不能伪装为正常问答段。
- 当前自动重试仅接受显式 `execution_policy.retry` 授权、可信 Host 证明 `not_started` 的 `probe_start_eagain` / `probe_start_eintr`。Claude/Grok 只读候选审查复用已接受验证产物、完整 dependency vector、配置/provider/index assignment，并绑定原 Host 的 PID/birth/generation 与 owner 身份；相同 Host 跨 Coordinator epoch 重连可继续，Host 被替换后旧决定不能消费。权限拒绝、UNKNOWN、已启动以及 candidate implement/validate/integrate 不进入该自动策略。
- 只读候选审查已经启动并以正数非零退出时，owner 可从原失败 action slot 显式 retry。带产物的此类失败仅接纳 Host 固定的 `candidate_review_invalid` 诊断：必须有同一进程的唯一 Failed/Exited 证据，校验原 spool 中的私有常规文件、精确内容及摘要，并在派发前重新核验原 Host 和冻结输入。其他审查结果或 UNKNOWN 不因此获得重试权限。
- 显式审查 retry 只恢复其从未启动、没有历史决策 binding、所有祖先仍可完成的阻断下游；恢复为 queued 后仍等待原依赖的业务 accept。预算增额本身不恢复下游。配置任务还须在安装锁内验证原有配置 pin，缺失时整次拒绝且不重建；祖先的未处置事件或待执行 retry 会保留阻断后代的 pin，ACK 关闭重试机会后方可按终态规则释放。
- 技术问题以 question ID 提交并结束当前段。重复答案不能重复 segment；答案过时则拒绝。权限问题不走这个代答通道。

macOS process group 不能囊括主动 setsid 的后代；Windows Job 可受嵌套/父 Job 约束。准入测试必须包含孙进程、终端关闭和 Host 崩溃；不支持的逃逸行为须在启动前拒绝相关 adapter/任务能力，检测到未知后台写入者时停止自动清理和恢复。

## 7. IPC、输出与磁盘故障

- 默认使用本机用户作用域 IPC：macOS Unix socket、Windows Named Pipe，限定当前用户访问并校验实例握手。不给远程监听，不需要 MCP。
- 任务辅助命令获得随机 attempt 能力；控制命令拥有工作流控制能力，二者不互通。能力仅本工具内部作用域，不是底层 CLI 账号材料；不放入模型可复制的全局配置。禁止任意路径读取和跨 attempt 事件。
- 单个控制事件上限 64 KiB；大报告写登记的 artifact，通过 ID/hash 引用。Host 流式排空 stdout/stderr，过滤器缓冲有上限；未知原始行默认不落盘。
- worker report 的 question/result/failure 附件≤1MiB，先在事务中验证原 capability/sequence/payload 并持久预占，再在锁外从登记的私有目录有界复制；重新打开并核验原对象、持其写锁，二次验证权限后同事务提交 event/ref/retained bytes 才 ACK。源文件仍归 worker，精确已提交重放不再读取源；复制或提交失败保留原 intake 和预占供原请求恢复。raw bytes 与 control metadata 分账，终态报告可用故障处理预留空间，但仍满足 min-free、SQLite 余量和全部未物化空间承诺；progress 不获得该权限。旧报告保持旧计费且迁移不授予文件权限；历史 legacy_pending、显式删除与完整2GiB验收仍待闭环。
- 每个新 segment（包括 resume）在 ClaimReady 原事务中预占 1MiB terminal/prelaunch 共享槽位和 21×64KiB progress 槽位，共两笔 reservation、2,424,832 字节。提交前连同本事务全部 control 增量复核可用磁盘；任何失败回滚整次派发。`host_artifacts` 固定原 run/task/attempt/segment/producer/command/revision，Pending 只恢复原两笔授权，重连不新增预占；旧 pending 缺少授权时拒绝派发。旧候选摘要可用于已核验的历史证据重建，不因此获得新执行权限。每段另外预占22,528字节槽位 manifest；Host 普通、候选、结构化、Codex、prelaunch 和 progress writer 使用固定不可变物理槽位。Store 在锁外核验原文件、持对象锁二次鉴权，同事务提交 ref/retained/event；terminal 与 Exited 共用一次物理计费，精确重放不依赖源文件。新行按写入时的 verified 标记计费，后到 ref 不改变历史退款。显式删除仍未提供。
- 受管 Host 附件 receipt 总长≤4096字节，绑定≤1024字节 canonical descriptor 与原物理 proof。候选、索引审查及诊断重试读取从受信 state/producer 派生 root，并核对原对象身份；内容相同的替换文件也拒绝。Store 事务内的只读快照不获取 writer lock，不创建/恢复对象或新增所有权。历史无 receipt 的证据仍走兼容读取。
- 显式 detailed 日志使用独立 diagnostics-read/diagnostics-ack，不复用业务 collect/ack。每次 Store 事务核对 run-control、controller、task 及原 attempt/producer/policy/reservation；原执行离线不撤销数据权限。reading fence 后锁外读文件，再核验并持久化完整页面才返回。expected_revision、request ID 和 ACK cursor 共同区分重放与新读；ACK 不改变业务状态或释放产物额度。
- 诊断页 raw payload≤32KiB、持久页面≤60KiB、完整 raw-JSON IPC≤64KiB，外层 request ID 限 ASCII128 字节。rolling gap 随完整 receipt 绑定；永久读取失败形成可重放/ACK 的 unavailable 页且 cursor 不前进，取消或提交失败保留原 reading 请求恢复。后续删除必须在同一事务确认 phase=idle 并 CAS reservation，reading/pending 都阻止删除；当前不提供自动清理或公开删除入口。
- 初始默认：每 attempt 结构化诊断日志 20 MiB，超出滚动截断并标记；每 run 控制 spool 16 MiB，用户控制事件总额度 128 MiB。artifact 初始用户额度 2 GiB，可配置；不把整个 worktree 大小等同于可随意丢弃日志。
- 控制记录优先于诊断日志，初始保留至少 64 MiB 控制/故障处理余量。近满时停止新派发、清理无 pin 且到期材料、削减可丢日志；不能驱逐待回调/恢复产物。
- event/spawn/delete 意图未能 durable 提交即不能 ACK/启动/删除。运行中存储失败时排空输出避免管道卡死，停止新的写入步骤，并通过已有控制余量记录 `storage_blocked` 后终止已拥有执行；恢复后对账。磁盘彻底不可写时不能承诺写下新错误，必须保留原已提交状态并在重启时识别未收尾执行。

数值是第一版明确的实现预算，不是已测性能结论。有限额度不能保证面对无限输出保存全部内容；必须保住状态和已接受关键事件，溢出可观测且拒绝新工作。

## 8. 轻量验收预算

在预先记录的基准机上，使用模拟 CLI 分别测：无任务、离线待回调、1/2 执行者、日志洪峰。排除外部 CLI/模型/原生 Codex 的资源，但包含本工具所有 Host、协调器、Hook 辅助进程。洪峰夹具固定每执行者 stdout/stderr 合计 1 MiB/s、每30秒一个4MiB巨行、独立报告通道每10秒一个不超过4KiB关键事件，持续20分钟；分别测合法schema和未知原始行。

- 完全空闲 30 秒防抖后无本工具后台进程。
- 离线仅待回调持续 5 分钟：本工具总 RSS 不超过 64 MiB，平均 CPU 不超过一个逻辑核的 1%，不周期启动子进程/模型。
- 默认两个模拟执行者运行 10 分钟，并另测来源 Host 上限 4 个：本工具总 RSS 不超过 128 MiB；20 分钟日志洪峰后内存不随已输出总量持续增长。
- 接收问题/完成到 durable 入库目标 2 秒内（本机正常 IO）；存在attached正证据、ownership明确、运输槽可用且无uncertain时，5秒内尝试回传。离线、归属冲突和原生安全边界等待分别计时，不为了SLO向detached会话发送。
- 输出限额、spool 限额、低磁盘和清理重试都分别注入验证，阈值在执行前冻结，不靠测试后调高阈值把失败改成通过。

这些预算属于待验证设计目标；若实际达不到，先改善实现或明确提出预算变更，不宣称“用了 Go 所以轻量”。

未消费的 succession authorization 在来源重绑定时使用 schema30 不可变派生证明：原 command/authorization、死亡观察、原 epoch、FIFO/预算及 initial marker 不变，每条经认证 owner_rebind 形成至多64跳完整唯一链；按 authorization 索引核对完整链与原来源记录。Prepare、Bind、scope handoff 和 bound-never-Applied 检查统一采用有效目标，同时保留原 predecessor/epoch 约束。prepared initial、intended 或当前 control 未 Applied 时拒绝重定向。历史迁移缺链、分叉、已消费目标漂移拒绝且原子回滚；旧 rebind 与新proof均计入真实run/global控制存储。initial root在同物理连接事务前与提交后检查。

公开 rebind 要求旧来源明确Exited、新来源Alive；同一存活来源仅能只读重放精确当前receipt，便于DB已提交而bootstrap文件写失败后的重试。未知/复用PID不授权新变更。原receipt不因派生重写。DB回复与后续bootstrap rename仍是两个提交域，不能宣称并发多generation文件发布线性化。运行时schema迁移COMMIT失败必须在原物理连接尝试ROLLBACK后无条件discard，不把sql.Tx的ErrTxDone当作已完成物理回滚。
