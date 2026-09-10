# macOS G0 正常信任步骤

2026-09-10更新：用户已完成此步骤，原生界面确认三项Trusted并产生3条真实Hook记录。以下保留为操作记录，**无需重复信任**。当前进展见 [原生G0结果](macos-native-g0-result.md)。

此步骤仅用于当前G0专用测试仓库。Windows已延期；原生回调尚未验收通过。最小Hook工具的最终验证/复核状态以 [本轮记录](macos-current-environment.md) 为准。

在普通macOS终端中进入本任务测试仓库并正常启动Codex：

```sh
cd /Users/guanjunhui/code/ai/codex-cli-orchestration-design/tasks/g0-macos/data/test-repo
codex
```

1. 在原生目录信任提示中审阅并确认这个测试仓库。
2. 输入 `/hooks`，分别审阅本项目的 `SessionStart`、`UserPromptSubmit`、`SessionEnd` handler。定义来源应为此仓库的 `.codex/hooks.json`，不要对其他来源批量信任。
3. handler只调用本任务 `g0-hook-darwin-arm64`，写入 `tasks/g0-macos/data/hook-events/`，nonce为 `g0-macos-20260910`。记录字段仅为会话/轮次/事件/来源标识及探针进程、时间，不保存prompt/transcript，stdout为空。
4. 审阅后通过原生界面信任这三项。信任完成后正常退出Codex；首次未受信任时跳过的SessionStart不计成功，后续会重新启动进行实测。

可审阅的定义副本：[macos-hooks.json](macos-hooks.json)。正常信任要求来自用户已确定的账号/权限边界以及 [官方Hooks信任流程](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks)。本任务不写 `trusted_hash`、不添加绕过信任参数，不替用户操作Trust。

这不是产品额外启动器：用户仍直接运行原生 `codex`，这里只是切换到专用测试目录。此页不要求API Key、登录迁移或修改全局AGENTS.md。
