# 支持范围

以下区分源码能力与真实运行证据。2026-10-06 迭代的原生结果绑定各自固定源码，详见[迭代验证范围](iteration-20261006.md)；它们不证明所有整合后场景、已安装版本或未列出的 Provider 组合。

| 功能 | 状态 |
|---|---|
| Codex owner 绑定、任务派发、collect/ACK/accept | 已实现 |
| 多任务、依赖 DAG、默认全局并发 2 | 已实现 |
| 按会话开启/关闭 Provider | 已实现；关闭等待执行树退出确认 |
| 代码修改的强制隔离与候选测试/审查/集成 | 三种 worker 共用托管流程；Claude/AGY 支持原生结构化审查；Grok 支持有界完整快照及显式分块审查 |
| Claude/Grok/AGY 主脑 | 显式 controller 启动入口；按托管进程归属，不冒充原生会话 ID |
| 会话模型、保存默认、单次覆盖 | 已实现；由实际 CLI 能力决定可用模型 |
| Grok | 1.0.34 只读及逐文件许可下的隔离编码/冻结/行为验收通过；需显式任务会话目录写授权；完整文本快照审查通过一次真实错误识别 |
| AGY | 1.2.16 已验证同会话两次 Question/Answer、三段结算、SQLite 历史封存、限定文件内容访问及隔离候选开发链；编辑工具轨迹、完整公共写任务和中断续写仍未验收 |
| Claude | 2.1.285 已验证同会话两次 Question/Answer、三段结算、限定原生 Read 及隔离候选开发链；编辑工具轨迹和完整故障恢复组合仍未验收 |
| 技术问答与原会话续接 | 已有绑定原会话、版本和累计预算的 typed continuation；AGY/Claude 的两轮问答已有上述原生证据。显式中断 Resume 的实现与测试不等于所有写任务的真实续写通过 |
| Codex Worker | 内部协议、状态封存及同线程两轮 Question 已有范围证据；完整第三段 Read 未完成，公共任务入口仍关闭 |
| 活 Host 重连 | 已实现 Coordinator 重启后的有界重连及原 spool 重放；不等于 Host 死亡后可以接管任意存活执行进程 |
| 原 Owner／Host 死亡后的恢复 | [公开恢复验收](../tasks/owner-bound-abandonment/docs/final-validation.md)覆盖 macOS arm64／Docker Linux deadBoth、successor Bound 后从未 Applied 即死亡、相邻 scope handoff 与 inc3 同 attempt／队列／预算恢复；[初始 Bind 后无账本恢复](../tasks/initial-owner-recovery/docs/final-validation.md)已通过 Mac ordinary/race 和 Docker Linux，保持原 FIFO/预算并完成公开 inc2；[pending authorization 来源重绑定](../tasks/owner-authorization-rebind/docs/final-validation.md)已通过 Mac Store/public 普通与race及Docker Linux，保留原授权并覆盖DB已提交后bootstrap写失败的同源重放；[bootstrap发布lease](../tasks/bootstrap-publication-lease/docs/final-validation.md)已通过Mac普通/race和Docker Linux，串行同candidate协作writer并拒绝目录/文件替换；显式 `submit --owner-launch-protocol 2` 的 [Coordinator原Wait监督启动](../tasks/owner-launch-supervisor/docs/final-validation.md)已在Mac race与Docker Linux通过：FirstHost在payload前死亡后可凭原Wait同OwnerID/incarnation重试一次，最终Supervisor也丢失时保持UNKNOWN；旧v1 intended不补造proof；[server-owned publication](../tasks/owner-publication-verification/docs/final-validation.md) 已在Mac race和Docker Linux闭合direct IPC串行发布、schema35 immutable journal、稳定stage真实SIGKILL修复与pending事务门禁；历史schema34/35两向协议明确前置拒绝，不代表成功跨版本恢复，物理断电仍未验 |
| 安装与保存配置恢复 | 已实现不可变 generation、原 Store/owner 绑定、安装重放及未准入 preparation 的显式 abandon；[配置发布崩溃验收](../tasks/config-generation-crash/docs/integrated-validation.md)覆盖 Mac arm64 / Docker Linux 四阶段真实 SIGKILL、精确重放、异主/身份漂移拒绝和损坏目录不重建。[supervised 公共配置入口](../tasks/supervised-config-entry/docs/final-validation.md)在 Mac arm64 受控 CLI 通过实际 model 参数、collect/accept 和原 Wait；[离线待回调资源验收](../tasks/resource-offline/docs/final-validation.md)已在同一 Mac arm64 候选通过 300 秒、35.08 MiB peak RSS、0.48% 单核 CPU 与无周期 fork；[idle30 验收](../tasks/resource-idle/docs/final-validation.md)已通过公开 ACK 后 30.05 秒自然退出；[60 分钟资源组合](../tasks/resource-soak/docs/formal-tail-validation.md)完成采样但 RSS 窗口波动超限，仍为失败；[四 Host 验收](../tasks/resource-host-capacity/docs/final-validation.md)已在 schema29 候选通过 30 秒、119.30 MiB 与第五提交拒绝；其他平台仍待验收；各资源证据绑定各自候选，不代表最新候选资源全项通过 |
| 自动重试与预算策略 | 已实现显式重试、累计硬预算及启动前 probe EAGAIN/EINTR 的有限预授权重试，含 Claude/Grok 只读候选审查（普通与分块）；审查重试固定原 Host 身份与候选输入。[限定validate未启动重试](../tasks/validation-start-verification/docs/final-validation.md)已在Mac arm64通过真实Host/Git/Store链：显式有限policy、AutoDirectory与固定native tool，只有原Start的EAGAIN/EINTR typed proof才可结算并创建fresh attempt；32MiB工具预约和8192byte保留元数据独立计费，UNKNOWN及旧physical Host的未认证spool不能退款或重试。预算 revision 增额已完成本地受控 CLI 验收；已启动validate和其他有副作用阶段自动重试仍未完成 |
| 详细诊断日志 | 已实现显式 detailed 授权、普通 Host / managed Codex 捕获，以及独立 read/ACK；[公开读取验收](../tasks/diagnostic-public-read-ack/docs/validation.md)覆盖 Mac IPC/CLI 和 Docker Linux core/Store。[显式诊断删除](../tasks/diagnostics-delete/docs/final-validation.md)提供已完成最新 attempt 的持久意图、部分删除恢复、物理确认后释放预留额度与历史 DAG proof；Mac arm64 / Docker Linux 定向通过。[Linux 有限文件系统 ENOSPC](../tasks/diagnostics-enospc/docs/final-validation.md)已覆盖诊断三阶段发布恢复、Host 原进程退出及无法持久终态时保持 UNKNOWN；不代表整盘或断电恢复。[显式 diagnostics 保留期](../tasks/diagnostics-retention-verification/docs/final-validation.md)提供 run-control plan→保存JSON→apply→exact replay，以全部可信 Exited 的最大 Store 接收时间筛选最新已完成 attempt；Mac 定向 race 12/64、Linux Store 8/58、build/vet与独审通过。自动 GC、其他 artifact/host_progress/report_events 保留期、完整原生洪峰/RSS及物理断电验收仍未完成 |
| worker report 附件 | 新 report 已接入先预占、私有有界复制、原对象复验与事务提交，支持删源后精确重放；[附件接管验收](../tasks/managed-artifact-intake/docs/validation.md)覆盖 Mac 公开 CLI/Store 与 Docker Linux。Host写入点已接入受管槽位，历史逻辑声明已完成[schema22计费迁移](../tasks/legacy-artifact-accounting/docs/validation.md)，仅记账且不产生文件权限；显式删除和完整资源故障矩阵仍未完成 |
| Host 附件槽位 | 已接入每段派发前预占、严格重放和新旧候选摘要隔离；[槽位授权验收](../tasks/host-artifact-slots/docs/validation.md)。物理writer与验证ref已接入，[Host写入验收](../tasks/host-artifact-writers/docs/validation.md)及[schema22历史计费验收](../tasks/legacy-artifact-accounting/docs/validation.md)已有Mac/Linux证据；显式删除与完整物理故障矩阵仍未完成 |
| 候选 DAG 多输入证据 | 已实现显式 v1 全依赖 capture、精确重放/审查重试、Host 前置校验与 quota 回滚；内容仍由单一 source_task 选择，见 [验收范围](../tasks/dag-inputs/docs/delivery.md) |
| 有序候选组合 | 显式 v2 compose 捕获 2..63 个已接受 producer，保留声明顺序与每源身份；受管 Git worker、冲突/UNKNOWN、候选锁等待及 v1 validate/review/integrate provenance 已在 macOS arm64 受控公开 CLI 通过。[Claude+AGY 受控跨 Provider Host/Git 组合](../tasks/compose-cross-provider/docs/final-validation.md)已通过，保留独立源锁和 reviewer 锁；Linux Store 通过，缓存容器无 Git，Git/Host 未验；真实跨 Provider 模型、Grok managed completion 仍未完成；整合后 new-base 见下一项，见 [验收与限制](../tasks/ordered-candidate-compose/docs/final-validation.md) |
| 整合后新基线返工 | 通过 `submit --source-control-file` 从已接受并结算的集成事件派生一个独立新 run；原历史/预算保留，新候选与 compose 绑定 lineage，目标从 queued 起持久预约，UNKNOWN 不释放。Mac arm64 三代公开 CLI/Host/真实 Git 和受控 Claude/AGY、实际 runtime race 已通过；限定 AutoDirectory 和受支持 DAG 形状，Linux/真实模型未验，见 [用法与验收](../tasks/new-base-rework/docs/final-validation.md) |
| 分支 DAG 返工 | 已实现显式 v1 单 producer dependent closure、原输入/边界证明、固定预算及目标预约；v0 保持 Claude 四步线性链。Claude／AGY组合已做 Store 协议验证，真实模型组合与多 producer联合返工仍未闭环，见 [范围与证据](../tasks/dag-rework/docs/delivery.md) |

Grok 只读内置工具限制为 read_file/list_dir/grep；编码增加文件编辑工具，禁用原生子任务和 web search，并传递 MCP 拒绝规则；真实 MCP 负向拒绝尚未完成原生验收。其 artifact 包含公开 assistant 文本，可能含过程说明，不能把最终回答匹配当成整个 artifact 精确匹配。

本仓库的测试使用合成 fixture 和本地受控子进程。多 Provider 的真实模型并发、任意版本/模型组合和无监督长任务不由这些测试证明。真实 Provider 测试默认 opt-in，运行会消耗对应账号额度。

Claude/AGY Hook 适配、Codex attached bridge 的源码和受控测试不等于真实下一轮或原生 TUI 消费已验收。Grok 显式 ACP controller 已完成[公开受控 actor 同会话回注验收](../tasks/acp-public-controller/docs/validation.md)，包括原会话下一次 Prompt、不可变 completion、ACK 丢失与 uncertain 门禁；真实 Grok/model/tools 资格和任意已有 TUI 附着不由此证明。显式 collect 仍只证明主动收集。完整崩溃/重放窗口、60 分钟双 Worker 组合及 Intel Mac/Linux/Windows 完整原生支持仍未完成。Linux arm64 已在本机隔离容器验证报告文件准入、进程身份/祖先/executable/组观察、Unix socket 凭证及 Coordinator 本地 owner/worker 控制边界；补充原生验证已覆盖组长退出后存活成员，以及未回收僵尸的 UNKNOWN 边界（原 Wait/自然 pipe EOF），见 [进程组证据](../tasks/linux-group-native/docs/native.md)。Linux `collect-only-hooks` 的 `plugin-run -- check` 已完成容器内真实打包/私有安装、Doctor及重复安装验收，见 [Linux 安装范围](../tasks/linux-portable-check/docs/acceptance.md)。其他portable action、Provider执行和sandbox仍关闭；进程/IPC范围见 [Linux 验收范围](../tasks/linux-identity/docs/coordinator-native.md)。

候选开发拒绝 Git LFS/filter、自定义 hooks 等执行扩展。[materialize worker 路径验收](../tasks/materialize-worker-isolation/docs/final-validation.md)已关闭绕过受管 Git 的入口，Mac 定向普通/race 验证扩展拒绝和原进程 journal；[原生隔离前置探针](../tasks/filter-sandbox-probe/docs/final-validation.md)已验证固定本地actor的可逆stdin/stdout转换及越界内容访问、fork、网络拒绝，尚未接入公共filter契约。[最终Git hook/中断验收](../tasks/final-git-extension-native/docs/final-validation.md)已用真实post-merge hook验证tracked写入后的uncertain判定及已启动merge的原Stop/Wait/tree；config/hook/tool pin、候选LFS/filter完整视图传播和签名仍未实现。权限不足或输入不明确时明确阻塞，不自动修改用户全局 Git 或 Provider 配置。

Grok 候选审查由 Host 提供完整 base/candidate Git 文本树，限制为 64 个文件、合计 128 KiB 内容、256 KiB JSON；超限、二进制、符号链接或 LFS 会阻止调用，不截断后继续。认证预检仅调用已锁定 CLI 的非生成 `models`，允许原生客户端续期登录；worker 沙箱不变。AGY 真实审查通过文件读取和错误识别；Grok 真实审查通过快照输入和错误识别。完整 Host 审查到集成另有受控进程回归，不能冒充所有真实模型的端到端验收。

显式 `indexed_candidate_review_v1` 分块路径支持更大的不可变候选，容量、DAG字段与配置选择见[Provider运行时](provider-runtime.md#大候选分块审查)。本机受控Provider已验证65个以上文件、三分块实际prompt消费、Store逐阶段接受、Source Host恢复及最终Git合并；真实模型分块效果尚未验收。

当前macOS arm64本地候选包 `0.1.0-closure.e662aaa0e2a9` 已完成[同源码双构建/双打包与私有安装管理验收](../tasks/closure-package-provenance/docs/owner-grant-current-20261010.md)：12条实际命令覆盖install/replay/Doctor/uninstall，28个包文件与24项source closure核对通过。包固定本机Python路径/hash；没有替换用户安装或注册Hook，不代表跨机器安装、真实TUI/Provider或Formal60资源资格。该包证据只绑定其source，不自动覆盖后续生产修改。
