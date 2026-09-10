# G0 实施与验收记录

日期：2026-09-10。当前结论：**G0.1 原生身份实验受阻，G0 未通过，G1–G4 未实施。** 探针实现、模拟协议测试和真实原生验收分别记录。

## 环境、范围与原文件

- 工作目录初始只有七份设计文件，无 Git、源码或测试。初始化 `main` 并提交原始设计基线 `53221d4`；原文件摘要见 [original-sha256.txt](original-sha256.txt)。未改写原设计评审结论。
- 适用规则来自用户提供的全局 AGENTS.md 内容；当前目录及上级路径未发现实际 AGENTS.md。未修改用户全局配置、全局 AGENTS.md、CLI 账号、权限或认证材料。
- macOS 26.3.1 / build 25D2128 / Darwin 25.3.0 / arm64 / APFS；Git 2.39.3 (Apple Git-146)，Codex CLI 0.153.4。
- 初查 PATH 无 Go、PowerShell、Parallels CLI、VMware CLI、QEMU；只说明这些命令未发现，不能证明用户没有其他测试环境。隔离 macOS 用户/VM、已登录的原生 Windows 环境尚未提供。
- 已向用户询问现成测试环境位置/连接方式，未要求提供认证内容。不能复制个人认证目录来制造已登录测试环境。

## 当前功能与分工

当前功能 G0.1 的完整合同见 [实施计划](../../../plans/implementation-v0.1.md)，关联 C01/C02/C09/C12 和 C13 的只读边界。

| 负责人 | 范围 | 状态 |
|---|---|---|
| 主 Agent | 计划、环境、证据、整合及最后判定 | 进行中 |
| g0_interface_research | 本机帮助/schema及官方接口，只读 | 完成；未连接真实会话 |
| g0_probe_impl | 独立 worktree 内 `tools/g0-probe/` 的最小只读 Go 探针与测试 | 进行中 |
| 独立复核者 | 未参与实现，复核最终实际代码及证据 | 待派 GPT-6 Astra / max |

写入隔离：主 Agent 编辑计划及记录；代码执行者在 `tasks/g0/tmp/worktrees/probe`、分支 `g0-readonly-probe` 工作，只提交指定模块。主 Agent 审核后整合，其他 Agent 不写主 worktree。

## 已执行命令与事实

完整的有限命令输出与退出码见 [preflight.json](preflight.json)。这些记录没有读取或保存用户会话历史、认证内容或环境值。

| 命令/检查 | 实际结果 | 能证明的范围 |
|---|---|---|
| `git status --short`（初始化前） | 输出 not a git repository；组合shell未单独记录该子命令退出码 | 初始目录无Git元数据，不能用Git推断此前修改历史 |
| `codex --version` | exit 0，codex-cli 0.153.4 | 本地CLI版本 |
| `codex app-server proxy --help` | exit 0，存在 `--sock` | 声明的候选端点参数 |
| `codex app-server generate-json-schema --experimental --out tasks/g0/tmp/schema` | exit 0，416份schema文件 | 静态接口；精选内容和原schema hash见下 |
| `codex app-server daemon version` | exit 1，默认 `app-server-control.sock` 不存在 | 当前默认候选服务不可连接；未证明普通codex启动后的行为 |
| Go官方下载元数据及压缩包 | 前两次下载exit 28，分别累计29,933,245和46,083,059/64,626,620字节后超时；拒绝不完整包；第三次断点下载exit 0，SHA-256与官方值相符 | 完整工具链来源/完整性，详见 [toolchain.json](toolchain.json) |
| `tasks/g0/tmp/go/bin/go version` | exit 0，go version go1.26.8 darwin/arm64 | 任务内工具链可运行；未修改全局PATH/安装 |

schema 精选原字段及方法枚举见 [schema-excerpts.json](schema-excerpts.json)：155个ClientRequest、1个ClientNotification、81个ServerNotification。保留 `Thread.id/sessionId`、`TurnStartParams.toolOutput` 等与G0有关字段。完整生成schema属于可重建临时材料。

接口核验的必要发现：

1. Unix socket 使用 WebSocket + HTTP Upgrade，stdio 才明确为 JSONL。`proxy` 帮助没有证明格式转换，因此探针直接采用标准 WebSocket over UDS。[官方协议](https://learn.chatgpt.com/docs/app-server#protocol)
2. `thread/read` 不恢复/加载会话；只读探针固定 `includeTurns:false`，返回数据仍可能带 preview，必须筛选后输出。[官方读取接口](https://learn.chatgpt.com/docs/app-server#read-a-stored-thread-without-resuming)
3. 本地schema的 `server/diagnostics` 提供 `process.id`；它只是与OS创建身份交叉核验的候选。`Thread.id` 区别于共享 `sessionId`，`canAcceptDirectInput` 不证明TUI附着。
4. 公开方法/通知枚举未发现明确的本地 TUI attach/detach 或订阅者映射。这个静态观察不是“绝对没有该能力”的结论；在线正证据仍未建立。
5. 最后订阅者离开及切换会话不保证立刻结束后台thread/触发SessionEnd，所以不能用loaded或SessionEnd单独判断在线。[订阅生命周期](https://learn.chatgpt.com/docs/app-server#unsubscribe-from-a-loaded-thread)、[Hooks SessionEnd](https://learn.chatgpt.com/docs/hooks#sessionend)

## 测试与原生验收分级

当前已执行原生模型回调场景：**0次**。Windows运行场景：**0次**。没有启动替代app-server，没有向个人会话发送测试消息，没有代批Hook，没有安装项目集成条目。

| 验收项 | 状态 | 未验证/阻塞原因 |
|---|---|---|
| C01/C02 | 受阻 | 缺合格隔离测试环境及原生TUI/Hook/thread交叉链；默认daemon不存在 |
| C09/C12 | 未验证（依赖上述环境） | resume/clear/fork/subagent Hook、正常信任/禁用/定义变化未执行 |
| C13 | 未验证 | 探针将限制请求字段；真实前后有效设置和原生审批体验未测 |
| C03–C08/C10/C11 | 未验证（前置功能未放行） | 身份、附着正证据未建立，不进入发送/关闭/恢复/对账实验 |

核心环境条件来自 `docs/g0-validation-plan.md` §1：独立OS测试用户/干净VM、正常已登录CLI，两平台分别验证。原生启动为用户正常 `codex`；基础场景每组合3次，故障屏障20次；本地窗口10秒、模型窗口120秒。此次实施不放宽这些条件。

## 复核与主 Agent 判定

代码复核待最小探针整合及最新测试后执行。审查将分别判定工具是否可交付、G0是否可放行，不允许把合成服务测试通过写成原生回调通过。

目前主 Agent 判定：**不放行 G0.1 / G0**。仍可推进的工作是当前范围内探针测试、证据记录与独立代码复核；完成后需要现成隔离macOS和原生Windows已登录测试环境以继续。

## 文件归属与清理

| 路径 | 用途 | 收尾处理 |
|---|---|---|
| 原始 `docs/*.md` 七份 | 用户原文件 | 原样保留并核对hash |
| `plans/implementation-v0.1.md` | 实施顺序和功能合同 | 交付保留 |
| `docs/implementation/g0/` | 运行/复核证据 | 保留 |
| `tools/g0-probe/` | 可复用Go探针及测试 | 整合后保留 |
| `tasks/g0/tmp/schema/` | 原生schema生成 | 摘要保存后删除 |
| `tasks/g0/tmp/go*` | 本任务工具链下载与校验 | 最新验证完成后删除无用下载和工具链；不安装全局 |
| `tasks/g0/tmp/worktrees/probe` | 唯一代码执行者工作区 | 整合且停止后盘点tracked/untracked/ignored再定点回收 |

实际删除清单、最终验证及复核记录在收尾补齐，尚未完成的清理不宣称完成。
