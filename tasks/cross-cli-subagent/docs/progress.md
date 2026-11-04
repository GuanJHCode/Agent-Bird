# 跨 CLI subagent 实施与复核

## 最新状态：阻塞已解除

2026-11-04 后续修复仅涉及 `test_native_auth_tripwire.py`：精确 libinfo 目录查询依赖、合成 CFFIXED_USER_HOME、0.166 四字段 refresh 协议。冻结修复 `7cb11e4c` 独审 PASS；主会话原工作区全套相关 Python 回归 240 passed、0 failed/errors/skipped，原 native baseline + guarded 均实际执行。生产代码不变，既有 Go 与双向真实模型证据有效。本轮约定范围验收 PASS。见 [auth-unblock.md](auth-unblock.md)。以下 BLOCKED 状态、HEAD/index 与原失败计数均作为历史保留。

后续操作：用户已授权推送远仓，目标为独立分支 `feat/cross-cli-subagent-20261104`。原 `main` 与远端 `main/master` 无共同祖先，不覆盖远端分支；以下验收状态及 HEAD/index 记录是推送前的历史证据，整体验收仍为 BLOCKED。

## 初次验收状态（历史）

**核心功能 PASS，整体验收 BLOCKED。** 两个方向均完成真实编码、主会话直接返工、第二轮代码/测试复核、独立 accept 和 ACK。唯一整体验收阻断是 native refresh-tripwire 的 `baseline_count=0`，guarded 分支尚未执行，不能宣告整体完成。

- 最终源码 `68e875868215ecb1fd8daf540799fd3d0eee25e3` 已保护性集成回原工作区；相对输入快照 125 文件、相对原 HEAD 175 文件，逐字节及执行位核对 0 mismatch。原 HEAD 未变、0 staged；未推送、发布或替换全局插件。
- 最终私有包 `.7`：wrapper smoke 7 通过；主会话使用最终安装补跑 delivery + G1，26 通过、0 失败/错误/跳过。
- 最终 Go race：540 顶层通过、8 跳过、0 失败；含子例 1175 通过。vet 与原工作区构建退出 0。native Python：212 项、211 通过、1 失败、0 错误/跳过。
- 正向 `.4`：真实候选加主会话人工可信收尾；不能称旧 driver 自动通过。反向 `.7`：实际加载 Skill，完整两 revision 自动流程通过；候选血缘、源摘要、事件绑定、终态由根会话和独审再次核对。
- 独立最终审核 `review_native_coding`：`BLOCKED（核心功能 PASS；整体验收 BLOCKED）`。所有代码实施 subagent 已完成，原失败状态与 refs 保留。
- 认证 fixture 已修正版本元数据及合法 RPC error 终结处理；严格合成 HOME 下仍观察到 preferences / network-policy 前置错误。连续诊断无新进展后停止原路径，没有扩大 sandbox、认证、真实 HOME 或全局 preferences 权限。
- 下一步是在保持同等隔离约束的 macOS 环境恢复有效 refresh baseline，重跑同一 baseline + guarded oracle 和受影响原生套件，再补独审。实际模型运行中源认证未变，不能替代未执行的 guarded 证明。

当前交付及逐项复核见 [acceptance.md](acceptance.md)，独审见 [independent-review.md](independent-review.md)，导出证据见 [manifest.json](../data/final/manifest.json)。下方仅保留过程与历史失败，不代表当前待办。

## 目标和范围

根据审查提交 `4b2b4916232c9d7526f51cf0a73204748c62faf8` 的结论，保留当前 Go 调度器和持久化状态机，补齐产品入口、通用主会话绑定、受管编码、主会话直接返工和可靠结果回收。主会话编排、集成与最终复核；subagent 实施及独立审查。未通过验收不得声明完成。不推送、不发布、不更改已有账户或全局安装。

当前实施基底是工作区 HEAD `5e7443d24cff6deef27ed1c52697a2fb32733ae4` 加用户已有变更；原始状态摘要见 `../data/initial-workspace.json`。审查提交不是该 HEAD 的祖先，不能直接用历史缺口代替当前事实。

## 决策与协作

- 两个只读调查分别负责产品复用契约、工具链与真实 CLI 验收环境，不递归派生。
- 原始工作区由主会话维护任务记录；代码实施在隔离 worktree `/private/tmp/agent-bird-cross-cli-5c9jyn8o/source`，快照 `eb0d49ce2a06d1268edb0a14f99ce66f3402fe47` 包含所有原有 tracked 变更与 35 个未跟踪 Go 文件。实施 subagent 是该 worktree 唯一写入负责人；主会话不并发修改其代码。
- 产品基线 `2e78f24c5e9694f287d19065f2ce9c80b16a59af` 的 Git 对象存在。仅抽取所需功能，禁止整支覆盖导致当前兼容与可靠性变更丢失。
- 同一任务以新 revision 和上一轮候选继续返工；技术重试与审查返工分别表达；ACK 与业务 accept 保持分离。

## 里程碑

| 阶段 | 负责人 | 状态 | 证据与下一步 |
| --- | --- | --- | --- |
| M0 基线、契约和环境核查 | baseline_contract / validation_environment | 已完成 | Go 1.26.10、私有 Python venv；用户已提供既有网关，凭据仅留受控进程内存，不写项目或日志 |
| M1 产品入口和主会话绑定整合 | implement_product_entry / review_product_entry | 实施及分项独审通过 | 提交不确定性恢复、checkpoint清理顺序、实际Codex app-server祖先识别均修复；主会话已实际绑定成功 |
| M2 受管编码与主审直接返工 | implement_product_entry / implement_native_coding / review_owner_rework | 实施及分项独审通过 | 候选Git证据、5s有界RPC、v2能力门禁、事务幂等与互斥已返工并通过；旧v1保留 |
| M3 独立审查、回归和真实闭环 | review_product_entry / review_native_coding / 主会话 | 核心通过；认证安全项阻断 | 双向真实两revision均验收；最终.7入口/G1通过，native认证baseline失败保留 |
| M4 按初始审查逐项复核 | 主会话 / review_native_coding | 已复核；整体BLOCKED | 四缺口与六核心场景PASS；guarded认证oracle未执行，不报告整体完成 |

## 最终验收清单

1. 打包后的二进制、wrapper、Skill、命令和文档一致，按实际安装入口验证。
2. Codex 主会话可委派 Claude 在独立工作区真实修改代码，并回收候选 OID、基线、diff 与测试证据。
3. 主会话检查实际候选后，直接提交绑定 task/revision/candidate 的反馈和验收标准，无强制额外模型 reviewer。
4. worker 基于上一轮候选完成同一 task 的下一 revision；保留旧结果并拒绝旧事件误操作。
5. 主会话复核实际代码与测试后才 accept；ACK 不得替代验收。
6. 重复回执、请求幂等、旧 revision、执行失败、恢复和取消不导致重复执行或错误验收。
7. Claude 主会话委派 Codex 的通用绑定、受管写入及回收分别验证；异步主动唤醒按宿主能力核验，collect 不冒充任意 TUI 唤醒。
8. 报告每项实际命令、测试计数/失败/跳过、候选与版本绑定、独立审查结果；未验证项及外部阻塞明确保留。

## 验证记录

- 工具链：`/private/tmp/go1.26.10-sdk/bin/go`，`go1.26.10 darwin/arm64`；测试进程 PATH 显式包含该目录，`GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp`。
- Python 验证环境位于 `/private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv`，仅任务私有依赖，不修改全局安装。
- 原始树 Python 基线已执行 `pytest tools/native-product-bridge tools/real-user-trial`；原始输出与 JUnit 报告保存在隔离任务 `data/baseline/`。
- 原始树 Python 新报告：146 tests，144 passed、2 skipped、0 failures/errors。
- M1 `go-all-final.json` 已由主会话直接解析：15 package 终结（14 pass，contract 无测试），453 顶层 pass、6 skip；包含子例 895 pass、6 skip，0 fail。原 agent session 不可跨 agent 接管，未虚构退出码；最终集成后由主会话独立启动验收进程。
- M1 独立审查 `review_product_entry`：BLOCKED，唯一 P2 为低层 remote reject 先删 control/bootstrap、随后才写高层 rejected checkpoint，崩溃可能留下永久 submitting。主会话核对 `main.go/task_entry.go/task_dispatch.go/task_reconcile.go` 确认，已交回实施者修复并补 crash-injection 回归。
- Claude first-party 未登录；用户后续已提供既有网关端点、凭据和明确模型。仅将凭据保存在主会话内存与私有无回显runner内存；尚未执行真实模型请求，未修改账户或全局配置。
- 当前 Codex `login status` 实测成功；二进制版本 `0.166.0`、SHA256 `39acf979729dd290f9a24ee38b2914e036c91b65952872e28ae2a6b88a7db7c1`。native worker 正在任务私有目录保留精确 binary/companion，满足原 helper 的 owner-only bundle 合同，不修改全局安装。
- Native 首批稳定补丁 36 文件，主会话核对 manifest 0 mismatch；直接解析三包 race 报告为 195 顶层/387 含子例通过、4 skip、0 fail；边界 race 101 通过；Python 167 tests、166 passed、1 skipped、0 failures/errors。Claude 两个固定端点的真实 CLI + 本地 stub Read/Write/rename/freeze 各 2 项通过，不代表真实模型验收。
- Native 独立安全审查 `review_native_coding`：BLOCKED，唯一 P1 为 Go helper 把正常 `CODEX_*` 上下文判为认证覆盖；主会话先核对生产分支和真实环境字段名，审核者独立确认。返工仅识别并丢弃已知非认证 context，未知 CODEX/OPENAI auth 与 refresh override 继续拒绝。
- M2 实际 CLI fixture 已走通 ACK→主审 request-changes→同 command 重放/冲突/旧 revision 拒绝→fresh revision 2→accept；尚待稳定审查与最终集成。fresh 调用观察到的 session 记录保留，禁止的是 payload 显式 resume/session 和未决 continuation，而非删除观察历史。
- M2 发现独立 result_ready 任务会提前退休 SourceHost，正补结构性候选保留与事务 CAS：provider/storage 等瞬时障碍不得使恢复能力永久丢失；accept/cancel/不可返工预算耗尽仍释放。
- 上一轮仅对审查提交完成关键源码核对和离线记录一致性检查；该证据不能代替本轮实现验收。

## 最终集成状态（2026-11-04）

- 干净候选：`88f985bf4a047707f6f6d533153bf35ffb01711f`，尚未将生产代码集成回原始脏工作区。
- M1/M2/native/driver/probe 各次独立审查已通过；最终集成审查另发现当前schema9与两份文档v5/v6叙述失配，已交独立`docs-sync-source`单写入者修正，仍需复核。
- 主会话全量Go：`data/final/go-full-88f985bf.json`，1107含子例通过、8显式环境项跳过、0失败；15包终结，contract无测试。`go vet ./...`退出0；竞态正在主会话独立运行。
- 私有完整原生安装 `0.1.23-cross-cli.20261104.4`；wrapper smoke新报告7通过、0失败/跳过，二进制SHA256 `956eac8ffcfee5788b4dfdacf3475108713df091ca7da2cd3d9d0fb345764ca0`。安装及manifest证据在隔离根`data/final/package-v4/`。
- Python显式原生opt-in报告200项：198通过、1失败、1跳过。失败是synthetic HOME的0.166 app-server在OAuth前返回`Failed to synchronize managed preferences`；baseline未触达loopback OAuth，guarded分支未执行。不能将其算成认证guard通过或失效，原失败保留。独立诊断已确认loopback通道正常、真实auth未读、所有诊断进程已回收。跳过是未提供集成G1 binary，下一轮补环境执行。
- 真实验收driver已拆成只收集的review、主会话明确OID/SHA后沙箱validate、直接request-changes、独立accept/ACK。revision2覆盖bool/string/list/None和int/float子类，候选源码确实变化，accept前后都验证原HEAD未变。不得以fixture或本地stub替代真实闭环。
- 下一步：完成文档增量复核、竞态及G1/native补验；停止全部原生子agent后，串行执行Codex→Claude两revision主审返工验收及Claude→Codex反向真实流程；按最初清单逐项复核，再保护性集成原工作区。

## 真实闭环与后续修复

- 所有原生subagent停止后，由当前真实Codex会话使用私有安装`.4`委派Claude 2.1.316、用户指定网关及明确模型；未改变账户或全局配置。试验根`/private/tmp/bird-c2c-5c9j`，原句柄见该目录`dispatch-result.json`，两次attempt、总active上限120000ms。
- revision1候选`a4b158afe296c286a38f687caf1f6319539e712e`，主会话实际读取源码/diff，4个加法用例沙箱通过。ACK1后提交新类型边界要求，完全相同的request-changes重放返回相同receipt，未重复执行。
- revision2候选`c2069762bd53c57406898ad1b6add644c2f8a3f5`，base为revision1。真实模型仅修改calc.py，精确类型检查拒绝bool及int/float子类等9组输入；4个加法用例保持通过。
- 真实试验暴露summary把candidate_oid投影耦合到剩余返工资格，最终attempt_limit时OID消失。原`.4`句柄不升级、不替换：主会话独立验证artifact SHA、projection SHA、原DB rework context SHA、完整event绑定及8条有界只读Git（0.124s）、实际源码/diff后，用原公开accept/ACK接口收尾。`manual-review-2.json`/`validation-2.json`/`manual-accept.json`/`manual-ack.json`保留此人工路径，不能声称旧driver自动通过。
- 最终inspect为completed、revision2、active_segments=0，collect为空。accept同command重放成功；accept/ACK后原HEAD仍是初始base，之后才在私有试验repo显式安全FF到候选，`integration-proof.json`证明工作区干净。原用户项目未作此集成。
- summary修复由独立worker在`candidate-summary-source`落地，3文件patch SHA256 `56d778e9fc0438e4778d4374ec0d63a24386f7737f2ef4de5fb6141309892925`。候选真实性与返工资格分离，保留写门禁与Git总界限；独审PASS，独立focused race通过。源快照`22d05424c0c048e694713e7939732ae17cb995ff`。
- 新私有完整原生包`.5` manifest SHA256 `98f13f94e128be8a8003642912b4cefc54f3fa7d4113e1f1a1ddd96d46865b30`，新wrapper smoke7通过。最终全量race与vet已由主会话跑完，待解析最终计数。
- 原Python认证tripwire前置同步失败仍保留，未被跳过或计为通过；需与反向真实Codex认证路径分别判断。原G1缺binary的skip已补环境，实跑1通过0跳过；两个固定Claude端点本地SSE工具运输各2项race通过，均不代替真实模型证据。
- 下一步：Claude受控主会话使用`.5`及原Codex pin完成反向真实流程；然后按最初审核表逐项复核、保护性集成、明确剩余认证fixture限制。
