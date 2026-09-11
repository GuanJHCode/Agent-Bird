# 完整 RPC 观察的最小附着状态机候选

本页是固定 `0.153.4` 源码导出的 G0 夹具输入规格，**不是原生通过或产品交付授权**。它保留使用已有信号组合的路线，不要求新增专用 attach RPC；也不把“所有 skills 刷新都设 unknown，等待下一次用户输入”当作 C05 解法。切回原会话必须能够从新的可区分证据恢复资格。

## 输入与状态

输入必须来自已确认原生 TUI 的同一实际连接完整双向 RPC 流；连接名不能代替进程/出生身份与原服务证明。只记录连接 epoch、本地接收序号、request id、method、成功/错误、thread/parent id、ephemeral、少量 source/status 枚举、`includeTurns/excludeTurns`、`cwds/forceReload` 及必要 marker hash。不保存用户输入、任意 developer instructions、历史正文或凭据。Hook 只辅助关联真实 `turn/start` 的 thread/turn，不能单独持续证明当前显示。

状态可限制为：`UNBOUND`、`PENDING`、`CANDIDATE(T)`、`DISCONNECTED`。同时保存已知 primary、至多一个 side 的 parent/child 关系、未决请求、正常 skills 刷新预期及候选显示集合。`CANDIDATE(T)` 只有在完整观察剩下唯一解释时产生；不通过 caller 自报的 selected_target/display_commit 补标签。

| 观察 | 候选转移 |
|---|---|
| 已确认连接建立 | 新 epoch，UNBOUND；不沿用旧进程/连接资格。 |
| TUI 的 `thread/start/resume/fork` 成功回执 | 登记 target 为待确认候选；回执本身不确认显示。与普通元数据读取或后台创建的上下文无法区分时保持 PENDING。 |
| 原生 side 创建 | 结合成功 fork 回执、新 thread、ephemeral 设置及之后同一 TUI 发出的固定 side boundary `thread/inject_items` 标记登记 parent/side；仅观察原生行为，不由夹具伪造该输入。fork 单独不足以注册 side。 |
| side 的 discard/unsubscribe/closed | 撤销 side 登记；当前候选受影响时先失效。删除/关闭/断线的错误不能视作成功切换。 |
| `thread/read(T, includeTurns=false)` | 仅登记不带语义保证的读取，不能将“最近 read”直接当 selected target；保留可能的非切屏解释。 |
| 可唯一归因于重放的 `skills/list([cwd], true)` | 可作为设置本地 active thread 之后的候选确认点；必须与唯一 target 前导或已知 parent→唯一 cached side 的状态相匹配，且没有未归类的正常刷新/读取上下文。 |
| `SkillsChanged` 通知、cwd settings 更新、空 cwds skills 刷新 | 按下节单独记账，不机械翻转 displayed thread。归因或顺序有歧义时保留候选集合并暂停交付。 |
| 同 TUI 切回已有 primary | 必须重新完成目标读取/重放匹配，恢复 CANDIDATE(primary)，无需新用户提示。当前“bare read”消歧未完成，不能假装该分支已经达标。 |
| 退出/断线/重连/未知影响显示的事件 | 立即失效；新连接重新建立可验证映射。新 epoch 不继承旧 Apps 请求或旧候选。 |

这里的确认点至多证明 source 中的本地状态提交先于该重放请求；它**没有证明终端像素已画完、没有后续切换排队、或外部观察零延迟**。是否加独立 TUI 侧采集验证、采用何种可接受失效延迟，由主会话处理合同；夹具不得把模型 ACK 反过来当附着证明。

## ListSkills 的全部生产来源

已从固定 commit 的官方 source archive 扫描 TUI Rust 路径，提取命名调用点并逐项分类。代码定义/响应 handler 不计作生产者，以下只有四类；没有依据 `/skills` 菜单名字推测网络行为。[扫描收据](idle-attachment-source-audit.json)

| 来源 | 实际 wire 参数 | 可观察分流 |
|---|---|---|
| session 配置/重放 `session_flow.rs:150` | `[self.config.cwd]`, true | 包括 cached side 和普通 snapshot 重放，必须结合目标/side 状态。 |
| `protocol.rs:145` 的 `SkillsChanged` | `[当前cwd]`, true | 完整 server→client 流中有前导通知；不能仅看请求名字。 |
| `settings.rs:499` 的 cwdChanged 分支 | `[新cwd]`, true | 有 `ThreadSettingsUpdated` 等设置来源；只有 cwd 实际变化才触发。 |
| `SkillsToggleView::close`（`skills_toggle_view.rs:216`） | `[]`, true | 关闭 Enable/Disable 视图即触发，没修改技能也会触发；空数组与重放的单 cwd 不同。 |

`/skills` 主菜单再 Esc 本身不请求刷新；但关闭其 Enable/Disable 子视图确实刷新。这修正了此前仅看 `handle_manage_skills_closed` 早退、遗漏视图 close 后续命令的局部判断。其空 cwds 不能被丢掉后再声称与切屏参数相同。

## read 前导仍是必要的资格缺口

不能把状态机简化成只认 start/resume/fork。非 side 目标若已有 Live channel、snapshot.session 存在、model 非空、rollout_path 为 Some，则：先做 `thread/read(T,false)`，`should_attach_live_thread_for_selection` 为 false，`should_refresh_snapshot_session` 也为 false；随后本地设 active id 并产生重放 skills 请求，**没有 thread/resume**。正常已访问的缓存 primary 满足这个条件。[选择门槛](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/session_lifecycle.rs#L525)、[snapshot predicate](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/thread_routing.rs#L1440)。

另一方面，false-turns read 还用于 fork parent 标题、导出、picker 预览、agents overview、后台状态和 safety buffering，不全是选择。部分有额外 paging/fork/archive/后台 request-id 上下文，但本轮没有完成所有这些序列的唯一分流证明。实现者必须以实际完整 RPC 推导上下文；不能输入“这是一次选择”的假标签，也不能只保留 read 与 skills 两帧而删掉额外 RPC。

因此这份候选可先实现纯观察、集合状态及负例的夹具/TDD；在 bare-read→重放匹配可区分之前，不应把它升级为覆盖切回活性的原生资格。具体可试子集和是否进入模型实验由主会话决定。这里没有建立一个否定所有完整流组合的不可区分反例。

## 顺序、Apps 与日志

LocalDaemon/Remote 路径存在 pending AppEvent 时会阻止新 terminal input，且 skills 请求在主事件处理中 await；这可用于识别有序的 UI 重放工作。[事件队列门槛](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/startup.rs#L733)。它不把所有后台网络任务排序到 UI 切换顺序中。

Apps 可先不用：当前本地只读观察为 apps=false、ChatGPT 登录布尔值 true。条件启用后，Apps 请求捕获 displayed id 再异步派发；旧任务没有因 widget 替换被取消，UI generation 只在本地事件校验，不在 Apps 请求/响应中。取最近 Apps thread 不可靠，不能作为状态机兜底。[捕获/派发](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/app/connector_mentions.rs#L35)。

可选 session_log 在 enqueue 前记录 SelectAgentThread 等 variant，但不含目标/generation；热键 toggle 直接调用选择流程，绕过该 variant。日志还序列化完整 AppCommand，默认关闭，不能直接当成现成白名单采集。它可以帮助失效或解释本地事件，尚未证明是必要条件或足以关闭匹配缺口。

本页没有安装代理、改变 Apps 开关、修改认证/权限或运行模型。source archive 与缓存只用于固定源码扫描，保留收据后清理。
