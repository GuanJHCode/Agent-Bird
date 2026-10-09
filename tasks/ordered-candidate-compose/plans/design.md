# F03：有序候选组合设计（契约已冻结）

状态：已实施并在 macOS arm64 验证，最终生产快照 `766ddea` 已通过独立静态审查；Linux 仅 Store 定向通过，Git/Host 因缓存镜像缺 Git 未验证。以下保留冻结合同，末尾记录为满足该合同所作的必要修订。F02 schema31 historical diagnostics binder 已合并，真实历史 DAG proof 回归通过。

## 已定目标与边界

一个 `compose` action 接受同一仓库、同一 common-dir、同一 base 的 2..63 个已 owner-accept producer candidate，严格按声明顺序组合出冻结的 candidate commit。公开 fixture 固定两个 source，并刻意用反字典序 `[B,A]`。compose 是 Git 动作，不是模型动作：没有也不得伪造为任一 producer 的 `ProviderLock`。冲突保留现有 Git journal、owned worktree 和 private ref，返回可操作失败；owner 另行显式安排冲突任务，不自动调用模型、不盲重试。

之后的 `validate → review → integrate` 保持单 `SourceTask` 的 v1 contract；source 是 compose task 的 accepted result。它们必须能认证其 candidate 来自合法 composition，但不禁止既有 review 携带额外依赖验证证据。无 post-integration rework，不调用真实 model/account，不安装或发布。

## Action 与输入合同

v1 的 action、字典序 `DependencyInputVector{Version:1}`、`CandidateIntentHash` 的既有字节分支及 replay 完全不变。

仅当 `candidate_action.version=2` 和 `dependency_input_version=2` 同时出现时启用 composition：

- operation 必须为 `compose`；`source_task` 为空，`ordered_source_tasks` 长度 2..63、无空值和重复。
- `ordered_source_tasks` 与 task 的 direct dependencies 是相同集合；数组顺序是语义，不排序。每个 source 必须是 owner-review completed/accepted producer。
- Store 按该数组捕获 `DependencyInputVector{Version:2}`，每一项为已有的完整 `AcceptedInput`（accepted event、adapter payload、decision command ID）。重试、续段和 replay 逐项比较 task ID 与 canonical JSON，不能重新按 task ID 排序。
- v2 compose 的 `Input` 为 nil；不存在“主 source”。`CandidateIntentHash` 已包含 `DependencyInputs`，因此自然绑定 v2 vector；不为 v2 创造另一套 hash body。

Store 的事务只做现有 accepted-input vector/payload/hash 的捕获和 launch-intent 绑定。它不知道 compose 输出的 CandidateOID/tree/privateRef，绝不在 grant 事务内持久化 manifest 或做 filesystem/Git 操作。仍使用既有 vector persistence 与 wire 限制；不新增 schema migration、segment 字段、配额桶或 host-artifact reservation。`internal/store/candidate_input.go` 的 historical-mode 交叉改动由 ROOT/F02 完成；F03 不写该文件，并在其稳定后只从现有 binder 接口接入 v2。

## CompositionManifest 与 Host 认证

新增有界 `CompositionManifest`（versioned）和 `CandidateDelivery` 的 composition variant。它们随 compose 的既有、最多 1 MiB Host terminal artifact 写出，并由 result event 的 artifact SHA 认证；不增加 reservation，复用该 segment 已授予的 terminal artifact quota。

Manifest 不嵌入至多 63 个完整 1 MiB artifacts。它包含：run/task/attempt/segment/work revision/plan revision，repo root identity、common-dir identity、base OID，声明顺序的 source task ID 与 candidate OID；以及每项 source 的 accepted event identity、payload SHA、decision command ID、source delivery SHA、source candidate receipt SHA 和其 provider-lock 摘要。它还保存同序 OID、compose CandidateOID、TreeOID、PrivateRef 与 PrivateRefExpectedOID，以及 canonical manifest SHA。source 的精确 payload、accepted event 与 receipt 仍由 v2 vector 中的 `AcceptedInput` 和其 event artifact 引用提供；manifest 只引用和摘要它们。

Host 在任何 Git 改变前依次：验证 launch intent/vector；读取每个 source 的物理 artifact（managed artifact 走 `readManagedCandidateArtifact`，其他文件用 `O_NOFOLLOW` 路径）；核对 event artifact SHA；解码 producer `CandidateDelivery`；核对 accepted owner decision、receipt SHA、provider-lock 摘要、binding、repo/root identity、common-dir 与 base；并验证 producer private ref 仍解析到 receipt 的 expected candidate OID。任一项不匹配即在启动 worker 前拒绝。

`gitops.ComposeCandidate` 目前返回的 `CandidateReceipt.Binding` 为 nil；Host 在解析 worker 成功输出后必须补入本 compose segment 的 `CandidateBinding`，并把同一 binding 放进 `CandidateDelivery.Binding`。Host 同时重新认证 compose receipt 的 repo/common-dir/base、ordered OIDs、candidate/tree、private ref 和 expected OID，再写 manifest 与 delivery。这使 receipt 的身份不借用 producer binding。

后继 `readCandidateDelivery`/`readAcceptedCandidate` 对 compose source 除原有 artifact hash 与 binding 检查外，验证 delivery 的 composition manifest SHA、其每项摘要与 receipt 一致，private ref 解析到 expected OID，且 candidate/tree/ordered OIDs 与 manifest 相等。validate 把 composition delivery 原样复制进自己的 delivery；review 和 integrate 继续逐层复制。review 的 `ProviderLock` 仍是 review 自己的 lock，不能用 compose 或 producer lock 替代。这样单-source v1 后继合同保持不变，同时 provenance 一直可追溯。

## 受跟踪的 Git 执行、恢复与失败

Host 不能在 `delivery.go` 直接调用 `gitops.ComposeCandidate`。它在 v2 compose 的 `prepareCandidateAction` 分支构造经认证的 `gitops.CandidateCompositionRequest`，调用 `gitopsworker.BuildInvocation` 生成私有 request，并把该 invocation 转为 `process.Command` 交回 `Host.execute`。实际子进程经现有 `process.Start`、Host stop/UNKNOWN 状态机和原始 handle 的 `Wait` 路径运行；finalizer 只在该 tracked process 终止并产生输出后解析 receipt。不得创建未受跟踪的 goroutine、直接 `Run`，或绕过 original-Wait/取消恢复语义。

实际 Git primitive 和 worker route 已存在：`internal/gitops/gitops.go:CandidateCompositionRequest/ComposeCandidate`，`internal/gitopsworker/worker.go` 的 `compose` case，及 `gitopsworker.BuildInvocation` 的私有 request/journal。F03 仅把它们接到 Host；request 使用 Host 认证的 repo root、dedicated automatic worktree、attempt ID、base、ordered OIDs、plan revision 和 Host 派生 private ref，payload 不得提供 journal/ref/worktree。已有 `CandidateJournal` 的 candidate intent、ref intent、freeze receipt、UNKNOWN 和 cleanup 记录继续是唯一恢复依据。compose 不 materialize target、不 integrate。

组合冲突或 Git 失败不自动 cleanup 已持久化的 journal/ref/worktree；失败 artifact 使用既有受控错误码（冲突须可区分），由 owner 创建一个明确的后续冲突处理 task。恢复先按 durable journal 与 private-ref expected OID 对账；不重新组合，也不删除仍可能被 owner 或 downstream pin 的 ref。

## 最小实现位置与验收

F03 是一个 writer，完整实现由同一负责人完成，避免机械按 Store/Host 分拆：

| 位置 | 最小职责 |
| --- | --- |
| `internal/adapter/action.go` | v2 action 的 `ordered_source_tasks` 解码/复制。 |
| `internal/contract/dependency_inputs.go`、`contract.go` | v2 validation 保持 v1 hash；manifest/delivery contract 的严格校验。 |
| `internal/store/dependency_inputs.go`（及稳定后的既有 binder 接口） | v2 按声明顺序 capture/replay 与既有 vector persistence；不写 F02 所有的 `candidate_input.go`，不做 Git/manifest persistence。 |
| `internal/host/delivery.go`（可加专属 compose helper） | pre-Git 认证、tracked worker command、receipt 补 binding、terminal delivery 与后继 provenance 验证。 |
| `internal/gitops`、`internal/gitopsworker` | 不改 primitive 或 route，除非实现中发现已认证 request 不能表达必需字段；先报告该缺口。 |

必要测试仅覆盖可观察的新行为，并复用已有 `gitops` compose/unit worker 与 candidate lifecycle 证据：

1. 正例：A/B 同 base、accepted，声明 `[B,A]`；Host 以 tracked worker compose，产物 receipt/manifest/graph 的 `OrderedInputOIDs` 同序，private ref 指向 CandidateOID；随后的 validate/review/integrate 成功消费该合法 composition。
2. 反例：同集合改为 `[A,B]`、vector 重排或 replay 改序，在 Store 拒绝且未启动 Git。
3. 反例：任一 source 未 accept，artifact/receipt/provider-lock 摘要、binding、repo/common-dir/base 或 private-ref OID 不符，在 Host 的任何 Git 变更前拒绝。
4. 反例：composition receipt 缺 Host 补 binding，或 manifest/candidate/tree/private ref 被替换，后继单-source action 拒绝。
5. 反例：冲突、crash 或 UNKNOWN 后恢复只按 journal 对账，保留 owned worktree/ref，不重跑 compose。
6. 兼容：现有 v1 单 source 和 v1 多依赖字典序 vector 的 hash/replay tests 原样通过。

仍真实缺失的信息：当前 `gitopsworker.BuildInvocation` 的 caller-to-`process.Command` 适配点在现有 Host action 分支中尚未实现，且 compose 输出到 finalizer 的严格 stdout envelope/大小限制需要在实现时以 worker 实际编码确认；这不是待产品裁决，不阻塞上述冻结合同。

## 2026-10-09 根会话接管后的必要补充

独立窄审 `aaab0b5` 发现已有 compose primitive/worker 路由缺少 candidate runner、Git process journal、common-dir 锁及完整目录身份。为满足上述原合同，允许最小修复 Git 边界：receipt 增加 repo/common directory identity；`ManagedComposeCandidate` 复用严格仓库准入、原 Handle Wait 和锁；worker 在锁定 journal 后拒绝已有副作用或未闭合 Git process 的重放。Host 请求带有限 `SourcePins`，worker 在 common-dir 锁内再次验证，关闭 Host 认证与实际 mutation 间协作 cleanup 的窗口。旧裸 Git 单输入 API 保持兼容，v2 Host 入口仍限定 2..63。

Host 使用单独最多 1 MiB 的 stdout 接收 worker envelope，避免把 stderr 的 CLI error envelope 混作结果。冲突返回 `compose_conflict`，不确定进程/产物返回 `compose_uncertain` 并保留 UNKNOWN；二者均保留 journal/worktree，不自动 retry 或清理。完整库存留在 worker journal，公开 compose receipt 删除 `Files` 后由 Host 补自身 binding、计算 receipt SHA；producer 的旧 `CandidateReceiptSHA256` 仍指向完整 journal receipt，其 artifact 内省略 Files 的旧合同保持不变。

Store 初次实现用跳过 decision reconciliation 来绕开 `source_task` 为空，已被独审拒绝。正确修复必须将严格合法 v2 compose 视为单源 decision predecessor 链的根，保留所有 pending/consumed/retry/revision/budget 检查，不增加授权旁路。

公开并行生产者验证又复现：两个 `FreezeWorkspace` 竞争 common-dir 锁时，旧 nonblocking acquire 令一个 producer 失败。候选专用 `acquireCandidateLock` 保留单一锁 FD/inode 和 common identity，仅对真实 flock busy 等待，最多 10 秒且可取消；不重试 Git，不先写 candidate intent。每轮和获得锁后复核身份，替换立即拒绝。`FreezeWorkspace` 和 `ManagedComposeCandidate` 使用该等待；final integration/cleanup 继续原 fail-fast 合同。
