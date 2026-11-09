# Provider、owner 与运行时配置

2026-11-05 私有候选的当前 scope、认证投影/refresh 限制、预算、v3 refs 与 MCP 用法见[本轮合同](../tasks/cross-cli-hardening-20261105/docs/delivery-scope.md)。下文旧版本试验保留其日期/身份，不是本轮包或 live 结论。

## 三个独立层次

1. **协议 Adapter**：构造固定参数、解析版本化事件、识别 session。禁止 `extra_args`；未知协议/能力显式拒绝。
2. **Execution Profile v1**：`role`、`model`、`reasoning`、`permission`、`timeout_ms`。模型名不当作命令参数拼接；思考值仅 `low` / `medium` / `high`；超时 1–3,600,000 ms，仍受 segment 已授预算约束。
3. **Provider Lock v1**：`provider`、`protocol`、`binary.path/version/sha256`。启动前验证版本/能力和摘要，在最终进程启动边界再次核对原 Provider 摘要；sandbox wrapper 不替换 Provider 的锁身份。

请求中的 `adapter` 示例：

```json
{
  "provider": "claude-code",
  "provider_lock": {
    "version": 1,
    "provider": "claude-code",
    "protocol": "claude-stream-json-v1",
    "binary": {"path": "/canonical/claude", "version": "observed --version", "sha256": "observed 64 hex digest"}
  },
  "profile": {
    "version": 1,
    "role": "reviewer",
    "permission": "read-only",
    "reasoning": "medium",
    "timeout_ms": 30000
  },
  "directory": "/canonical/workspace",
  "prompt": "目标、输入、范围、约束、产物、验收条件"
}
```

省略 `model` 使用 Provider 的当前默认模型；需要可复现的模型选择时填写明确模型 ID。`provider_lock` 与旧 `binary_*` 字段不能混用。旧请求保留兼容层；新 profile 的权限不可与旧 allow/deny 列表混用。

本机 Claude CLI 接入第三方模型服务。验收默认沿用用户已有配置，不强制 `haiku` 等官方别名；CLI 返回 403 RBAC 不能直接归因于 Claude 官方登录。本轮真实双向试验中，Codex→Claude worker 已 dispatch 并启动，但父模型后续改写 opaque 引用导致闭环中断；Claude→Codex 父 CLI 请求在 dispatch 前收到 HTTP 401，未创建 worker。两方向均无 accept/ACK/FF，详见[本轮交付记录](../tasks/cross-cli-hardening-20261105/docs/delivery-scope.md#真实双向-live-结果)及[本轮双向 live 结果](../tasks/cross-cli-hardening-20261105/docs/delivery-scope.md#真实双向-live-结果)。试验未改动服务地址、登录或凭据。

### 角色与隔离

只读指文件写入权限：保留既有 Provider 读取/认证环境，不宣称只允许读取 cwd 的全系统白名单。

- reviewer：`permission=read-only`，Claude 原生 `--permission-mode plan` 或 AGY 原生 `--mode plan`，加 macOS 文件写入隔离。两者支持 typed `model`/`reasoning`；AGY 读取任务优先明确使用 `view_file`，其终端工具路径尚未完成可靠性验收。
- implementer：`permission=workspace-write`，必须是经授权的 Git linked worktree。保留 Claude 原生 `default` 审批；只允许工作区内容及受控临时/产物目录写入，拒绝 `.git` 和公共 Git 元数据写入。主脑仍需通过现有 Git Host 生成/审查/整合候选。
- 控制数据库、owner/control/bootstrap 凭据和其他任务的 spool/report capability 不向新 profile Worker 开放。当前任务可以读取自己的报告能力文件，在其 `artifacts/` 子目录写报告产物。
- 不添加 bypass、自动接受所有工具、`acceptEdits` 或全盘写权限参数。Provider 原生审批拒绝时，任务失败或等待，主脑不能伪造审批。
- typed Claude 的两个角色固定禁用原生 `Agent`、`Task` 工具；CLI 缺少 `--disallowed-tools` 能力时拒绝启动。主脑派发的任务说明也应明确禁止 Worker 再派工。该限制不等于任意 Bash 启动其他 CLI 的路径均已封闭；`.10` 参数与能力回归通过，真实 CLI 效果仍待验证。

当前新 profile 的真实续接测试未通过，故拒绝 session resume：`profile_resume_not_verified`。Codex 生产启动要求 exact native SHA pin 与正式安装的 receipt/full-native runtime，未知摘要仍返回 `codex_trial_guard_not_ready`；Grok 的只读 profile 需要明确会话写授权。AGY 只开放 reviewer，implementer 返回 `implementer_unsupported`。旧协议的显式恢复接口与已有安全校验保留，不能通过退回旧 profile 规避新请求的权限要求。

Claude + AGY 的生产安装主流程已通过同 run 的真实随机文件读取、产物哈希、collect、ACK 与 accept 验收；脚本自动生成 owner-only 请求文件，见 [复现入口](../tasks/multi-cli-mainflow/docs/usage.md)。它验证 CLI 调用闭环，不代表模型编辑、候选提交生成或原生主脑 TUI 已验收。

### Host 管理的候选工作区（增量能力）

实现任务可在 `adapter` 中增加以下字段，`directory` 必须是尚不存在、父目录可信的绝对路径：

```json
{
  "candidate_workspace": {
    "version": 1,
    "repo_root": "/canonical/source-repository",
    "base_oid": "immutable-commit-oid",
    "paths": ["src/add.py", "tests/test_add.py"]
  }
}
```

与 `profile.role=implementer` / `permission=workspace-write` 一起使用。Host 在持久化 prepared 后从明确的 base 创建 linked worktree，在 Provider 成功且整个进程树退出后冻结实际改动。`paths` 为精确文件路径，支持新增、修改、删除；不是 glob，也不扩大 sandbox 写权限。未声明文件（包括 ignored 文件）、符号链接、已暂存改动或 base 漂移均拒绝冻结。用户原工作区的未提交内容不会成为输入。

冻结产物是 Host 写入的 JSON，包含 `provider_result`、`candidate_receipt_sha256` 和 `candidate`。后者绑定 `run_id/task_id/attempt_id/segment_id/work_revision/plan_revision`、base/candidate/tree OID、路径/blob/mode/deletion 和 private ref。完整文件身份清单留在既有 Git journal；摘要产物不包含完整 `files`，不能直接作为清理授权。Provider 文本不是 Git 身份证明。后续测试、独立审查与集成必须另行绑定这个 candidate OID；普通 `accept` 仍不等于 Git 集成完成。

本增量拒绝需执行 hooks、filter、fsmonitor、signing、include、partial clone 等 Git 扩展的仓库；不会用跳过 hooks 或放宽 Worker 权限解决兼容性。候选清单上限为 4096 条文件/目录和 256 KiB 相对路径总量；最多 256 个声明路径，每个 512 字节。单个冻结文件/Git 输出上限 16 MiB，原始 Provider 结果上限 512 KiB，JSON 编码后最终 artifact 上限 1 MiB。超限拒绝，保留已创建工作区用于显式对账。这些是本次候选处理边界，未提供整个工作区的磁盘配额。

停止会取消准备及冻结中的 Git 进程；不能确认进程树退出时保持 unknown。每个 Git 命令的 intent、spawned、exited/unknown 与身份写入现有 journal，不记录环境或输出正文。失败或中断后不自动 reset、删除或重试冻结。新 profile 的 resume/answer 后续执行仍未验证，managed workspace 也不支持原 session 续接；不要将 waiting_user 或已有命令等同于可恢复闭环。

### 同一候选的测试、审查与集成

在现有 DAG 中声明 `implement → validate → review → integrate`，全部使用 `owner_review`，不配置 fallback。每个后继 adapter 声明 `candidate_action`：

```json
{
  "kind": "candidate",
  "directory": "/canonical/new-validation-worktree",
  "candidate_action": {
    "version": 1,
    "operation": "validate",
    "source_task": "implement",
    "command": ["/canonical/python3", "-m", "unittest"],
    "target": {
      "worktree": "/canonical/user-worktree",
      "ref": "refs/heads/main",
      "base_oid": "immutable-target-base-oid"
    }
  }
}
```

`source_task` 必须是直接依赖。协调器只绑定最新、已退出、经 owner 接受的 Host 结果；Worker report 不可充当候选证明。现有 launch intent 摘要绑定消费任务、版本、前驱事件、产物和接受命令，重放时重新比对，不增加任务数据库或消息队列。

- `validate`：无 Provider/profile，Host 从候选提交建立新工作区。命令必须是规范化的绝对可执行普通文件，摘要在启动时再次验证；记录实际退出码、输出摘要、候选/tree OID 和测试阶段版本。环境限定为任务缓存及工具 PATH，不继承 Provider 认证。测试改动候选或输出超限会失败。
- `review`：省略 `kind`，当前仅允许已验证结构化通道的 Claude 只读 reviewer Provider/profile，设置新 `directory` 和验收 prompt，省略 `command`。独立进程读取同一候选，返回严格 JSON `decision`（approve/reject）和非空 `summary`。Claude 使用由 Adapter 固定的原生 `--json-schema`，仅消费结果的 `structured_output`；缺失、额外字段或不合约值均拒绝，不退回正文或剥除 Markdown 代码块。Host 把审查与实际测试摘要绑定；拒绝意见保存在失败产物中。
- `integrate`：使用 `kind=candidate`，`directory` 等于 target worktree，省略 Provider/profile/command。三个阶段的 target 必须完全相同。Host 校验接受的审查、候选与测试证据，并通过现有受监督 Git Host 集成。检查整个 `target_base..candidate` 的路径，保留含空格/换行的文件名，目标分支漂移或用户文件冲突会拒绝。

主脑仍负责逐阶段 collect、检查 artifact、ACK 和 accept；接收事件不等于接受代码。失败测试和拒绝审查不能继续集成。开始集成后无法确定结果、最终 journal 落盘失败或并发取消，均保留可能已落地的 outcome 并进入 unknown；同任务普通重放检测既有 integration intent 后拒绝，需要显式对账。

ACK 后 collect 为空属于正常消费行为。对当前版本、最新已退出 attempt 的有效 Host 产物，`summary` 可返回 `reviewable_result` 中的精确事件绑定；它不执行接受决定，也不替代原有审核 guard。主脑可将该绑定整理为 `accept --request <0600-json>`（显式包含 `decision`、稳定 `command_id`、任务/版本、control 文件路径及事件绑定），避免手工转录长 ID；不得同时混用业务 flags。该恢复投影只覆盖有有效非空注册产物的结果。

`provider-diagnostics.json` 只记录有界输出的白名单事件/工具类别计数和截断标志，不保存 prompt、工具输入、消息正文或环境。它用于定位停在哪一阶段，不证明完整工具调用轨迹。

该链路已有真实 CLI/coordinator/Host/Git 加模型 fixture 的回归。第三方默认模型在原生结构化输出修复后完成真实编辑、冻结、测试、独立审查及同版本集成；该试验的 owner 决策由 Go harness 执行，不能替代主脑自主验收。此前审查失败见 [前轮记录](../tasks/p0-candidate-delivery/docs/delivery.md)，本轮结果见 [反馈闭环记录](../tasks/p0-feedback-loop/docs/delivery.md)。

### 绑定审查反馈的返工

新增 owner 命令 `rework --request <0600-json>`。请求包含源任务 `task_id/work_revision/control_file`、旧候选事件 `candidate{event_id,event_revision,event_hash}`、`candidate_oid`、审查任务 `review_task_id/review_work_revision`、审查事件 `review{event_id,event_revision,event_hash}`、审查 `action_slot`、具体 `feedback`、新 `acceptance` 和稳定 `command_id`。

目前限定完整、唯一的 `implement → validate → review → integrate` 线性链，不接受额外后继或 fallback。源实现和测试必须是 owner 已接受且已退出的 Host 结果，审查必须是同一候选的独立拒绝（或 owner 拒绝的 review result）；验证实际 artifact 大小、SHA、任务/版本/计划、候选/tree 和测试链。超时或格式错误不是有效审查反馈。初始 submit/fallback 不得自行设置保留字段 `candidate_workspace.rework`。

事务保留任务 ID、DAG、plan revision、Provider Lock、权限、文件范围、测试命令、目标 base、全部 attempts 和预算账本；四个任务的 work revision 一起提升。实现从旧候选建立新 worktree，prompt 包含旧审查意见及新验收条件；冻结 receipt 的 `revision_sha256` 绑定这次反馈上下文。下游重做测试与独立审查，不能接受旧事件来完成新版本。

消费审查的 action slot，复用 `retry_decisions.revision_payload` 保存规范化决策、新 adapter 和 receipt。重复同一决定返回原 receipt；不同反馈/候选/验收条件冲突。该 revision payload 字段由 schema v6 的原子迁移引入（当前 schema 为 v9），新增记录纳入 global/run quota，删除时由事务触发器释放额度；不会重置额度或另建任务队列。

任一相关 segment 活跃/unknown、旧问题仍未解决、Host 非 ready、剩余 attempt/时间预算不足，或集成曾建立过 attempt，整笔返工拒绝。旧 Worker report capability 撤销，只允许经认证的原事件精确重放 ACK；新事件拒绝。旧 Host event 同样保留 durable ACK；晚到旧版本事件核对原 segment 身份后仅记 internal 历史，不推进当前任务。伪造新版本的旧 segment 事件拒绝。

feedback/acceptance 各最多 16 KiB；编码后的单任务 adapter 另限 16 KiB，给 64 KiB IPC 中的前驱和身份数据留空间；完整决策最多 256 KiB。任一超限在事务提交前拒绝。此能力是受控新 attempt，不是 Provider 原 session 续接或任意动态计划。

### 探测与升级

每个 Provider 有一份冻结的稳定版窗口，而不是单版本常量。窗口记录在 `tools/orchestrator/internal/adapter/compatibility_catalog.json`，Claude 清单复用已冻结的 2026-11-03T03:52:16.898784+00:00 快照，其余 Provider 清单保持 2026-11-02T01:53:00Z 的原内容。运行时不联网解析 `latest`。规范化版本只用来判断是否落在基线和上限之间，并且必须出现在该次冻结的已发布清单里；锁和启动身份仍使用原始 `--version` 文本、绝对路径和 SHA-256。`ValidatePin` 与最终启动前的摘要复核保留。区间外、无法解析、预发布或未发布版本返回明确错误码和窗口两端，不降级执行。

| Provider | 基线 | 冻结上限 | 协议 |
| --- | --- | --- | --- |
| Claude Code | 1.0.61 | 2.1.317 | `claude-stream-json-v1` |
| AGY | 1.2.3 | 1.4.12 | `agy-stream-json-v1` |
| Grok | 1.0.34 | 1.0.70 | `grok-streaming-json-v1` |
| Codex | 0.154.0 | 0.166.0 | `codex-jsonl-0.154.0` |

Claude 共收录 405 个已发布版本（1.0 系列 62、2.0 系列 70、2.1 系列 273），仅表示冻结版本策略。低于 2.1.267 的版本仅允许 typed reviewer/read-only，不能使用 legacy profile、候选 action 或 workspace。1.0.86 之前使用 `--disallowedTools`，之后使用 `--disallowed-tools`；所有 typed Claude 仍须宣告 `--tools`，缺失即 `provider_capability_unsupported`，不会降级为宽权限。写入仍仅 exact 2.1.267/2.1.316 + 已核验 SHA。未逐版运行本轮 native/live；[合流依据与边界](../tasks/cross-cli-hardening-20261105/docs/T10-compatibility.md)。

Codex 协议名仍然表示 0.154.0 起的 JSONL 合同，不表示只允许 0.154.0。历史 SHA `4f859826…afcc` 只用于识别那一枚基线二进制，防止它被标成其他 Provider。Codex 生产 profile 还要求 exact native pin 与已安装完整 native runtime；原生认证 guard（`plugins/codex-orchestrator/lib/trial/auth_guard.py`）按受控 bundle 与精确 pin 验证。Codex write 仅 exact 0.166.0；只读目录窗口不能扩大 coding 门禁。

AGY 的调用参数在本批次多了一项：`BuildInvocation` 对每个 AGY 版本都追加 `--add-dir <请求的工作目录>`，`CheckCapabilities` 也把 `--add-dir` 列为 AGY 必需 flag。原因是窗口两端行为不同——1.2.3 不把进程 cwd 当工作区，cwd 范围的读取会答成"没有活跃工作区"并转向用户 HOME 搜索，1.4.12 遵守 cwd。该目录本来就是 profile 自己授权、并且已经过 `EvalSymlinks` 复核的工作目录（`internal/host/profile.go`），沙箱读侧是 `allow default`、写侧默认全拒只放开 scratch，Implementer 角色才放开工作目录，所以显式声明工作区不扩大任何文件权限。不宣告该 flag 的构建判 `provider_capability_unsupported`，不会被降级执行。

版本放行不打开其他能力。`profile_resume_not_verified`、AGY `implementer_unsupported`、Grok `grok_session_write_required`、Claude 对 `Agent`/`Task` 的拒绝，以及权限扩展拒绝都保留。`--help` 里有某个参数，只满足能力检查；事件协议仍走现有解析器。Grok 1.0.67 及 1.0.70 的 `streaming-json` 增加 `usage` 记录，解析为诊断事件，不进入最终回答，也不移动工具边界。

更新上限时，重新查询上表中的官方发布源，替换目录文件和 `tasks/cli-version-compatibility/data/` 下的清单，保留旧清单。不要把运行时改成每次启动访问网络。然后重跑 adapter 测试，并对新上限二进制做 `--version`、`--help` 和事件字段核对。预发布版继续排除。

源码新增 Grok 1.0.34 的显式只读会话写授权：`task run --grok-session-write` / `profile.grok_session_write=true`。Host 派生新目录，原认证配置保留；内置工具限定为读取/列目录/搜索，另加 MCPTool 拒绝规则。当前新高层原生验收尚未完成，本机已安装插件仍为旧版；此实现不等于已对外开放。见[本轮进度](../tasks/mvp-grok-readonly/docs/progress.md)。

2026-09-28 起,Grok 结果流按真实协议事件 `tool_started`/`tool_completed` 识别助手消息边界:结果 artifact 只保留最后一个工具边界之后的公开文本(最终回答),此前记录的整体公开拼接不再作为结果;无工具事件时行为不变。工具事件本身不投影任何内容,终态后的文本或工具事件均按 `provider_event_after_terminal` 拒绝。该边界修复尚无新版真实模型复核,见 `tasks/grok-final-answer/docs/delivery.md`。

高层入口支持 `task probe --provider claude|codex|agy|grok` 和 `task run --provider claude|codex|agy|grok`，默认 Claude。`task run` 固定创建一个 reviewer / read-only Worker、一次 attempt，并由 owner review；实现任务使用 managed candidate 提交路径，不能由只读入口或版本目录放行推导写能力。AGY 与 Grok 不支持 implementer；Codex 仍要求 exact native SHA pin 与正式 full-native runtime，managed write 另限 exact 0.166.0。

Grok 只读任务也须事先取得本任务新会话目录写入及有界执行的明确授权，再设置 `--grok-session-write`（managed submission 使用 `profile.grok_session_write=true`）；缺少授权字段返回 `grok_session_write_required`。登录、锁确认或 probe 成功不替代该授权；它不授予工作区写权限。probe 仅检查版本与能力，不证明真实模型成功。此前 AGY 1.2.5 的真实高层只读验收及 Grok 1.0.30 的会话创建失败属于[历史试验](../tasks/mvp-multi-provider-entry/docs/delivery.md)，不代表当前 Grok 错误码或当前版本的真实验收结论。

```text
orchestrator provider-probe --request /private/probe.json
orchestrator provider-lock  --request /private/confirmed-lock.json
```

探测请求：`provider`、`binary_path`，可带 `lock_file`。路径须规范化且不指向 symlink。仅运行有界的 `--version` / `--help`，不会调用模型或修改锁。输出给出锁候选及 profile / resume 能力结论。

写入锁必须提供用户确认的 `confirm_sha256`；更新已有锁还必须提供其 `previous_sha256`。模型不得自行替用户确认升级。已有版本漂移时拒绝启动，重新探测、展示差异并取得确认。控制面的文件权限不隔离任意同 UID 恶意进程；最后一次摘要检查与 OS path exec 之间仍有这一既有威胁模型之外的竞态窗口。

## Owner 与返回方式

`owner_mode` 和 `delivery_mode` 分别表示授权来源和结果返回方式。legacy 省略字段的 native 默认仍保留；local owner 新提交空 delivery 默认 collect，显式非 collect 拒绝 `managed_owner_native_delivery_unavailable`，高层 task/MCP 固定 local/collect，不能由 legacy 默认推导 native chat 能力。

通用接入先准备 `0600` 的 owner 请求：

```json
{"controller_thread":"stable-main-agent-id","origin_pid":12345,"origin_birth":"OS process birth identity"}
```

执行 `owner-bind --state-dir <state> --request <file>`。PID/birth 必须是该控制连接的当前进程或同 UID 祖先进程；应选择持续存活的主脑进程。`birth` 使用进程身份探针，与协议中的 `process.Birth` 一致，不能只传 PID。返回投影字段和 `owner_capability` 路径，不返回 token。

提交时把这些 owner 字段加入现有 submit 请求，设置：

```json
{"owner_mode":"local","delivery_mode":"collect"}
```

协调器再次校验能力文件、已登记的 token 哈希、IPC peer 祖先链和 owner 身份。已登记 Source Host 的后代在 Worker 第一条事件到达前也不能派工；历史 Host 身份在 offline/rebind 后仍参与校验，PID 复用以 birth 区分。

新的 v2 bootstrap 磁盘文件只存非敏感元数据。Host launch ID/token 保存在既有 `0600` control capability 中，由可信 CLI 在内存重建 Hello，经限长 64 KiB、限时 5 秒的匿名 pipe 交给 Source Host；不写 argv、环境或日志，读取后关闭 FD。失败和退出保留 metadata/control，不能靠删除恢复材料重试。v1 bootstrap 仅保留只读启动兼容，不自动迁移；其中仍有旧 token，主脑不得将它或 owner/control/report capability、原始数据库作为诊断输入。

服务端在 HostHello 的注册或恢复状态写入前，核对持久化的 launch/owner 绑定、真实 Unix socket peer PID/birth、内核 executable 路径和 owner 存活。合法同 PID 重连只豁免当前 launch 精确匹配的 Host 自身，其他 Worker/Host 祖先仍拒绝。此修改不宣称阻止同 UID 主脑任意读取 control 文件；主脑仍只使用公开投影和已登记产物。

若任一活跃执行片段尚无持久化 PID/birth，`owner-bind` 与新 native submit 返回 `owner_registration_deferred`，防止 Host 意外退出后被重新收养的 Worker 自行取得主脑身份。判定按 segment 进行；同一 Host 的其他 Worker 身份不能代替它。已有 local owner 可继续提交；resume/retry/accept/answer/rebind 在此期间还要求连接祖先中存在已登记主脑身份。查询和停止仍使用原控制凭据。若 owner 同时丢失，须先通过显式 Host 恢复与进程对账解决不明片段，再登记/rebind 新 owner；不能清除 unknown 状态或删除数据库绕过检查。

`collect` / `wait-events` 返回紧凑事件与 `delivery_id`、`collection_proof_sha256`。本机收集模式的 ACK 使用这两个值，绑定该页所有 actionable event 的 ID/revision/hash/action slot；不可替换成 native `history_proof_sha256`。native 模式仍使用已有 history proof。ACK 只确认事件处置，`accept` / `answer` / `retry` 仍分别执行原有业务校验。

### 显式 rebind

旧 owner 丢失后，先用新主脑身份执行 `owner-bind`，再提交：

```json
{
  "version": 1,
  "owner_mode": "local",
  "owner_capability": "/private/state/owners/new-grant.json",
  "control_file": "/private/state/control/original-control.json"
}
```

执行 `rebind-owner --state-dir <state> --request <file>`。旧 owner 必须已失效，新 owner 必须当前存活且满足 peer 校验。原 run 的 `origin_context_id` 保留；rebind 不启动 Worker。后续恢复仍需要显式 `resume` 和进程对账。原生 rebind 的 attachment 证明流程不变。

上述流程适用于 v2 bootstrap。旧 v1 的显式 rebind 在修改数据库前返回 `legacy_bootstrap_rebind_unsupported`，保留现场；当前增量没有提供旧恢复材料迁移，不能将只读启动兼容描述为完整恢复兼容，也不能手改凭据或版本号绕过。

当前 `owner_projection_version:1` 明确返回 managed_process、native_push=false、typed_resume=false、automatic_rebind=false；只支持 fresh invocation 和 explicit rebind，不观察同进程聊天切换。父退出为 stop_and_retain，unknown 未确认前保留现场与资源。六类业务 mutation（resume/retry/rework/request-changes/accept/answer）在 receipt replay/事务更新前核验真实当前 owner；query/stop/ACK 保留原 bearer 收尾合同。

## 并发与数据库

在私有 state 目录创建 `0600` 的 `runtime-limits.json`，由下次协调器启动读取并事务保存到同一个 SQLite 数据库：

```json
{
  "version": 1,
  "global": 2,
  "providers": {"claude-code": 1},
  "workspaces": {"/canonical/worktree": 1}
}
```

数值范围 1–32；未指定的 Provider/工作区沿用全局值。默认全局 2。路径按规范工作区归一化；不合法字段、版本、权限或数值拒绝启动，不静默忽略。修改配置在下一次安全重启后生效；删除配置文件不会撤销数据库中已保存的策略。降低上限不杀现有任务；prepared/running/stopping/unknown 均计数。符合其他资源限制的 ready 任务可继续启动。

数据库当前 schema v11；v10 保存预算组政策，v11 保存 immutable rework refs/object/pin。旧合同不自动升级，升级前先安全停止并备份完整 state。以下为 v2～v9 的历史演进：v2 归并既有 runtime schema，v3 增加 local owner / collection receipt，v4 保存并发策略，v5 增加事务额度计数，v6 在既有 retry 账本保存绑定反馈的 revision payload，v7 增加 Host progress 记录及 global/run 额度触发器，v8 增加按 controller thread / Provider 保存 enabled 与 model 的 provider settings，v9 在 runtime_hosts 增加 owner_request_changes_v2 能力位。DDL、回填与 schema 版本更新在同一事务中提交。旧二进制拒绝新 schema；升级前应在协调器安全停止后备份整个 state，不能直接降级覆盖。

`owner_request_changes_v2` 默认 false，由 HostHello 注册或重连时更新；升级数据库不会自动赋予旧 Host 新能力。owner `request-changes` 要求对应 launch 唯一 ready Host 声明该能力，否则返回 `host_upgrade_required`（没有唯一 ready Host 时为 `rework_host_not_ready`）。已排队的 v2 返工在调度及下发 launch 时再次校验；不支持的 Host 不获得该 launch，旧任务不因缺少此能力而被一律拒绝。

spool 的 `ReadAfter` 只读取未归档后缀；`Read` 包括历史，供会话与进程恢复使用。ACK 前缀先持久化，再归档；中断产生的双份记录按 sequence/content 对账。归档不等于清理或释放存储额度。额度由 SQLite triggers 跟随事件 insert/update/delete 同事务变化，包含全局、run 和报告 capability；回滚不会泄漏计数。

### 受控报告产物

新提交报告的单个 artifact 上限与 Host 输出一致，为 1 MiB。协调器在读取内容前校验真实文件大小，再通过同一个 NOFOLLOW 文件描述符有界计算哈希，并核对前后文件身份。新 profile 的报告根目录限定为当前 capability 的 `artifacts/`；旧报告 capability 的已注册根目录保留兼容，但不允许 symlink 穿越。

已接受 artifact 的引用字节计入原有 segment/run/global 存储准入与事务计数，旧记录在迁移时回填。因此接近原额度的工作流可能更早被拒绝。相同文件被多个独立报告引用时保守重复计费；归档或 ACK 不退费，删除相应持久记录才更新计数。额度管理的是已提交记录及其产物，不是未提交工作区文件的 OS 文件系统配额。


### 未测量耗时的保守结算

`.12`修复退出事件省略`active_ms`时的免费结算。首次确认`Exited`后，缺失、零值或负值耗时均按该段`granted_ms`计满；可证明的正数耗时封顶于grant。UNKNOWN继续保留预占，重复退出不重复计费或退款。旧已结算账本不自动追补、不重置。

2026-09-28 起 Host 上报单调测量的段用时：`spawned` 成功并复核身份后打点，到"退出被观察（Wait 返回，含管道排空）或停止被确认（含宽限与强杀确认）"为止，毫秒向下取整，仅注入 `Exited` 事件（`internal/host/host.go`）。测量边界为 [−头差, +尾差]：起点前 exec→打点有毫秒级少计窗口，终点侧管道排空与最多 10 秒停止宽限计入、方向为多计；合成观测中尾差主导（在盘账本 `data/endurance/ledger-before-restart.json`：真实 `sleep 60` 上报 60010ms），deadline 段上报 3974/grant 4000（spawn 前调度间隙在窗外）。store 端仍按 `used=min(测量, granted)` 封顶结算；2026-09-29 起 Host 侧终态对称：`ensureLaunchUnknown` 与 `recordLaunchFailed` 一样，遇到已落盘的 `exited` 记录不再追加 `unknown`——`internal/events` 在 `os.Link` 使记录持久之后才返回，故 `ExecuteLaunch` 可能在终态已持久化的情况下才报错。段的 spool 因此不会同时携带两个进程终态；存储侧 `status!='exited'` 守卫继续保证迟到的 unknown 只成为历史、不重开结算。确认退出前的停止（pre-spawn `finalizeStopped`）与不确定路径保持 0，继续走保守计满，绝不臆造已用时间。deadline 自停改用独立有界宽限上下文（最多 `gracefulStopWindow`＝10 秒）后再强杀，兑现 §6"先温和取消、最多等 10 秒"的承诺；此前该路径复用已过期预算上下文，宽限窗口坍缩为 0。该窗口的两端现在都有测试：下界由 `TestDeadlineStopAllowsGracefulTerminationWindow`（要求子进程真的有时间清理）钉住，上界由 `TestTERMIgnoringChildIsEscalatedWithinBoundedGrace`（`trap '' TERM` 的真子进程必须在 15 秒字面预算内被 SIGKILL 收口）钉住，把常量改成 30 秒会让后者转红。以上为合成 OS 子进程与 store 回归验证的层次；真实 Provider 段的精确耗时分布尚未复验。

本机MVP试验的`max_active_ms=600000`是单segment上限；默认预算组总额是`7200000ms`，还受累计已用/预留额度和attempt次数限制。不能把单段上限当作组总额，也不能另起run绕过恢复预算。

## 2026-09-18 本机 Grok 修复结果

运行协调器已升级并公布 `grok_readonly_v1`；提交入口在预留 handle 前检查主任务和 fallback 的协议能力。原 run/task 标识、一次 120 秒预算及历史现场保留。真实 read_file 返回正确最终答案，collect→ACK/accept 完成。整体 artifact 为公开消息拼接，原计划的整体精确匹配检查未通过；该失败保留，主脑按最终回答语义限定接受。详见 `tasks/mvp-grok-readonly/docs/delivery.md`。不声明原计划全项通过或 MCP 原生拒绝已验证。
