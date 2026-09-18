# 安装与卸载

当前源码支持 macOS；用户包以 Apple Silicon 为首要目标。准备 Go 1.26+、Python 3、选用的主脑 CLI，以及要使用的 Claude、Grok 或 AGY CLI。Provider 的安装和登录由用户自行完成，项目不提供账号或密钥。

从仓库根目录运行：

```sh
./scripts/build-plugin.sh
./dist/plugin/install.command
```

构建脚本从自身位置定位项目，生成 `dist/plugin`，不会把开发者目录写进源码。输出目录必须不存在；再次构建可选择新的版本和输出目录：

```sh
./scripts/build-plugin.sh 0.1.0-dev.2 ./dist/plugin-dev2
```

`install.command` 通过 Codex 官方 plugin/marketplace 命令注册生成的本地目录。安装后保留来源目录，并新开 Codex 会话加载 Skill。首次实际使用会将运行时安装到系统用户数据目录中的独立版本目录。

当前仓库尚未发布这一版预编译 Release，不要把源码压缩包当成已构建的安装包。

## 四种 CLI 的统一安装包（预览）

维护者可以一次构建 macOS Apple Silicon 安装包：

```sh
./scripts/build-release.sh 0.2.0-preview.4 ./dist/release-preview4
```

输出包含 `agent-bird` 目录、压缩包、`SHA256SUMS` 和固定下载摘要的
`agent-bird.rb`。用户解压后，在该目录运行：

```sh
./agent-bird install codex   # 也可选 claude、grok、agy
```

安装器先检查对应 CLI 和包内容，将运行文件保留在用户数据目录，再调用该
CLI 的原生插件安装命令。它不启用自动信任或跳过权限确认。同名 Marketplace
指向其他来源时会停止，保留原安装；不同来源的自动替换仍会拒绝。Codex 旧插件迁移使用下面的独立新身份，不删除编排状态。

安装 Claude、Grok 或 AGY 插件后，当前可靠的主脑归属入口仍是：

```sh
./agent-bird start claude    # 也可选 grok、agy
```

直接从普通原生会话或 `/clear` 后自动绑定新的安全归属尚未完成。

本机已通过 Claude、AGY 的真实安装、重复安装和已安装文件一致性检查；
Grok 在用户明确授权对本次包使用原生 `--trust` 后也已安装验证成功。
Grok 1.0.34 普通安装会停止并要求该信任确认；本安装器不自动添加该参数，
也不修改模型工具权限。三者的已安装主脑启动入口均通过 `--help` 检查。

安装不等于模型任务验收。AGY 已通过一次真实文件读取和结构化审查；
Grok 已修正登录续期路径，并通过一次 Host 完整 Git 快照的真实错误识别。
Grok 快照审查支持小型纯文本候选，具体边界见[支持范围](support-matrix.md)。

Codex 新包使用 `agent-bird@agent-bird`。运行安装器会通过官方配置接口启用
新插件、关闭旧 `codex-orchestrator@codex-bird` 的后续加载，并保留旧来源、
缓存和运行时。配置更新不请求当前会话热重载；在新 Codex 会话中使用
`$agent-bird`。旧会话继续使用其已加载的入口，不需要结束或清空历史。
不要手动删除旧缓存，也不要移除 `codex-bird` 来源。

本机已完成 preview.4 Codex 安装，新旧运行时入口均检查通过，旧缓存保留。
随后在无其他客户端操作的维护窗口，将默认协调器原地升级，
运行代次从 9 升至 10；38 张表的业务数据摘要不变，其余 6 个协调器未变。Claude/AGY/Grok 原生插件当前仍是
已验证安装的 preview.3；使用 preview.4 包的 `agent-bird start <cli>` 才会
从新运行时启动这些主脑。未对旧原生插件做静默覆盖或重新授信。

生成的 Homebrew formula 是待发布工件，下载地址只有对应 Release 发布后才可用；
尚未提供可直接使用的 Brew tap。

## 非 Codex 主脑

构建后直接使用 `./dist/plugin/agent-bird controller start --provider claude`
（也可选 `grok`、`agy`）。无需安装 Codex 或运行 `install.command`。
入口检查所选 CLI，继承当前终端配置与登录，保留一个独立的主脑进程归属。
让主脑读取环境变量 `AGENT_BIRD_SKILL` 指向的使用说明，然后通过
`AGENT_BIRD_COMMAND` 指向的运行时执行同样的任务命令。
不向第三方 CLI 自动安装插件或修改配置。若使用自定义 `--state-dir`，其子 CLI 中的编排命令也必须传入相同参数。

主脑退出时检查其进程组；如果 leader 已退出但后代仍存活，返回 `controller_process_tree_unknown` 并保留私有上下文，不把它标为关闭成功。主动脱离进程组的后代不在这项检测保证内。

## 升级

Provider 二进制改变时重新探测和确认 pin。协调器不支持新协议时返回 `coordinator_upgrade_required`；不能删除状态或另建状态目录来绕过共享配额。升级前核对活动任务，保留原 handle、预算及数据库备份，完成受控切换；不要直接运行其他机器的一次性修复脚本。

## 卸载

先在各自 Codex 会话关闭不再需要的 Provider，并确认停止完成。使用 Codex 的插件管理功能移除本插件。插件移除不代表允许删除未结束任务的运行时、历史和候选。

CLI 的 `doctor` / `uninstall` 支持按安装版本检查及卸载；仍被任务 pin 的版本会保留。默认保留运行历史和 Provider 的独立配置，不手动批量删除用户数据目录。
