# sd-qual-01：准备与时钟不确定，未到回传资格

**结论为 `infrastructure_inconclusive`，排除出基础成功样本。** controller 实际创建了专用 listener 和普通 `codex` TUI，并向 PTY 写入唯一初始提示；没有证据确认这份提示被原生 `turn/start` 接受。原始窗口内未取得初始 READY 屏障，没有调用 standalone helper，没有启动可见的 Go job，也没有发送回调。不能据此否证真正 idle 的 standalone 接口，更不能声称完成过模型或业务 ACK。

本轮运行使用冻结 controller `02aa787`、helper `c3f35d3`、CLI `0.153.4`（`b973d440…1261e3`）及已审 Go（`feb9b9ad…e989a285`）。执行前在同一外层重新验证各源文件/二进制 SHA、普通 PATH 解析、socket/case/job 不存在；没有沿用跨日旧 preflight。主会话运行期冻结了这些文件。[原始 controller 记录](/Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-pending-resolution/docs/native-sd-qual-01-controller.json)、[逐文件来源与 SHA](native-sd-qual-01-artifacts.json)

| 事实 | 记录 |
|---|---|
| 本任务 listener | PID `64731`，出生 `Sat Sep 12 02:16:30 2026`（本地），普通 `codex app-server --listen unix://` |
| 本任务 TUI | PID `64759`，普通 `codex`；初始 `/status` 的 root 为 `01a091af-7695-71b1-a3b1-a0251fed78d0` |
| 初始输入 | `2026-09-11T18:16:30.900382Z`，2090 字节，同一写入包含提示及 Enter；这只是终端写入记录 |
| 原模型窗口 | `18:16:30.898340Z → 18:18:30.898340Z`，没有重设或扩大 |
| 捕获异常 | `18:33:46.725390Z`，`stage=initial`、`ValueError`；旧版未保存细分错误码/栈 |
| 退出输入 | `18:33:46.950849Z` 写 `/quit`；当时 controller 尚未观察到 TUI 退出，后续 OS 确认其已不存在 |
| 业务产物 | 专用 job 目录一直未观察到文件，当前匹配本目录/nonce 的 Go PID 为零 |

初始输入到 `/quit` 的墙钟差为 **1036.050467 秒**，原 `mach_absolute_time()` 单调差只有 **14.060052 秒**。这证明两种时钟明显分歧；并未判定它一定来自校时还是系统休眠。把原 `since` 和已捕获的 `failure.at` 送入同版 `window()` 会确定拒绝。冻结路径中 initial 循环先经过 `matching_prompt_hook → window`，在没有 job 文件时尚不会进入 `native_ready` 或 helper；因此窗口拒绝是强支持的解释，但本轮**没有原始异常栈，不能把复现当作现场栈证据**。[OS/时窗只读记录](native-sd-qual-01-local-state.json)

独立固定源码审查另外确认：裸大段提示和同批 Enter 可被 TUI `PasteBurst` 作为粘贴内容/换行吸收，终端写入不等于提交。它解释了一个可达的输入问题；本轮没有捕获输入队列内部状态，不能将其写为唯一根因。后续只对新 nonce 使用完整 bracketed-paste 帧和独立 Enter，并要求恰好一条本 root 的真实 `UserPromptSubmit`。

收尾时在同一已核 PID/出生身份/socket 的服务上进行了有界只读 RPC；initialize 与最终 helper 完全相同，包含 `experimentalApi:true`。每个探针均在退出时关闭连接，**没有 `turn/start`、`thread/resume` 或新 thread**。

| 只读请求 | 实际事实与限度 |
|---|---|
| `server/diagnostics` | `process.id=64731`，远控状态 `disabled`；没有连接表/连接 ID |
| `thread/read(includeTurns:false)` | root 匹配、`idle`、`historyMode=paginated`、`source="vscode"`，cwd/CLI 版本匹配；返回的 `turns=[]` 只是该元数据读法的默认值，不能证明完整历史为空 |
| `thread/read(includeTurns:true)` | 原生 `-32601`，31 字节错误消息；只保存 SHA 和安全分类，命中 `not supported` |
| `thread/turns/list(limit:4,itemsView:"full")` | 同一 `-32601` 和消息 SHA，未获得可检查的 turn/items |
| 有效设置基线 | 元数据有 model/modelProvider/cwd，只保留类型与 hash；缺 approvalPolicy/sandboxPolicy 等，因此没有执行条件性最小 resume |

`source="vscode"` 后经固定源码确认是普通 `codex app-server` 的默认 SessionSource，不代表由 VS Code 客户端创建，也不能作为 TUI 附着证明。未来固定共享服务可精确期待该值；原数据的 `source_cli:false` 不作改写。两个历史方法的不支持事实只适用于本次未确认首轮接受的 root，不推广到已经执行工具并持久化的正常 thread。[协议形状](native-sd-qual-01-protocol-shapes.json)、[原生元数据](native-sd-qual-01-native-state.json)、[设置基线](native-sd-qual-01-settings-baseline.json)

两次只读临时封装问题也保留在记录中：一次误用了不存在的异常类，连接仍由 context 关闭；另一次用 helper 的仅允许 `includeTurns:true` 请求构造器发送 false，在本地被拒绝。后续直接构造已授权的只读请求后重新采集，避免把本地拒绝误当服务事实。部分记录含 `Rpc.initialize` 自动设置的 `machine_binding_checked/native_binary_pinned` 布尔值；**这些不是独立机验结果，不采信它们升级资格**，归属使用另存的 OS 身份、SHA 与 diagnostics PID。

最终 TUI 已不存在。listener 的 FD7/8/9 可在本进程内闭合成对；FD32 有 peer 地址但全局 lsof 未找到对应持有者，既没有据此断言活外部客户，也没有据此断言无客户。最后 diagnostics 的 `core.threads.live=0`、`mcp.connections.live=0`，没有实际证据指向其他客户端。主会话针对**这一个已拥有的测试服务**明确授权收尾；探针关闭后再次核验 PID/出生身份/native SHA/socket inode，仅向 `64731` 发送 SIGINT。

`CLOCK_MONOTONIC_RAW` 测得 **67.426459ms** 后服务不再运行，默认 socket 消失；没有向 peer 或其他进程发送信号，job 仍为空。此为手动实验收尾，不是产品自动连接归属或退出策略通过。[最终清理记录](native-sd-qual-01-cleanup.json)

保留主任务 `data/sd-qual-01` 的不可变原始受限记录及空 job 目录，防止重用 nonce；本报告复制必要 JSON 并保存原 SHA。旧候选 preflight 和公开源码缓存已清理。没有启动 `sd-qual-02`。
