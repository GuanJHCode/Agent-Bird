# 剩余 G0 能力：实施与候选验证

日期：2026-09-12；基线 `cd3d054`。状态：第二个固定会话样本已证实真实初始轮次结束早于作业完成，但 helper 因进程身份字段不一致在本地拒绝，尚未发送 standalone 回传。正在修复该字段并用实际记录验证完整准备流程。完整 G0 未放行。

本轮将 idle 回传、当前 TUI 附着、完整交付对账交给三个 subagent，使用独立 worktree；主会话编排、核对边界和整合，另一 reviewer 审查。上轮退出停止、记录保留、原会话明确续接的结果保持。

## 本轮取得的进展

1. **纠正回传接口判断。** 官方 `turn/start.toolOutput` 允许客户端实际执行的独立工具输出，无需旧 pending call；`input=[]`，结果保留 `functionCallOutput` 身份。原设计允许经设置连续性验证的最小 `thread/resume`。旧报告中过强的排除语句已修正。真正待解的是普通原 TUI 的原服务可达与当前附着。[官方接口](https://learn.chatgpt.com/docs/app-server#start-a-turn)、[固定源码核查](idle-analysis.md)
2. **实现最小离线对账检查器。** 对给定历史读取核对固定 delivery、controller、epoch、事件版本和摘要；区分历史匹配、逐事件 ACK 和等待用户的事件。查不到标记保持 uncertain，完整 ACK 不补造运输确认。工具明确不认证原服务或业务事务，也不授权自动重发。
3. **追查两个可落地的替代方向。** SDK `ExternalMessage` 提供工具级输入入口，但仍需原服务连接。launchd socket activation 可研究保持普通命令的按需连接，实际需要额外前端和原生私有 UDS；它没有自动补齐当前显示身份和退出终止关联。未安装、未改配置，也未以静态结果冒充原生运行通过。[按需连接机制](idle-socket-activation-mechanism.md)
4. **修正附着研究中的过强结论。** 缓存 side thread 切换后仍会产生 skills 请求；开启 Apps 时还存在带 thread 的异步请求。独立复核因此否定了“没有任何外部事件”的初版反例，后继报告已撤回它，保留信号组合的验证价值。最近一次 Apps 请求也不能独自证明当前显示，因为旧请求可能迟到。[修正后的证据与边界](attachment-current-tui-thread.md)、[复核闭环](attachment-review-corrections.json)
5. **完成首版资格工具并进入实测。** 控制器 18 项、helper 16 项离线测试及独立复核通过后，执行了 `sd-qual-01`。样本在 initial 阶段结束，记录到明显双时钟差异，但异常仅保存 `ValueError`，不能唯一归因。初始字节已写入 PTY，未证实原生 user turn 接收；helper 未执行，任务目录为空。[原记录](native-sd-qual-01-controller.json)、[固定提交与验证](native-main-verification.json)
6. **以实测修正后续准入。** 固定 TUI 源码表明同批裸文本与 Enter 存在 paste-burst 不提交分支，后继改用原生粘贴帧与独立提交。普通 `codex app-server` 又会明确使用 `SessionSource::VSCode`，因此本共享服务样本应精确核验 `vscode`；这不改变原生设置，也不证明完整 C13。现场两个历史读取方法返回 `-32601`，当时尚无已证实的持久 user turn，仍需真实运行证据，不能由 schema 覆盖该拒绝。[下一 case 约束](../plans/contract.md)
7. **第二次实测推进到回传准备。** `sd-qual-02` 具有唯一真实输入 Hook、精确 root/turn 的 READY 完成事件，以及事件两侧的 Go pending 读数。作业随后 completed/count=1，owner 与 root 一致、ACK=null。helper 在 0.211 秒内因把真实 `ps comm=codex` 当作绝对路径而拒绝，源码顺序和原 binding 复现共同证明未到 RPC。后继实现将保留 `comm` 并独立增加实际可执行路径，旧 case 不修改或重试。[样本与诊断](native-sd-qual-02-result.md)
8. **持久历史读取得到实际阳性。** 另一新私有服务在 0.031 秒内读到 sd02 精确 root 的完整首次轮次，`historyMode=paginated`、`itemsView=full`，包含完成的命令项和精确 READY final。thread 为 `notLoaded`；该诊断没有新 TUI、resume、turn/start 或模型请求，不能补算原服务、当前附着或回传成功。独立复核确认受限记录与这些结论一致。[诊断与范围](native-sd-qual-02-history-diagnostic.md)

## 仍待解决的边界

| 项目 | 已有能力 | 完整验收尚缺 |
|---|---|---|
| 在线 idle 自动回传 | standalone toolOutput 的文档及固定源码支持；真实首轮结束与作业完成的顺序证据 | 修复本地准备流程后，取得真正 idle 原会话自动消费的完整样本；通用接入与当前附着仍待解 |
| 当前 TUI 附着 | 普通入口可复用已存在的原生 listener；真实 thread/服务身份有旧样本；skills/切换请求可继续研究组合 | 全部显示切换的可区分性、刷新与失效，非唯一 read 的消歧及异步请求迟到处理 |
| 完整交付对账 | 有界离线历史标记核对、分页信息与部分 ACK 分离；既有本地语义槽 | 来源已核验的原生运输阳性/否定、旧发送者隔离、compact/分页和六类故障屏障；逐事件真实事务 |

原设计允许重复展示，要求避免重复业务副作用；缺少原生幂等键不单独构成失败。关闭与发送也不要求跨系统绝对原子，但必须有可解释的接收边界。当前无 Goal 路线不以修复原生 Goal CAS 为前置。

## 验证、整合与清理

离线检查器经过两个独立发现的修复：同一原生 item 的身份变化在过滤前检查，已结束的分页遍历不能接成同一完整链。主分支新鲜回归 15/15、1.308s；独立 archive 回归 15/15、1.599s，原反例全部转绿。示例输出匹配，Go 源码与审核二进制未变化，原七份设计文件摘要保持。[主分支验证](main-verification.json)、[独立复核及原始输入](review-evidence/docs/reviewer-final.md)

原始 RED 日志保留了 unittest 输出中的行末空格，完整 diff 的相应格式警告如实记录；代码和文档 diff 检查通过，未为隐藏历史失败改写日志。

已整合的 delivery worktree、其 schema/Go 缓存和独立 reviewer 临时副本已清理；源码、分支和必要证据保留。其他源码缓存及原生实验工作区仍在使用。[清理记录](cleanup.json)

`sd-qual-01` 与 `sd-qual-02` 的本任务 TUI、服务及 socket 均已清理；sd02 的 Go worker 和 caffeinate 也已退出，原 case/job 及未 ACK 结果保留。sd02 在控制器 10 秒窗口内未证实 TUI 正常 `/quit` 退出，后续 PID 缺失不补算正常退出通过。主分支重新核对 sd02 五份归档共 25,945 字节与原记录一致，六份启动源码摘要与固定 `8eeb456` 一致；独立复核通过的范围是这些事实及分类。下一独立 case 遵循 [本轮合同](../plans/contract.md)，原 case 和期限不复用。此前报告、离线测试及原生样本分别判定，G1–G4 保持未启动。
