# Codex Bird Orchestrator

在 **Codex CLI 中直接让 Claude 处理任务**。Codex 负责派发、检查结果和后续决策，Claude 负责执行。

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

实现采用 Go + SQLite，提供持久化任务调度、Host 事件、结果验收及候选版本绑定。

[开发者构建](docs/development-install.md) · [CLI 接口](tools/orchestrator/README.md) · [分层测试](docs/testing.md) · [运行协议](docs/runtime-protocols.md) · [Git 协议](docs/git-cleanup-protocol.md)

私有运行现场、能力文件和原始验收产物不随源码发布；历史文档中的本机证据不一定能从公开检出直接复跑。

## 许可证

[MIT](LICENSE)。预编译包内的 `THIRD_PARTY_NOTICES.md` 和 `licenses/` 保留 Go 及依赖的许可信息。[参考项目记录](docs/reference-projects.md) 单独说明设计参考范围。
