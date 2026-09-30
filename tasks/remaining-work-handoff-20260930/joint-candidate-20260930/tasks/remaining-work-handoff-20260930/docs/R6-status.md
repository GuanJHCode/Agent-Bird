# R6 — N00–N06 状态与来源纠正 (联合返修候选, 2026-09-30；第 2 轮 F-A/F-B/F-C 返修补齐)

口径统一：**unique run = pass + fail + skip**。不写"730 vs 732 身份"。
本节逐项如实标注部分完成 / 待验证 / 待复核；不宣称整批本地完成。
第 2 轮的三处事实更正（A 必测实际缺件、paired 窗端与 warm 臂语义、N03 storm 标签口径）
均以 root 2026-09-30 复核为出处逐段标注，**不改写第 1 轮的历史表述**。

## 0. 身份分账（双方默认 ordinary/race 均 732 unique run，禁止按数量择一）

来源：`data/root-gate-identity-counts.json`（root 独立重计）。

| 侧 | unique run | pass | fail | skip | 说明 |
|---|---|---|---|---|---|
| A(primary) | **732** | 726 | 2 | 4 | 两条 FAIL = bootstrap empty-record 子测试及父测试 |
| B(second) | **732** | 730 | 0 | 2 | 原"730"是 pass 数，非身份总数 |

身份集合差集各 **3**（`differences`，非重复、非"择一去重"）：

- onlyPrimary(3)：
  `internal/adapter TestProbeCauseDecompositionObservation`、
  `internal/host TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes`、
  `internal/host TestS4StageTimingObservation`
- onlySecond(3)：
  `internal/host TestEscapedPipeHolderProgressDoesNotAdvanceAfterTerminal`、
  `internal/host TestPipeHoldingDescendantRecordsSingleStoppingProgress`、
  `internal/store TestHostProgressRejectsUnknownSegmentAndEnvelopeBranches`

三列分账：
- **常规回归（强制，进默认身份集）**：B 的三条 Host/store N04（含本返修 R3 组合判据）、A 的 `TestProgressStaysBounded...`。
  > **第 2 轮更正（引用 root 第 1 轮复核发现，不改写历史）**：本节在第 1 轮把 A 的
  > `TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes` 写成"保留为保守终态断言"，
  > 与事实不符 —— 该测试**从未进入联合运行树**：联合树
  > `scratch/second/tools/orchestrator/internal/host/stop_timing_test.go` 当时 SHA 为
  > `9377183c743b8575834f00a012f10cad0a3a9fcb256c16188fbe9ef22bbb3ce4`（397 行，无此函数），
  > 而 A 冻结原文 SHA 为 `1322b337f1e214c695a82b3631f873f2417ecc707a2523b9db7a52b879e17781`
  > （541 行，`stop_timing_test.go:411` 起）。root 以 `A\J` 差实测击穿（`A_minus_J` 含该身份），
  > 且第 1 轮全量门禁 `evidence/R7/r7-ordinary.jsonl` 重算同样缺它。
  > 该测试断言的单元格（leader 先退出 + 后代持管道下「进度有界 ≤21、interrupted⇒恰 1 条 /
  > unknown⇒0 条、settlement 后不推进、holder 终态后才清理」）在第 1 轮**无任何测试覆盖**。
  > 第 2 轮 F-A 已把 A 原文**逐字**并入联合树与 `candidate/`（合并后文件 SHA 即
  > `1322b337…`，与 A 冻结逐字节相同），自此**"第 2 轮起实际在集"**。
- **opt-in 诊断（实验文件，不进默认身份集）**：A 的 `TestProbeCauseDecompositionObservation`、`TestS4StageTimingObservation`；本批 run_n01/ n03 harness 的时序窗测量、`zz_n01_timeline_test.go`/`zz_n03_probe_cause_test.go`（experiments-only）。这些是测量面，非验收身份。
  > 第 2 轮如实登记：这两个身份**既不在默认集、也不以 SKIP 形式在默认集** —— 联合树里根本没有
  > 承载它们的源文件（`grep -rln "TestProbeCauseDecompositionObservation" scratch/second/tools/orchestrator/`
  > 零命中；`TestN01S4TimelineProbe`/`TestS4StageTimingObservation` 同）。因此它们的测量只能在
  > **独立实验树**上进行（第 2 轮：N01 paired 用 `scratch/exp-n01` = 联合树 +
  > `snapshots/second/data/experiments-only/zz_n01_timeline_test.go`；N03 cause 观测用 A 系树
  > `scratch/primary`）。默认身份集不因实验树而增长。
- **历史复用**：A/B 既有分析、批次三 E1c–E1e、r6/original-gate 记录，仅作旁证，不升级为新机制证据。

### 0.1 第 2 轮联合默认身份集（计数由日志得出）

来源：`evidence/ROUND2/GATE/r7-ordinary-round2.jsonl`（普通全量 `go test -json ./...`，
`-count=1`，默认并发，未改 `-p`），重算器 `scripts/identity_diff_round2.py`，
结果 `evidence/ROUND2/GATE/identity-diff-round2.json`。

| 集 | unique run | pass | fail | skip |
|---|---|---|---|---|
| J1（第 1 轮 `evidence/R7/r7-ordinary.jsonl`） | 733 | 731 | 0 | 2 |
| J2（第 2 轮，合并 A 必测之后） | **734** | **732** | **0** | **2** |

J2 窗口：`2026-09-30T04:58:05Z–05:00:01Z`（UTC，普通全量，`-count=1`，默认并发，未改 `-p`；
`gate-rc=0`，stderr **0 字节**）。集内 2 个 SKIP 是 B 树既有的 real-claude 门控身份
`cmd/orchestrator TestRealClaudeManagedCandidate` 与 `TestRealClaudeReviewerProfile`
（**在默认集内、以 SKIP 形态存在**，不是第 2 轮新增，也不是第 1 轮所称的"两个 opt-in 移出默认集"）。
分包 unique run：host 204 / cmd 121 / gitops 51 / adapter 48 / coordinator 35 / install 23 /
ipc 9 / process 8 / events 4 / instance 3 / gitopsworker 2 / admincli 1 = **734**。

差分格（每格逐条身份见 `identity-diff-round2.json`）：

| 格 | 期望 | 说明 |
|---|---|---|
| `union(A,B) \ J2` | 恰等于两个已登记 opt-in 身份 | 出现第三个即漏件 |
| `J2 \ A` | R3 组合判据 + B 独有必测三件 | 联合新增，非改名 |
| `J2 \ B` | A 必测 `TestProgressStaysBounded…` + R3 组合判据 | 第 2 轮 F-A 并入 |
| `J2 \ J1` | 恰为 `TestProgressStaysBounded…` 一条 | 第 2 轮唯一新增身份 |
| `J1 \ J2` | 空 | 不得削减第 1 轮已交付身份 |
| `A \ J2` / `B \ J2` | 只允许两个 opt-in | 其余一律 violation |


## 1. 逐项状态

| 项 | 状态 | 已验证（本返修） | 仍待验证 / 待复核 |
|---|---|---|---|
| N00 输入快照 | 部分完成·已验证 | 5697 输入逐 SHA 核对、`review-inputs.json` `a42a9325…` 一致（root 复验） | 原仓实时漂移六项不盲合并（D4） |
| N01 S4 | 待验证·**S4 不可关闭** | 归因已降级为 `running→child main(m1)` pre-user-code 首执行相关延迟（`docs/n01-s4-findings.md`、`N01-analysis.second.md`）；harness 分桶/拒覆盖修复 + 自测 GREEN；**第 2 轮 F-B**：`run_n01.py` 新增 `paired` 模式并真实登记窗端（`evidence/ROUND2/FB/paired-window/`） | OS 首次 exec 唯一归因**未证**；同机负载无法隔离；默认并发稳定性 OPEN。**第 1 轮 paired 窗降级为「窗端元数据缺失样本」**：归档 `evidence/R2/n01-paired/window.log` 只有 `start=`，`end=`/`load_end=` 为空，`n01-paired-summary.json.utc_window_start=""`；且该窗 warm 臂的 provider 绝对路径逐迭代变化（`warm-00/warmroot`…`warm-09/warmroot`），并非该探针声明的 warm ⇒ 双臂同为冷路径，fresh/warm 差分不成立。第 1 轮报告引用的"load 6.98/8.50/9.46"在归档中无法定位（见 §2.1） |
| N02 S2 | 部分完成·消费改接已落地（第 3 轮据 C1 更正，全量依据见 §1.1） | 观察协议 helper 落地（单一 readline+shield 软诊断+30s 总看门狗+durable/EOF/failure 交叉验收）；真 RED(7 断言失败)→GREEN(10/10)；含永不ready/提前EOF/错误旧lease/失败后迟到ready负例（R1） | 两个真实消费测试（`test_local_service_chain.py`、`test_owner_helper_production_pipeline.py`）尚未改接此 helper（**第 3 轮勘误：本句自 C1 起失效，原文逐字保留不擦除，见 §1.1 失效陈述 1**）；产品准入 10s/5s 未改（遵 D1）（此半句第 3 轮复测仍成立） |
| N03 S1/S3 | 部分完成·**第 2 轮已补最小独占窗** | findings 路径纠正 `T101139→T101600`；harness 纠正 `started`(工作前)/`finished`(工作后)；**第 2 轮 F-C**：逐窗记 UTC start/end + `sysctl vm.loadavg` 前后快照 + 自建 storm 真实状态 + 窗间间隔，路径参数化（argv/env），自测 RED(3)→GREEN(6)；实测 cause 10 + controlled-storm 10 窗 `evidence/ROUND2/FC/storm-window/` | cause 分类保留；历史失败实例同因归因仍 **UNPROVEN**；storm 窗为本批**自建**负载，标签按窗如实区分（见 §2.2） |
| N04 Host 进度 | 部分完成·**组合判据已返修 + A 必测已并入** | R3 新组合判据 `TestN04PipeSensitiveDescendantHoldingStdoutIsNecessary`：持管道 GREEN×3、后代存活不继承 stdout 真 RED×3（`result_ready`），生产 `host.go` SHA 不变 `ee6ce551…`；保留 B store guard(M1/M2)+escaped holder 终态后排空/存活断言；**第 2 轮 F-A**：A 必测 `TestProgressStaysBoundedWhileDescendantHoldsPipeAndNeverFinalizes` 逐字并入联合树与 `candidate/`，合并后 `stop_timing_test.go` SHA = A 冻结 `1322b337…`，定向 `-count=1` PASS 且与 R3 判据在 `-run` 同前缀下互不劫持 | 活跃 15s ticker 上界、`KindHostProgress` coordinator IPC 路由、`captureWriter` 1MiB 溢出、UNKNOWN 终末进度语义**仍未验证** |
| N05 能力索引 | 待复核·未改动 | — | 索引覆盖面须由 R5 联合闭包核对；本返修未改 N05 产物 |
| N06 门禁/交付 | 部分完成·差异检查已纠正 | R4 保留原始 `passed=false`/`diff-check-exit=1`；联合候选 `git diff --cached --check` rc=0 + 逐新增文件 whitespace 全 PASS + 三类负控制；R5 双 manifest + REQUIRED 钉件 + 隐藏必红 + 冷搬迁校验 | 真实全量 ordinary/race/build/vet/Python 复跑与源码包/安装/receipt/index 同快照绑定属 R7/root integration；本返修未自述整批完成（**第 3 轮勘误：该事项已由 C3/C4 执行并经 root 复测，见 §1.1 失效陈述 3**） |

### 1.1 第 3 轮勘误（追加块；原文逐字保留，未做任何擦除或改写）

> **身份注明**：本块由「N 系列返修」**第 3 轮唯一指定执行人**追加，既不是收尾集成轮（C1–C5）作者的手笔，
> 也不是第 1/2 轮实施者的表述。触发依据是 root 收尾轮复核裁定 L3：
> 「R6-status N02 行『尚未改接』陈旧 → 转第 3 轮微任务处理（勘误块 + manifests/receipt 机械重绑），不静默改写」
> （逐字见 `/private/tmp/bird-nseries-root-review-20260930-pzpzp52_/docs/root-review-closeout-20260930.md:38`）。
> 本第 3 轮**未重跑任何时序或测试**，只引用已归档的 C1/C3/C4 证据与 root 复测；本轮唯一的生产侧改动是
> 本文件的这次勘误，其余全为 manifest/receipt 的机械重绑。
>
> **失效陈述 1（本文件 `R6-status.md:87` N02 行「仍待验证」列首句）**：
> 「两个真实消费测试（`test_local_service_chain.py`、`test_owner_helper_production_pipeline.py`）尚未改接此 helper」
> —— **自收尾集成轮 C1 起失效**。两文件的 owner-ready 观察共 **3 个调用点**已改接到
> `tools/native-product-bridge/n02_owner_readiness_observer.py` 的同一读取协议。第 3 轮 `grep -n` 实测：
> `observe_owner_ready_line` 在 `candidate/tasks/g0-completion/tests/test_owner_helper_production_pipeline.py`
> 为 `:149`（定义）+ `:201`、`:363` 两处调用；在 `candidate/tools/native-product-bridge/test_local_service_chain.py`
> 为 `:31-32` import 复用 + `:151` 一处调用（**同一套函数、无平行实现**）。勘误依据（逐条给路径）：
> - 真 RED：`evidence/CLOSEOUT/C1/red/c1-legacy-red.log` → **`3 failed, 2 passed in 7.28s`**；三条失败均为真实断言失败
>   （late-success 误拒 `got TimeoutError()` / failure-before-ready 误接受 / stale-lease-identity 误接受），
>   `control` 与 `premature-eof` 两格旧形态本就正确 ⇒ 记 2 passed，证明该探针**不是空集真空**。
> - GREEN：`evidence/CLOSEOUT/C1/green/c1-observer-green.log` → **`5 passed in 8.93s`**（observer 臂复用同一改接函数）。
> - 消费测试：本批归档合计件 `evidence/CLOSEOUT/C1/green/c1-consumers-rewired.log` → **`3 passed in 2.40s`**；
>   **root 2026-09-30 在联合树逐文件复测为 1 passed + 2 passed**
>   （`root-review-closeout-20260930.md` §1 第 1 条），与本批 C3 全量门禁的分项相加一致：
>   `evidence/CLOSEOUT/C3/python-pipeline-consumer.log` → **`2 passed in 0.95s`**（pipeline 消费者），
>   `evidence/CLOSEOUT/C3/python-bridge.log` → **`32 passed, 1 skipped`**（native-product-bridge 全套，含
>   `test_local_service_chain.py`；唯一 SKIP 是 B 树既有 `test_local_product_driver.py:89`）。
> - helper 未动一行：`shasum -a 256` 第 3 轮复测 `n02_owner_readiness_observer.py` =
>   `96260b2e6b4efc2695345bb5f0ff432c8e4e119cef990d3b3f54a2b1dda8de12`（与 R1 登记同值）；两个消费文件复测 =
>   `12cce6f7af274f4d0c8d4430d1c7904b4b962e4bc24db0dce988c27dd93cc69c`（local chain）/
>   `2d7665cdacc50239d774ad488078b54d36a0e7496247aaacc8bdc7aa2c1e0237`（pipeline），与 C4 成员对账逐字节相同。
> - 预算未扩（N02 行右列后半句「产品准入 10s/5s 未改（遵 D1）」**仍成立，不列入失效**）：第 3 轮 `grep -n "asyncio.wait_for"`
>   实测 local chain 只剩 `:102` read_json 的 10、`:244`/`:269` 两处 `bridge.wait()` 的 5、`:281` 外层
>   `asyncio.wait_for(scenario(), 30)`；pipeline 只剩公共 `_line`（`:104`）的 5 与 `_wait_process`（`:108`）的 5。
> ⇒ 状态列据 root 指示改为**「部分完成·消费改接已落地」**。「部分完成」保留：真 Provider/native、Linux 隔离、
> 断电恢复与 **S4 默认并发稳定性仍 OPEN**（见返修报告 §三、receipt `unverified` 字段），本行不宣称 N02 关闭。
>
> **失效陈述 2（本文件 §3「不改动声明」首句；追加本块前为 `R6-status.md:135`，追加后为 `:191`）**：
> 「未删除既有断言（仅新增 pipe-sensitive 判据与观察协议 helper）」—— 括注中的**「仅新增」只对第 1/2 轮成立**。
> C1 把两消费测试的 `assert await _line(owner)=="owner-ready"` 硬观察**替换**为 `observe_owner_ready_line(...)`
> 调用，并**新增** durable 身份 / 先行 failure / 提前 EOF 三类交叉验收断言（`grep -n "_durable_owner_ready_bound"`
> 第 3 轮实测：pipeline `:111` 定义、`:204`/`:366` 断言；local chain `:31` import、`:155` 断言）。
> 如实登记为「既有观察断言的**形状被改接**，公共 `_line` 的 5s 定义与其余额外调用点未动」，原句保留不擦除。
>
> **失效陈述 3（本文件 `R6-status.md:91`，N06 行「仍待验证」列首句）**：
> 「真实全量 ordinary/race/build/vet/Python 复跑与源码包/安装/receipt/index 同快照绑定属 R7/root integration」
> —— 该事项已由收尾集成轮执行并经 root 复测，不再是待做项。**如实注明归因**：此条由 **C3/C4** 关闭，
> 不单独归因于 C1。依据：`evidence/CLOSEOUT/C3/` 全量 ordinary 与 `-race ./...` 均为
> **734 unique = 732 pass / 0 fail / 2 skip**（root 独立重算，`root-review-closeout-20260930.md` §1 第 3 条）、
> Python 默认面 **57 passed / 1 skipped / 0 failed**；`evidence/CLOSEOUT/C4/` 源码包 1834 成员
> （+3/改 3/删 0，逐路径有解释）+ 隔离安装 doctor healthy + receipt/index 同快照重绑
> （原绑定态 index `e09a9c97cf162407148753ea70d4d6318f5f51e64e7be1935e70eea86d430ab4` →
> receipt `bddcf6c5e5968befacc850cf3ebec52f03c3348c3fb01661d586b552aed3740b`，root 实测 50/50 命中、0 失配）。
> 本第 3 轮因本文件 SHA 变化而做的**新**重绑产物在
> `candidate/evidence/ROUND3/joint-manifests-final/` 与 `evidence/ROUND3/receipt/`（工作区侧），
> 旧件全部原样保留、不覆盖，最终绑定态以第 3 轮新件为准。

## 2. 空串摘要纠正（`N01-analysis.second.md:11`）

- 错误：把 original-gate 的 `OutputHash=afb9510d…` 标为"空串摘要"。
- 事实：`host.go:1053` 的 `hashText` 为 SHA-256；**空串** SHA-256 恒为
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。
  original-gate 是 **exit0 + 非空输出**（`afb9510d…` 本身即非空输出摘要），**不得**改写为
  "截止前完全无输出"的同型证据；r6（-15 + 空串）才是无输出那一族。

## 2.1 paired 窗负载引用的勘误（第 2 轮，root 复核发现）

第 1 轮 `workspace/docs/repair-report.md` 与本文 §1 旧行都引用过
"paired 窗 `window.log` UTC start/end + load 6.98/8.50/9.46"。按归档实测：

- `evidence/R2/n01-paired/window.log` 全文两行：
  `N01 paired fresh/warm window: start=2026-09-30T03:56:01Z end=` 与
  `load_start={ 7.55 8.68 9.54 } load_end=` ⇒ **只有窗初负载被记录，窗尾为空**。
- `6.98` / `8.50` / `9.46` 三个值在 paired 证据目录内**零命中**
  （`grep -rn "6\.98\|8\.50\|9\.46" candidate/evidence/R2 evidence/R2` 无负载命中）。
  能定位到的最接近数值是**别的窗/别的量**：`9.46728515625` 是
  `snapshots/second/data/n03/storm-identities/summary.json` 的 `load_after[0]`（B 的 N03 storm 窗），
  `6.9814868` 是 `snapshots/second/data/n01/storm-warm-timeline/timelines/iter-003.json` 的一个
  迭代计时值，`8.50` 无任何对应负载记录。
⇒ 该引用不成立，属跨窗混抄；第 1 轮 paired 窗连同其引用一并降级为**窗端元数据缺失样本**，
数据保留不删。第 2 轮修复后的窗见 `evidence/ROUND2/FB/paired-window/`（窗端与前后负载齐全）。

## 2.2 N03 窗的 storm 标签口径（第 2 轮）

任务书写"标签一律 no-self-storm"，但 N03 最小窗的定义就是**本批自建的 controlled churn storm**。
把 storm 之内的窗也标成 no-self-storm 属于伪造，故按窗如实区分，不整体贴一个标签：

- 报告级：`outer_window_label = "no-self-storm"`，只表示**外层独占窗序列内没有额外自建 storm 并发**，
  `label_note` 逐字写明"绝不表示机器安静"。
- 窗级：每个子窗记 `self_storm_active` 真实布尔值 —— `warm`/`quiet_controls`/
  `cause_observation_quiet`/`cause_chain_units` 为 `false`，`storm_windows`/
  `cause_observation_storm` 为 `true`；另有 `storm_active`（churn p50 ≥ 1000ms 判据）与逐窗
  `sysctl vm.loadavg` 前后快照佐证。
- F-B 的 paired 窗本身**不建** storm ⇒ `self_storm = false`，标 no-self-storm 名副其实。

此口径差异需 root 裁定（见返修报告"需要裁定的偏离"）。

## 3. 不改动声明
产品预算（proxy/activation 10s、owner 5s、grace、S4 5000ms）与权限均未扩大；
未删除既有断言（仅新增 pipe-sensitive 判据与观察协议 helper）；未用默认 SKIP 掩盖未验证项。

第 2 轮补充：
- A 必测是**逐字节并入**，合并后 `stop_timing_test.go` SHA 与 A 冻结一致（`1322b337…`），
  未改任何 fixture、未放宽任何断言（`cmp` 实测 identical）。
- 生产码零改动：`internal/host/host.go` SHA 仍为 `ee6ce551560bd04dd108cf5d23646e38a9cf9f66af1e1dbe0e58638ea732bf22`。
- 本轮只改**测量与元数据面**（`run_n01.py` 窗端/paired 模式、`n03_s1_s3_storm.py` 窗台账与路径参数化），
  未新增产品预算，未把任何必测改为 SKIP，未删除任何已交付身份（`J1 \ J2` 为空由重算器机械核对）。
