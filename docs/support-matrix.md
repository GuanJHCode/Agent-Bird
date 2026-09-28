# 支持矩阵

**多 Provider 本机增量（2026-09-17）：** `0.1.0-multi-provider.1` 增加 AGY/Grok 选择入口。AGY 1.2.5 在当前 Codex owner 下经正式安装、高层 task run、随机文件读取、哈希校验、ACK/accept 后 completed；仅支持只读分析。Grok 1.0.30 已登录，隔离下创建会话因 FS_PERMISSION_DENIED 失败，typed 执行继续拒绝；旧未登录记录已不是当前状态。公开 preview.1 尚不包含增量，见[本轮交付](../tasks/mvp-multi-provider-entry/docs/delivery.md)。

**公开下载预览（2026-09-17）：** `v0.1.0-preview.1` 提供 macOS arm64 预编译插件，运行逻辑对应已验证的中断反馈修复版。后续新 Codex CLI 会话已自然派发真实 Claude，记录进度并收到退出码0的完整报告；主脑因部分比较结论证据不足拒绝原稿、完成ACK并自行修正交付，任务保持 failed、0 active。这证明自然触发、真实进度和验收拒绝收尾，不证明真实超时分支或 Claude 返工闭环。

**中断反馈修复（2026-09-17）：** 本机已安装 `0.1.0-interruption.1`，新增明确停止原因、有界未完成进度，以及不支持续接时的明确拒绝。全量533项与Host/store及CLI定向race通过，独立审查通过；无活跃段时正常升级共享服务，原中断任务身份与revision保持。真实模型超时、新版本新线程自然触发和长时稳定性尚未复验，见[交付及边界](../tasks/mvp-interruption-feedback/docs/delivery.md)。

**简化入口最新验证：** macOS arm64 原生插件 `0.1.0-task-entry.1` 已更新。当前 Codex 通过高层 `task run` 自动绑定，使用 `/tmp` 别名目录真实启动 Claude 2.1.274，结果验收/ACK 后 completed、0 active；重复句柄提交拒绝。原任务句柄返工/复审/集成已通过协议夹具，尚未重验新版完整真实开发返工与新会话自然触发。参见[交付](../tasks/mvp-task-entry/docs/delivery.md)。

**第一阶段在 `.13` 第十轮真实通过。** 主脑加载Skill、自主提交DAG并作出4次owner accept，4任务completed、4个result delivery全部durable ACK；实际测试、独立真实审查及Host集成绑定同候选 `0e32a9aa680d184c5d9b6c4391d9af621b31cf68`，目标clean。主脑自然结束后核验身份正常退出；四段预算均按未知耗时保守计满。本轮无retry/rework，不提升第二阶段或原session续接状态。`.10`隐私失败、`.11`预算缺陷和旧超时证据均保留。见[真实记录](../tasks/p0-feedback-loop/docs/autonomous-main-trial/report.md)。

**最新检查点（2026-09-17，第十三轮）：** 真实新主脑显式接管原 run并恢复原`.13` Host；新增bool规格后owner reject/rework，真实Worker产生C2 `70cd7453e3c395c16a35a787e77502f52cf13fcd`，Host重测、独立复审approve、主脑验收、Host同候选集成全部完成。四任务completed/revision2，四份结果ACK，target clean，最终22测试通过。本轮有两次根线程协议/命令范围纠正，计有人看护的完整反馈链，不计完全无人指导自主闭环。[最终交付与边界](../tasks/mvp-host-explicit-recovery/docs/delivery.md)。

核验环境：2026-09-16，macOS arm64 / Apple M3 Pro、Go 1.26.8、Python 3.14。结论只覆盖注明的层次。

**第二阶段历史：第十一轮未完成。** `.13` 真实主脑已自行提交四任务 DAG，但实现 Worker failed/exited（API error，具体原因未定），未产出候选交付；主 CLI 随后明确报告 `403 IP access denied by API-Key restriction`。两者不能自动归为同因。目标保持原基线且 clean，下游未运行，无 accept/ACK；预算 settled 600000ms，确认无 active/unknown 后正常关闭主 CLI。第一阶段历史成功仍有效，当前服务可访问性另行受阻。见[本轮交付与恢复边界](../tasks/mvp-stage2-real-feedback/docs/delivery.md)。

本轮[定向普通与 race 回归](../tasks/mvp-stage2-real-feedback/docs/stage2-regressions-report.md)通过，只提升自动化证据。新增[需求确认场景](../tasks/mvp-stage2-owner-refinement/docs/acceptance.md)第十三轮已完成新增规格后的有人看护返工链；“新增规格后 owner 拒绝并返工”和“独立 reviewer 发现原需求缺陷后返工”分列，前者已完成但有两次根线程纠正，后者仍未真实验收。真实进程的有限资源采样不等于长时稳定；根线程观察器的凭据暴露另记于[观察边界](../tasks/mvp-stage2-real-feedback/docs/observation-boundaries.md)，不归因于被测主脑。

**2026-09-17 恢复后重验：第十二轮仍受外部访问限制阻塞。** 用户确认恢复后，用同一 `.13` 包和原 Provider pin 新建 `clamp-owner-refinement` 试验。主脑正常响应、加载 Skill、自行提交 DAG，Worker 实际启动；约 09:33（北京时间），Worker 登记产物和 main 各自明确出现 `403 IP access denied by API-Key restriction`。Worker failed/exited、exit1，无候选交付；目标保持 base/clean，预算 settled 600000ms，无 accept/ACK/rework、无 active/unknown，main 正常退出0。不是600秒 deadline 停止，不归因于原生审批拒绝，也不算原 session 恢复或真实返工通过。见[第十二轮记录](../tasks/p0-feedback-loop/data/autonomous-main-trial/trial-12.json)。新观察器7项纯内存脱敏回归通过，不改变产品运行逻辑。

| 功能 | 状态与证据 | 边界 |
|---|---|---|
| Go 调度、SQLite、DAG、summary | 自动化测试；IPC 连接后取消、半帧及 readiness 错误有回归 | 无第二套任务数据库或队列；不等同真实 Provider 故障恢复 |
| Host bootstrap / HostHello 身份 | `.11`起v2元数据无token、私有control+限时限长FD传输，服务端在写DB前验证真实peer/executable/live owner；Unixsocket与race回归、`.13`真实Host链通过 | v1只读启动兼容、legacy rebind明确拒绝；不能阻止同UID任意control读取，不把成功链当全部攻击边界验证 |
| 流式权限拒绝诊断 | 修复SDK message:string漏计；`.13`真实implement捕获1次Write/classifier流式拒绝，固定字段接线有效 | 仅截断capture观察值，原因/范围未完整确证；不算完整原生审批拒绝或越界验收 |
| 超时协议诊断 | `.13`固定快照、TryLock和停止顺序回归通过；真实implement/review均after_wait terminal_seen/wait_complete真、protocol_error=none | 正常退出接线真实通过；超时分支目前fixture验证，旧超时根因未定，group退出不等于EOF |
| 本机 owner / collect / ACK / accept | 第十轮真实主脑4次直接accept和4份请求ACK均持久成功，4result全部acked | 重复请求幂等仍以专门回归证据为准，本轮未执行重复owner决定 |
| 真实主脑自主派工 | 第十轮 `.13` 真实主会话自主生成并提交DAG，Worker真实编辑/冻结，实际测试和独立真实review approve，Host同候选集成；4任务completed、4accept、4ACK、自然结束 | 本机macOS/Claude第三方默认模型/collect/小型明确文件范围；无脚本代业务决定，正常原生审批仍逐项确认 |
| reviewer 写入隔离 | 实际 macOS sandbox + shell/Git 测试 | 限制文件写入；保留既有 Provider 读取/认证环境，并非全系统读取白名单 |
| Claude Code 2.1.267 reviewer | 真实 Haiku 读取随机文件、产物哈希、收集与验收通过 | 仅覆盖小型读取任务；不是复杂代码审查验收 |
| Claude implementer + 候选冻结 | 真实 Claude 2.1.267 Haiku 及 2.1.272 第三方默认模型在 Host 创建的独立 worktree 编辑；Host 冻结绑定任务/版本的新增、修改、删除，fixture 验证保护原工作区 | 真实模型仍为小函数修改；原生审批拒绝/问答、复杂开发和续接尚未完整验收 |
| 同候选测试、独立审查与集成 | 第十轮四份Host receipt同candidate/tree，独立review第一attempt约182.297s approve，目标同OID且clean；main及observer各自5tests/OK | 真实成功路径通过；测试失败、审查拒绝、目标漂移、旧事件等负向边界仍主要为fixture，不提升第二阶段 |
| Claude 第三方默认模型开发链路 | 原生结构化输出修复后，真实编辑→冻结→实际测试→独立真实审查→同候选集成通过（145.39s） | owner 决策由 Go harness 执行；未改地址/凭据。此前超时原因未全部确定，不能据一次通过认定长时稳定 |
| Worker 非递归派工 | `.10` typed Claude 固定原生 Agent/Task deny及能力缺失拒绝，[Adapter/CLI race](../tasks/p0-feedback-loop/docs/adapter-cli-race-final-10.log)通过 | 第七轮旧 `.9` 诊断有 Agent 尝试但不能确定执行；新guard真实效果待验收，不宣称任意shell派工均被封闭 |
| ACK 后验收绑定恢复 | summary.reviewable_result + accept --request；第九轮4次真实主脑接受均使用JSON请求，最终integration完成 | 仍要求读取真实artifact与显式业务决定；不自动批准，不等于旧事件可接受新版本 |
| 实现技术中断重试 | 第七轮 `.9` 实现300秒stopped、修改未冻结，下游未执行；implement retry当前不支持 | 不是审查retry或反馈rework；不得重置预算、重跑原prompt冒充续接；保留原run和未冻结修改 |
| 未测量耗时预算结算 | `.12`起缺失/零值保守扣满；第十轮四段真实退出均settled、grant/used各600000，漏扣修复已真实接线 | 当前Host未准确上报耗时，成功/失败/早停均保守收费；旧账本不追改，本轮无重试，真实重试后的累计验收仍待补 |
| 审查技术中断显式重试 | 第九轮主脑自主对同一只读review做两次显式retry，第3个真实attempt返回approve并被接受、集成 | 新attempt不是原session续接或反馈返工；`.11`原计费有缺陷，不能把本轮记为完整预算恢复通过；单任务组/无结论/已退出等guard保留 |
| 带反馈返工 | Trial13新增bool规格后真实owner reject/rework→C2→Host重测→独立复审→同候选集成，四任务revision2 completed | 有两次根线程纠正，仅计有人看护完整链；原规格reviewer主动拒绝、完全无人指导仍未验收；仅四任务线性链，非原session resume |
| 静止run显式Host恢复 | `0.1.0-host-recovery.1`普通/race、原`.13`跨版本、独立审查通过；Trial13新main实际bind/rebind/recover成功 | 原Host已退出、全部segment已退出等guard；一次性startup intent，uncertain不盲重试，不重置预算；实际累计预算未在本轮重数 |
| 候选 Git 安全边界 | Git 子进程身份/退出确认、取消及 unknown 保留；Git 扩展、越界文件、符号链接和资源溢出拒绝回归 | 仅支持没有可执行 hooks/filter/fsmonitor/signing 等扩展的小型仓库；失败工作区和 journal 保留，不能直接重跑续接 |
| 新 profile 的 session resume | **拒绝** `profile_resume_not_verified` | 实际 Claude 续接试验得到 exit 1 / `process_tree_unknown`，未声称可用 |
| Claude 旧 stream-json 协议 | 解析/会话/恢复 fixture 与 CLI 框架测试 | 旧配置不自动获得新角色隔离能力 |
| Codex 0.154.0 协议 | 版本 fixture、结果完整性测试 | 生产 guard 保留；新 profile 和真实任务启动仍拒绝 |
| AGY 1.2.5 reviewer（1.2.3 历史已验证） | 新 Profile；1.2.5 默认模型通过高层 task run / view_file 随机文件读取、哈希核验、ACK/accept；1.2.3 Gemini 3.8 Flash Low 底层验收保留 | 未开放 implementer/session；泛化任务选用 `run_command` 的路径出现失败/超时，尚未定因 |
| AGY / Grok 旧协议 | 固定参数、解析与 session fixture | 不代表新角色隔离或真实续接可用 |
| Grok 1.0.30 | 当前已登录，模型列表可见；只读 sandbox 内创建会话返回 `FS_PERMISSION_DENIED`；新 Profile 仍拒绝 | help 与旧适配参数存在差异，真实模型执行及会话隔离尚未验收；未登录仅为历史记录 |
| 原生 Codex 回注 | 既有 Python 接入、owner 和 history proof 保留 | 本轮没有真实 Codex TUI 回注验收；通用调度不依赖它 |
| Python 正式 runtime | 独立导入、包装路径和真实本机 service fixture 测试 | 安装目录仍用 `runtime/g0`，以兼容既有 pin |
| 全局 / Provider / 工作区并发限制 | SQLite 调度测试 | 默认 2，unknown 保留资源；配置下次协调器启动生效 |
| 输出洪峰与资源观测 | 合成 OS 子进程实测 600 秒、stdout 5,999.5 MiB；capture 1 MiB、父 FD 8 / 子 FD 4，退出树确认通过 | 仅单进程采样，未覆盖真实 Provider、生产 IPC、多 Worker 或长期 SLO；[记录](../tasks/p0-feedback-loop/data/endurance-trial/docs/endurance-report.md) |
| 受控重开 / spool ACK | 已退出后重开 Host/SQLite，5 个事件一致，spool ACK 重复幂等，显式新 segment 成功；quota 另有本机基准 | 旧 Host.Ack 重复返回 conflict；不是原 session、活进程故障恢复、生产 quota 或历史清理验收 |
| 本机私有安装与复建 | `.13`固定包install/双doctor、重复版本保护通过；167文件源码快照独立rebuild10及双doctor通过，原`.13`已完成第十轮真实主脑闭环 | `.rebuild10`自身只验证构建安装，不自动继承模型验收；未替换用户安装或改Provider锁；[试用说明](../tasks/mvp-local-trial/docs/claude-collect-trial.md) |
| GitHub Actions | main `6f5827d` 的 [run `34942483490`](https://github.com/GuanJHCode/Codex-Bird-Orchestrator/actions/runs/34942483490) 成功 | 旧失败结论已过期；本轮候选冻结改动仅有本地验证，尚无对应远端 CI |
| Linux / Windows 生产执行 | 未支持 | native process / sandbox / peer 身份边界明确拒绝 |

本地`.11`自动化验证：[Go全量普通](../tasks/p0-feedback-loop/docs/go-test-final-11.log)、[全量race](../tasks/p0-feedback-loop/docs/go-race-final-11.log)、[vet](../tasks/p0-feedback-loop/docs/go-vet-final-11.log)、[build](../tasks/p0-feedback-loop/docs/build-final-11.log)均exit0；[私有安装/双doctor](../tasks/mvp-local-trial/data/private-install-11/data/verification.json)和[独立rebuild8](../tasks/mvp-local-trial/data/rebuild-verification-11.json)通过。166文件源码快照可复建；安装/自动化通过不代表真实主脑闭环通过。旧版本测试与失败证据保留。

最新主流程证据见 [多 CLI 验收](../tasks/multi-cli-mainflow/docs/real-mainflow-07.json)，此前完整记录见 [运行时交付](../tasks/provider-runtime-completion/docs/delivery.md)。绿色 fixture 测试不提升矩阵中未完成的真实验收项。

本轮候选冻结的实际测试及剩余项见 [P0 增量记录](../tasks/p0-implementation/docs/delivery.md)。它不代表首个自主开发闭环里程碑完成。

后续同候选测试、独立审查及集成的前轮记录见 [P0 第二步](../tasks/p0-candidate-delivery/docs/delivery.md)。本轮结构化审查和反馈返工的最新结果见 [反馈闭环记录](../tasks/p0-feedback-loop/docs/delivery.md)；主脑自主闭环需单独验收。

`.13`增量当前验证：[全量普通](../tasks/p0-feedback-loop/docs/go-test-final-13.log)、[Host全套race](../tasks/p0-feedback-loop/docs/host-race-final-13.log)、[vet](../tasks/p0-feedback-loop/docs/go-vet-final-13.log)均exit0；预算专项[Store race](../tasks/mvp-active-budget-accounting/docs/delivery.md)通过。第十轮main与observer分别复跑5tests，observer记录为[trial-10-target-tests](../tasks/p0-feedback-loop/docs/trial-10-target-tests.log)。
