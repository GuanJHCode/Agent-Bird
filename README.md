# Codex Bird Orchestrator

在 CLI 中通过 **Agent Bird** 派发独立任务、检查候选结果并明确验收。

**版本边界：** 下方公开下载仍为 `v0.1.0-preview.1`，没有发布或替换全局安装。2026-11-09 私有 `.4` 包已完成 PKG-01～04 独审/root验收，短引用源码及离线回归见[当前交接](tasks/cross-cli-hardening-20261105/docs/final-handoff.md)。历史 `.3` 两方向真实 live 均未通过，新 `.4` 未运行真实模型试验，产品核心仍 BLOCKED。managed owner 仅为 `managed_process`，通过 wait/collect 返回，不证明原生聊天绑定或 native push。

> 用 Claude 检查这个项目的代码，列出问题和改进建议。

## 下载与使用

当前提供 **macOS Apple Silicon（M 系列芯片）预览版**，需要已能正常使用的 Codex CLI 和 Claude CLI。无需 Go、Python，也不用另开 Claude 会话。

1. [下载安装包](https://github.com/GuanJHCode/Codex-Bird-Orchestrator/releases/download/v0.1.0-preview.1/codex-orchestrator-macos-arm64.zip)，解压到准备长期保留的目录。
2. 双击其中的 **`install.command`**，完成一次插件安装。
3. 在目标项目打开一个**新的 Codex CLI 会话**，直接输入“用 Claude 处理这个任务：……”。

[所有版本与 SHA256 校验文件](https://github.com/GuanJHCode/Codex-Bird-Orchestrator/releases) · [安装问题](docs/installation.md) · [使用示例](docs/quick-start.md)

解压目录是插件来源，安装后请保留。尚未提供 Homebrew 包；请下载上面的安装包，不要选择 GitHub 自动生成的 Source code 压缩包。

## 可以怎样使用

- **分析**：“用 Claude 比较这个项目与……，给出优缺点和源码依据。”
- **开发**：“用 Claude 实现……，只修改……；完成后测试、审查。”
- **反馈**：“给刚才的任务补充要求：……，沿原任务返工并复审。”
- **查进度**：“查看刚才 Claude 任务的状态和已有进度。”

未自动触发时，明确说“使用 codex-orchestrator 插件，让 Claude 处理……”。首次使用或 CLI 版本变化时，Codex 可能要求确认版本；正常工具审批仍然保留。安装不修改 Claude 账号、第三方模型或权限配置。

## 当前边界

这是有人看护的技术预览。已验证自然语言派发、真实 Claude 返回与进度记录，也有同候选测试、审查、返工和集成的真实试验；不保证所有复杂任务无人干预完成。

- 结果返回、进程退出0和 ACK 不等于验收通过。主脑可能拒绝结果，因此任务会显示失败。
- 中间进度有数量和长度限制，不是完整日志；超时或预算耗尽会停止，不自动加预算重跑。
- 某些中断任务暂不支持原生续接。主脑应先核对原任务，再决定后续操作。
- 候选开发暂不支持 Git LFS filter、自定义 hooks 等 Git 执行扩展，包含全局配置；插件会拒绝，不自动修改用户 Git 配置。
- 当前安装包仅支持 macOS arm64；Intel Mac、Windows、Linux 尚未提供对应用户包。

详细证据和未验证范围见 [支持矩阵](docs/support-matrix.md)。

## 开发与协议

实现采用 Go + SQLite，提供持久化任务调度、Host 事件、结果验收及候选版本绑定。各 Provider 的 CLI 试用窗口是冻结的已发布稳定版区间，不是单一版本常量；见 [provider-runtime](docs/provider-runtime.md) 与 [支持矩阵](docs/support-matrix.md)。版本窗口不等于真实模型或 coding 验收；Codex 执行还需要 exact pin、已安装 receipt 和完整 native runtime。

[开发者构建](docs/development-install.md) · [CLI 接口](tools/orchestrator/README.md) · [分层测试](docs/testing.md) · [运行协议](docs/runtime-protocols.md) · [Git 协议](docs/git-cleanup-protocol.md)

私有运行现场、能力文件和原始验收产物不随源码发布；历史文档中的本机证据不一定能从公开检出直接复跑。

## 许可证

[MIT](LICENSE)。预编译包内的 `THIRD_PARTY_NOTICES.md` 和 `licenses/` 保留 Go 及依赖的许可信息。[参考项目记录](docs/reference-projects.md) 单独说明设计参考范围。

## 当前源码的多 CLI 入口

当前源码支持 `controller start`、`routing`、`task dispatch/wait/reconcile`，以及
`inspect/collect/request-changes/accept/ack/stop`；本轮候选另有 `mcp configure/serve` 的 8 个普通工具（非 Tasks）。`reconcile` 仅用于保留原 control receipt 的 `submitting`/`submit_uncertain` handle 或 `reconciled` 重放；正常 queued 提交无需 reconcile。取消先用 `task inspect --handle H` 观察 task_id/work_revision，再调用 `task stop --handle H --task-id <observed-task-id> --work-revision <observed-work-revision>`，并检查执行树确已终止。历史下载包不自动获得这些能力。
安装后使用该版本的 `scripts/invoke.sh` 绝对路径；无需 `PLUGIN_ROOT`。
Claude、Grok、AGY 的 owner 是受控主进程作用域，同一进程内多个原生聊天共享此作用域。
Worker 不能再次派发。已有 handle 保留原 runtime 路由，升级不能改绑为“最新任务”。

完整 native 包可通过 [私有包装 smoke](tasks/cross-cli-subagent/scripts/smoke-installed-package.sh)
在任务目录实际构建、安装和 fixture 验证，不修改全局账户或 CLI 配置。
[入口生成器](tasks/cross-cli-subagent/scripts/build-native-entries.py) 以
`--installed-root <private-dest>/versions/<version> --output <new-entry-dir>`
生成 Codex/Claude/Grok/AGY Skill 入口。入口绑定当前安装的绝对 wrapper，不复制 receipt-bound binary。
保留该安装及固定 Python 路径；移动或修改后需重新正式安装、重新生成入口。
这些入口文件的生成不等于各 CLI 已加载插件，需显式加载所需 Skill，且不能据此声称已通过原生 UI 验收。

写任务限定 managed candidate：Claude gateway 仅 exact 2.1.267/2.1.316，Codex 仅 exact 0.166.0。
AGY/Grok 继续只读。能力 probe 不执行模型；fixture、版本 catalog 与 CLI 帮助也不证明真实 coding 成功。
主审从 `inspect` 读取 revision、candidate 和完整 reviewable tuple，使用 `request-changes`
提交反馈，后续 revision 使用 fresh invocation；`accept` 仍需完整 event tuple，结果投递 ACK 独立。
