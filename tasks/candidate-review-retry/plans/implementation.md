# Candidate review prelaunch retry

Goal: F01后续阶段的一个真实垂直增量。在现有有限预授权retry基础上，让candidate review阶段同类未Start的probe EAGAIN/EINTR可安全重试，保持所有候选/DAG/预算契约。不是整个F01完成。

Base: integration 32b717367f1a93ae93e5d90b18ee509ff47147c6，F03 dependency vector与单producer closure/F05已经稳定。独立分支feat/closure-candidate-review-retry-20261007；实现worker交付 b976cf1 后已交还 ROOT 唯一写入权。子Agent不得再派生，不能重做第一批或动Owner/Launch公共wire/schema。

冻结行为：
- 仅显式带既有execution_policy.retry的candidate review（包括已稳定indexed review）与原失败事实prelaunch-probe且not_started；不扩失败stage/code，不把主worker Start失败/任何可能有副作用状态算noStart。
- 准入继续拒绝candidate implement/validate/integrate、gitops及fallback/不支持组合；权限拒绝、UNKNOWN、超时、已启动、旧或伪造事件不自动重试。
- 同一仍live/ready且精确scope的Host；Host故障/owner切换不触发retry，由F10处理。
- 新attempt保留原work revision、固定候选身份、来源receipt/config/provider与完整immutable dependency vector/index assignment；严禁重新采样已变化上游或通过retry刷新许可。复用既有manual reviewRetry的正确输入冻结机制，必要时抽最小共享helper；不能重建不同语义的新输入。
- 相同事件/命令重放幂等；事务先决定retry再对依赖暴露最终失败，不使依赖被错误终结或重复派发。累计预算、attempt上限、退避及新process许可原规则继续有效，不退款/不重置。
- 不新增DTO/schema；若真实必要契约冲突先返回ROOT。CLI公开submit/dispatch中安全放开review对应guard，不能只内部Store可达。

验收：现有非candidate通过/拒绝回归，candidate review真实Store链prelaunch EAGAIN→新attempt+不变input；reopen保留due/budget，迟到/重复/Host offline不双派；indexed与legacy review同契约。有效RED和最终JSON必须留原session至terminal并统计top/named/fail/skip。Worker可运行隔离纯Store/contract单测和build/vet；Mac物理Provider/进程/sandbox以及Docker token归ROOT，准备真实CLI受控场景后由ROOT串行跑。无账号模型、用户安装、push或发布。

交付：最小实现/回归、本地提交（允许），本文件更新决策/路径/原始证据/未验证项；由ROOT独审、实际CLI验证后集成。禁止把代码生成、compile或helper证明当整项完成。

## 实施记录（worker）

- 2026-10-07：仅允许带 `execution_policy.retry` 的 readonly candidate review（Claude 或 Grok review（普通与分块均为既有支持路径））进入 policy retry；candidate implement/validate/integrate、写权限 review、fallback 与 gitops 仍在 admission 拒绝。
- `queuePolicyRetry` 对合法 review 将原始 grant 的 accepted input、完整 dependency vector、adapter digest（包含 index assignment）固定进既有 review-retry decision；Claim 使用原有 replay/binding 机制，不重新采样输入。普通 policy retry 的空 revision payload 不变。
- 证据（原命令输出在本 agent session，无额外 JSON 文件）：
  - RED：`go test -count=1 ./internal/store -run '^TestPolicyRetryCandidateReviewPrelaunchPreservesFrozenInputs$'`，原实现失败 `execution_policy_retry_stage_unsupported`。
  - GREEN focused Store：6 项命名测试通过（candidate prelaunch、indexed assignment、非 review 拒绝、dependency vector、durable backoff、failure facts）。
  - `go test -count=1 ./internal/store`：通过，`ok .../internal/store 39.005s`（在最后一次 indexed helper test 前；最终 focused 集覆盖该变更）。
  - CLI preflight：3 项命名测试通过；`TestCLIIndexedCandidateDevelopmentDAG` 在三个 readonly indexed Grok review 都显式携带 retry policy 后通过，15.328s，证明公开 submit→grant→受控 review→integration 的正常链仍可达；`go build ./cmd/orchestrator` 与 `go vet ./internal/store ./cmd/orchestrator` 退出 0。
  - 受控 Host/Coordinator：3 项命名测试通过，Host `0.916s`、Coordinator `0.932s`，覆盖 trusted noStart facts、回放及同 Host wake。
- 未验证：未触发真实 kernel EAGAIN/EINTR、未运行真实账号 Provider、Docker 或全包 native CLI；未生成测试 JSON session 文件。ROOT 负责最终 CLI/native 验收与集成。

## ROOT 接管与最终验收

- b976cf1 的实现证据未留原 JSON；worker自述不作为最终验收。ROOT独审发现同HostID rebind能消费旧重试，真实 `root-host-rebind-red.jsonl`（session80816 exit1）复现；新增私有 Host identity digest固定generation/PID/birth/owner tuple，Claim前核验。epoch刻意排除以允许原Host重连，无public DTO或schema变更。
- 原“Grok必须indexed”审查意见已撤回：base原生支持legacy完整快照。ROOT同时验证Claude及legacy Grok真实Store policy链；indexed fixture通过r2实际失败入队、重复event、DB重开、不提前重试、新attempt冻结input/vector/assignment、三shard接受及整合。v2配置缺TimeoutMS、v3错误忽略原failure历史，均为fixture错误，不计产品RED；最终focused session40238 exit0，5顶层/24具名PASS。
- identity新增真实BeginCoordinatorEpoch/RegisterHost四场景：same tuple可继续，新PID、同PID新birth、offline拒绝且无新attempt；原RebindOwner场景覆盖owner/generation变化。稳定Go代码两轮独立只读审查PASS。
- ROOT完整Store session72077 exit0，310顶层/866具名PASS，0fail/0skip。race及公开CLI最终证据另附receipt，不沿用旧worker日志。
- 当前不证明真实kernel EAGAIN/EINTR、真实模型、Linux Provider或Intel/Windows原生链。仅本F01垂直增量，不是F01–F13整体完成。

最终验证：Store race session79833 exit0，48顶层/135具名PASS；串行公开CLI/Host/Coordinator race session55294 exit0，7顶层/12具名PASS（包含携带retry policy的实际三shard CLI整合链，未注入真实内核失败）。两批0fail/0skip；CLI测试内部build的受控产品binary不是race插桩，scope按实际编译边界记录。`go build ./...`、`go vet ./...`、`git diff --check`均0。原JSON的session、SHA与计数见 `../data/root-final-receipt.json`，不重复采用worker自述。所有本批ROOT测试取原终态，未创建Docker容器。
