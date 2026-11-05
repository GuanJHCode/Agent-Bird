# T08 Claude stdio 上下文与认证环境

实际 .2 安装包使用固定 Claude 2.1.316、仅 control initialize/mcp_status、OS deny network，加载8工具成功且18个公开sentinel全继承，但原产品投影前后摘要不同。新出现的键精确为 CLAUDE_CODE_SESSION_ID、CLAUDE_PROJECT_DIR；输出只记录键名/整体digest，不记录真实凭据。旧 .2 及报告保留历史，未运行模型。

官方 [MCP stdio说明](https://code.claude.com/docs/en/mcp#option-3-add-a-local-stdio-server) 定义项目根上下文；官方 [环境变量说明](https://code.claude.com/docs/en/env-vars) 定义会话关联ID自动注入MCP子进程，不能当作认证路由选择器或native聊天证明。运行事实来自本机固定binary，官方现行说明仅佐证字段用途。

只精确排除这两个动态上下文名字，保留其他CLAUDE_/ANTHROPIC_及所有原source-auth家族的missing/changed/new/unset检测；相同前缀的未来变量不排除。原controller/owner PID+birth+ancestry、workspace、token、Host认证guard均不改，不以CLAUDE_CODE_SESSION_ID证明native会话。原算法标识/版本和MCP config request1不变；升级后重新configure并重启controller，旧proof不做宽松迁移。

新增projection红测、相同前缀future-auth拒绝、真实多进程controller→MCP上下文注入后到provider-not-configured的正例（无worker/模型）。修后normal/race及独审/root验收，功能commit/push，再从新clean source构建唯一 .3；不得将 .2 的8工具加载当作source proof或live通过。外部证据 t08-claude-context-evidence 与 t12-native-r2 保留原失败。
