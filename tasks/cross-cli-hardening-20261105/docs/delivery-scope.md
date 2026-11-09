# 2026-11-05 私有候选范围与使用合同

状态：截至 2026-11-09，私有 `.4` 已完成唯一包身份及 PKG-01～04 独审/root验收；公开下载仍为 `v0.1.0-preview.1`，无 release 或全局替换。`.4` 的短引用、源码回归、同包 Python 和无网络 native metadata 已验收相应层次，但未执行真实模型试验。历史 `.3` 两方向真实试验未完成核心闭环，core/T12 与 T09 native push 仍 BLOCKED。最新资料见[当前交接](final-handoff.md)、[TASKS.json](../TASKS.json)、[.4 root证据](../data/public-evidence/T11-package-r4-root.json)和[独立包审查](../reviews/T11-package-r4-review.md)；各历史日期、身份及失败记录保留。

## 交付身份与验收边界

当前私有包 `0.1.24-hardening.20261105.4`，source commit `7b7f0421fc9dc553be29d871b16d15f779293b76`、tree `51b0bd73b3efcb607a2ff47476eced440f2b3731`；archive SHA256 `1817a713d710ac0d6c75539b1c765b5826f991d2ab9b81befd78b47b42d33597`、binary SHA256 `11e573d8d240cafb25d580d0a62862f2f617cf5b6ca38340c16df42222ea9a89`、package manifest SHA256 `665429b8ea50adfe5a1036665a04121dbaeb3c86a4dcbfa5f0a8334314aebb99`。两套普通/空格安装与 receipt 身份详见[.4 root证据](../data/public-evidence/T11-package-r4-root.json)。历史 `.3` 身份及原安装审查保留在[原包证据](../data/public-evidence/T11-package-root.json)和[原包审查](../reviews/T11-package-review.md)，不继承为新包 live。

本轮新安装写 installed `manifest.json` schema2，以 `installed_root` 和原 receipt 的完整 manifest SHA256 绑定规范绝对版本目录。整个 destination 同文件系统 rename 也需重装，doctor、pin、Go/Python native bridge 与入口计划均拒绝位置不匹配。历史 schema1 保留无位置绑定的原合同；portable `package-manifest.json`、`runtime-manifest.json` 和 SQLite schema 不因此升级。卸载 quarantine 恢复仍绑定原逻辑 `versions/<version>`，并在删除前核验原 receipt 和 manifest 快照；不会把任意移动后的目录当作合法恢复。

T01/T02/T03/T04/T05/T06A/T06B/T07/T10 的单项证据见本任务 reviews/ 和 data/public-evidence/，只证明各自快照/层次。新包源与回归树的149个生产材料逐文件一致；Go normal/race各1,654 pass（656顶层、998子测试）、0fail、8既有opt-in skip，vet/全模块build通过；实际安装Go的Python301pass/7native opt-in skip/0fail/0error。两CLI无网络metadata各8工具/source projection一致，不证明模型续审。详见[当前回归](../data/public-evidence/T12-regression-r4-root.json)及[生产源绑定](../data/public-evidence/T12-source-binding-r4.json)。T09保守关闭，沿用wait/collect。

| parent → provider | version / artifact | scope、read/write | fresh/resume、delivery | 平台与证据、未覆盖 |
| --- | --- | --- | --- | --- |
| managed Codex → Claude | Claude 写 exact 2.1.267 / 2.1.316 + SHA lock；读窗口见 provider-runtime | managed_process；reviewer read-only；implementer 仅声明的 managed candidate | fresh invocation；无 typed resume；collect/wait | macOS；历史 `.3` 真实 dispatch 与首次 inspect 成功，后续 opaque ref 被改写，stop 后 worker 已退出；核心 LIVE 未通过 |
| managed Claude → Codex | Codex 写 exact 0.166.0 + SHA pin、receipt、full-native runtime；读窗口仍须 exact admission | managed_process；read-only 或限定 candidate write | fresh invocation；无 typed resume；collect/wait | macOS；历史 `.3` 真实父 CLI 10 次 HTTP 401，watchdog stop；未 dispatch、未创建 worker，核心 LIVE 未通过 |
| managed Codex/Claude/Grok/AGY → AGY/Grok | 冻结已发布版本目录 + binary lock；Grok 另需显式 session-write 授权 | 进程作用域；仅 reviewer/read-only；coding 未开放 | fresh；无 typed resume；collect/wait | macOS；旧 live 只限旧版本/原范围；本轮无新真实闭环 |
| MCP parent → 上述 provider | 官方 Go SDK 1.8.0，8 ordinary tools；provider 门禁不变 | namespace + logical controller + 原 owner/control；不是 chat 绑定 | collect/wait；无 Tasks / native push | 私有 `.4` 安装包的 8-tools metadata 验证通过；不证明模型续审或核心双向 live |
| legacy controlled native 通道 | 独立精确 pin、attached connection/lease/thread/epoch、history proof | 不接管任意已有 TUI；不从 managed grant 推导 | 仅原合同；本轮不提升能力 | 当前 pin 下 idle/busy 原线程真实父模型续审缺证据，BLOCKED |

## 真实双向 live 结果

以下仅为历史 `.3` 的原始两次有界试验；新 `.4` 未执行真实模型试验，不能将短引用离线修复或 metadata 当作新 live PASS。用户确认没有可用本机 Claude 配置；不猜测认证模式，不再请求或复制密钥，不自动追加/重置模型预算。

两方向具体状态、账本与限制按本轮独立审查结果摘要如下。Codex→Claude 使用 `gpt-6-luna/low` 父模型与 Claude CLI `cli-default` worker，成功 dispatch 和首 inspect 后因父模型改写引用而 BLOCKED；已确认 worker interrupted/exited、active/unknown/reserved 均为 0，消耗 1 attempt、53,160ms。Claude→Codex 父模型沿用户默认 `deepseek-v4.1-flash-1017`，10 次 HTTP 401 后 watchdog stop；没有 dispatch/task/worker，账本均为 0。`unrecognized_model` 仅为 warning，不是 404 证据。两方向均无 accept/ACK/FF，不追加模型试验；失败原因不推断为 key 失效或模型不存在。包安装验证通过不等于核心真实双向通过。

## owner、父进程退出与认证

`owner_projection_version:1` 才返回 `owner_scope=managed_process` 及能力投影；旧响应默认不加字段。该 scope 证明 PID/birth/ancestry 所有权，不证明同进程内的 chat/thread 隔离。`observes_chat_switch=false`、`native_push=false`、`typed_resume=false`、`automatic_rebind=false`；`fresh_invocation=true`、`explicit_rebind=true`、`parent_exit_policy=stop_and_retain`。

local submit 省略 delivery 时设为 collect，显式非 collect 返回 `managed_owner_native_delivery_unavailable`。成功投影的 delivery 对象没有 reason 字段；该原因来自拒绝响应。父退出不意味着任务已确认停止：保留 unknown、槽、预算及恢复材料，不自动换 owner 或再开 worker。新父必须显式 bind/rebind 并沿原 handle 对账。fresh worker、显式 rebind、原生 session resume 是不同动作。

`resume/retry/rework/request-changes/accept/answer` 继续验证当前持久 owner、真实存活/birth、managed peer ancestry 和同事务 snapshot；旧 summary、review_token 或曾成功的授权不能替代当前检查。query/stop/ACK 保留原 bearer 收尾合同，不新增父活性门禁。候选读取验证在 DB 锁外有限并发执行，返回前重查授权和事实；不缓存 mutation 权限。

认证沿用来源 Provider 的已授权路由。Codex 仅接受受控 metadata 投影、精确 pin 和安装身份，子进程保留 refresh tripwire；不导出原始认证材料。Claude gateway 在隔离的私有 HOME/config 下运行，API key 或 token 只进入精确子进程环境，不写 argv/产物；OAuth/多重认证冲突拒绝，worker 不加载父 hooks/settings/MCP。这些约束不证明账户永远可用。401/403 应记录非 secret 错误与本次状态，不替换账户、自动刷新、改地址或复制凭据到请求/日志来绕过。模型默认不因故障被切换。

## 预算、返工与回收

CLI dispatch v2 明确区分 `max_active_ms_per_segment` 与 `max_active_ms_total`，可用 `budget_group_id` 共享组余额；MCP 对应 `budget.per_segment_ms/total_ms/group_id`。多任务共享同组，不各领一份总预算；retry/rework 继承已有组，unknown 保留预占，缺失/无效用时保守计满。legacy v1 `max_active_ms` 请求/旧账本继续原兼容合同，不自动重写为新政策；不能把旧单段上限当新组总额。

CLI `task request-changes --handle <H> --request <0600-file>` 的请求显式 `version:3` 才走 immutable refs；MCP 新 `request_changes` 默认 v3，可显式 `version:2`。已进入 v1/v2 的旧返工合同不自动升级为 v3；旧 Host 也不因数据库升级自动获得新能力。

v3 保存完整原始 brief 与每轮完整 feedback/acceptance，按原 candidate 生成后继 revision，Host 校验 refs 绑定和哈希后材料化输入；反馈不能扩大原声明路径或权限。每个编码正文对象上限 256 KiB（含 JSON 结构/转义），传输 chunk 上限 16 KiB、manifest 上限 8 KiB，最终模型 stdin 硬上限 512 KiB。超限拒绝，不静默截断、摘要或丢弃原条件。

schema 当前为 v11（v10 预算政策、v11 refs 对象与 pin）。迁移不等于重写旧合同。对象 GC 只有在证明已知 link 集合及身份后回收；未知 entry/link 保留 charge，barrier/tombstone metadata 可能继续存在。`cleanup_reason` 说明局部回收状态，不能据此称整个 runtime 清空。

## 任务内 MCP 配置

以下为模板，绝对路径必须替换为本任务实际路径。请求和锁文件为 owner-only `0600`，state/配置 root 及 key 目录 `0700`；root 必须尚不存在，其父目录已存在可信。不要放到工作区、全局 CLI 配置或全局插件目录。配置不含 owner/control token，也不绑定尚未启动的父 CLI。

`configure-request.json`：

```json
{
  "version": 1,
  "root": "/private/task/mcp-root",
  "state_dir": "/private/task/state",
  "workspace": "/canonical/task-repository",
  "providers": {
    "claude": "/private/task/locks/claude.json",
    "codex": "/private/task/locks/codex.json"
  }
}
```

通过本轮已验收安装包的绝对 wrapper 生成配置（具体路径以[T11 包证据](../data/public-evidence/T11-package-r4-root.json)中的安装记录为准）：

```sh
"/absolute/installed/plugin/scripts/invoke.sh" mcp configure --request /private/task/configure-request.json
```

configure 生成 `config.json`、`mcp.json` 和 `codex-args.json`。生成的 `mcp.json` 形状如下，command 自动固定为执行 configure 的 resolved executable 绝对路径，不需要手工替换为 wrapper：

```json
{
  "mcpServers": {
    "agent_bird": {
      "command": "/absolute/installed/plugin/bin/codex-orchestrator",
      "args": ["mcp", "serve", "--config", "/private/task/mcp-root/config.json"]
    }
  }
}
```

使用实际生成的文件，不照抄示例安装布局，也不改写 command 为源码或 latest。MCP 宿主通过其本次启动支持的配置入口显式加载生成的 `mcp.json`；Codex 的本次启动参数在生成的 `codex-args.json` 中。Codex stdio 默认过滤环境，生成参数额外列出本次 configure 环境中相关认证/路由变量的精确名字及 `AGENT_BIRD_OWNER_REQUEST`，不写变量值。投影覆盖现有四 Provider 的 OpenAI/Codex、Anthropic/Claude、Grok/XAI、AGY/Antigravity、Google/Gemini/Vertex、AWS、Azure/Foundry 家族及 HOME、XDG 配置位置、proxy/CA，排除明确列出的八项 Codex 非认证上下文，以及 Claude 自动注入 stdio MCP 的 `CLAUDE_CODE_SESSION_ID`、`CLAUDE_PROJECT_DIR` 两个会话/项目上下文字段；这些字段不能授予 owner、聊天路由或目录权限，仍由原 guard 核验。仅精确排除上述名字，其他未知同族变量仍受摘要保护。新父新增相关变量而配置未转发时，摘要不匹配会拒绝，不能静默使用默认账户；应重新 configure 并重启 managed controller。该合同保留四 Provider 既有 Host 认证准入，不表示新增云路由或保证任意未来 Provider。旧版仅能列出 8 工具的 metadata 证据不能替代完整环境证明、dispatch 授权或父模型续审。生成文件不等于已加载，最终包验收会核对 source/binary/receipt 和入口身份。直接调试 stdio 服务时也可使用已安装 wrapper：

```sh
"/absolute/installed/plugin/scripts/invoke.sh" mcp serve --config /private/task/mcp-root/config.json
```

serve 无 owner 也可列工具。MCP 新 dispatch 必须由本版已安装 `controller start` 启动的 managed parent 发起；单凭原生祖先进程或旧 launcher 请求不能跳过新的环境证明。controller 在私有请求中写入可选的版本化 source-auth 环境摘要，不写入原始值；MCP 每次验证真实 managed owner 后核对当前环境摘要，缺失/无效证明或环境变化均拒绝并给出重新 configure、重启 managed controller 的恢复提示。原 CLI 缺省该可选字段仍沿原合同；query/stop/ACK 不因本门禁要求旧父仍存活。通过后仍走原 worker 排除、来源/锁及权限门禁。签名 key 位于 `<state_dir>/mcp-keys/<namespace>/token-key`，`0600`，不交给模型或 worker，不打印/复制其内容。只有路由配置和 opaque 引用参与调用，不新增业务数据库或全局配置。

## 8 个普通工具

| 工具 | 输入和处置 |
| --- | --- |
| `dispatch` | 显式 request_id/provider/role/prompt/acceptance/model/budget；implementer 另需精确 paths；directory 若给出须等于配置 workspace。Grok reviewer/read-only 必须显式 `grok_session_write=true`，省略/false 返回 `grok_session_write_required`；非 Grok 传 true 返回 `profile_provider_state_mismatch`，不开放 Grok coding。返回 durable opaque handle 供 MCP 使用，以及 `cli_handle` 原 private handle 路径供原 CLI 收尾；不确定回复重用 request_id，不盲目再次提交。 |
| `inspect` | handle，可选 task_id；读取候选、预算、允许动作；仅已验证候选返回 `review_tokens[task_id]`。仍须读取合同、diff 与同候选测试证据并自行审查。 |
| `wait` | handle，可选 task_id/cursor/timeout_ms；最多 30000ms。取消等待或 stdio EOF 只取消调用，不 stop/ACK/accept。 |
| `collect` | handle，可选 task_id/cursor/include_diagnostics；读取有界页、delivery_id、collection_proof_sha256；next_cursor 原样用于下一页。 |
| `request_changes` | handle/review_token/command_id/feedback/acceptance；可选 version=2/3；从已签完整 tuple 写原 CLI 同格式 checkpoint，再走原 taskEntry 返工，新 worker invocation；不从最新 summary 猜候选。 |
| `accept` | handle/review_token/command_id；明确接受签名绑定的候选，仍走真实 owner 与原业务校验。 |
| `ack` | handle/delivery_id/collection_proof_sha256/decisions；该页全部 actionable 事件的真实 tuple 和处置，非验收。 |
| `stop` | handle/work_revision，可选 task_id；使用 inspect 刚观察的 revision；requested 不等于 confirmed，需复查无 active/unknown。 |

新候选的 canonical dispatch handle 使用 79 字符 `m1r_` 短引用，review token 使用 47 字符 `m1v_` 短引用；均保留完整 HMAC 和原 owner/完整 tuple/业务授权校验，旧 sealed 引用仍可解析。inspect 可在私有 request 的 `mcp-reviews/` 创建不可变辅助元数据（目录 `0700`、文件 `0600`、最多 64 条 × 16 KiB）；不修改业务数据库或决定，重复 inspect 不重写同 tuple。损坏、越权或超额记录明确拒绝，缺失的当前 tuple 可重新 inspect 重建。旧二进制不能解析新短引用，须保留兼容安装与私有元数据。详见[短引用合同](../plans/T08-short-references.md)。此生产变更已使用唯一私有 `.4` 包完成安装/metadata等离线验收，历史 `.3` 真实试验仍不代替短引用的新模型传递/live证据。

`cli_handle` 仅为只读路由元数据，不是 owner/control authority。MCP 服务关闭后，可使用同一已安装 wrapper 的 `task inspect/collect/request-changes/accept/ack/stop`，将该路径传给 `--handle` 并提供原命令要求的其余真实参数；仍受原授权、receipt 和 revision 校验。不要把 opaque MCP handle 当作 CLI 文件路径。

调用参数示例（不是 shell 命令，`H`、`T` 取真实响应，禁止自行编造）：

```json
{"request_id":"review-one","provider":"claude","role":"reviewer","prompt":"审查声明范围并给源码证据","acceptance":["只读，报告问题与验证依据"],"model":"cli-default","budget":{"per_segment_ms":60000,"total_ms":90000,"max_attempts":1}}
```

Grok 只读派发另需先取得本任务 session-write 授权，并显式传入该字段；它只授权派生新会话目录，不扩大工作区写权限：

```json
{"request_id":"grok-review-one","provider":"grok","role":"reviewer","prompt":"只读审查声明范围并给证据","acceptance":["只读，报告问题与验证依据"],"model":"cli-default","grok_session_write":true,"budget":{"per_segment_ms":60000,"total_ms":90000,"max_attempts":1}}
```

inspect 参数：

```json
{"handle":"H","task_id":"T"}
```

inspect 有 `review_tokens[T]` 且父已完成审查后，才将该 opaque 值原样用于决定：

```json
{"handle":"H","review_token":"<inspect.review_tokens[T]>","command_id":"accept-one"}
```

或返工（不要同时接受同一结果）：

```json
{"handle":"H","review_token":"<inspect.review_tokens[T]>","command_id":"rework-one","version":3,"feedback":"指出原条件中未满足的具体项和证据","acceptance":"保留原条件，写明本轮可检验要求"}
```

review_token 绑定 namespace/route/logical controller/run/task/work_revision/event id+revision/hash/action slot/segment/candidate；不是 owner capability，不允许混用 handle 或跨任务重建。显式 rebind 只变物理 PID/birth，不改变 logical controller。MCP `request_changes` 在业务 mutation RPC 前将签名原 tuple 写入原 CLI 同格式的 `owner-decisions` checkpoint，之后复用原 taskEntry 与 receipt replay；不新增决定账本。回复不确定时重用原 command_id 和原内容；CLI→MCP 与 MCP→CLI 均沿原 checkpoint 重放同一回执，两向的同 command 改正文均拒绝。相关 v2/v3 fixture 覆盖这两向路径，仍不作为最终安装包或 live 验收。

ACK 必须从 collect 页复制实际字段；每项 `decision` 仅 `handled/waiting_user/stale/rejected`，另带稳定 command_id。ACK、accept、Host 最终集成是三件事：事件处置不接受候选，接受候选不自动 ACK 或集成文件。无集成工具；沿既有已授权 Host 同候选集成合同执行。

服务使用官方 Go SDK 1.8.0 的普通 stdio tools，不启 Tasks/native chat callback；最多 4 个在途调用，每调用最多 45 秒。输入 frame 上限 2 MiB、CLI 捕获 128 KiB、完整 MCP result 256 KiB；超限错误不返回截断页的 proof。工具列表、help、版本/能力 probe、loopback 和 native metadata 装载都不等于 live-provider 或原聊天模型续审通过。

## 后续授权的独立连通性检查

402af7cc交接后用户另授权尝试Claude既有Key/API。[本次最小复测](claude-connectivity-20261109.md)的已观测补测返回HTTP401（原来源API-key模式、默认模型保留）；首次采集异常结果UNKNOWN。两次均已收尾，无进一步自动重试。该检查未使用`.4`产品派发闭环，不改变本页PKG/REG或历史`.3`身份，也不将`.4`LIVE从NOT_RUN提升。

用户随后已配置生效CC Switch Claude路由；[新配置验证](cc-claude-connectivity-20261109.md)使用AUTH_TOKEN/Bearer和原默认模型，单次CLI返回HTTP403、无模型回答，具体权限/接入原因未确定。这与旧聊天Key/API的401分开记录，不改变产品`.4`LIVE=NOT_RUN。
