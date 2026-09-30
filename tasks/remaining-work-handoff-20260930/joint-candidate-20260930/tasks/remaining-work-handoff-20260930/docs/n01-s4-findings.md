# N01 结论：S4 `TestGrokHostLaunchUsesAuthorizedSession` 默认并发失败分类

状态：待独立复核（实施者不自审）。最高自述状态 READY_FOR_ROOT_REVIEW 的一部分；未自宣关闭稳定性问题。

## 1. 结论（机制定性）

S4 = **到期正确中断**；延迟集中于 `running→child main(m1)` 的 **pre-user-code 首执行相关区间**。
fresh/warm 差分支持"新路径首执行相关"，与 OS 首次 exec 评估**一致但未唯一归因**（该区间同时包含
sandbox-exec 后续工作、解释器解析/启动、OS 调度、安全评估、shell 初始化与标记落盘）。
**不断言**为"外部 OS 首次 exec 评估"这一唯一机制；本测量面亦**不能排除全部实现竞态**
（`Handle.Wait` 截止/完成同就绪选择性仍有残余证据不足）。

- 断言行：`internal/host/grok_session_test.go:165`（现行号与归档一致）。
- 预算来源：`ExecutionProfile.TimeoutMS=5000` 与 `GrantedActiveMS=5000` 同为 5.00s（`host.go` ExecuteLaunch 取二者最早者），测试无独立观察预算，观察=产品截止。
- 子进程：`sandbox-exec` 包裹下对一个**新写入** `/bin/sh` fixture provider 的首次 exec。

## 2. 决定性证据（真实 OS，隔离工作区，有界窗口）

原始记录：`data/n01-s4-20260930T095457/report.json` 与各窗 `stage-observations.jsonl`、
`harness.jsonl`、`real-s4.jsonl`；harness 为 opt-in 诊断测试
`internal/host/s4_stage_timing_test.go`（默认门禁中 Skip，纳入源码清单与 ./internal/host）。

同一产品原语（`prepareGrokCommand`+`process.Start`/`Wait`）的分阶段墙钟（单位 ms，exit_observed）：

| 窗口 | 条件 | A 冷启动 grok 链 | A2 同文件 warm | B sandbox 稳定路径 | C 冷 fresh 直执 | C warm | 真实 S4 |
|---|---|---|---|---|---|---|---|
| W0 | 有背景负载(load8-9) | 4479–4620 | 15–19 | 9–16 | 4484–4682 | 5–7 | pass 4.51–4.89 |
| W1 | fresh-exec 风暴 | 8651–8720 | 14–16 | 11–12 | 8676–10658 | 4–7 | **fail 5.07** ×1 / pass ×9 |
| W2 | 风暴+race | 4468–5243 | 17–25 | 10–21 | 4481–4508 | 5–9 | **fail 5.07/5.08** ×2 / pass ×8 |
| NC | 真静默复跑 | **468–511** | 12–24 | 8–9 | **525–770** | 7–16 | （仅 harness）|

测量器负控制（strict，仅静默窗断言）全部成立：warm<冷、稳定路径恒定 ~10ms、
D 阶段 first-stdout≈spawn 而 exit≈3016ms（证明 first-byte 与 exit 两口钟观测不同事件，时钟未坏）。

NC no-self-storm 冷启动 ~470ms 与历史 `h3-experiments-results` E1b「首 exec≈500ms 常态」逐字吻合；
W0 的 4.5s 发生在同机其它会话 fresh-exec 活跃期（标签 no-self-storm，非"静默对照"），W1 风暴把冷启动推到 8.6–10.6s，
真实 S4 因此在 5.00s 预算内**未完成首次 exec** → 正确到期中断。
> 返修说明(R2/D2)：本批窗口与另一实施会话重叠，4.5s/p50/p99 等分位数只能作"混合负载观察"，
> 不作机器常态或独立效应量；"no-self-storm"仅表示无本批自建 storm，不等于无外部负载。

限定：load average 本身不能区分 W0(4.5s)与 NC(0.47s)（NC 1 分钟 load 仍 5.9）——
驱动量是**并发"新文件首 exec"评估串**（fresh-exec churn 型），与 H3 E1C1「CPU/churn 型负载
推不动 2s 预算」的分型一致；泛化"机器忙"表述不成立。

## 3. 风暴复现签名（与历史一致）

W1/W2 复现的失败现场逐字为：
`result={Status:interrupted ExitCode:-15 EventCount:4 OutputHash:e3b0c442…(空串摘要) ArtifactPath:} err=context deadline exceeded`
—— 子进程在截止时**仍在 exec 评估、无任何输出**，被 SIGTERM(-15)，属正确到期中断。

另存在**次签名**（batch3 默认门禁原记录）：`ExitCode:0` + 非空 OutputHash(`afb9…`)。
该次签名是子进程恰在 5.0s 附近自然退出/刚越过截止，与 -15 同源（冷首 exec 被推高到≈5s），
差别仅在 kill 与 completed 的相对时刻；两者都不需要启动逻辑竞态即可解释。

## 4. 与四类归因的区分

- 到期正确中断：**成立**（-15/空输出=截止时子进程未产出；0/有输出=截止临界）。
- 实现竞态：**本测量面不支持为 S4 根因，但未排除全部竞态**；启动链无缺陷证据，warm/no-self-storm 冷恒 <0.8s，
  FIFO/退休唤醒/停止栅栏等已在批次三修复且复核 PASS。`Handle.Wait` 截止/完成同就绪选择性仍有残余、证据不足，
  不得据此宣称排除所有实现竞态。
- 测试观察问题：`process.Handle.Wait` 的 `select{done; ctx}` 在 done 与 ctx 同时就绪时**非确定择一**，
  理论上可把"恰在截止前完成"的子进程误判为 deadline（次签名的一个候选解释，窗口小于调度量子，本数据非主因）。
- 首执行相关延迟：**成立**，是 `running→m1` pre-user-code 区间的主要成分；fresh/warm 差分支持新路径首执行相关，
  与 OS 首次 exec 评估**一致但未唯一归因**（无系统级 policy/exec trace，子机制仍为推断）。

## 5. 处置与不改动声明（红线遵守）

- **未**把 5s 改大；**未**改任何产品/测试预算；**未**放宽隔离或关闭系统安全服务；**未**加压越界。
- 启动逻辑**本来正确**：按纪律以充分验证关闭"实现缺陷"这一子问题，不硬改产品。
- 一轮静默 NC 绿**不**关闭 S4 的默认并发稳定性：默认并发在同机制下仍可能到期中断，故 **S4 稳定性保持 OPEN**。
- 本实验的有界性：≤3 风暴窗、单窗 churn 150s、单命令 ≤420s、迭代计数见 report，全程未并发跑全量构建/门禁。

## 6. 交主会话裁定项（实施者一律未实施）

1. 门禁调度纪律（H3 §4(c) 延伸）：probe/launch 类全新 Mach-O 首 exec 不与其它并发门禁首跑叠加；
   隔离外层串行调度是合理执行纪律，但不得写成历史失败的已证根因。
2. 测试保真选项（属测试面，不改产品预算，仍需独立审查+主会话裁定）：S4 provider fixture 是每轮全新文件，
   与生产中"已评估的稳定路径 pinned 二进制"不一致；可考虑在预算外预热该 fixture 一次，
   使 5s 度量启动流而非 OS 首 exec。此为测试观察保真问题，非产品缺陷，且触及 oracle → 需裁定，本轮不动。
3. `Handle.Wait` select 择一性：是否需要"done 与 deadline 同就绪时优先真实完成/保持唯一合法终态"的
   确定化，属观察协议；本轮不改，留裁定。

## 7. 复验命令（隔离缓存，专属输出目录）

```sh
python3 -B tasks/remaining-work-handoff-20260930/scripts/n01_s4_experiment.py \
  <全新绝对输出目录>
# 静默定点复跑（含 opt-in harness）：
ORCHESTRATOR_S4_STAGE_TIMING=1 ORCHESTRATOR_S4_STAGE_CONTROL_STRICT=1 \
  go test -count=1 ./internal/host \
  -run '^TestS4StageTimingObservation$|^TestGrokHostLaunchUsesAuthorizedSession$'
```
