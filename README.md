# Agent Bird

使用 Codex，或通过通用启动入口使用 Claude、Grok、AGY 作为主脑。主脑负责拆分任务、检查结果和验收；执行者按独立任务和预算运行。

```text
$orchestrate 开启 claude
$orchestrate 开启 grok
$orchestrate 开启 agy
$orchestrate 关闭 grok
$orchestrate 将 claude 当前会话模型设为 <model-id>
```

- 写任务强制使用托管的独立 Git worktree；失败现场保留，重试使用新隔离区。
- 开启检查对应 CLI、版本和能力；关闭只停止当前主脑归属范围内的对应委派。
- 模型选择：单次指定 > 当前会话 > 保存默认 > CLI 默认。
- 默认全局并发为 2；结果、ACK 和业务验收分别管理。

## 安装

当前目标为 macOS Apple Silicon，需要已安装的主脑 CLI 和对应 Provider CLI。本仓库当前提供源码，尚未发布新的预编译安装包。

开发者安装需要 Go 1.26+ 和 Python 3，在仓库根目录执行：

```sh
git clone https://github.com/GuanJHCode/Agent-Bird.git
cd Agent-Bird
./scripts/build-plugin.sh
./dist/plugin/install.command
```

安装后保留 `dist/plugin` 来源目录，在目标项目打开新的 Codex 会话，输入上面的 Skill 命令或“用 Claude 处理这个任务：……”。首次使用或二进制发生变化时，确认实际版本指纹。安装不修改 Provider 的认证、第三方配置或权限策略。

不使用 Codex 时，构建后可直接启动主脑，无需运行 Codex 插件安装命令：

```sh
./dist/plugin/agent-bird controller start --provider grok
# --provider 也可使用 claude 或 agy
```

在该 CLI 中让主脑读取 `AGENT_BIRD_SKILL` 指向的说明，再提出委派需求。
此入口按启动进程隔离委派；同一进程内清空或新建原生对话不会改变归属。
四家薄插件的生成入口见 [轻量协作](docs/lightweight-collaboration.md)。生成包内置运行时；其他 CLI 的真实安装与自动 Skill 发现仍待原生验收，不会修改其权限配置。

[使用说明](docs/quick-start.md) · [安装与卸载](docs/installation.md) · [开发与验证](docs/development.md)

## 支持范围

| Provider | 只读分析 | 代码实现 / 候选审查 |
|---|---|---|
| Claude | 支持 | 支持托管 worktree 流程 |
| Grok | 已验证版本的显式只读 profile | 逐文件许可下隔离编码、冻结及行为验收通过；有界完整快照审查通过一次真实错误识别 |
| AGY | 已验证版本的只读 profile | 1.2.5 已通过一次真实隔离编码 / 使用 Claude 审查 |

这是有人看护的技术预览。当前版本不保证中断后的原生会话续接；预算不会自动重置。Grok 结果 artifact 可包含过程说明，不能将其视为纯最终答案；原生 MCP 拒绝的负向验收尚未完成。完整限制见 [支持矩阵](docs/support-matrix.md)。

## 源码与隐私

本仓库只包含源码、可复现测试、合成 fixture 和通用构建说明。不包含个人配置、凭据、运行数据库、原始对话、Provider 会话或历史私有验收目录，也未导入旧仓库 Git 历史。

项目根目录由脚本自身位置推导；路径在执行时解析。系统工具路径和运行时绝对路径校验是安全契约，不是开发者机器路径。请勿提交本机生成的 lock、handle、capability 或模型配置，参见 [发布边界](docs/publication.md)。

[MIT 许可证](LICENSE)。Go 依赖见 [go.mod](tools/orchestrator/go.mod)，使用和再分发时应遵守各依赖许可。
