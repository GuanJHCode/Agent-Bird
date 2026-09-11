# G0 原生返回通道与替代路线实验

日期：2026-09-11；起始代码 `6169c5d`。用户明确授权实验并在失败时自主改用其他候选。主 Agent 负责编排、派发、整合和验收；subagent 负责实现与实测。参考 `tasks/cc-plugin-review/docs/findings.md`。

## 固定目标与范围

macOS 优先，Windows 延期；保留原方案主会话控制、执行者独立存活、原会话自动接收、关闭/恢复隔离和可靠交付目标。各路线必须记录原生消息角色及来源，不能用伪造用户输入、写聊天文件、手动“继续”或扩大权限算通过。原有已信任 Hook、认证、权限和历史业务会话保留。

只实现完成 G0 所需的合成宿主、实验脚本、原生转发配置及证据。暂不实施生产调度、真实第三方 CLI adapter 或发布安装器。新的原生调用可消耗正常已有登录下的模型额度；本地观察窗口 10 秒、模型处理窗口 120 秒，限流/服务失败记基础设施不确定，不误判原生能力。

## 工作划分

| 工作 | 负责人 | 独立范围与完成判据 |
|---|---|---|
| 原生子 Agent 返回实验 | `native_return_runner` | 独立 worktree；拥有实验脚本及 native-return 证据。先使用短时合成工具输出验证真正原生 CLI 内主控结束当前响应后能否自动接收子 Agent 结果，记录 thread/turn、角色、时间和实际动作；失败给出可重现原因 |
| 最小合成执行宿主 | `synthetic_host_impl` | 另一 worktree；只拥有 `tools/g0-return-lab/`。先写失败测试，再实现可独立启动、有限延迟产出固定结果、只读等待与读取；不实现生产调度器。真实本地 I/O、断开观察者后继续、结果落盘及有界参数验证 |
| 会话身份与切换实验 | `native_identity_runner` | 第三个独立 worktree 和自有原生 TUI；验证同 thread 双实例写锁、退出后接管、fork/new/clear 的 Goal 归属及 root/child 身份，不操作主 runner 的会话 |
| 备用通道核查 | `native_return_alternatives` | 只读；查原生后台工具、会话恢复、原生工具结果/消息队列以及受控服务等候选，给出下一条具体实验，不修改配置或运行模型 |
| 工具/结果独立审查 | 非实现 subagent | 每个候选依据当前代码和实测证据判定；发现有效问题先修复，不将模拟服务或本应用协作工具当原生 CLI 证明 |
| 整合与候选切换 | 主 Agent | 依据证据决定继续、修复、切换；只有可行路线成立或已无可推进候选时收束本轮 |

各写入者使用不同 Git worktree，不能覆盖主树/其他 Agent 修改。主树的计划和整合由主 Agent 持有。专用原生测试仓库沿用 `tasks/g0-macos/data/test-repo`；两名运行者各自独占已记录 PID/创建身份及 thread 的 TUI，使用不同 nonce 和数据目录，不操作对方的实例。

## 候选与顺序

1. 原生子 Agent 薄转发：先验证主控 idle/busy 时的原生完成返回，再接入合成宿主，覆盖退出持续执行和明确原会话恢复。正常三次基础样本后才认为行为成立。
2. 原生后台工具/异步命令：如子 Agent 路径缺少必要语义，检查同等的原生工具完成通知；不得凭会话句柄存在认定通知可达。
3. 官方受支持的其他恢复/交付机制：核对消息角色、身份和唤醒边界；不把普通用户消息队列等同工具结果回调。
4. 受控原生服务或启动集成：如前述均失败，实验可管理服务/启动条件的候选。若候选必须改变产品入口或其他明确需求，先形成具体可审查差异，不能静默算原方案通过。

本轮优先试验了原生 CLI 的持久 `/goal` 与原生 `clock.sleep`。active Goal 的正常退出／精确 resume 已自动读取既有 Host 结果，在线 Goal 也能主动续轮；后续又实测到编辑 Goal 后旧 cell 完成新 Goal 的竞争行为，不能据此放行 G0。不将测试专用的 durable SleepItem 扩展或当前应用的 goal 工具视为 CLI 能力证明。结果正文来自真实 Host 读取工具；原生 goal 内部控制提示只承载恢复编排，不冒充执行结果或新用户授权。最新判定见 [实验汇总](../docs/implementation/g0/native-return/result.md)。

独立 Host 的生存与可靠数据保存属于共同实验能力。任一候选通过在线样本仍不足以宣布 G0 通过；退出/恢复、用户竞争、C09 身份变化、交付对账与幂等按原 G0 标准补齐。只有基础设施瞬时错误则有限重试同一有效实验，不无限等待或扩窗口。

## 产物与清理

可复用工具放 `tools/g0-return-lab/`；实验脚本按用途放 `tasks/g0-native-return/scripts/`；必要证据放 `docs/implementation/g0/native-return/`。下载、构建、worktree 中间物放本任务 `tmp/`，本轮运行数据放 `data/`。保存退出码、PID 创建身份、版本和合成事件元数据，不保存凭据、完整环境或无关个人会话正文。收尾按已知归属核对并清理临时资源，保留工具、报告与必要恢复材料。

## 实施结果

- 合成 Host 已通过独立审查，源提交 `0fc9b0b` 整合为 `98ee890`；确定性覆盖测试修复 `0d9de85` 整合为 `b0293fd`。实验二进制 SHA-256 为 `cf7bfc0acf6f3348210c50c75f9a0ea85b6a56e1abafbbcec85156580dc40cec`，原生运行者使用已复核字节。
- 普通子 Agent：已观察到一个 idle 完成提示但无自动续轮的失败样本；一个 busy 交付样本实际为 native `agent_message`。原生 `clock.sleep` 有一个被子 Agent 邮件提前中断的样本，属于活跃轮次等待。
- 无 goal 的退出/恢复对照未自动续轮。原生 goal-01 单样本已观察到 `/quit` 后 native PID 消失而 Host 仍运行，随后 Host 结果落盘；普通无附加 prompt 的精确 `codex resume` 自动续轮、真实工具读取结果、完成 Goal 并 ACK，完整观察窗口内未再续轮。
- 上述 Goal 控制上下文由原生系统以 `role=user` 的 goal context 提供，不含外部任务结果；实际 Host 数据单独来自 `custom_tool_call_output`。不能将“无新人工输入”写成“没有 user 角色消息”。
- Runner 初版采集器 `153a4ff` 的白名单问题已修订并以旧版红/新版绿回归；`9300d2a` 已通过独立复核，整合为 `267a771`。goal-01/02/04 三个不同 root 的正常退出/恢复事实、goal-05 强杀与 goal-06 真实用户输入事实均已独立复核，整合为 `8c9b145`；goal-03 因退出发生在 Host 完成之后排除，保留为变体证据。
- 身份运行者已观察到同一 root 的第二个普通 resume 被 active writer 锁拒绝；旧 TUI 正常退出后新实例恢复成功。普通 fork 的原生 get_goal 为空，可另建自己的 Goal；旧 root 的 paused Goal 恢复时出现原生选择框，明确选择 Leave paused，不把它算作 active Goal 的自动恢复。
- 单事件 ACK 夹具 `65d8311` 已通过独立复核，整合为 `1ab1c4a`：绑定 controller thread、结果产生 revision 和 event hash；ACK/revise 使用同一个 OS 文件锁，决策与效果计数同一原子提交；改变 command ID 不能绕过同一 action slot。仅 handled 的 effect_count 为 1，其他决策为 0。修改 revision 或取消后，旧结果不能通过换传新 revision 再次生效。原四字段 read/wait 和在跑二进制保持不变。新二进制 SHA-256 为 `8ed9567aaf95867204f448aa5bd498a7ce5895790a0f24149419d9b460c962db`；整合后完整 race 与 vet 新鲜通过，见 `main-host-verification.json`。环境身份比较仅用于防误归属，不宣称抗同用户伪造认证。
- 身份材料 `e3f2825` 已独立通过并整合为 `2bc100a`。同一 exec cell 的 Goal 编辑竞争反例 `a33e933` 经措辞修正 `ac16fff` 后独立通过，整合为 `707d4a1`／`4a754f3`：普通 UI 修改目标后，旧 continuation 仍将新 Goal 标为 complete。反例在固定 120 秒窗口内出现；整轮最终结束超窗，不能另算窗口内完成样本。原生 Goal 无业务版本校验的问题不能由 Go ACK 守卫抵消。
- 提前 resume 的 pending→一次 Host wait→completed，以及在线 active Goal 在 final 后 23ms 续轮，均已独立复核，整合为 `ec48fc6`。后者在结果产生之前启动，因此属于主动继续编排，不是结果事件才唤醒真正 idle 主控。
- `944df4c` 新增 `runner_ready.py` 的宽松 Goal 匹配和未校验时间字段导出两项问题，已由 `d416e08` 修复并独立通过，整合为 `21a85e6`。工具只证明 Host-ready，不读取 Goal／rollout／SQLite；8 项 CLI 回归在旧版 7 项失败、新版全部通过。已保存样本的 Goal 资格由另一套严格采集器与进程证据确认。
- 同 root 正向 owned start／inspect／ACK 的原生事实 `d9d9117` 已独立通过并整合为 `c8298fd`，决策与效果计数均为 1。跨 thread 原生负向分支被平台检查中断，未派发；现场恢复后仅完成正向样本并正常退出，负向项不计通过。
- 最后一项 goal-09 与 ACK 白名单 `7d7884c` 已独立复核并整合为 `e289334`：修复后 Host-ready 真实正例成立，重复 ACK 保留首条 command ID／effect_count 1；整条 ACK 流程超出初始 120 秒，不计该窗口通过。原生 TUI／Host 已退出，离线证据及清理已完成，不再新增模型场景。
- 尚缺完整当前 TUI 附着、真正 idle 回调、compact、原生权限／问题等待与交付对账等条件；核心 Goal 竞争已出现反例，0.154.0 对应公开源码未提供保护。G0 不放行，不为凑次数重复缺少前提的 20 次屏障；后续新实验须有具体原生能力或经用户明确改变的合同作为依据。
