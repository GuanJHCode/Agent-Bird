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
| 主 Agent | 计划、环境、证据、整合及最后判定 | 当前可推进范围已完成；原生实验受阻 |
| g0_interface_research | 本机帮助/schema及官方接口，只读 | 完成；未连接真实会话 |
| g0_probe_impl | 独立 worktree 内 `tools/g0-probe/` 的最小只读 Go 探针与测试 | 已提交并整合；未实施回调 |
| g0_independent_review | 未参与实现，GPT-6 Astra / max | 三轮复核完成；1项有效问题已修复关闭，最新无未解决问题 |

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

已实现最小工具：[tools/g0-probe](../../../tools/g0-probe/README.md)，独立Go模块，仅支持明确socket/thread的串行白名单读取。首次整合 `46c64df`，最终修复代码 `050af97`（执行者分别为 `a438f61`、`1a0fa3e`）。工具不会发送轮次、注册Hook或派发执行者；成功也只报告 `metadata_only / attached=unknown / g0=not_verified`。

下列全部属于**探针工具的模拟协议或构建验证，不是 C01–C13 原生验收**：

| 验证 | 最新实际结果 | 证据 |
|---|---|---|
| 失败先行 | 执行者先运行行为测试，stub导致exit 1；保留成功/参数场景红灯选集，非编译失败 | [worker-tdd-red.log](worker-tdd-red.log) |
| 首次全套绿灯 | `go test -count=1 ./...` exit 0，10.832s | [worker-tdd-green.log](worker-tdd-green.log) |
| 修复后整合测试 | `go test -race -count=1 ./...` exit 0，15.814s | [verification.json](verification.json) |
| 整合后静态检查 | `go vet ./...` exit 0 | 同上 |
| macOS arm64构建 | `go build -trimpath ...` exit 0 | 同上 |
| Windows amd64交叉编译 | `GOOS=windows GOARCH=amd64 go build -trimpath ...` exit 0；**原生运行未验证** | 同上 |
| 实际macOS二进制拒绝路径 | 无参exit 2；本任务不存在的socket exit 1；stdout空，stderr固定类别 | [binary-checks.json](binary-checks.json) |
| G0R01失败先行回归 | 无效101后保持连接，修前deadline/cancel均约3秒而失败；修后0.40/0.10秒通过 | [红灯](worker-review-fix-red.log)、[绿灯](worker-review-fix-green.log)、[完整命令](worker-review-fix-commands.log) |
| G0R01原始二进制复现复测 | 第9.3秒发送错误101；修后约10.009秒退出timeout，连接/服务已关闭，临时根删除 | [binary-checks.json](binary-checks.json) |

`verification.json` 保留精确命令、Go路径、任务内缓存路径及输出。测试使用真正的Unix WebSocket合成服务，覆盖请求白名单、敏感canary过滤、协议异常、消息/JSON有界、整体deadline及连接关闭；没有调用模型。Go依赖固定为 `github.com/coder/websocket v1.8.15`。

保留的红灯选集命令为 `go test -count=1 -run 'TestReadOnlyTranscriptAndSanitizedEvidence|TestInvalidArgumentsAreBoundedAndNeverReflected' ./...`，在执行者worktree模块目录、与verification记录相同的Go及缓存环境中执行，exit 1，3.156s。该日志对应实现前的stub，不是对当前已实现代码运行的结果。首次全套红灯25.736s来自执行者实时报告；未保留其原始完整输出，不能把选集日志标成全套原始日志。

本地探针产物保留在 `tasks/g0/data/`，hash见binary-checks。它们不是产品v0.1发布包，Windows产物仅为下一次原生验证准备。

首次整合的测试/产物报告另存为 `verification-46c64df.json`、`binary-checks-46c64df.json`，仅作历史证据，不替代上述最后一次代码修改后的验证。

| 验收项 | 状态 | 未验证/阻塞原因 |
|---|---|---|
| C01/C02 | 受阻 | 缺合格隔离测试环境及原生TUI/Hook/thread交叉链；默认daemon不存在 |
| C09/C12 | 未验证（依赖上述环境） | resume/clear/fork/subagent Hook、正常信任/禁用/定义变化未执行 |
| C13 | 未验证 | 探针将限制请求字段；真实前后有效设置和原生审批体验未测 |
| C03–C08/C10/C11 | 未验证（前置功能未放行） | 身份、附着正证据未建立，不进入发送/关闭/恢复/对账实验 |

核心环境条件来自 `docs/g0-validation-plan.md` §1：独立OS测试用户/干净VM、正常已登录CLI，两平台分别验证。原生启动为用户正常 `codex`；基础场景每组合3次，故障屏障20次；本地窗口10秒、模型窗口120秒。此次实施不放宽这些条件。

## 复核与主 Agent 判定

计划及证据初审0项有效问题；代码复核发现G0R01后，已按失败测试→最小修复→最新验证→原独立审查者复核关闭。最终复核新增有效问题0项，最小只读探针可交付；详见 [review.md](review.md)。探针交付与G0放行分别判定。

主 Agent 最终判定：**探针工具通过本次验证与独立复核；G0.1受阻，G0不放行。** 当前范围内无需测试环境即可完成的工作已完成。继续需要独立macOS测试用户/干净VM及原生Windows已登录Codex测试环境的位置或现成连接方式；无需提供认证内容。拿到环境后仍须证明原服务身份链与可失效的TUI附着正证据，再按原规格进入后续实验。G0.2及G1–G4均未实施。

## 文件归属与清理

| 路径 | 用途 | 收尾处理 |
|---|---|---|
| 原始 `docs/*.md` 七份 | 用户原文件 | 原样保留并核对hash |
| `plans/implementation-v0.1.md` | 实施顺序和功能合同 | 交付保留 |
| `docs/implementation/g0/` | 运行/复核证据 | 保留 |
| `tools/g0-probe/` | 可复用Go探针及测试 | 整合后保留 |
| `tasks/g0/data/` | macOS探针和Windows交叉构建产物 | 交付保留，hash及证据范围见binary-checks |
| `tasks/g0/tmp/schema/` | 原生schema生成 | 摘要保存后已删除 |
| `tasks/g0/tmp/go*` | 本任务工具链下载与校验 | 最新验证后已删除下载包、元数据和临时工具链；未安装全局 |
| `tasks/g0/tmp/worktrees/probe` | 唯一代码执行者工作区 | 日志复制保留、缓存删除后，确认tracked/untracked/ignored清洁，普通worktree remove已回收 |

清理已完成，详见 [cleanup.json](cleanup.json)：6类登记材料、9份工作区日志副本已删除；原工作tree和带expected OID的本任务分支引用已定点回收。`tasks/g0/tmp/` 已不存在，`/tmp/g0-probe-*` 遗留0项。源码、可复用测试、两份探针产物、原始文档和所有必要报告保留。没有全仓clean/prune/GC、没有删除共享Git objects、没有push或发布。
