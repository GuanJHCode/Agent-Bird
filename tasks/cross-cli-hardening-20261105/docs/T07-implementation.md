# T07 实施与验收记录

基线 `2347d34ce1e5ca4e40751f53064f3d99001f4832`，Go 1.26.10 darwin/arm64，schema 10。实现者 `/root/implement_t07`；无递归委派、commit/push、真实模型调用或凭据读取。工作区仅 `tmp/t07`。最终待独立审查及主会话集成签收。

## 实现

1. `internal/coordinator/local_owner.go` 复用既有 owner_grants、controller_thread、PID/birth、HostGeneration。`OwnerBindRequest.owner_projection_version=1` 与 Ready `managed_owner_projection_v1` 协商投影。默认legacy请求不增加字段；未知版本拒绝。共享 `OwnerBindResponse` 严格支持两种shape，缺能力投影即不可用，不反推native能力。CLI `owner-bind` 仅协商成功才发送扩展；`task_entry.go` 改用共享严格response。完整复制CLI owner输出的测试消费方显式剥离两个非授权投影字段。
2. 投影冻结为顶层 `owner_scope=managed_process`，`owner_capabilities.version=1`；collect/wait、fresh_invocation、explicit_rebind可用，native_push/typed_resume/automatic_rebind/observes_chat_switch关闭，parent_exit_policy=stop_and_retain。CODEX_THREAD_ID、MCP ID、native字符串不作为宿主原thread证明。OwnerGrant capability/digest/schema不变。
3. `server.go` local submit先验证真实authority，省略delivery默认collect，显式非collect拒绝 `managed_owner_native_delivery_unavailable`；legacy native授权/返回路径及旧持久记录不迁移。
4. 本轮发现并修复dead owner旧control可以新排resume决定的问题。6类公开resume/retry/rework/request_changes/accept/answer先核原task/thread/control，再读取DB当前owner并查kernel PID/birth。原context属于owner_grants的local run还须真实peer ancestry包含这个run owner。native既有bearer委派保留，但同样要求真正owner活着，且不依赖delivery_mode字符串。
5. 新 `store/owner_action_guard.go` 通过context携可信DB snapshot（含HostGeneration）；Store业务mutation在同一tx、任何replay之前重核，rebind后旧snapshot拒绝，tx中再次查kernel birth。trusted内部Store直调没有RPC peer，保留既有库边界；所有公开六类RPC都必设snapshot，无test/env授权绕过。
6. query/stop/ACK原bearer安全收尾合同与专用recover-host inspection保持。该恢复inspection只做对账；后续HostHello另核当前绑定及owner存活，不自动dispatch新决定。QueueRequestChanges只新增同样tx hook，不带T04 v3。

## 验证与证据边界

计数均为Go JSON中的test/subtest终态动作数，未把exit code或旧报告当作测试通过。

- `baseline.json`：14pass。
- `focused-v2.json`：协商/strict decoder/public collect/native拒绝/dispatch等47pass。
- `race-focused.json`：4包46pass，含currentOwner、controller TERM/KILL、worker、parent loss、budget rebind。
- `projection-final.json`：22pass，包含legacy native路线不受local gate影响。
- 首轮全量 `regression.json`：1261pass/8skip/2fail，唯一根因是固定PATH未含codex导致sandbox子测试及父测试失败；补所需~/.local/bin后 `sandbox-recheck.json` 3pass。保留原失败，未修改测试来消除。
- `action-guard-red.json` 保留修复前resume反例；`action-guard-focused.json` race 26pass，含6类初始授权、wrong birth、本地sibling拒绝、native live独立delegate、显式rebind且预算完全不变、5类tx stale snapshot（当时尚未加第六hook；最终116pass证据已覆盖六类）。
- `guard-regression.json`：全模块 uncached exit0，1291pass/8skip/0fail，14个有测试包通过（contract包无tests）。此轮在最后授权的3行legacy QueueRequestChanges tx hook之前结束。
- 最后hunk后 `guard-final-race.json`：Store/Coordinator race exit0，116pass/0fail/0skip，包含全部六方法stale snapshot、已有retry receipt的rebind后重放、owner request-changes完整族、当前owner授权、worker、budget rebind及stale epoch。
- 最后hunk后 `guard-final-cli.json`：public CLI race exit0，7pass/0fail/0skip，含owner返工、auto workspace和managed dispatch重复handle/worker递归。
- 最后hunk后 `guard-final-build.json` / `guard-final-vet.json`：均exit0。gofmt只读检查与git diff --check均通过。
- 最后hunk后未机械重跑整个模块；受影响路径已经上述race/CLI补验，其余未受影响全量证据复用。

原stop反例的拒绝期待与 `docs/provider-runtime.md`「查询和停止仍使用原控制凭据」冲突，是错误oracle而非产品bug。原失败 `owner-control-counterexample.*`及源码保留；新增正向测试证明原control可安全终止且不改owner binding。resume反例属于新权限，保留 `owner-resume-counterexample.*`及overlay源码，永久回归要求拒绝且保持interrupted。

8项skip来自原有opt-in的原生Claude运输/真实模型/观测测试：

- `TestProbeCauseDecompositionObservation`
- `TestNativeClaudeGatewayReadOnlyTransport`
- `TestNativeClaudeGatewayManagedWriteTransport`
- `TestS4StageTimingObservation`
- `TestRealClaudeManagedCandidate`
- `TestRealClaudeReviewerProfile`
- `TestRealVersionMatrixRead`
- `TestRealVersionMatrixInvocationStages`

未做live-parent原生clear/resume/多聊天真实验收；不能声称verified_native_session、主动native push、typed resume或聊天隔离已验证。普通MCP session不是聊天ID。

既有native bridge的attachment/lease/owner-helper/service/backend receipt链由 `lib/native_bridge/bridge.py` 核验，脚本用于transport/history ACK；本改动不把它包装成KindOwnerBind的native session证明，也不修改该路线。这里的native protocol正向仅证明既有控制委派及delivery字段保持，不代表回执进入真实原thread。

## OWN映射与交付

- OWN-01：真实socket peer ancestry、PID/birth错配、cap/token/HostGeneration、stale snapshot与旧epoch既有回归；无法强制操作系统真实PID重复，PID复用以同PID错birth的fixture及现有exit-identity OS测试证明guard。
- OWN-02：明确managed_process不保证宿主内聊天隔离。collect/rebind可用，local native push关闭。live-parent项未执行，非PASS冒充。
- OWN-03：旧control新执行决定须当前owner或显式rebind；rebind不重置预算；既有unknown/epoch/bootstrap恢复回归复用当轮结果。旧handle原路由保持。
- OWN-04：worker继承env/已有handle快路径不能递归；父退出停止/unknown保留现有合同由当轮OS子进程测试覆盖，无模型调用。

实际差异在 `implementation.diff`，全部文件SHA在 `source-manifest.json`，命令/exit/计数在 `validation-summary.json`。外部必要红绿证据、ADR及复现源码保留；无需清理的临时文件留在项目源码。最终未独审，不commit。

历史试跑错误也保留：focused.json因新测试误用不存在TaskStatus helper编译失败；focused-green.json因新负向CLI fixture漏原有ORCHESTRATOR_ENABLE_TEST_FAKE=1而先撞running_package_invalid；action-guard-protocol.json因测试将run ID传给实际接task ID的SummarizeRun而not_found。均修正测试接法并在后续有效报告通过，生产guard未用test/env绕过。原有fake flag只在既有fake adapter/未打包CLI fixture中沿用。

最终源码冻结：implementation.diff SHA256 `41a838ea5b21d65e59cef4a88a0876c4990b1bb7b90238f9c10e4d748a02c49e`；source-manifest.json SHA256 `94054d1eaaf3003866e3e23dde0e2e810ed696a1b9f60ab920509cbf0cda0ab2`；shared owner_action_guard.go SHA256 `d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150`。全部测试/构建/vet会话已结束，源码冻结后无再改动，待独审/主会话集成验收。
