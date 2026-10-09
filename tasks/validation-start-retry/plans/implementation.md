# F01 validate worker 未启动的有限策略重试

基线：integration `9552f0e`。ROOT 冻结范围，单 worker 独占本 worktree；独立审查只读稳定提交。

## 正确性契约

- 仅 v1 `candidate_action.operation=validate`、`AutoDirectory=true`、显式有限 execution retry policy。排除 indexed review、fallback、session、自定义额外执行参数；不开放 manual retry、implement、compose、integrate 或任意 validate 运行失败。沿用现有预算、attempt 上限、backoff、hold/claim、幂等 ACK、Coordinator wake 和 work revision。
- `process.Start` 仅在真正进入原 `cmd.Start` 并返回 EAGAIN/EINTR，且 Process/ProcessState 均为空、context 仍有效时，形成不可由普通错误字符串或序列化 JSON 冒充的 typed never-started proof。任何 non-nil Handle 保留原 Stop/Wait；post-Start 身份观察失败、取消、权限错误、任意其他错误和 cold-restart `starting` 均保持原有拒绝/UNKNOWN 语义。
- 新 failure stage `validation_start`，codes `validation_start_eagain` / `validation_start_eintr`，worker_state `not_started`、probe_state `not_applicable`。其含义仅是验证 worker 未运行；之前受管理 Git 物化及其完整 journal 已发生。不能伪称整个阶段无副作用，也不能借用 `prelaunch_probe`。
- 活 Host 只在本次调用持有上述 proof、验证 profile 符合范围且未收到 stop/cancel 时，允许本次 `starting` 结束为 `not_started` 并写认证的 launch_rejected 事实。使用独立窄入口，不普遍放宽旧 `recordLaunchFailed`。诊断及事件持久化失败须保持 UNKNOWN；已有 UNKNOWN/终态不得覆盖；重开 Host 不得从 launch.json 推导 proof。
- 新可选 `CandidateAction.CommandSHA256` (`command_sha256`) 固定 validation executable；只有 validate 可用，开启本重试时必须是规范有效摘要。旧无重试 action 保持兼容。摘要作为原 adapter/grant/decision 的认证内容，Host 首次及每次重试均重核，不在 Store 写事务内跑 FS/Git。
- 使用独立 `candidate_validation_retry` decision kind，复用现有 ledger，无新 schema。冻结原 accepted source receipt、完整 dependency vector、原 adapter（包括 tool digest/command/target/config）、工作/计划 revision 和实际 Host 身份；queue、pending/claim、bound replay、历史 proof 重建均核验，不影响旧 review decision 的含义。
- 原候选输入只允许既有生产者或有序 compose 的有效 artifact；新 attempt 使用既有 AutoDirectory 唯一目录，不改写原候选、receipt、原 attempt 或预算历史。重试不足额度、输入/工具/Host 漂移、未settled、stop、未知证明均拒绝。

## 最小验收与证据

1. 必要 typed proof 拒绝矩阵（syscall 起点/Context/Handle/post-Start/string spoof），先保留原码失败或明确新 API 编译 RED，不能伪称业务 RED。
2. 聚合 Store 策略/冻结输入/身份/预算/幂等/claim-replay 拒绝矩阵，覆盖相邻旧 probe/review 行为。
3. 一条真实 Host + tracked Git 物化 + 受控 Start 返回失败 + Store 自动入队 + 新 attempt 验证成功/原 Wait 链。Start 故障注入必须 per-call、无全局环境后门，报告明确不是内核自然 EAGAIN 资源耗尽。受控 seam 不能把先启动后返回错误变成 proof。
4. 稳定代码后定向 race、whole build、受影响包 vet；不重复全仓/全 Store。保存原始 go test JSON、stderr、exit、source SHA、准确 top/named PASS/FAIL/SKIP，所有失败保留。
5. ROOT 核原始证据及独立固定快照审查后本地集成。无真实 Provider/model/account、用户安装替换、push/release。Intel/Windows 延期；当前 F13 包不自动覆盖本次新生产源码。
