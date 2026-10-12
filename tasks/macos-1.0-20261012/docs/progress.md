# macOS 1.0 candidate

用户将目标改为四 Provider 的实际插件串行互调。工作分支仍为
`feat/macos-followup-20261012`；重建起点为已推送的
`8504f88955d432435bf3550053eae8a2703afa92`，不回退最初 `7861d26`。
原产品 worktree 被用户删除；用户随后明确授权按原路径重建，Git 历史和两个隔离
worktree 保留，未提交 native probe 及三条分支另有本地 bundle 备份。

## 最短实施链

1. 主会话：统一同步 call 请求/结果和串行、超时/取消接线，复用现有 Go task 核心。
2. 主会话：Codex app-server 动态工具接入受限项目读写；原保护保留。
3. 独立子任务：验证四 CLI 的进程级插件/MCP加载入口，四方共用同一调用协议。
4. 主会话：候选包、依赖检查、安装与最短示例。
5. 串行真实验收：Codex→Claude、Claude→AGY、AGY→Grok、Grok→Codex；另验实际
   文件修改、失败返回、超时或取消。没有真实记录前不标记通过。

当前已有 Claude/AGY/Grok 受控执行基础和旧原生证据；本次四条插件互调尚未验收。
Codex dynamicTools reducer 已提交，合成协议及定向 race 已通过；尚无公共调用能力。

## 边界

本阶段只做单子任务串行调用，禁止递归。暂不做 Linux、Host丢失恢复、自动重试、
filtered多阶段接线、LFS/hooks/signing、长资源优化及复杂retention。不修改真实账号、
现有安装或全局配置，不发布正式tag/release，不推main。用户自行安装候选包。

主会话独占 integration，负责共享协议和CLI；agy_resource 与 darwin_git 各保留原
隔离 worktree，最多两子Agent，不递归。已有有效证据复用；仅受影响短链补测。
