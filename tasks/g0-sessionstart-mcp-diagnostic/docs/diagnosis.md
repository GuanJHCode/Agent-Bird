# sd07：首输入前等待 SessionStart 构成循环门禁

首要分支已经由固定源码闭合：0.154.0 普通全新会话先排队 pending SessionStart，真正调用只发生在执行 turn 时。sd07 零初始输入，因此“SessionStart → 首次输入”的门禁方向错误；本次无需把额度或 MCP 文本定为根因，也没有重试 sd07。

最小非模型探针已执行：只读扫描官方固定提交 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 完整 archive 的 3792 个 Rust 文件，查找 `run_pending_session_start_hooks` 与 `queue_pending_session_start_source`。结果只有 [Session::new:1639 排队](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/session.rs#L1639)，以及 [run_turn:287](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn.rs#L287) /同函数 :545 的实际调用；另有定义/import/compact 排队。没有会话创建后、首个 turn 之前的自动执行调用点。[探针收据](offline-sessionstart-probe.json)

普通启动的顺序是：SessionConfigured 事件 → 安装 MCP runtime、验证 required servers → 安排 startup prewarm → 排队 SessionStart → 返回 session、启动等待输入的 submission_loop。收到 `Op::TurnInput` 后才启动 RegularTask/run_turn；run_turn 先解析所需 MCP、capture step、构建 skills/plugins，再运行 SessionStart，随后 UserPromptSubmit/输入记录。startup prewarm 仅建立 tool snapshot 和预热连接，不调用 SessionStart。[初始化](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/session.rs#L1605)、[输入分派](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/handlers.rs#L589)、[turn 内前置步骤](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn.rs#L232)

sd05/06 的受限 PTY 包含 `Starting MCP servers`、`cua_repl`、`node_repl`；sd07 的 6602 字节归档没有这三个原始字符串。三次均无 initial-submit/binding/send-attempt 文件。此差异不证明 sd07 UI 已就绪，却足以提醒不能把前两次文字状态直接套作第三次原因。旧报告“这些文字解释了缺失 SessionStart”的因果表述证据不足；保留原始事实，不改旧 case。[精简对比](case-comparison.json)

当前声明中，`node_repl` 来自全局 MCP 配置，`cua_repl` 来自 unified-computer-use 插件；两者 `startup_timeout_sec=120`，声明的命令及核对的依赖路径存在。插件的 cua-repl launcher 会读取 `CUA_REPL_NODE_REPL_PATH`，启动 node_repl 子进程并继承 stdio，所以二者可能共享 runtime 依赖。此处只保留 server 名、命令路径、超时、env key 名和路径存在性；未输出 env 值、token 或其他配置。[白名单收据](plugin-startup-whitelist.json)

MCP 可以在输入已经到达后延迟 SessionStart：required server 验证会等待连接；构建 tool catalog 也会按 required/cache/optional grace 选择等待。配置的 `startup_timeout_sec` 分别用于客户端建立、initialize 和初始 tools/list，不能把它视作整个 CLI 启动绝不超过 120 秒的承诺。但是现有证据没有说明哪一阶段曾卡住，也没有证明 native pipe 或 desktop bridge 故障；命令/路径存在只排除了对应路径缺失。[required 等待](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/codex-mcp/src/connection_manager/required.rs#L15)、[初始化与目录读取](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/codex-mcp/src/rmcp_client.rs#L925)

下一候选应消除这个循环前置条件，而不是再次等待 SessionStart、延长窗口或改 MCP。原生 positional initial prompt 的源码核验见 [候选说明](positional-prompt-candidate.md)。本诊断未启动服务/TUI/模型/RPC，未改账号、配置、插件、链接或源码；只更正审查结论并保存证据。
