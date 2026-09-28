# 安装与更新

## 首次安装：三步

前提：macOS Apple Silicon（M 系列芯片），终端中 `codex` 和 `claude` 已能正常使用。原生 Codex 插件命令已在 Codex CLI 0.154.0 上验证。

1. [下载 macOS 安装包](https://github.com/GuanJHCode/Codex-Bird-Orchestrator/releases/download/v0.1.0-preview.1/codex-orchestrator-macos-arm64.zip)。解压到准备长期保留的目录，例如 `~/Applications/Codex-Bird`。
2. 双击解压目录中的 `install.command`。它会用 Codex 自带命令注册 marketplace 并安装插件。
3. 在目标项目新开 Codex CLI 会话，输入“用 Claude 处理这个任务：……”。

不要下载 GitHub 的 Source code (zip/tar.gz) 作为安装包。用户无需 Go、Python、编译代码或手写请求。解压目录仍是插件来源，**安装后请保留该目录**。

[全部版本](https://github.com/GuanJHCode/Codex-Bird-Orchestrator/releases)提供安装包及 `SHA256SUMS`。需要校验时，将两个文件放在同一目录并执行：

```sh
shasum -a 256 -c SHA256SUMS
```

版本对应：发布标签 `v0.1.0-preview.1`，插件及运行时 `0.1.0-preview.1`。目前没有 Homebrew formula，也未提供 Intel Mac、Windows 或 Linux 安装包。

## 常见安装问题

- **找不到 codex 命令**：在能正常运行 `codex` 的终端中执行 `sh "/完整路径/install.command"`。
- **macOS 提示无法确认开发者**：当前包未签名或公证。确认下载源和校验值后，按 macOS 的系统提示处理；不要关闭系统整体安全检查。
- **安装后没有自动派发**：新开 Codex 会话，明确说“使用 codex-orchestrator 插件，让 Claude 处理……”。`codex plugin list` 应显示 installed、enabled。
- **不认识 plugin 子命令**：先使用支持原生插件的 Codex CLI，再安装本插件。
- **开发任务报 Git execution extension**：当前候选开发链不支持 Git LFS filter、自定义 hooks、fsmonitor 等执行扩展，全局或系统配置也可能触发拒绝。插件不会自动删除这些配置；只读分析与候选开发是不同能力。需要保留这些扩展的项目暂不使用自动候选集成。

## 更新

本机已安装当前版本且使用正常时无需重装。以后更新前，先让 Codex 检查现有任务是否仍在运行或处于未知状态；不要删除旧插件源或运行时来强行升级。

若新版本仍使用同一 marketplace 源目录，按对应版本说明更新该目录后运行安装命令，并新开 Codex 会话。若下载到了另一目录，先核对 `codex plugin list` 中的来源，避免误从旧来源重装。跨运行时协议版本的后台服务升级仍需核对现场；安装脚本不会强杀服务、恢复旧任务或扩大预算。

## 卸载

通过 Codex 的插件管理移除 `codex-orchestrator@codex-bird`。运行历史和旧版本仍可能被任务引用；移除插件不代表这些数据可以删除。先确认没有活动或待恢复任务，再按[安装生命周期协议](git-cleanup-protocol.md#6-安装升级和卸载)处理保留数据。

安装不会修改 Claude 账号、第三方模型配置，也不会放宽原生权限。开发者构建和旧完整运行时的操作见[开发者安装](development-install.md)。
