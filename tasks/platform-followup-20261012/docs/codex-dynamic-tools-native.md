# 固定 Codex Dynamic Tools 原生握手

- 时间：2026-10-12
- 固定二进制：Codex 0.154.0，SHA-256
  \`4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc\`
- 范围：仅验证生产 \`prepareManagedCodexState\`、\`codexManagedCommand\` 和
  原始 \`process.StartDuplex\` 外层 Seatbelt 下，app-server 接受
  \`initialize.capabilities.experimentalApi=true\` 与
  \`thread/start.dynamicTools\` 的固定 namespace。
- 截断：\`OnThread\` 故意返回错误，因而未发起 \`turn/start\`；\`OnTool\`
  未被调用。此证据不覆盖模型、读写、续接、TUI 或账号行为。

执行命令：

\`\`\`sh
C06_CODEX_HOST_TOOLS_NATIVE_OPT_IN=root-reviewed-codex-dynamic-tools-handshake-v1 \
C06_CODEX_HOST_TOOLS_NATIVE_EVIDENCE_DIR=<0700-dir> \
tasks/g0-macos/data/go/bin/go test -count=1 \
  ./internal/host -run '^TestManagedCodexHostToolsNativeHandshake$' -json
\`\`\`

结果：1 个命名测试通过，2.01 秒；原始 Handle 已启动、原始 Wait 与 tree
receipt 均成功、管道由 \`codexsession.Run\` 关闭并加入，未调用 Stop。受控
metadata 见同任务 \`data/codex-host-tools-native-20261012/\`（其包含 JSON
测试记录及**启动前** source/config/auth、空 workspace manifest 和控制配置
hash；provider pin 另有启动前后 hash，不含账号或环境内容）。

