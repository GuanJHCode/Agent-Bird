# N01:S4 `TestGrokHostLaunchUsesAuthorizedSession` 阶段分解(2026-09-30)

任务书:N01/P0。预算(2s probe、5s 场景、10s grace、TERM 语义)全程未动;
产品码零修改;所有新增身份为隔离工作区实验文件(`internal/host/zz_n01_timeline_test.go`),
**禁止进入集成/候选**。原始证据在本目录各 `*.jsonl` 与 `timelines/iter-*.json`。

## 1. 记录在案的失败与复现

| 记录 | 签名 | 位置 |
|---|---|---|
| root original-gate(在盘) | interrupted, **ExitCode:0**, EventCount:4, OutputHash **非空**（afb9510d…，`hashText`=SHA-256 之非空输出摘要）5.05s | `root-closeout-20260930/evidence/original-gate/go-test.jsonl` |
> 返修说明(R6)：原此行把 `afb9510d…` 标成"空串摘要"是错误的。空串的 SHA-256 恒为
> `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；original-gate 实际是
> **exit0 + 非空输出**，不得改写为"截止前完全无输出"的同型证据。r6(下行)才是 -15 + 空串那一族。
| r6 merged(在盘) | interrupted, ExitCode:-15, EventCount:4, 空串摘要, 5.06s | `bird-batch2-r7-delivery…/r6-merge-feasibility…/merged-all-tests.jsonl` |
| 本工作区 fullgate#1(新) | interrupted, ExitCode:0, EventCount:4, OutputHash **fba4871a…(非空)**, **5.04s** — 与 original-gate 同族再现 | `evidence/n01/fullgate-default/` |
| 本工作区 fullgate#2(新) | **pass 3.04s** — 间歇性确认(同树两轮一败一过) | `evidence/n01/fullgate-timeline/` |
| 安静对照 ×5(新) | 5/5 pass,0.52–0.65s | `evidence/n01/quiet-real/` |

注:原 r6 归档把 EventCount:4 解释为"prepared/spawned/running+终态";源码事实是
`finalizeStopped` 中 stopped 事件走 `appendStructuredEvent` **不计入** eventCount
(host.go:483 直挂 vs `appendPhase` 包装),4 = prepared/spawned/running/exited,终态对
stopped+exited 各恰一。此更正不影响归因。

## 2. 阶段分解实验(插桩身份 `TestN01S4TimelineProbe`,同夹具四标记 m1..m4)

安静 fresh ×5:见 `quiet-timeline/timelines/`。典型:`running +0.05s → m1 +0.57s`,
即**新写入 provider 文件的首 exec 评估段约 0.52s**(与批次三 E1b 常态一致),
m1→m4≈10ms,parent 观察段(result/exited 事件)≈40ms。

fullgate#2 真实门禁并发下的插桩迭代(`timeline-out/iter-001.json`):m1=+0.564s,
通过;同轮 S4 身份本身 pass 3.04s ——门禁风暴把该评估段推到秒级。

fresh-exec 风暴(预算事前声明:8 线程、cap 3200、150s;churn 实测
p50=4047ms p99=4632ms max=4681ms;`storm-timeline/summary.json`):
**5/5 迭代全部把 ~4.4–4.55s 耗在 running→m1 同一段**;m1→result 恒 ≈20–40ms;
churn max 4.68s 时测试整体 4.44–4.60s 通过(刀刃未翻,与 E3b 一致)。
**spawn/prepare/解析/drain/settle 等实现段在风暴下仍是毫秒级。**

warm 负控制(同风暴形状,churn p50=4633 p99=10003、84 次≥4.9s;
`storm-warm-timeline/`):provider 路径跨迭代恒定 → iter2–5 **全部 0.09s 通过**,
m1 与 running 同刻;iter1(该路径首 exec)恰被评估排队推过 5.0s →
**interrupted,5.06s,单一合法终态(stopped+exited 各一,无 result)** —— 这是风暴条件下
自然命中 S4 记录失败族的一次真实再现。

## 3. 确定性终态族测试(`TestN01S4DeadlineSubsignatures`,×2 全绿,`deadline-subsignatures/`)

- pre-exec-term(sleep 8 无 trap):TERM 默认语义 → interrupted/exit -15/无输出、
  无 result 事件、无 result artifact → r6 记录族。
- term-trap-exit0(sleep 8 + `trap 'exit 0' TERM`):interrupted/exit 0/无输出 →
  与门禁记录族(exit 0)同边界:完成与 TERM 送达的竞态只产出一个终态。
- 门禁记录族的"输出已捕获"(fba4871a…)需要观察越界竞态,无法确定性构造,
  已两次自然命中并留档,如实登记为竞态边界证据。

## 4. 四类候选解释的判决(本测量面内)

| 假设 | 判定 | 依据 |
|---|---|---|
| 到期正确中断 | **支持** | 风暴/warm iter1 越界时,deadline→TERM→单一 interrupted 终态,无假成功 |
| 实现竞态 | **不支持**(本测量面内) | prepare/spawn/parse/drain 段毫秒级;终态对唯一性有确定性测试 + 既有 4 组 race 契约测试 |
| 测试观察问题 | **不支持为根因** | 测试无独立观察预算,观察=产品 5s;插桩显示观察段 <100ms |
| 首执行相关延迟（`running→child main(m1)` 的 pre-user-code 区间） | **支持,但机制未唯一归因** | running→m1 段独吞 4.4–4.6s(风暴)/0.5s(no-self-storm);warm 同风暴 0.04s,差分明确；该区间同时含 sandbox-exec 后续、解释器解析/启动、OS 调度、安全评估、shell 初始化与标记落盘，与 OS 首次 exec 评估**一致但未唯一归因** |

> 返修说明(R2/BLOCKER-1)：原把该行写成"外部调度迟滞(OS 首 exec 评估)——支持,主通道"越过了证据边界。
> 统一降级为"首执行相关的 pre-user-code 延迟"，不宣称排除所有实现竞态，也不升级为已证 OS 机制。

边界(不得夸大):机制内部解释(syspolicyd 评估队列按 path/vnode 键控、dyld/签名子阶段
相对贡献)与批次三 E1c–E1e 相同,仍是**推断/候选解释**;历史 original-gate/r6 失败实例
是否同因触发**未证明**(当时无阶段记录);warm 差分把归因收窄到"新文件首 exec 评估",
不排除与风暴共变的其他 OS 资源。

## 5. 状态与建议(交主会话裁定,非本执行者权限)

- S4 默认并发稳定性:维持 **OPEN**(间歇复现、一轮绿不关闭;本轮新增再现+分解证据)。
- 不改 5s/2s/grace(任务书禁止静默加时,未加)。
- 裁定候选:(c) 把"probe/S4 类时间敏感测试不与其它全新二进制构建/首 exec 并发"
  写入门禁调度纪律(串行窗口已证明可绿);(a) 无产品码需求 —— cause 链 F6 已修。
- 下一可区分实验(未做,超本批预算):专用串行窗口内 `fresh churn + -count=10 -race`
  对 S4 身份(批次三 §2 已列);本轮 warm-iter1 自然命中已提供失败族样本。
