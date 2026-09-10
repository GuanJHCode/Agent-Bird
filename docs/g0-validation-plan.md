# G0：原生 Codex 会话回调实验规格

状态：实验计划，尚未执行。目标是证实或否定原生会话接入路径；不是通过文档检查把可行性算作通过。关联 [C01–C13](acceptance-v0.1.md) 和 [证据](evidence.md)。

## 1. 实验环境与固定判据

在独立 OS 测试用户/干净 VM 与临时 Git 仓库中进行，macOS 与原生 Windows/PowerShell 分开验证。使用正常已登录 CLI，不改认证或权限，不访问真实业务仓库。模拟执行者只写固定标记、单调计数器和合成结果；测试 Codex 的继续处理可能产生实际模型调用，须在原型测试阶段明确执行，不与当前文档复核混称。

记录 OS/架构/文件系统、原生 Codex/实际 daemon/探针/Go/Git 版本、原生启动 argv、集成条目和 Hook 定义摘要。不记录环境值、账号凭据、完整个人会话历史。

每一受支持组合的串行基础场景运行 3 次，故障屏障场景运行 20 次。执行前固定：本地连接/持久化观察窗口 10 秒；允许主控模型处理的窗口 120 秒。提供者限流/不可用标记 infrastructure_inconclusive，记录后重测，不记作通过；超过窗口不能无限等待直到碰巧成功。记录实际延迟，不用这两个观察窗口替代 G4 资源/传输 SLO。

原生源服务、身份、在线信号、角色语义和歧义对账均须成立；任一核心条件缺失则该组合 no-go。不能通过手动输入“继续”、改启动命令、开启新主控或调宽权限来算通过。

## 2. 接入与身份（C01/C02/C09/C12）

1. 仅安装本项目 Skill 和 command Hook，用户正常输入 `codex`，不加 `--remote`，不启动第二个 app-server。
2. 通过正常 `/hooks` 流程审阅并信任定义。分别测试未信任、被禁用、策略禁止、Hook 执行失败、升级后定义摘要变化；首次 submit 在创建执行者前必须拒绝缺少的准入条件，不代批信任。
3. 只读检查该版本帮助/schema。0.153.4 的 `app-server proxy --sock` 和共享 daemon 是候选端点访问路径，不是预先确定可用。探针只连接候选原服务，按协议 initialize/initialized，然后只读 thread 元数据。
4. 在原生 TUI 发一个无业务副作用的唯一标记轮次，交叉核对同一次 Hook、原生通知、服务身份及 `Thread.id`。读取相同历史/cwd/名字/最近时间不算同服务证明。
5. 同仓库同时启动 A、B 两个不同会话，使用不同 nonce。记录 Hook 的非敏感 `session_id/source/turn_id`、thread.id/sessionId/parentThreadId/forkedFromId；缺少字段如实记录。
6. 分别执行 resume、compact、clear、fork、在 fork 内新建工作流、原生子 Agent 开始/结束。工作流绑定发起它的当前 `controller_thread_id`，子 Agent Hook 不抢占。压缩不能丢绑定，clear/fork 不自动继承旧工作流操作权。
7. PID 与创建身份、实例 nonce 共同记录；Hook 的父进程可能是 daemon，不能用它推断 TUI 身份。

产物：每个平台一份“候选端点→实际服务→TUI实例→controller_thread_id”的证据链。无法形成链则停止后续该组合的自动回调实验。

## 3. 在线、忙闲及关闭恢复（C03–C10）

对以下状态逐个提交带固定 envelope 的 milestone/question/result，记录单调时间的 UI/OS动作、RPC、原生 turn/item 通知、任务状态和 ACK：

| 状态 | 必须观察到的行为 |
|---|---|
| 普通模型轮次忙碌 | 结果进入原 thread，在原生可处理边界继续，不并行伪造新主控 |
| 长工具执行中 | 工具正常结束/仍可取消；回调不强行中止工具 |
| 原生权限或用户问题等待 | 原生提示仍可正常回答；连接器不代答、不更改审批 |
| TUI打开但idle | 无需用户输入，主控收到 functionCallOutput 并调用测试 ACK |
| 正常退出/强杀CLI/关闭终端 | 模拟执行者计数持续并完成；确认离线后不发新的 turn/start |
| 同一TUI进程切换到另一thread | 旧thread不会因PID仍活而被视为当前附着 |
| 仅桥接器仍订阅 | 不把自己造成的loaded状态当成用户在线 |
| 原生resume原thread | 自动补交积压，事件只影响该工作流 |

在线信号须记录“原生 TUI 实例当前附着此 thread”的正证据、获取来源、刷新/失效条件和最大延迟。loaded、canAcceptDirectInput、thread/status、PID、SessionEnd、thread/closed 任一单独值都不够。没有可靠正证据则 C05–C07 不通过。

同 thread 两 TUI 验证 owner_conflict：停止旧交付者后启动新者，再恢复旧者；旧 socket 仍能发送时不能仅因租约到期认定已隔离。冲突归属未解决期间执行者继续，主控自动决策暂缓，结果仍持久化。

连接器 RPC 使用固定白名单，不复制文档示例中的 model/permissions/cwd/provider 等选项。确需 resume 时核验最小参数不会覆写非凭据有效设置；记录连接前后差异，禁止调用账号/配置/审批修改方法。

用户新输入与回调并发单独设置屏障：用户输入先到、toolOutput先到，以及idle→busy边界两者同时写入。验证原生用户消息不丢失/覆盖，回调仍是functionCallOutput，顺序以原服务记录为准；用户取消或修改合同一旦提交，旧action必须被controller/task revision拒绝。不能仅用“已有busy轮次”场景代替这个竞争窗口。

## 4. 交付 ACK 歧义与语义幂等（C08/C11）

发送前持久化 `delivery_id + controller_thread_id/epoch + event_ids/revisions + payload_hash`，放入 toolOutput.output envelope。保存原生 turn/item 回执。实验故障代理只位于新探针连接与原服务之间，不代理或改写原生 TUI 通信。

在每个屏障切断连接或终止探针/测试协调器：

1. 本地提交意图后、发送前。
2. 请求写出后、原服务回执前。
3. 原服务接收/排队后、历史尚未可读。
4. 回执已收到、本地状态尚未提交。
5. history_confirmed 后、主控尚未处理。
6. 主控决定及逐事件 ACK 已提交、响应丢失。

另测试原服务崩溃、分页历史、compact 后历史、重复批次展示、部分 ACK、waiting_user 后又来问题。对账使用经该版本实测的 thread/read 或分页 turns/items 接口；必须证明查询覆盖发送窗口，排除仍可写入的旧连接和原生待处理队列。查询不到标记但不能证明未接收时，必须为 uncertain，禁止盲重发。

主控处理每事件时选择 handled/waiting_user/stale/rejected，并提交协调器认可的 action slot。故意让模型在重复回调中使用不同 command UUID，计数器仍只能产生一次真实派发/答案续接/整合意图。

运输对账完毕后普通进展即使业务 ACK 尚未完成，也不能阻塞后续问题发送；运输尚 uncertain 时则不能跳过盲投。受支持故障恢复后若一直无法自动对账并补交，C11失败，不能用“让用户手动collect”作为通过。

## 5. 关闭提交边界（C10）

用屏障分别在在线检查前、检查后写入前、原服务接收后、回执落库前关闭 TUI。记录该轮次实际继续/取消/排队行为。

原服务可对账的接收为提交边界：接收前已证实离线则待交；已接收后关闭允许原生已提交轮次按其语义完成或取消。恢复后必须补齐尚未处理事件且无重复副作用。不能把无法观测的网络时间假定为接收时间；不能承诺外围进程撤销已提交输入。

若该原生版本没有足够观测或条件接收能力，报告具体不足，不能把“偶尔少一次回调”或“一直不敢发送”记为满足可靠回调。

## 6. 结果模板与退出条件

每项记录：测试ID、输入和屏障、环境、候选接口、观测窗口、实际原生角色/turn/thread、OS与RPC时间线、durable event/decision计数、最终文件计数、PASS/FAIL/INCONCLUSIVE、证据路径。

- 帮助/schema = 静态接口证据。
- Hook/只读连接 = 身份与生命周期证据。
- 模型在原会话自动消费 = 交互证据。
- 故障恢复和无重复副作用 = 可靠性证据。

每种证据只证明自己的范围。全部核心场景通过的明确版本/平台组合才能进入 v0.1 支持矩阵；G0未执行或失败，方案仍是待验证设计。
