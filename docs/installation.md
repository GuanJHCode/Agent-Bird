# 安装与卸载

当前源码支持 macOS；用户包以 Apple Silicon 为首要目标。准备 Go 1.26+、Python 3、Codex CLI，以及要使用的 Claude、Grok 或 AGY CLI。Provider 的安装和登录由用户自行完成，项目不提供账号或密钥。

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

当前仓库没有对应的新预编译 Release，不要把源码压缩包当成已构建的安装包。没有提供 Homebrew formula。

## 升级

Provider 二进制改变时重新探测和确认 pin。协调器不支持新协议时返回 `coordinator_upgrade_required`；不能删除状态或另建状态目录来绕过共享配额。升级前核对活动任务，保留原 handle、预算及数据库备份，完成受控切换；不要直接运行其他机器的一次性修复脚本。

## 卸载

先在各自 Codex 会话关闭不再需要的 Provider，并确认停止完成。使用 Codex 的插件管理功能移除本插件。插件移除不代表允许删除未结束任务的运行时、历史和候选。

CLI 的 `doctor` / `uninstall` 支持按安装版本检查及卸载；仍被任务 pin 的版本会保留。默认保留运行历史和 Provider 的独立配置，不手动批量删除用户数据目录。
