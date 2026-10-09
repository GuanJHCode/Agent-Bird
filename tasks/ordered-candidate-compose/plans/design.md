# F03-A：有序双候选组合设计（待契约冻结）

状态：仅设计；不授权实现。基线为 `f7db2e8`，调查到的已有底层能力是
`gitops.ComposeCandidate` 与 `gitopsworker` 的 `compose` 路由。F02 的 diagnostics
删除变更与本设计没有共享写入权；先冻结以下公共合同后才可实现。

## 目标和边界

一个公开 DAG 的两个 producer 从同一 `base_oid` 生成、完成并经 owner accept 后，
由一个显式 compose task 依声明顺序把两项候选合成为冻结 commit。后续
`validate → review → integrate` 只消费该 compose task 的 accepted result，最终
integration 指向当前 target，且验证的 candidate、private ref、所有输入 OID 与
composition manifest 一一对应。

本期只覆盖 clean、独立的两个 producer；冲突停止 compose 并保留 journal/ref/
worktree，不做自动冲突解决。保留既有 input event、accepted evidence、provider lock、
candidate receipt、repo common-dir/base、配额、UNKNOWN 与 original Wait 语义。没有
post-integration rework。

## 新合同：显式 v2，不重写 v1

`candidate_action.version=2` 与 `dependency_input_version=2` 必须同时出现才选择
v2。v1 action 或未声明版本仍走当前单 `SourceTask` / task-ID 字典序 vector 和既有
`CandidateIntentHash` 字节合同；不得排序后将 v2 数据伪装成 v1，也不得修改历史
v1 vector/hash/replay。

v2 的 action 增加 `ordered_source_tasks: []string`。它是语义顺序，必须与 task 的
direct dependencies 同集合、同长度、无空值、无重复，长度为 2..63；本 F03-A 的
验收固定为两个且故意采用反字典序。Store 按此数组装配
`DependencyInputVector{Version:2}`，并在 replay/retry 逐项比较 task id、完整
`AcceptedInput` 的 canonical JSON，而不是按 task id 排序。

`operation="compose"` 只在 v2 合法：`SourceTask` 必须为空，producer 必须是 direct
dependencies 且 owner-review completed/accepted。`validate`、`review`、`integrate`
继续需要恰好一个 `SourceTask`，该 task 必须是 compose task 并且是其 direct
dependency；它们不得重新直接引用两个 producer。由此 `Input` 始终是单一、内容承载
的 compose accepted result，而 v2 vector 是 compose 自己的输入证据。

需新增 canonical `CompositionManifest`，至少包含版本、run/task/attempt/segment、
repo root/common-dir identity、base OID、声明顺序的 source task IDs、对应 accepted
event identity/SHA/decision command ID、同序 producer candidate OIDs、compose candidate
OID/tree OID/private ref、plan revision、以及 canonical manifest SHA。不得从分支名或
最新工作区推断任何字段。

## Store、计费和认证

Store 在现有 bind 流程中新增 v2 分支：先验证 DAG 和 action 版本/operation，再按
`ordered_source_tasks` 捕获所有 `acceptedCandidateInput`。每项 producer payload 必须
是可验证的 candidate delivery，且 repo/common-dir/base OID 相同；candidate receipt
的 ordered input OIDs 必须能认证 producer candidate OID。任一不一致、重复、缺失、
未 accept、base 漂移或历史 capture 改变均拒绝，不启动 Host。

为保证 crash/retry 不会重新组合，新增 segment runtime 的
`composition_manifest_json` 与 `composition_manifest_sha256`（或等价的独立、以
segment ID 键控的持久表）。写 grant 的同一事务持久化 v2 vector、canonical manifest
和其 SHA；`storageAdmission` 计入 raw vector、manifest 和 hash 的实际新增字节，仍受
现有 64KiB wire/存储限制。`CandidateIntentHash` 新增仅在 action/version=2 的独立
canonical body，绑定 vector 与 manifest SHA；v1 的三字段及其已有扩展分支原样保留。

Host 读取 compose grant 时必须重新验证 vector、manifest hash、每个 accepted event
artifact SHA 和 decision ID，重解 producer delivery 并比较 base/repo/common-dir/OID。
compose 的 `CandidateReceipt` 和 manifest 写入受管理的 Host artifact；结果 event 的
artifact hash 认证该字节。下游 `readAcceptedCandidate` 必须验证 compose manifest、
receipt、private ref expected OID、candidate/tree/ordered OIDs 的相等性，不能只信任
`Input` 的单一 artifact。这样 validate/review/integrate 的既有 provider-lock 和 target
binding 仍从 compose receipt 取得，且 `OrderedInputOIDs` 一直保留声明顺序。

## Host 调用和恢复

compose 是 Host 的无模型候选动作。它使用现有受控 Git worker 路径调用
`gitops.ComposeCandidate`：request 取已认证的 repo root、专用 compose worktree、
attempt ID、base OID、manifest 的 ordered OIDs、plan revision 和仅由 Host 派生的 private
ref；不让 payload 指定 journal/ref 路径。复用现有 `CandidateJournal` 的 create intent、
freeze、private-ref pin、worktree identity、UNKNOWN 与 cleanup 记录。它不 materialize
target，也不调用 integration。

compose 成功后把 receipt/manifest 作为一个 candidate result 交给现有 owner accept；
其后 validate 物化 frozen compose candidate，review 绑定 validate evidence，integrate
按现有 protected target contract 只快进/整合该 final candidate。任何 source receipt、
manifest、target base、plan/work revision 或 provider lock 变化都使旧 review/integration
证据失效。失败、取消和 UNKNOWN 保持原 journal 和 original Wait 恢复路径，不能用
cleanup 删除仍被 downstream/owner decision pin 的 private ref。

公开 fixture 使用受控 fake Provider 与 Git fixture，不调用真实 model/account；它要验证
worker request 的 repo/common-dir/base/private ref、受控 accepted artifacts 和实际 Git
ancestor/tree，不将合成的 mock receipt 当作 Host 行为证据。

## 实现归属（冻结后）

| 负责人 | 唯一文件/责任 |
| --- | --- |
| F03-A Store | `internal/contract/dependency_inputs.go`, `contract.go`, `internal/adapter/action.go`, `internal/store/candidate_input.go`, `dependency_inputs.go` 及对应 migration/persistence tests：v2 wire、intent/hash、顺序/replay/计费。 |
| F03-A Host | `internal/host/delivery.go`（必要时专属 compose helper 与 tests）：认证 manifest，调用 existing Compose primitive，交付 compose receipt，保持 lifecycle。 |
| Gitops | 不改 `internal/gitops`/`gitopsworker`，除非独立审查证明现有 request/journal 无法承载已认证字段；其公共行为已有受控 Git 证据。 |
| F02 | diagnostics explicit delete 的独立文件和测试；不得修改上述 v2 action/vector 合同。 |

## 最小验证矩阵

1. 正例：A/B 同 base、均 accepted，声明 `[B,A]`（反字典序）；compose receipt 的
   `OrderedInputOIDs`、manifest、private ref 与 Git graph 同序且候选包含两项提交；随后
   validate/review/integrate 到 target。
2. 反例：同一集合但 action 顺序与 vector/manifest 不一致，或 replay 将 `[B,A]` 改为
   `[A,B]`，必须在 Store 拒绝，不能启动 Git。
3. 反例：任一 producer 未 accept、candidate/base/common-dir 不同，或其 artifact/receipt
   SHA 被替换，必须拒绝。
4. 反例：compose worktree/ref/receipt 在 crash 或 UNKNOWN 后身份变化，恢复仅按已有
   journal 对账；不重新合成、不删除 pin。
5. 兼容：已有 v1 单 source 与 v1 字典序多依赖历史向量的 intent hash、replay 和测试
   保持不变。

## 待产品/契约裁决

1. v2 compose 是否只接受两个 producer，还是开放 2..63；设计采用 2..63、首个公开
   fixture 固定两个。
2. compose 冲突是终态 reject，还是要生成显式、人工指定的冲突解决 task；本设计选择
   前者，后者不能暗含在 compose。
3. manifest 应内嵌在 `DependencyInputVector` 还是独立 segment 字段；本设计建议独立
   字段避免改变 v1 vector 的 canonical bytes，并要求明确 schema/migration 评审。
4. validate/review/integrate 是否允许 v2 vector 额外携带 producer evidence；本设计禁止，
   它们只依赖 compose task，避免它们绕过 compose receipt。
