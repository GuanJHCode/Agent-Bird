# 剩余 G0 能力：实施与候选验证

日期：2026-09-12；基线 `cd3d054`。状态：sd03 已通过真实初始轮次、线程关系及原服务 prepare，随后因 binding 摘要约定不一致中止，发送状态仍不确定。摘要修复和跨组件回归已通过；sd04–sd07 未进入发送。sd07 暴露了实验门禁错误：原生 SessionStart 在首轮执行中触发，不能作为首次输入前置条件。当前正在实施 sd08 的原生初始提示与后验双 Hook，尚未下发。完整 G0 未放行。

本轮将 idle 回传、当前 TUI 附着、完整交付对账交给三个 subagent，使用独立 worktree；主会话编排、核对边界和整合，另一 reviewer 审查。上轮退出停止、记录保留、原会话明确续接的结果保持。

## 本轮取得的进展

1. **纠正回传接口判断。** 官方 `turn/start.toolOutput` 允许客户端实际执行的独立工具输出，无需旧 pending call；`input=[]`，结果保留 `functionCallOutput` 身份。原设计允许经设置连续性验证的最小 `thread/resume`。旧报告中过强的排除语句已修正。真正待解的是普通原 TUI 的原服务可达与当前附着。[官方接口](https://learn.chatgpt.com/docs/app-server#start-a-turn)、[固定源码核查](idle-analysis.md)
2. **实现最小离线对账检查器。** 对给定历史读取核对固定 delivery、controller、epoch、事件版本和摘要；区分历史匹配、逐事件 ACK 和等待用户的事件。查不到标记保持 uncertain，完整 ACK 不补造运输确认。工具明确不认证原服务或业务事务，也不授权自动重发。
3. **追查两个可落地的替代方向。** SDK `ExternalMessage` 提供工具级输入入口，但仍需原服务连接。launchd socket activation 可研究保持普通命令的按需连接，实际需要额外前端和原生私有 UDS；它没有自动补齐当前显示身份和退出终止关联。未安装、未改配置，也未以静态结果冒充原生运行通过。[按需连接机制](idle-socket-activation-mechanism.md)
4. **修正附着研究中的过强结论。** 缓存 side thread 切换后仍会产生 skills 请求；开启 Apps 时还存在带 thread 的异步请求。独立复核因此否定了“没有任何外部事件”的初版反例，后继报告已撤回它，保留信号组合的验证价值。最近一次 Apps 请求也不能独自证明当前显示，因为旧请求可能迟到。[修正后的证据与边界](attachment-current-tui-thread.md)、[复核闭环](attachment-review-corrections.json)
5. **完成首版资格工具并进入实测。** 控制器 18 项、helper 16 项离线测试及独立复核通过后，执行了 `sd-qual-01`。样本在 initial 阶段结束，记录到明显双时钟差异，但异常仅保存 `ValueError`，不能唯一归因。初始字节已写入 PTY，未证实原生 user turn 接收；helper 未执行，任务目录为空。[原记录](native-sd-qual-01-controller.json)、[固定提交与验证](native-main-verification.json)
6. **以实测修正后续准入。** 固定 TUI 源码表明同批裸文本与 Enter 存在 paste-burst 不提交分支，后继改用原生粘贴帧与独立提交。普通 `codex app-server` 又会明确使用 `SessionSource::VSCode`，因此本共享服务样本应精确核验 `vscode`；这不改变原生设置，也不证明完整 C13。现场两个历史读取方法返回 `-32601`，当时尚无已证实的持久 user turn，仍需真实运行证据，不能由 schema 覆盖该拒绝。[下一 case 约束](../plans/contract.md)
7. **第二次实测推进到回传准备。** `sd-qual-02` 具有唯一真实输入 Hook、精确 root/turn 的 READY 完成事件，以及事件两侧的 Go pending 读数。作业随后 completed/count=1，owner 与 root 一致、ACK=null。helper 在 0.211 秒内因把真实 `ps comm=codex` 当作绝对路径而拒绝，源码顺序和原 binding 复现共同证明未到 RPC。现已保留真实 `comm` 并增加独立实读的 `executable_path`；完整 collector→controller→helper.main→真实 Go read/inspect 的副本回归及独立复核通过。旧 case 未修改或重试。[样本与诊断](native-sd-qual-02-result.md)、[整链回归](standalone-helper-sd02-chain.md)
8. **持久历史读取得到实际阳性。** 另一新私有服务在 0.031 秒内读到 sd02 精确 root 的完整首次轮次，`historyMode=paginated`、`itemsView=full`，包含完成的命令项和精确 READY final。thread 为 `notLoaded`；该诊断没有新 TUI、resume、turn/start 或模型请求，不能补算原服务、当前附着或回传成功。独立复核确认受限记录与这些结论一致。[诊断与范围](native-sd-qual-02-history-diagnostic.md)
9. **短命令检查暴露启动资格不足。** status/quit 已改为固定 paste 帧与独立 Enter，并共享各自原 10 秒窗口，但 `native-command-input-01` 的 status 仍未采到 root，未进入正常 quit 验证。原窗只观测到 SessionEnd，没有 UserPromptSubmit/turn 事件；原始界面数据未保存，无法确认唯一原因。固定源码的 StartupDraft 会接收文本而忽略 Enter，说明 PID/首字节不足以代表可交互就绪。该样本已清理，sd03 改用受控 initial 的后验身份资格；不重复旧输入或放宽状态解析。[失败记录](native-command-input-01.md)、[sd03 合同](../plans/contract.md)
10. **sd03 取得真实 prepare 阳性，并暴露跨组件摘要错误。** 唯一初始输入、READY/pending 屏障、原服务 full history、family/session 和 Go owner 检查均通过。helper 生成发送意图后，controller 用原始文件 SHA 比较 canonical JSON SHA 而拒绝。没有发送收据，超窗只读历史未观察到结果，仍不能证明未发送。现已统一 binding 的 canonical 摘要，并用原始 prepared 记录副本执行真实文件序列化、`issue_once` 和 receiver 回归；重复发送保持拒绝。[sd03 原生事实](native-sd-qual-03-result.md)、[整链回归](standalone-helper-sd03-binding-hash.md)、[整合验证](native-sd-qual-04-main-verification.json)
11. **sd04–sd06 将启动问题与回传接口区分开。** sd04 的一次初始提交没有获得本服务 Hook；sd05 增加 paste 前 SessionStart 门禁，10 秒内未出现；sd06 将这段等待纳入首次 120 秒原窗口，仍未出现，PTY 同时显示 MCP 启动和额度耗尽提示。这些样本均未进入 prepare/send-once，原数据保留，自有服务及 socket 已清理。它们不构成回传接口失败证据。[sd04](native-sd-qual-04-result.md)、[sd05](native-sd-qual-05-result.md)、[sd06](native-sd-qual-06-result.md)
12. **下一步使用新样本继续验证。** 一次既有 reset 已通过官方接口兑换并单独记录；sd07 沿用普通入口与用户当前配置，SessionStart 等待、单次初始输入和后验身份共用原初始 120 秒窗口。新鲜检查发现普通 CLI 现为 0.154.0，旧 0.153.4 固定摘要会拒绝；另一个独立探针复现了 SessionStart 扫描跨过截止点后仍可能写入 paste 的问题。版本契约核验与输入前截止检查正在落实，通过后才冻结下发，不复用 sd01–sd06。[额度恢复记录](native-quota-restore-sd07.json)、[sd07 合同](../plans/contract.md)

当前 sd07 的最小适配及截止修复已经独立复核：native 44 项、helper 35 项通过，主树组合 79 项通过，原七份设计文档摘要保持。实际使用的 0.154.0 schema、27 份固定源码及本地平台收据已归档；它们未证明原生回传成功。默认 app-server 日志路线另经限定核验，缺少完整双向请求/响应及缺项检测输入，不能仅加投影器就实现可靠附着。[sd07 冻结与准入](native-sd-qual-07-registration.json)、[版本核验](../../g0-native-0154-contract/docs/compatibility.md)、[日志输入核验](../../g0-attachment-log-availability/docs/result.md)

sd07 随后执行一次，在原 120 秒窗口内以 `owned_session_start_missing` 结束，输入及 helper 调用均为零。独立复核确认原始档案、保护基线和自有资源清理一致；PTY 的 usage/weekly 字样不证明额度耗尽，也没有 MCP 启动标记。[sd07 实测](native-sd-qual-07-result.md)

后续全源码调用点核验纠正了准入前提：启动只排入 pending SessionStart，实际执行在 `run_turn` 内，所以零输入等待它构成循环。先前 schema 审查未覆盖这一触发时机，sd05/sd06 报告用界面文字解释原因的表述也证据不足。sd08 改为原生 `codex <固定初始任务>`，从 TUI 启动前开始计时，提交后再核对两个真实 Hook、family/session 和 Go owner；工具结果仍只通过原 helper 的 `toolOutput` 发送。[诊断](../../g0-sessionstart-mcp-diagnostic/docs/diagnosis.md)、[原生初始消息调用链](../../g0-sessionstart-mcp-diagnostic/docs/positional-prompt-candidate.md)、[sd08 合同](../plans/contract.md)

另确认 0.154 提供内建 daemon start/bootstrap，可在目标 TUI 打开前启动原生 listener；普通无参数入口自身不保证拉起它，已有 embedded TUI 也不会因此迁移。它补充了安装前准备候选，尚未解决后续冷启动、当前附着或退出停止。[内建 daemon 核验](../../g0-native-daemon-bootstrap-review/docs/result.md)

## 仍待解决的边界

| 项目 | 已有能力 | 完整验收尚缺 |
|---|---|---|
| 在线 idle 自动回传 | standalone toolOutput 的文档及固定源码支持；sd03 真实首轮结束、作业完成与原服务 prepare；摘要跨组件回归通过 | 新样本的真正 idle 原会话自动消费、原历史项与 ACK；通用接入与当前附着仍待解 |
| 当前 TUI 附着 | 普通入口可复用已存在的原生 listener；真实 thread/服务身份有旧样本；skills/切换请求可继续研究组合 | 全部显示切换的可区分性、刷新与失效，非唯一 read 的消歧及异步请求迟到处理 |
| 完整交付对账 | 有界离线历史标记核对、分页信息与部分 ACK 分离；既有本地语义槽 | 来源已核验的原生运输阳性/否定、旧发送者隔离、compact/分页和六类故障屏障；逐事件真实事务 |

原设计允许重复展示，要求避免重复业务副作用；缺少原生幂等键不单独构成失败。关闭与发送也不要求跨系统绝对原子，但必须有可解释的接收边界。当前无 Goal 路线不以修复原生 Goal CAS 为前置。

## 验证、整合与清理

离线检查器经过两个独立发现的修复：同一原生 item 的身份变化在过滤前检查，已结束的分页遍历不能接成同一完整链。主分支新鲜回归 15/15、1.308s；独立 archive 回归 15/15、1.599s，原反例全部转绿。示例输出匹配，Go 源码与审核二进制未变化，原七份设计文件摘要保持。[主分支验证](main-verification.json)、[独立复核及原始输入](review-evidence/docs/reviewer-final.md)

原始 RED 日志保留了 unittest 输出中的行末空格，完整 diff 的相应格式警告如实记录；代码和文档 diff 检查通过，未为隐藏历史失败改写日志。

已整合的 delivery worktree、其 schema/Go 缓存和独立 reviewer 临时副本已清理；源码、分支和必要证据保留。其他源码缓存及原生实验工作区仍在使用。[清理记录](cleanup.json)

`sd-qual-01` 与 `sd-qual-02` 的本任务 TUI、服务及 socket 均已清理；sd02 的 Go worker 和 caffeinate 也已退出，原 case/job 及未 ACK 结果保留。sd02 在控制器 10 秒窗口内未证实 TUI 正常 `/quit` 退出，后续 PID 缺失不补算正常退出通过。主分支重新核对 sd02 五份归档共 25,945 字节与原记录一致，六份启动源码摘要与固定 `8eeb456` 一致；独立复核通过的范围是这些事实及分类。下一独立 case 遵循 [本轮合同](../plans/contract.md)，原 case 和期限不复用。此前报告、离线测试及原生样本分别判定，G1–G4 保持未启动。

sd03–sd07 的后续执行与清理见各自原生事实记录；sd03 的唯一发送意图保持 uncertain，sd04–sd07 没有进入发送。正常 quit 与外层清理分别判定。较早验证记录中的准入字段描述其冻结时点；当前状态以本页、本轮合同及最新独立 case 记录为准。
