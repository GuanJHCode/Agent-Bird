# T12 两方向真实 MCP 试验与 cleanup/预算独立审计

VERDICT=PASS

> PASS 仅表示两次真实失败被如实记录、已知进程与账本清理证据充分且预算事实可复核；它不表示产品双向 LIVE gate 通过。产品验收结论仍为 `BLOCKED`。

## 检查范围

- Git root/worktree：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration`，branch `feat/cross-cli-hardening-20261105`，HEAD `48b3a9b25f7428c50c6305abbb1fb8eee14c48f0`，worktree clean；目标分支与 `origin/feat/cross-cli-hardening-20261105` 对齐。
- 产品包身份：版本 `0.1.24-hardening.20261105.3`，源码 commit `d700356c9ee497952b22ec9220d1fde111f71848`、tree `5b04a4d7fb49c47a5f16ebe26a04ddc3a1ee509a`、archive SHA-256 `dfbbf79b29471a9ec81b92598b74b03ba02af96ad4eac7f4b0e4ca53ee6529f8`。`d700356c..48b3a9b2` 只有文档/证据与一个 native gateway fixture 测试变化，没有改变本次 `.3` 包二进制。
- Harness/bootstrap pins：`scenario.py=0793486c05885a0420f299fb2842861e3a0a476389c2e1d803b4a5978952edd3`，`t12_harness.py=daceb63d793f41e64fcd9bedc3ebec06b5cc8237223c6a661db9f4d27f322274`，`final_trial.py=e5f4d534f8c8a37a370b68713c8c79dc78c2cfbeea6dfdc03d9c10626fb170e3`。
- 两方向只读核对 contract/bound-contract、launch、parent stdout/stderr、scenario-result；第一方向额外核对 observed-dispatch 的非秘密摘要、引用诊断与原始 CLI inspect 元数据；SQLite 均使用 `mode=ro` + `PRAGMA query_only=ON`，只查状态/计数/预算字段。
- 未读取 `state/control`、`owners`、`mcp-keys`、token/auth、session JSONL、source credential loader，也未输出 opaque handle/control-file、header、endpoint 或密钥。未重跑 CLI、模型、HTTP、认证或任何 live trial，未修改产品代码/状态库，未发送信号。
- 已有测试证据仅复核不重跑：独立 fresh harness `160/160 PASS`，JUnit SHA-256 `c1ae75ad2051ca9538b1684e566d70b43cac9f1a8ac37ea6415e5c342415a330`；bootstrap `20/20 PASS`，JUnit SHA-256 `40d77e2b4876d0ca0d0dd0c641e5ce0d2517d8072b68ba185ff94a141f191e01`。

## BLOCKERS

无。两次核心场景均正确保持 `BLOCKED`，未伪造 accept/ACK/FF，也未用父进程退出状态冒充 worker 退出。

## NON_BLOCKING

### 1. Codex parent -> Claude worker：真实 dispatch 成功，但引用被模型改写，LIVE gate 未完成

- bound contract 与 live-run contract 逐字节一致，SHA-256 均为 `fc419a422b5fb96eb4db24194c944b37852e65c00ba8dfcaa4aca363ca8fa0ab`；冻结预算为每 segment `60000ms`、组总额 `90000ms`、最多 `2` attempts，未发生 setup/budget reset。
- Codex 父模型真实完成一轮：parent JSON 记录 `turn.completed`，usage 为 input `148869`、cached input `123392`、output `2936`、reasoning output `0`。它成功调用 `dispatch` 和首次 `inspect`。
- dispatch 返回的 opaque handle 长度为 `847`；首次 inspect 输入与该返回值完全相等并成功。之后 wait/collect/inspect 输入长度依次为 `843/850/847`，SHA-256 分别不同于任何先前工具返回值，三次均为 `invalid_mcp_reference`。这证实失败原因是父模型改写 opaque MCP 引用，不是 MCP 服务端随机丢失原引用。
- scenario 为 `BLOCKED`，父进程 exit `0`、未超时，墙钟 `74.21436816698406s`。exit 0 只说明父 CLI 自行结束，不作为 worker 清理证据。
- 权威 CLI inspect 与只读账本共同证明 worker 生命周期已收敛：task/attempt=`interrupted`，segment=`exited`，segment_runtime=`exited/stopped`，`stop_reason=owner_stop_requested`，`active_segments=0`、`unknown_segments=0`、`pending_stops=0`。账本明确而非依赖父进程 exit 0。
- 预算已真实消耗且未重置：attempts used/remaining=`1/1`；used=`53160ms`、reserved=`0ms`、remaining=`36840ms`，budget_runtime=`settled`。provider-progress 元数据存在（只核对 size/hash，未读内容），结合实际 segment/host 账本，足以证明 Claude worker/provider 路径真实启动并产生进度；但 attempt 被 owner stop 中断，不能宣称 worker 完成、候选产出或验收通过。
- 无 review decision、无 delivery receipt、无 accept、无 ACK、无 FF；唯一 collection receipt 不等于 ACK 或 acceptance。

### 2. Claude parent -> Codex worker：父 CLI 出站被 401 阻断，未 dispatch

- bound contract 与 live-run contract 逐字节一致，SHA-256 均为 `4ae3a92b33b27547501eebf7aecec33cc9edf941aa82599df1181e6c4064eed7`；冻结 worker 为 `gpt-6-luna/low`，但从未创建 worker task。
- Claude init 记录默认模型 `deepseek-v4.1-flash-1017`、9 个 tools、Agent Bird MCP `connected`。随后恰好 10 个 `api_retry`，attempt 1..10，全部 `error_status=401`，错误类别为 authentication/authorization。
- `unrecognized_model` 是本地 warning；证据中没有 HTTP 404，也没有 model-not-found 响应，不能将该 warning 解释为模型 404。
- 父请求确实到达能返回 HTTP 401 的上游路径，因此“父 CLI 有真实网络出站尝试”已证实；但没有 assistant/result event、没有成功模型 completion。由于没有 dispatch，Codex worker/model 出站未发生、也未证实。
- scenario 为 `BLOCKED`，父进程 exit `2`、`timed_out=true`，墙钟 `174.17279916699044s`；output channels closed。state DB 中 runs/tasks/attempts/segments/budget_runtime/reservations/runtime_hosts/host_launches 全为 `0`，所以 used/reserved/unknown/active 均为 `0`，不是“消耗后重置”。
- 无 accept、无 ACK、无 FF。

### 3. Stop callback 与 owner cleanup 归因

- Harness `scenario.py:435-444` 调用 `task stop --handle ... --task-id ... --work-revision ...`；产品 `task_entry.go:414-426,499-512` 明确接受并转发 `--task-id`/`--work-revision`，因此 callback 错误不是 unsupported CLI flags。
- 第一方向未超时；callback 是在 postflight 发现没有唯一 accepted+ACKed candidate 后才于 `scenario.py:458-460` 执行。此时父 CLI 已退出，账本已由 owner cleanup 收敛为 `interrupted/exited/stopped`，故 `BLOCKED_CalledProcessError` 是已结束 owner/runtime 上再次请求 stop 的失败表象。Harness 没有保存脱敏后的具体 CLI error code，因此不能进一步声称精确子错误。
- 第二方向在超时前执行 callback，但从未 dispatch、没有 observed handle，`read_handle` 因文件不存在形成 `BLOCKED_FileNotFoundError`；随后 controller-first cleanup 执行。该错误符合“无任务可 stop”，不是 worker 清理失败。
- `scenario.py:238-306` 仅跟踪 controller 及其已验证 PID/birth/executable descendants，先给 Go controller/PGID cleanup 机会，再只对仍存活且身份相同的记录进程发信号。两方向 `remaining_owned_pids=[]`、`unverified_owned_pids=[]`、`discovery_errors=[]`、output channels closed；审计时对 scenario 记录的全部 PID 做只读存活检查，无匹配存活进程。
- 第一方向 state ledger 另有 `runtime_hosts=offline`、`host_launches=offline`，是 detached SourceHost/worker 已退出的独立证据。源码中 coordinator 与 SourceHost 都以 `Setsid` 启动（`main.go:809-875,1209-1237`）；Harness 不应按名称或全局进程扫描杀未知进程。本次没有未知进程、没有在审计中额外清理；已知 idle coordinator 是否由 harness 回收只由已记录身份/存活状态决定，不应扩展为杀任意同名进程。

### 4. Harness 可维护性建议

- `scenario.py:443-444` 当前只保存异常类（`CalledProcessError`/`FileNotFoundError`）。未来仅做 offline harness 修复时，可保存一个严格白名单化的产品 error code/category（不得保存 argv、handle、control path、header、endpoint 或 stderr 全文），以直接区分 already-stopped/owner-unavailable/handle-missing。该缺口不影响本次 `BLOCKED` 真实性或账本清理结论。
- 不应为获得更好错误文本而追加 paid trial；short-ref 修复也只应离线实现和测试。本报告不建议修复模型引用、自动 accept 或自动 ACK。

## 剩余风险

- 两方向均未产生可验收 candidate，未走 accept/ACK/FF，故 LIVE-01..LIVE acceptance gates 不能 PASS；本报告的 PASS 不能用于产品发布验收。
- 第一方向证明了真实 worker/provider 启动和进度/预算消耗，但未读取 provider 输出正文，不能把它提升为“worker 正确完成模型任务”。第二方向只证明 401 网络出站尝试，不证明模型推理执行。
- Harness 对 stop callback 只留异常类，精确子错误只能依据调用时序、产品参数合同和最终账本高置信归因；缺少当时脱敏 error code 是剩余诊断粒度风险。
- SQLite 与进程证据是审计时快照。state DB SHA-256、失败日志/结果 SHA-256 见 `data/evidence-sha256.json`；后续若任何状态或证据文件变化，应重新核对受影响部分。
