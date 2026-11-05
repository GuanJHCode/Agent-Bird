# T08 独立审查记录：MCP 普通工具 r4

VERDICT=PASS

## 检查范围

- 冻结审查树：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t08-r4`
- 分支：`work/t08-r4-hardening-20261105`
- 基线/HEAD：`0e2b6988409563b63f567adba54d638e01885b14`
- 完整 feature manifest：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t08-evidence/data/frozen-r4-manifest.json`
- manifest SHA256：`cc9f7e01156afb63bd5e9c3962627ceabdc35ffaab694a4d2f7b833fe4c505fc`
- manifest 共 19 个 feature 文件；审查时逐文件 SHA256 全部匹配，`git diff --check` 通过。
- r3 到 r4 仅变更授权的 `source_auth_environment.go`、`source_auth_environment_test.go`、`mcp_config_test.go` 与 ADR-008；其余 r3 已审字节未变。
- 阅读并核对 ADR-008、T08 handoff/acceptance、全部 `mcp_*.go`、controller/current-owner、dispatch/request-changes、plugin wrapper、Python wrapper test 及新增 Go 测试。
- 重点检查：8 个工具与官方 Go SDK 1.8.0；输入/owner/control/state 拒绝；route/review 绑定和逐次授权；dead-owner 收尾；request-changes 原 tuple/replay/CLI fallback；accept/ACK 分离；cancel/EOF；stop unknown；有界结果与 int64 ACK；reasoning 兼容；Grok gate；worker 防递归；Codex stdio 环境过滤和四 Provider auth/route 等价。

## BLOCKERS

无。r4 冻结快照明确 PASS。

历史审查阻塞均已在后续冻结轮次修复：

1. r1：缺少 wrapper fallback 的 `cli_handle`，MCP `request_changes` 未写原 CLI checkpoint，且 Grok 缺 typed `grok_session_write`。
2. r2：仅转发 `AGENT_BIRD_OWNER_REQUEST`，会丢 Provider auth/route 环境并绕过未知 `OPENAI_*`/`CODEX_*` fail-closed。
3. r3：source-auth proof 机制正确，但投影漏掉 Grok `GROK_HOME`/`XAI_*` 和 Claude cloud route 等配套 credential/config 家族。
4. r4：扩为当前 Codex/Claude/AGY/Grok 的 17-prefix union，加 4 个 XDG exact key；新增 missing/changed、unknown suffix 和端到端 controller→fake CLI→原 MCP stdio fixture，关闭上述缺口。

## NON_BLOCKING

### 源码审查结论

- `source_auth_env` 使用版本化算法、8 字节大端长度前缀、变量名排序和 last-wins map；unset 与 empty 不同。
- controller 对去掉 `CODEX_THREAD_ID`、加入 Agent Bird 自身变量前的最终 Provider 环境生成 proof；owner request 只保存摘要，不保存环境值。
- 投影覆盖当前四 Provider 可确认的 ambient 认证、路由和配置位置：
  - `OPENAI_*`、`CODEX_*`，保留 `CODEX_HOME`，仅排除 Host 已列出的 8 个非 auth runtime context；
  - `ANTHROPIC_*`、`CLAUDE_*`，以及 Bedrock/Vertex/Foundry 配套 `AWS_*`、Google/Cloud、Azure/Foundry 家族；
  - `GROK_*`、`XAI_*`，包括原 `GROK_HOME`；
  - `AGY_*`、`ANTIGRAVITY_*`、Gemini/Google 家族；
  - HOME、大小写 proxy、CA 变量及 4 个 XDG 配置位置。
- configure 只将当前存在的精确变量名和 `AGENT_BIRD_OWNER_REQUEST` 写入 Codex `env_vars`，不写值。未来同家族新增变量未被旧配置转发时，摘要不等并 fail-closed。
- 每次 MCP `dispatch` 都重新验证 managed owner request、launcher ancestry/birth/executable/hash 和环境摘要；未缓存 auth/owner 授权。
- proof 缺失、版本/算法/格式错误返回 `mcp_source_environment_unverified`；摘要变化返回 `mcp_source_environment_changed`；两者均给出 `reconfigure_mcp_and_restart_managed_controller` recovery，且在创建 dispatch 状态前拒绝。
- `dispatchMapped` 仅是包内 mapping seam，SDK 注册表只注册经过 gate 的 `dispatch`，不存在 RPC 绕过入口。
- 旧 CLI 缺 optional proof 仍按原合同工作；query/stop/ACK 和旧 receipt/token 收尾未误加新 live-owner gate。
- r2 的 `cli_handle`、Grok typed gate、MCP/CLI 双向 request-changes replay、same-command replay/changed-body conflict、accept/ACK 分离等修复在 r4 未回退。

### 本独立审查实际执行

使用 Go 1.26.10、`GOENV=off`、`GOTOOLCHAIN=local`、`GOPROXY=off`、`GOMAXPROCS=2`，独立 `GOCACHE=/private/tmp/agent-bird-t08-review-r4-go-cache`；PATH 含固定 Go 与 Codex 0.166.0。所有命令均已终态。

1. r4 auth/source 环境聚焦普通测试：
   - 范围：`TestMCPConfigureUsesPrivateTaskScope`、`TestMCPDispatchRequiresManagedSourceEnvironment`、`TestSourceAuthEnvironmentProjection`、`TestSourceAuthEnvironmentAdditionalRoutes`、`TestMCPManagedSourceEnvironmentAdmission`
   - 结果：133 passed、0 failed、0 skipped；package pass。
2. 同范围 race：133 passed、0 failed、0 skipped；package pass。
3. `go vet ./cmd/orchestrator`：exit 0。
4. `go build ./cmd/orchestrator`：exit 0。该命令临时在冻结树生成的自有 binary 已立即删除；最终 status 和 19-file manifest 恢复一致。

r4 上述独立 133-node normal/race 是审查工具直接输出；r3 的独立聚焦 normal/race 各为 21 nodes。两轮均未写 raw log 文件，原始证据仅存在本审查会话的 tool outputs。不得为它们补造日志路径。root 后续 fresh 全模块报告属于集成签收证据，不冒充本独立审查执行。

已有可查 raw logs 仅覆盖较早冻结轮次：

- `data/mcp-focused-normal.jsonl`、`data/mcp-focused-race.jsonl`：r1 focused。
- `data/r2-mcp-focused-normal.jsonl`、`data/r2-mcp-focused-race.jsonl`：r2 MCP focused，各 69 passed、0 failed、0 skipped。
- `data/r2-legacy-request-changes-normal.jsonl`：r2 legacy CLI request-changes，12 passed、0 failed、0 skipped。
- `data/xsys-affected-normal.jsonl`：SDK/x/sys 影响包证据。

## 剩余风险

- 本 PASS 仅针对 frozen r4 源码与 fixture；不替代集成树 19 字节复制后的 manifest、全模块 normal/race/vet/build、最终 package 或安装签收。
- 未执行真实 Provider/model/cloud；fixture 完整环境只到达原 `mcp_provider_not_configured` admission，不冒充 live Provider 成功。
- Codex/Claude 实际宿主 MCP 环境加载属于独立 native metadata 层；未完成项必须记录为 NOT_RUN/BLOCKED，不能由本 fixture PASS 外推。
- T09 conservative 文档此前独立 PASS 仅表示其正确保留 `collect/noTasks` 并明确 native BLOCKED；不表示 native gate 已通过。
