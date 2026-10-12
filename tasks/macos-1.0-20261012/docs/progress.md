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

## 已完成：可信结果分页读取

新增 `call_result_v1` owner 鉴权查询，只有原 segment `exited`，或经过核验的
`not_started + launch_rejected` 才返回终端内容。复用已提交 Host artifact receipt
与完整性校验；每页至多8192 UTF-8字节，整件至多1MiB，返回原payload hash、终态与
下一偏移。不ACK、不代替业务验收，也不把预启动拒绝写成进程退出。
DB schema保持41；IPC版本保持1，新增查询kind，无迁移。

独立审查原版发现 launch_rejected/64KiB 两项缺陷，已修正；主会话复核修正实现。
有效验证：Store定向3顶层/9 named PASS，race同3顶层/9 named PASS，fail=0、skip=0。
原始证据在外层本任务专属artifact目录
`tasks/platform-followup-20261012/data/call-result-{count1,race-count1}.jsonl`；
不是旧仓库产品源码。尚不构成真实Provider调用验收。

首次原生调用在目标启动前返回 `current_codex_origin_ambiguous`：当前发起进程链有
多个Codex；保留门禁，下一步使用显式Controller发起进程身份，不通过删改身份校验开放。

## 已完成：统一串行 call / MCP 入口

`call --request <0600-json> --state-dir <0700-short-path>` 使用 v1 请求：provider、task、
directory，默认read-only；workspace-write必须有paths，默认timeout_ms=120000，最长1h，
provider_lock为已确认pin。仅一次dispatch，不自动重试；写任务返回隔离候选Workspace与
可信冻结receipt的changed_files。结果保留handle，业务accept仍显式执行。

`mcp --provider-locks <private-dir> --state-dir <private-short-path>`提供
`agent_bird_call`。四方复用同一协议；目标仍受实际provider profile门禁，当前Codex目标
未开放。实现初始化、串行busy、取消通知、EOF取消并join；取消保留原终态验证，未知
不声称退出。预提交journal已持精确dispatch handle，中断后可走现有stop/reconcile。
本版未加自动恢复/自动清理未知journal。Codex Controller发起方单独接入managed身份，
不修改原生身份歧义门禁，不因此开放Codex Worker。

独立审查修复：journal提交后崩溃窗口、MCP JSON ID词法等价取消；两位审查者复核通过。
CLI/MCP/发起方定向race：6top/13named PASS，fail=0、skip=0；后续journal与分页绑定
修改后CLI定向回归：5top/12named PASS，fail=0、skip=0。不运行全量或长压力测试。

真实Codex已通过临时MCP配置发现并选择`agent_bird_call`，原生记录出现mcp_tool_call；
但第一次请求被发起CLI默认工具审批阻断（approval policy never），Claude未启动。
这只是插件发现/调用路由证据，不能记为Codex→Claude任务通过。

MCP传输/取消依据：
https://modelcontextprotocol.io/specification/2025-06-18/basic/transports
https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/cancellation
Codex进程配置与单工具策略依据：https://developers.openai.com/codex/mcp
