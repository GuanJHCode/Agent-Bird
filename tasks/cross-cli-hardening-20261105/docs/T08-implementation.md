# T08 本地普通 MCP 工具：实现与集成签收

历史记录：以下保留首次 T08 façade 实现记录及其当时状态，不表示当前包/交接结论。2026-11-09 短引用源码已完成，当前冻结源为 `7b7f0421`；最新包、回归和阻塞状态以[当前交接](final-handoff.md)为准。

实现完成，源码独审 /root/review_t08 r4 PASS；root按19文件SHA逐项核对集成，无手工冲突合并。base c7d5f8a0已单独包含独审通过的T06B取消测试同步。此结论不是最终安装包或真实父模型闭环通过。

8个普通stdio工具复用原CLI/IPC/SQLite。官方Go SDK1.8.0固定，x/sys0.41；未引入Tasks、新业务DB、自动规划模型或原生聊天回调。v3 refs、预算组、owner授权和明确accept/ACK保持原合同。输入frame2MiB/CLI捕获128KiB/完整结果256KiB，不截断分页proof；typed严格字段和int64原值，4并发、45s单call、wait<=30s。

configure生成任务内private配置，key只在worker不可访问的StateDir；不绑定未来owner、不写global配置或审批。MCP dispatch须可信本版managed controller与每call版本化source-auth-env证明，source值只在进程内，request只有摘要、Codex配置只有精确变量名。当前四Provider相关认证/路由族和HOME/proxy/CA/XDG纳入，未知suffix/值改变/empty-unset不同；8个非认证Codex上下文排除。缺/坏/不匹配证明拒绝，提示重新configure和重启controller；不隐式默认账号。旧CLI缺optional证明保持兼容，query/stop/ACK不加父活性门禁。

review_token绑定原owner/run/task/revision/event/hash/slot/segment/candidate，不替代当前Store/IPC授权。MCP/CLI request-changes共享原owner-decisions checkpoint，两向回复丢失按原command和body回放，变正文拒绝。dispatch另给只读cli_handle供MCP关闭后原wrapper收尾。Grok显式grok_session_write必须为true且仍只reviewer/read-only，非Grok true拒绝。

先前独审问题与红证据均保留：capture io.WriteString绕限及溢出被忽略；缺MCP→wrapper checkpoint/cli_handle；Grok无session-write输入；Codex过滤owner与认证环境；r3投影漏Grok/cloud/AGY家族。r4未放宽Host认证/路径/worker反递归/Store授权来制造通过。root独立发现并修复stale stop conflict分类。

root集成Go1.26.10 full normal/race各1598 pass（641顶层+957子项）、0fail、8既有skip，vet/build均0；四份production_files_sha256相同be3536425c468587503487dcd89bb355a68f8e5caf1b776807a55361ea9d5e67。skip为两diagnostic timing/probe、两需明确nativeClaude路径、四真实模型/版本矩阵准入；不伪称这些未运行项通过。另明确固定Claude2.1.316路径补跑owned-loopback原生ReadOnly/ManagedWrite两项：2pass/0skip，无真实凭据或模型。独审r4扩集合normal/race各133pass/0skip（原始tool输出、未落raw日志的限制在独审MD明确）；worker完整相关normal/race228pass/0skip及Hostauthguard16pass，根全模块报告独立核数。

Codex0.166.0和Claude2.1.316均在私有环境实际连接MCP、发现精确8tools，未发父prompt/turn或模型；Claude control-only18publicsentinel全继承，Codex初版过滤REQUEST→白名单恢复有红绿证据。二者是OS-native-local快照，最终安装路径/receipt另须重跑。额外native-managed-admission探测在Seatbelt下因setuid/bin/ps拒绝而bootstrap前BLOCKED，OwnerBind/task/worker均0，未到sourceproof；没有依据修改产品守卫，也不以此冒充真实派发。

T09仅保留managed collect/wait、native_push=false和现有拒绝码；native整体BLOCKED，不纳入新native实现。T11最终私有包、T12同包双向真实父/worker和T13最终签收仍单列。用户提供的网关配置仅由root在当前授权子进程内存使用，不纳入源码、参数或验收日志；T12无推理models元数据预检404不证明认证成功/失败，不冒充真实模型验证。
