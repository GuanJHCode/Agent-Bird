# Agent Bird macOS 1.0 candidate

这是 macOS arm64 候选包，不是正式发布。请同时读取本次 macos-handoff.md 的真实验收和未通过项。
Go 核心负责调度、owner 校验、沙箱、结果、超时与退出证明；Python 只负责安装和各发起CLI的薄入口。

## 依赖及安装

需要 macOS arm64、Python 3.10+、Git，以及已安装并登录的目标 Provider CLI。
本次开发验收版本：Claude 2.1.296、Grok 1.0.50 (c58f321264ba)、AGY 1.3.3；
Codex 目标严格固定 0.154.0 的已确认 arm64 binary（最新 Codex 可以作为发起方）。
安装器不下载 Provider、不登录、不刷新认证、不改全局配置，不替换你已有的插件。

先用交付记录中的 SHA-256 核对 tar.gz，再解压；包内 candidate-checksums.json 只证明文件自洽，不能单独证明来源。
解压后在包目录运行（将路径替换为你本机已有的真实可执行文件）：

```sh
python3 agent-bird.py setup --runtime "$HOME/.bird1" \
  --provider-binary "claude=$HOME/.local/bin/claude" \
  --provider-binary "grok=$HOME/.local/bin/grok" \
  --provider-binary "codex=$HOME/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex"
python3 agent-bird.py check --runtime "$HOME/.bird1"
```

`--runtime` 必须是全新的、归当前用户所有的短路径；不会覆盖现有目录。
Provider 参数可按需提供；AGY 可追加 `--provider-binary "agy=$HOME/.local/bin/agy"`。
setup 先核对候选包文件清单，将两个外围启动器复制到 runtime/adapters 并复核摘要。安装后可使用 `python3 "$HOME/.bird1/adapters/agent-bird.py"` 代替下文的 `python3 agent-bird.py`；删除解压目录不影响运行。
setup 会校验所选CLI版本、摘要和帮助参数，仅对支持的目标生成私有 pin；这不证明账号可用。
check 检查安装与 PATH 依赖，也不发起登录或模型请求。发起方CLI需在 PATH 中。
缺少 pin 的目标会明确失败，不改用其他 Provider。

## 从任意 Provider 发起调用

下面每条都实际启动发起方CLI并加载 Agent Bird MCP 插件。请使用绝对项目路径。
四方调用同一个 `agent_bird_call`，参数都是 provider、task、directory、mode、paths、timeout_ms。

```sh
python3 agent-bird.py start --runtime "$HOME/.bird1" --provider codex \
  --directory /absolute/project --prompt '调用 Claude 读取 README.md，返回摘要。'
python3 agent-bird.py start --runtime "$HOME/.bird1" --provider claude \
  --directory /absolute/project --prompt '调用 AGY 修改 docs/note.md；mode=workspace-write，paths=["docs/note.md"]，返回结果。'
python3 agent-bird.py start --runtime "$HOME/.bird1" --provider agy \
  --directory /absolute/project --prompt '调用 Grok 读取 README.md，分析一个问题并返回结果。'
python3 agent-bird.py start --runtime "$HOME/.bird1" --provider grok \
  --directory /absolute/project --prompt '调用 Codex 修改 answer.txt；mode=workspace-write，paths=["answer.txt"]，完成后读取确认并返回结果。'
```

AGY 目前缺少可用账号，上述涉及 AGY 的命令尚未完成真实验收。Codex 目标通过固定 0.154.0 app-server 和 Host 文件工具读写；新调用使用兼容模型 gpt-5.6-sol，不改变发起方或全局模型配置。
Grok 使用本进程 ACP plugin-dir，不写全局 trusted_folders；Codex/Claude 用进程级 MCP配置，
AGY 用私有发起工作区的 MCP 配置和指定项目附加目录。不会覆盖项目已有 MCP 配置。

一次一个子任务，不递归。默认子任务120秒，可传timeout_ms（最长1小时）；
start 的发起方总预算默认300秒，可用 --timeout-seconds 调整至7200秒以内。
Ctrl-C 取消当前发起流程；是否已退出必须以结果 process_exited 和持久task状态为准。

## 修改结果和失败

写任务要求项目是已有Git仓库，且 paths 为明确相对文件路径。修改在隔离候选工作目录完成，
结果返回 workspace、changed_files、changes_known、handle；不会自动覆盖原项目的用户修改。
请在返回 workspace 中检查修改，通过现有 task accept / integration 流程接纳后再集成。Codex 文件工具每文件最多32KiB、每次写任务累计512KiB，路径必须逐文件列明；不提供 shell/构建命令工具。
第一版不支持通配路径、自动合并、Codex命令执行/续接、递归或并发子任务、自动重试、失主恢复，
也未开放 filtered compose/rework/retry/continuation、完整 LFS/hooks/signing。

失败返回 failure_reason；changes_known=false 表示可能已发生部分修改，不能把空列表当作无修改。
process_exited=false 表示尚无可信终态证明。未知运行保留 handle 和 active journal，不能自动重跑；
先通过现有 task inspect / stop 确认原任务。已证明退出的普通失败可重新发起。
原Host丢失的历史Wait不会被重建或伪造。

## 本地构建

在固定交付SHA的干净产品仓库执行：

```sh
sh tasks/macos-1.0-20261012/scripts/package-candidate.sh \
  /absolute/go1.26/bin/go /absolute/canonical/python3 /absolute/new/output
```

输出包含插件payload、启动器、安装说明、平台能力JSON、文件校验表和tar.gz。
codeSHA来自干净产品HEAD；平台hash为固定JSON序列化加domain前缀后SHA-256，详见交接文档。
不创建tag、不上传GitHub Release。
