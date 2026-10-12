# Agent Bird

Agent Bird 让 **Codex、Claude、AGY、Grok 平等地调用彼此**。任意 Provider 都可以发起任务，通过同一个 `agent_bird_call` 插件工具启动目标 Provider，执行任务并取得结果。没有固定主脑，也不要求先运行 Codex。

当前版本：**1.0.0-rc.2，macOS Apple Silicon 预发布版**。每次串行执行一个子任务，不递归委派。

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

安装器校验包文件和所选 CLI 的版本、摘要、能力，仅为支持的目标生成私有 pin。缺少 pin 的目标明确失败；`check` 不验证账号资格。发起方 CLI 必须在 PATH 中。

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

只读为默认模式。写任务要求已有 Git 仓库、明确的相对文件路径；修改在隔离候选工作目录完成。结果返回 `workspace`、`changed_files`、`changes_known`、`handle` 和 `process_exited`，**不会自动覆盖原项目**。检查返回的候选目录后，通过已有任务验收/集成流程接纳修改。

默认子任务超时 120 秒，可通过 `timeout_ms` 调整，最大 1 小时。发起方预算默认 300 秒，`--timeout-seconds` 可调至 7200 秒。Ctrl-C 取消；只有 `process_exited=true` 才表示已有可信退出证明。失败且 `changes_known=false` 时，候选目录可能已有部分修改。

## 支持范围

| 项目 | 当前状态 |
|---|---|
| Codex、Claude、Grok 发起和被调用 | 已有真实插件调用验证 |
| AGY 入口 | 已实现；当前缺少可用账号，成功链路未验收 |
| 已验证方向 | Codex→Claude、Claude→Grok、Codex→Grok、Grok→Codex、Grok→Claude |
| Claude→Codex | 统一路由支持；最终模型修复后未单独重测 |
| 文件修改、失败返回、超时退出 | 已真实验证；不代表全部 12 个方向都已实测 |
| Native TUI、Linux | 本版本不作验收声明 |

本次使用的目标版本：Claude 2.1.296、Grok 1.0.50 (c58f321264ba)、AGY 1.3.3；Codex 目标严格固定 0.154.0 arm64 及其二进制摘要，使用该版本实际提供的 `gpt-5.6-sol`。较新的 Codex 可以作为发起方。模型和账号的后续可用性仍由 Provider 决定。

Codex 通过 app-server 和受约束文件工具执行，不修改 Codex 源码。其文件工具支持 UTF-8 文件，每文件最多 32KiB、每次累计写入 512KiB；不提供 shell/构建命令工具。

暂不支持自动合并、递归或并发子任务、自动重试、失主自动恢复、Codex 续接、通用 filtered compose/rework/retry/continuation，以及完整 LFS/hooks/signing。超时不是完成：已有 Claude 编码尝试因超时结束，未计入成功编码验收。

这是候选版本，AGY 账号及其双向真实验收尚缺；不宣称四方正式 1.0 全部验收完成。

## 从源码构建

需要 Go 1.26+、Python 3.10+，在 macOS arm64 的干净 checkout 执行：

```sh
./scripts/build-release.sh 1.0.0-rc.2 "$PWD/dist/release"
```

可用 `GO=/absolute/path/to/go` 指定 Go 工具。输出包括安装包、SHA256SUMS、README、许可证及平台能力元数据。构建会记录实际源码 SHA；不会自动发布 GitHub Release。

Go 核心位于 `tools/orchestrator`，插件和兼容运行代码位于 `plugins`、`runtime`。`tasks` 仅保留打包或测试所需代码；设计文档、实施计划、历史报告和运行日志不包含在发布树中。原开发分支仍保留历史交接记录。

```sh
cd tools/orchestrator
go build ./...
go test -count=1 ./cmd/orchestrator -run 'TestProviderCall|TestProviderProjection|TestCodexPluginWrite'
```

## 卸载与许可

先结束由该 runtime 发起的会话，确认任务退出，保存需要的候选目录和结果，再删除自己创建的 runtime 目录。不要直接删除仍有运行任务或未知进程状态的 runtime。

项目使用 [MIT License](LICENSE)。再分发依赖说明和许可文本见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 与 `licenses/`。Provider CLI 由用户自行安装，发行包不包含其二进制或账号信息。
