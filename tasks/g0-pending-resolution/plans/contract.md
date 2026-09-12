# 解决 G0 剩余回传、附着与交付对账

日期：2026-09-11；起始基线 `cd3d054`。用户明确要求“解决待处理项，主会话负责编排，subagent 落地”。目标是补齐现有能力缺口，不能用新增报告或本地夹具通过替代原生验收。

## 既定约束

- 普通 `codex` 入口，无替代启动器、Codex 本体修改、MCP 派发或回调、键盘伪造结果和聊天历史修改。
- 沿用正常账号、权限和原生交互；本轮 macOS 优先，Windows 延期。仅操作明确归属本任务的测试会话与进程。
- 退出停止、保留记录和检查点，恢复原 thread 后明确继续；打开会话不自动启动执行。[已验收生命周期](../../g0-retained-resume/plans/contract.md) 不被重置。
- 在线真正 final→idle 的自动结果回传仍需成立；不能把活动 turn 内等待、Goal 连续续轮或下次用户输入后消费算作通过。
- 原七份设计文档保持历史基线。G0 原生准入和后续功能顺序保持，完整 G0 未通过不实施 G1–G4。

## 分工与推进

三个 subagent 先并行核查独立的能力边界，并提出可以实际验证的候选。各自使用独立 worktree，不同时改同一工作树。主 Agent 核对合同与交叉依赖、协调原生实验顺序、串行整合；未参与该产物的 reviewer 独立复核。

| 责任 | Agent / 分支 | 本轮产出 |
|---|---|---|
| 真正 idle 回传 | native_return_runner / exp/g0-pending-idle | 受支持通道和固定版本调用链；有新候选时落地有界原生采集与实验 |
| 当前 TUI→thread 附着 | native_identity_review / exp/g0-pending-attachment | 可达原服务、当前显示身份、切换及断连失效依据；可行候选的最小验证 |
| 交付对账 | retained_host_impl / exp/g0-pending-delivery | 真实工具输出与三态、分页、compact、部分 ACK、六个故障屏障的可实现接口及缺口；适配与测试候选 |
| 计划、组合、最终验收 | 主 Agent / main | 固定输入、接入前提、实际验证和清理；不代替 subagent 实现 |

上述初轮研究与离线实现已冻结整合。原生资格实验阶段，`retained_host_impl` 在 `exp/g0-pending-standalone` 实现限定 RPC helper，`native_return_runner` 在 `exp/g0-pending-native` 实现采集器、专用 PTY 和串行 controller；两者不共用写入 worktree。`retained_lifecycle_review` 与 `native_identity_review` 分别只读复核固定 helper 和 controller 提交，主会话在两项完整实现通过复核后才编排原生运行。

每条路线先复用已存证据，核查具体新增能力或遗漏调用链；没有新前提时不盲跑旧版本矩阵或重复失败的模型实验。候选不成立即转下一个仍符合合同的通道；静态证据不能标为原生通过。

主 Agent 已准许交付路线先实现一个只读、有界的离线 G0 对账检查器：核对给定历史中的精确标记、分页信息和逐事件 ACK，缺少可靠否定证据时保持 uncertain。该工具不发送网络、不验证原服务身份、不写业务事务，不计作完整 C11 或 G0 通过。

## 固定当前会话的 standalone 原生资格实验

沿用此前已授权并已实测的本任务共享 listener 环境，验证原接口本身，不等待通用 UI 观察器实现：

- controller 仅创建和操作自己的服务、TUI 与专用 PTY；普通 `codex` 连接该已存在服务。默认 socket 已被占用时拒绝启动或替换。出现其他客户端时不停止其服务或进程。
- 通过确切原生 `/status`、已知 PTY、进程创建身份、真实 Hook 及原生连接元数据交叉核对同一服务、TUI 和 root。实验期间 controller 独占自己的输入端，固定显示 root，不执行切屏；该受控事实不作为产品通用附着算法。
- root 真正 final 后，客户端完成固定无业务副作用的合成工具，先持久保存意图与结果，再仅提交一次 `turn/start` 的真实 `toolOutput`，`input=[]`。不用 Goal、MCP、任意 history 注入或用户文本承载结果。
- 再以原服务的实际历史接口读取精确 `functionCallOutput` 及 envelope，区分新轮、模型文字、持久历史与逐事件业务 ACK。不因请求响应返回成功就声称已进入历史；uncertain 不自动重试发送。
- 不代理 TUI 通信，不修改账号、权限、全局配置或 Apps 开关。限定 RPC helper 与原生 controller 分属独立 worktree；`send-once` 会启动模型轮次并写入历史，工具冻结并复核后才运行。
- 先做一个资格样本；通过后再安排独立基础重复或必要故障窗口，沿用 10 秒本地、120 秒模型窗口。预建 listener 不计作冷启动自动接入；固定当前 UI 的样本不计作一般切换身份或完整 G0 通过。G1–G4 保持未启动。

### 2026-09-12：首样本与下一次前瞻性测时

`sd-qual-01` 在 initial 阶段结束为未通过，未进入 helper/standalone。输入至退出的墙钟增量为 1036.050467 秒，Python monotonic 增量为 14.060052 秒；异常记录仅有 `ValueError`，无法唯一定位分支。保留 `infrastructure_inconclusive` 及诊断不足，不据此否定回传接口，也不从 PTY 写入成功推定模型已接收提示。原记录、原时间窗口和 case 目录均不复用。

准许准备唯一新 case `sd-qual-02`，仍须修改冻结、独立复核及新鲜现场检查后才运行。使用本机已核实包含休眠的 `CLOCK_MONOTONIC_RAW`，helper/controller 采用同一 clock identity、同一 boot 和各阶段原始起点；本地仍为 10 秒，初始输入和唯一发送分别仍为 120 秒。旧 clock identity 的记录不得换算成新期限，超窗响应不能标成功，不能因此重发旧事件。墙钟仅提供日期及异常诊断，不重置运行预算。

新增固定异常子阶段、错误码和时钟读数，以区分预算到期与其他验证失败。原生日志的时间关联仍须独立成立；跨墙钟不连续无法证明关联或先后时保持不确定，不修改日志时间、不扩大原查询窗口。初始长提示的实际接收也需核对原生行为，不以输入字节数替代 Hook/turn 证据。没有新增系统时间设置、账号权限、TUI 代理或产品安装授权。

仅在 `sd-qual-02` 的外层运行进程中，可使用系统 `/usr/bin/caffeinate -i -t 300 -w <controller_pid>`，降低自动 idle sleep 对实验的干扰；这不是对首样本时钟差异的根因断言。该断言最多持续 300 秒，或随受测 controller 退出而释放，须记录本任务 PID 并验证释放。原生服务与 TUI 的 argv、10/120 秒窗口均不变；不使用 utility 包装模式或 `-u/-d/-s`，不修改持久电源设置。手动休眠仍按原连续时钟窗口判定，不延时补算通过。依据为本机 `/usr/share/man/man8/caffeinate.8` 的 `-i/-t/-w` 语义。

### 2026-09-12：第三个独立 case 的后验身份资格

sd02 的进程字段不一致已修复，实际记录克隆的完整 prepare 链已通过。另一个短命令样本 `native-command-input-01` 在原 10 秒内未取得 `/status` root；启动阶段的 plain-text draft 可以接收 paste 而忽略 Enter，但原样本没有记录足够的界面状态，不能认定唯一原因。PID 与首字节不再被称为可交互就绪证据，旧样本不重试或改写。

前瞻登记唯一新模型 case `sd-qual-03`，允许按下列范围实现；须冻结修改、独立复核及新鲜前检后才下发实际运行：

- 仍是全新、归属本任务的共享服务与普通 `codex` TUI，记录创建身份、实际可执行路径、固定二进制摘要和 socket；默认 socket 被占用则拒绝。主会话控制唯一 TUI 的输入，不切屏；额外 task-owned 探针必须明确登记，不能声称完全没有其他连接。
- 不把 `/status` 作为提交前置。仅提交一次固定 initial paste 与独立 Enter，再于原 120 秒窗口接受同一服务的唯一真实 `UserPromptSubmit(root, turn)`。没有该证据即不准入，不补第二次 Enter 或提示，也不以等待时间认定 UI 已就绪。
- 对该精确 root 检查固定版本的线程关系：现有 prepare 的原服务 `thread/read(includeTurns:true)` 必须显式返回 `parentThreadId:null`、`forkedFromId:null`、`ephemeral:false`，并匹配 root、cwd、版本与 `source=vscode`。记录 UUID `sessionId`，后续读取保持一致；不假定它等于 root。SQLite 对 `thread_spawn_edges WHERE child_thread_id=root` 的读取必须成功且为零行。
- 真实 Go 任务的 `controller_thread` 必须等于该 root；首次 READY、pending 两侧屏障、完成结果、后续 standalone 历史项及 ACK 继续精确绑定同一 root/turn/nonce。上述身份与关系检查必须在第一次发送前完成。
- 仍只发送一次真实 `toolOutput`，`input=[]`；uncertain 不重发。本地 10 秒、首次输入和唯一发送各自原 120 秒窗口不变；旧 case、旧期限和旧 binding 均不复用。
- 允许 sd03 外层沿用同样有界的 `caffeinate -i -t 300 -w <controller_pid>`，记录并核实释放；不改变 native argv、持久配置、账号或权限。正常退出与外层清理分别记录。

这一后验资格只用于固定新 TUI 的接口实验，不能替代通用当前会话附着、冷启动安装或完整 G0。历史 sd02 的离线回归可在测试内部显式替换唯一 active nonce 常量，不改原输入；运行入口只接受 sd03，不增加环境或 CLI 绕过参数。

### 2026-09-12：第四个独立 case 与摘要合同修复

sd03 已取得真实 initial、同 root 的 READY/pending 屏障及原服务 prepare 阳性。它生成唯一发送意图后，controller 把原始 binding 文件 SHA 与 helper 的 canonical JSON SHA 比较，导致本地拒绝。未取得发送收据，是否已写入原生连接仍不确定；超过原窗口的只读历史未观察到结果，不作为未发送证明。该 case 已停止并保留全部原记录，不重试。

前瞻登记唯一新 case `sd-qual-04`。沿用 sd03 的普通入口、单次初始输入、后验身份资格、显式 family/session、真实 Go owner、原始 10/120 秒窗口和有界 caffeinate。仅统一 binding 的 canonical JSON 摘要约定；二进制与原始归档仍使用原始字节 SHA。新 case 的发送仍仅一次，uncertain 不重发，不增加 RPC 或配置变更。

运行前须独立复核专用摘要修复，以及真实 controller 文件序列化→helper `issue_once`→控制器读取发送意图的离线整链回归；随后冻结 helper/controller 的唯一 active nonce 为 sd04 并完成主树新鲜验证。历史 sd02/sd03 回归只可在隔离测试内显式选择对应 nonce，不改变原文件、旧期限或运行入口，不增加可绕过资格的参数。准入仅限此受控原生样本，不计作通用附着或完整 G0。

离线门禁已完成：controller `59a2d67`/`ef949c3` 与 helper 整链 `6e32da75`、nonce 冻结 `9527c495` 均经独立审查 PASS；主树对齐后下发一次 sd04。原生结果为 `initial_binding_inconclusive`，未进入发送；sd03/sd04 均不重试。下一独立 case 若继续，须另登记，不能把本次 lock 文件名或插件缓存当作 Hook 证据。

### 2026-09-12：第五个独立 case，先观察到本服务 SessionStart 再提交

sd04 的 paste+Enter 落在 TUI 启动后约 1 秒，当时插件缓存仍在写入；`hook-events` 窗口内 0 条，包括没有本服务 `SessionStart`。不能据此否定 send-once 接口，也不能把 home 下 thread lock 文件名当成提交证据。

前瞻登记唯一新 case `sd-qual-05`。沿用普通 `codex` 入口、单次 bracketed-paste + 独立 Enter、后验 `UserPromptSubmit` 身份、canonical binding 摘要、一次发送、uncertain 不重发、原 10/120 秒窗口和有界 caffeinate。仅增加：在那一次 paste 之前，必须在启动预算内观察到属于本 service PID 的唯一真实 `SessionStart`；没有则记录明确子阶段失败并中止，不补第二次 Enter。失败时把有界 PTY 字节归档到本 case，供诊断，不把 PTY 文本当作 current-UI 附着证明。历史 sd02/sd03/sd04 原文件不改、不重跑。须冻结、独立复核和新鲜前检后才下发。

sd05 已下发：10 秒内无 SessionStart，PTY 停在 MCP 启动。不重试。下一独立 case 若继续，须把 SessionStart 等待放到初始 120 秒窗口（仍只 paste 一次），不能改全局配置或关掉 MCP 来凑通过。

### 2026-09-12：第六个独立 case，SessionStart 等待放入初始 120 秒窗口

sd05 已下发：10 秒本地启动预算内无 SessionStart，PTY 停在 MCP 启动（cua_repl/node_repl 1/3）。不重试。不能据此否定 send-once 接口，也不能关 MCP 或改全局配置来凑通过。

前瞻登记唯一新 case `sd-qual-06`。沿用普通 `codex` 入口、单次 bracketed-paste + 独立 Enter、后验 `UserPromptSubmit` 身份、canonical binding 摘要、一次发送、uncertain 不重发、原 10 秒本地窗口（服务/TUI/quit/lab read）、唯一发送 120 秒窗口和有界 `caffeinate -i -t 300 -w <controller_pid>`。仅把 paste 前必须出现的、属于本 service PID 的唯一真实 `SessionStart(source=startup)` 等待从额外 10 秒启动预算改到初始 120 秒窗口：该窗口从 origin 捕获后开始，覆盖 SessionStart 等待与随后仅一次 paste+Enter 及后验身份；没有本服务 SessionStart 则记录 `owned_session_start_missing` 并中止，不补第二次 Enter。失败时把有界 PTY 字节归档到本 case，供诊断，不把 PTY 文本当作 current-UI 附着证明。历史 sd02/sd03/sd04/sd05 原文件不改、不重跑。运行入口只接受 sd06，不增加环境或 CLI 绕过参数；历史回归只可在隔离测试内显式 patch 对应 nonce。须冻结、独立复核和新鲜前检后才下发。

sd06 已下发：初始 120 秒窗口内无 SessionStart，PTY 停在 MCP 启动且 weekly usedPercent=100。不重试。不能关 MCP 或改全局配置。准许用官方 app-server `account/rateLimitResetCredit/consume`（与 TUI `/usage` 同源）兑换一次已有 banked reset，不改配置文件、不关 MCP；兑换后 weekly usedPercent 从 100 到 0。该兑换不是 G0 样本，不占用 sd06 nonce。

### 2026-09-12：第七个独立 case，额度恢复后仍走普通入口

前瞻登记唯一新 case `sd-qual-07`。沿用 sd06 的普通 `codex` 入口、单次 paste+独立 Enter、paste 前本服务唯一 `SessionStart` 放在初始 120 秒窗口、后验 `UserPromptSubmit`、canonical binding 摘要、一次发送、uncertain 不重发、原 10/120 秒窗口和有界 caffeinate。不重跑 sd-qual-01..06。运行入口只接受 sd07，无环境或 CLI 绕过；历史回归只可在隔离测试内显式 patch。须冻结、独立复核和新鲜前检后才下发。

sd07 下发前的新鲜检查发现普通入口已经指向 `0.154.0`，实际二进制 SHA 为 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`；用户当前配置摘要也已变化，本任务没有修改它。sd07 尚未创建 case、启动原生进程或发送输入，因此继续使用该未消费登记，按实际安装版本核验官方固定源码及本地 schema 后，统一 helper/controller 的版本与二进制摘要。普通入口和用户当前配置保留，不切回旧二进制、不改链接或全局配置；运行前重新捕获保护基线，不能套用 sd06 的旧基线。历史 0.153.4 回归只在测试内固定旧路径和快照，原记录不变。独立复核同时发现 SessionStart 扫描可能跨过截止点，必须在首次 paste 前复查同一原始 deadline；该修复不延长窗口或增加输入。版本适配与该边界修复均通过复核和主树验证后才准入。

sd07 已执行一次，输入为零，原 120 秒窗口后以 `owned_session_start_missing` 结束；自有进程及 socket 已清理。其 PTY 未出现 MCP 启动标记，usage/weekly 字样不能用于量化额度或确定原因。随后固定 0.154.0 全源码调用点核验发现：启动只排入 pending SessionStart，真正执行点在 `run_turn` 内，先于该轮 UserPromptSubmit。因此将 SessionStart 作为首次输入的前置条件是实验门禁错误；schema/字段兼容并不证明触发时机兼容。保留 sd07 原始失败记录，不重试；不由此否定尚未执行的回传接口。

### 2026-09-12：第八个独立 case，原生初始提示与后验双 Hook

前瞻登记唯一新 case `sd-qual-08`。固定 0.154.0 支持原生 positional `PROMPT`，默认 UDS 复用条件不排除它；原生 StartFresh 将初始消息保存到新 thread，再通过 `Option::take` 和正常 UserTurn 路径提交。以这一已有原生输入路径驱动受控实验：`codex <固定初始用户任务>`，不增加专用启动器、`--remote`、设置或权限覆盖。参数只承载原初始任务，不承载工具结果；产品普通入口要求保持。

- 初始 120 秒窗口必须在启动携带 prompt 的 TUI **之前**开始，覆盖原生启动、首轮 Hook、READY/pending 屏障及作业完成；不在 post-launch origin 捕获时重新开始。服务/TUI/本地读取及 quit 仍用原 10 秒上限，窗口不能叠加延长。
- TUI 启动前持久保存本服务身份、socket、唯一 case 与初始 prompt 的摘要/字节数及原窗口。启动后尽快独立读取实际 TUI PID/birth/executable 与 socket，保存 origin；不得再称其发生于初始输入之前。两份记录通过摘要关联，初始任务仅经 argv 提供一次，不补 PTY paste 或 Enter，也不重启 TUI。
- SessionStart 改为**后验**证据：原窗内必须出现本 service 唯一 `source=startup` 的 SessionStart，以及同 session/root 的唯一 UserPromptSubmit。保留实际 Go owner、原服务 source/cwd/version、显式 family/session、SQLite parent edge、原 READY/pending 屏障及发送前复查。
- 限定输入状态须拒绝第二次 argv prompt 或混用 initial paste/Enter。原生额度、权限或启动错误正常生效；没有后验双 Hook 就不进入 prepare/send。仅发送一次真实 `toolOutput`，`input=[]`，uncertain 不重发。退出与外层清理分别判定。
- 运行入口只接受 sd08；sd01–sd07 的目录、记录和窗口均不复用。历史回归仅在隔离测试内显式选择旧 nonce/版本。源码实现、真实输入路径证据和跨组件回归经独立复核及主树验证后才下发；有界 caffeinate、普通用户配置、固定二进制摘要均沿用 sd07。

## 接口解释与判据

官方 `turn/start.toolOutput` 接受客户端实际执行的独立工具输出，`input=[]`，以 `functionCallOutput` 持久化；不要求存在旧的 pending tool call。旧研究对“必须补回旧 pending call”的概括不作为本轮排除此接口的依据。[官方 App Server 说明](https://learn.chatgpt.com/docs/app-server#start-a-turn)

该接口仍须实际触达普通 TUI 所使用的原服务；新建执行者服务或相同历史不是原服务接入证据。`loaded`、父进程存活、SessionStart 或订阅计数也不能单独代表当前显示 thread。

通过条件沿用 [原 G0 实验规格](../../../docs/g0-validation-plan.md)，并应用已授权的退出生命周期调整：

1. 明确端点→服务→TUI 实例→当前 thread 的可刷新及可失效链；切换、恢复、强杀不会误认当前控制者。
2. 真正 idle 时，完成或阻塞事件无需新用户输入即可进入原 thread，保持真实工具输出身份；busy/用户输入竞争和权限等待保持正常语义。
3. 原服务持久历史与排队状态能支持已交付、可证明未交付、uncertain 三态；uncertain 不盲重发。运输确认与逐事件业务 ACK 分开；改 command UUID 不重复产生语义副作用。
4. 每组合基础样本三次、故障屏障二十次；本地窗口 10 秒，模型窗口 120 秒。缺少准入前提的组合不为凑数运行，也不标通过。

## 现场与交付

新建材料按本任务 `plans/`、`docs/`、`scripts/`、`data/`、`tmp/` 分开；`tmp/` 只放可回收 worktree、下载和测试缓存。源码、必要运行记录和可审查证据保留。收尾列清单，核对归属后清理；不删除原有任务材料或配置。

状态：sd04–sd07 均已保留原始失败记录并完成自有资源清理。sd07 的前置 SessionStart 检查已被源码证据否定；当前前瞻登记 sd08 的原生初始提示路径与后验双 Hook，尚未实现冻结或下发。完整 G0 仍未通过。
