# Agent Bird

Agent Bird 让 **Codex、Claude、AGY、Grok 平等地调用彼此**。任意 Provider 都可以发起任务，通过同一个 `agent_bird_call` 插件工具启动目标 Provider，执行任务并取得结果。没有固定主脑，也不要求先运行 Codex。

已发布版本：**1.0.0-rc.2**；本分支为 **1.0.0-rc.3 macOS Apple Silicon 候选版**，尚未更新 GitHub Release。每次串行执行一个子任务，不递归委派。

## 安装

从 [GitHub Releases](https://github.com/GuanJHCode/Agent-Bird/releases) 下载 `agent-bird-1.0.0-rc.2-macos-arm64.tar.gz` 和 `SHA256SUMS`，在下载目录校验后解压：

```sh
shasum -a 256 -c SHA256SUMS
tar -xzf agent-bird-1.0.0-rc.2-macos-arm64.tar.gz
cd agent-bird-1.0.0-rc.2
```

需要 Python 3.10+、Git，以及已安装并登录的 Provider CLI。按实际需要指定目标 CLI：

```sh
python3 agent-bird.py setup --runtime "$HOME/.bird1" \
  --provider-binary "claude=$HOME/.local/bin/claude" \
  --provider-binary "grok=$HOME/.local/bin/grok" \
  --provider-binary "codex=$HOME/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
python3 "$HOME/.bird1/adapters/agent-bird.py" check --runtime "$HOME/.bird1"
```

替换为本机已有可执行文件的路径。`--runtime` 必须是全新、较短的规范绝对路径，安装不会覆盖已有目录。有可用 AGY 账号时，可在 setup 中追加 `--provider-binary "agy=$HOME/.local/bin/agy"`。

安装器校验包文件和所选 CLI 的版本、摘要、能力，仅为支持的目标生成私有 pin。rc.3 在创建 runtime 前校验全部参数并探测所选目标，仍可仅配置一个目标；安装时再次核对摘要。缺少 pin 的目标明确失败。rc.3 的 `check` 显示当前 pin 是否变化、平台构建身份及有上限的磁盘/残留目录统计；这些只读信息不授权自动清理，也不验证账号资格。发起方 CLI 必须在 PATH 中。

安装不下载 Provider、不修改真实账号、全局配置或已有插件。启动器保存在 runtime/adapters，安装成功后无需保留解压目录。

## 使用

每条命令都会启动相应发起方 CLI，加载本进程的 Agent Bird 插件，再实际调用目标 Provider。使用绝对项目目录。

```sh
# Codex → Claude
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider codex --directory /absolute/project \
  --prompt '调用 Claude 读取 README.md，返回摘要。'

# Claude → Grok
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider claude --directory /absolute/project \
  --prompt '调用 Grok 分析 README.md 中的问题，返回结论。'

# Grok → Codex
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider grok --directory /absolute/project \
  --prompt '调用 Codex 修改 answer.txt；mode=workspace-write，paths=["answer.txt"]，完成后读取确认。'

# AGY → Claude（需要可用 AGY 账号）
python3 "$HOME/.bird1/adapters/agent-bird.py" start --runtime "$HOME/.bird1" \
  --provider agy --directory /absolute/project \
  --prompt '调用 Claude 审查 README.md，返回建议。'
```

调用 AGY 时将任务中的目标改为 AGY，并确保 setup 已配置其目标 pin。四个入口使用同一协议：`provider`、`task`、`directory`、`mode`、`paths`、`timeout_ms`。

rc.3 优化（构建候选版可用）：插件结果默认返回最多 4KiB 的 UTF-8 摘录，并保留失败、退出和文件变更事实。`result_truncated` 表示是否截断，`result_bytes` / `result_sha256` 标识完整结果；需要详情时调用 `agent_bird_result`，传 `call_id`、`offset=next_offset`，可选 `limit_bytes`（默认 4096，最大 16384）。详情来自同一 MCP 连接的最近一次任务，下一任务开始或连接关闭后失效；读取详情不会重新执行任务。原始完整结果仍由任务 handle 保留。CLI `call` 的完整输出不变。

Claude 发起方仅暴露委派所需工具，关闭本进程的技能列表和工具搜索，直接调用 MCP；其余账号、模型、权限和全局配置不变。四个 Provider 统一使用简短委派指令，优先返回结论，不自动读完所有详情。这些优化尚未包含在已发布的 rc.2 安装包中；可从本分支构建 rc.3，安装到全新 runtime。

只读为默认模式。写任务要求已有 Git 仓库、明确的相对文件路径；修改在隔离候选工作目录完成。结果返回 `workspace`、`changed_files`、`changes_known`、`handle` 和 `process_exited`，**不会自动覆盖原项目**。检查返回的候选目录后，通过已有任务验收/集成流程接纳修改。

默认子任务超时 120 秒，可通过 `timeout_ms` 调整，最大 1 小时。发起方预算默认 300 秒，`--timeout-seconds` 可调至 7200 秒。Ctrl-C 取消；只有 `process_exited=true` 才表示已有可信退出证明。失败且 `changes_known=false` 时，候选目录可能已有部分修改。

派发/返回优化保留原有保护：同次调用复用已验证的能力探测；新协调器通过 `wait_call_terminal_v1` 等待已提交的进程终态，旧协调器回退到原状态查询。Host 离线但尚无退出证明时返回 `call_process_unknown`；已有可信退出不会因 Host 随后断开而丢失。只读结果查询在单次串行调用内绑定协调器 epoch，后续省去重复 Ready；协调器变化、响应错配或传输失败直接返回，不重发请求。停止操作保留原路径。完整结果的续页复用最多 1MiB 的已验证内存快照，每页仍核对所有权、事件和产物有效性；从 offset 0 开始读取会重新校验磁盘。

## 支持范围

| 项目 | 当前状态 |
|---|---|
| Codex、Claude、Grok 发起和被调用 | 已有真实插件调用验证 |
| AGY 入口 | 已实现；当前缺少可用账号，成功链路未验收 |
| 已验证方向 | Codex→Claude、Claude→Grok、Codex→Grok、Grok→Codex、Grok→Claude、Claude→Codex |
| Claude→Codex | rc.3 已通过真实读取和同一 call 的详情回读 |
| 文件修改、失败返回、超时退出 | 已真实验证；不代表全部 12 个方向都已实测 |
| Native TUI、Linux | 本版本不作验收声明 |

本次使用的目标版本：Claude 2.1.296、Grok 1.0.50 (c58f321264ba)、AGY 1.3.3；Codex 目标严格固定 0.154.0 arm64 及其二进制摘要，使用该版本实际提供的 `gpt-5.6-sol`。较新的 Codex 可以作为发起方。模型和账号的后续可用性仍由 Provider 决定。

Codex 通过 app-server 和受约束文件工具执行，不修改 Codex 源码。其 UTF-8 文件工具支持片段读取和单文件字面搜索（文件上限 1MiB），整文件读取和写入仍限 32KiB，每次累计写入 512KiB；不提供 shell/构建命令工具。Claude、Grok、AGY 继续使用各自已有的受约束原生文件工具，派发协议不依赖特定主脑。

目标 Codex 可用 `search_file(path, query, context_bytes=128)` 定位最多 20 个匹配，返回字节偏移、行号和受限上下文（可选 4–256 字节/匹配）；用 `read_file(path, offset, limit_bytes)` 读取片段（默认 4096、范围 4–16384 字节，保持 UTF-8 完整）。每次都返回完整文件 SHA256；续读传 `expectedSHA256` 可拒绝文件变化。编码任务优先用 `replace_text(path, expectedSHA256, old_text, new_text, verify_bytes=128)`，仅接受唯一精确匹配；可选 4–1024 字节的实际写后回读，`verified=true` 才代表回读摘要与持久化凭据一致，无需为同一确认再调用读取。仍受声明路径、隔离候选目录、原写入预算和持久化凭据约束。工具清单变化会改变原有 `HostToolContractSHA256`；旧工具契约不能冒充新契约续接，数据库 schema 保持 41。

普通委派只加载约 3KiB 的技能入口；高级 CLI 契约保存在按需引用的 `legacy-cli.md`。调用阶段耗时写入私有 handle 旁的 `call-metrics.json`，也可见于 CLI 的 `timings_ms`，不会进入模型的紧凑结果。指标只用于定位开销，不作为退出或成功证明。

暂不支持自动合并、递归或并发子任务、自动重试、失主自动恢复、Codex 续接、通用 filtered compose/rework/retry/continuation，以及完整 LFS/hooks/signing。超时不是完成：已有 Claude 编码尝试因超时结束，未计入成功编码验收。

这是候选版本，AGY 账号及其双向真实验收尚缺；不宣称四方正式 1.0 全部验收完成。已由 Agent Bird controller 启动的会话可以继续使用本连接的 MCP；任意外部既有会话的热挂接需要新的身份绑定协议，本版未开放。

## rc.3 本轮验证

- 局部文件工具已通过真实 Claude→Codex 插件修改：22,525 字节文件先搜索定位，两次各读取 17 字节，使用 `replace_text` 完成一次精确替换，结果为 `result_ready`、`process_exited=true`。已核对候选文件、0600 持久化写入凭据及原项目未变；整条调用约 63 秒，不据此宣称固定 token 或速度提升比例。相关定向 race、参数边界测试及独立审查通过。
- 新包安装、依赖检查通过；Claude→Codex 通过真实插件读取项目并调用 `agent_bird_result` 回读同一任务结果，SHA 一致，未重复启动目标。Claude 初始化工具为 2 个、技能为 0 个，无 ToolSearch 轮次。
- Claude→Grok 的完整长文本回显在 120 秒超时，返回 `call_timeout`、`process_exited=true`；发起方停止，未重试。该用例不算长结果成功验收。大结果截断、UTF-8 分页、跨连接隔离由定向测试验证。
- 本轮有针对性的单元和 race 回归通过；原发布版的 `TestSlotFreedOnOneHostDispatchesTaskStarvingOnAnotherHost`、`TestServerRoutesControlToBoundHostAndDurablyCollects` 在本机 Host 注册阶段出现 EOF，已在未修改的 `131949c` 上复现，未计入通过。未重复全量测试或长时间压力测试。
- 上表其他方向复用既有真实证据，不表示全部方向在 rc.3 重新实测。AGY 成功链路仍缺账号；未新增 native TUI 或 Linux 验收。

## 从源码构建

需要 Go 1.26+、Python 3.10+，在 macOS arm64 的干净 checkout 执行：

```sh
./scripts/build-release.sh 1.0.0-rc.3 "$PWD/dist/release"
```

可用 `GO=/absolute/path/to/go` 指定 Go 工具。输出包括安装包、SHA256SUMS、README、许可证及平台能力元数据。构建会记录实际源码 SHA；不会自动发布 GitHub Release。

Go 核心位于 `tools/orchestrator`，插件和兼容运行代码位于 `plugins`、`runtime`。`tasks` 仅保留打包或测试所需代码；设计文档、实施计划、历史报告和运行日志不包含在发布树中。原开发分支仍保留历史交接记录。

```sh
cd tools/orchestrator
go build ./...
go test -count=1 ./cmd/orchestrator -run 'TestProviderCall|TestProviderProjection|TestCodexPluginWrite'
```

普通功能分支的 CI 按改动选择检查；Go 包包含反向依赖及测试依赖。共享契约、工作流、未知/删除路径、main/master 和无法确定 diff 时保留全量检查，README-only 改动仅做 patch 检查。

## 卸载与许可

先结束由该 runtime 发起的会话，确认任务退出，保存需要的候选目录和结果，再删除自己创建的 runtime 目录。不要直接删除仍有运行任务或未知进程状态的 runtime。

项目使用 [MIT License](LICENSE)。再分发依赖说明和许可文本见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 与 `licenses/`。Provider CLI 由用户自行安装，发行包不包含其二进制或账号信息。
