# G0.1 附着接口与完成条件核查

日期：2026-09-11；起始基线 `88528c04157e7b5e781673379fd86b2ba71ae9b4`。本轮沿用 macOS 优先、Windows 延期和 G0.1 未放行不得进入下一功能的要求。

**结论：完整目标未完成，G0.1 仍受阻。** 本轮发现了能增强后续身份实验的 Hook 通知字段，但没有找到可刷新、可失效的当前 TUI/thread 附着信号，也没有找到新的合格按需启动候选。故没有重新发起模型轮次、重复安装或实现 G0.2–G4。

## 本轮任务拆分与结果

| 任务 | 负责人 | 实际输出与判定 |
|---|---|---|
| 原生附着候选核查 | `g0_attachment_research`，只读 subagent | 检查当前 schema、官方说明及本机二进制中的相关符号/局部实现；候选与局限见下表 |
| 完成条件审计 | `g0_completion_audit`，独立只读 subagent | 对照原设计、验收矩阵、当前实施计划及已提交 daemon 证据；单样本成立不等于 G0.1 通过 |
| 环境、启动路径与证据整合 | 主 Agent | 复验二进制、端点、旧进程和文件摘要；生成并保存协议摘要，保留现有可信工具 |

两个 subagent 均不写代码或修改运行环境，本轮没有多个写入者或新 worktree。以前探针实现与测试记录继续有效，但本轮没有重跑它们，不能将历史测试称为本轮测试。

## 当前环境与新增证据

[预检](preflight.json) 确认普通入口仍为 Codex 0.153.4，原生二进制 SHA-256 为 `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`，与上一轮一致。默认端点采用上一轮 listener 实测路径 `~/.codex/app-server-control/app-server-control.sock`，预检时不存在；六个旧测试 PID 均不存在。预检时原有 `.zprofile`、Codex 配置和 daemon 设置的摘要均与上一轮一致。

收尾的[第一次校验](verification.json) 有一项失败：`~/.codex/config.toml` 摘要已改变，文件修改时间为 2026-09-11 09:41:56（北京时间）。主 Agent 和两个 subagent 均未执行配置写入；仅凭摘要不能判断修改内容或来源。已保存[有限元数据](config-drift.json)，没有复制配置正文，也没有回滚。不能声称全局配置在整个实验期间保持不变；以后原生实验需重新建立当时的有效设置基线。本轮没有发起依赖该设置的新原生模型实验。

本轮新执行 `codex app-server generate-json-schema --experimental --out tasks/g0-attachment/tmp/schema`，退出码 0。输出 416 个 JSON，包含 155 个 ClientRequest、1 个 ClientNotification、11 个 ServerRequest 和 81 个 ServerNotification。[协议摘要](schema-audit.json) 保存方法清单、关键定义及顶层 schema 摘要；这只证明该二进制生成的静态接口。

| 候选 | 本轮核查结果 | 对 G0.1 的影响 |
|---|---|---|
| `initialize` / `Thread` / `server/diagnostics` | 初始化没有返回连接身份；Thread 没有当前 TUI 映射；诊断只有进程字段及 name/value gauges | 不足以查询当前界面归属；不能把指标数量当作身份 |
| `remoteControl/client/list` | 按 environmentId 查询远控客户端；客户端字段没有本地 connection/thread/当前界面关系 | 不证明原生 TUI 附着；本轮未启用远控或调用该方法 |
| `thread/environment/connected` / `disconnected` | 参数为 threadId、environmentId，表示执行环境连接 | 不能冒充 TUI attach/detach |
| `hook/started` / `hook/completed` | 通知外层具有 threadId；事件类型没有 TUI attach/detach/heartbeat | 可作为未来 fork/子 Agent 精确身份实验的候选，尚未实测，不能解决在线判据 |
| 内部 `ThreadEntry::update_has_connections` / `MessageProcessor::connection_closed` | 本机符号表存在内部连接状态；局部实现核查未找到公开附着查询出口 | 内部实现线索不等于外部可用协议；不据此推断不存在其他未核查路径 |
| TUI 订阅/切换逻辑 | 内部有 tracked thread 退订和 agent thread 附着处理 | 即使取得订阅关系，也仍需证明“当前显示的 thread”，不能直接等同 |
| `daemon bootstrap` / in-process 打开 listener | 本地帮助描述 durable management；本轮未找到能从正常 in-process TUI 经 Hook 打开同进程本地 listener 的受支持接口 | 不操作旧 daemon 管理器；没有新启动候选。上一轮冷启动实测仍是已知组合的运行依据 |

官方 [Hooks 的 SessionEnd 说明](https://learn.chatgpt.com/docs/hooks#sessionend) 明确将会话结束与切换/退订区分开；因此不能用该事件代表即时界面断连。官方 [App Server 订阅说明](https://learn.chatgpt.com/docs/app-server#unsubscribe-from-a-loaded-thread) 也不把 loaded 等同于当前 UI。原生运行证据仍采用 [上一轮退出前后对照与冷启动结果](../daemon/result.md)，本轮没有把静态复核计为新增端到端样本。

## 要求逐项核对

| 当前或后续要求 | 权威证据 | 判定 |
|---|---|---|
| 任务拆分、明确责任、subagent 执行与独立审计 | 实施计划及本轮三个任务 | 本轮分工已执行；不代表产品目标完成 |
| C01 原服务、TUI、根 thread 关联 | daemon/listener-start.json、native-protocol-fields.json、before-tui-exit.json | 预先建立服务条件下 1 个样本成立；基础 3 次未完成 |
| C01 符合直接入口的服务接入 | daemon/no-listener-start.json | 已知冷启动组合回到 in-process，尚无合格自动启动候选 |
| G0.1 必需当前附着正证据 | daemon/before-tui-exit.json、after-tui-exit.json，本轮 schema | 退出前后均 attached=unknown；不满足准入 |
| C02 同仓库双会话，C09 resume/compact/clear/fork/子 Agent | g0-validation-plan.md §2；当前记录 | 未执行完整场景；新 Hook 字段不替代实测 |
| C12 完整信任、禁用、策略禁止及定义变化 | macOS 正常信任记录 | 只验证正常信任和 Hook 执行；其余未验证 |
| C13 连接/必要 resume/投递后的有效设置连续性 | 配置摘要、只读 probe | 有限读取边界证据；完整行为未验证 |
| G0.2–G0.4：C03–C11 回调/排序/恢复/对账 | 实施计划后续功能清单 | 尚未实施或完整验证；故障屏障 20 次未执行 |
| F01–F26：G1–G4 产品与交付 | 实施计划、验收矩阵 | 均未实施，不允许绕过 G0 |
| Windows 真实实现与验收 | 已明确延期的实施范围 | 延期未验证；不阻挡当前 macOS 调查，不声称两平台完成 |

## 恢复实验的具体条件

继续所需的是原生能力证据，不是重复安装、重复信任或再给一次操作权限：

1. 有可外部读取的服务实例、TUI 实例和当前 thread 绑定，能够刷新，并明确处理切换、正常退出、强杀和原服务重启后的失效；桥接器自身订阅不能让用户被误判在线。
2. 有符合普通 `codex` 入口的原服务接入/按需启动候选；在全空闲服务退出后仍能再次建立正确接入，不要求替代启动器或手动预热。
3. 候选具体后，先验证上述两项，再按 G0 规格执行三次基础场景、双会话与身份变化；全部成立并独立复核后才进入 G0.2。后续回调与故障验收仍需分别完成。

当前没有确认仍在运行的实验进程或作业可等待。本轮增加了接口排除证据及精确恢复条件，属于有进展的调查；完整目标保持未完成。[接口独立核查](interface-review.md)、[清理清单](cleanup.json) 和[最终检查](final-checks.json) 已保留。最终检查中的文档与证据检查通过，配置保全检查仍为失败；这两者均不替代 G0 准入。

[最终独立复核](final-review.md) 对新增文档/证据未发现有效问题，同意交付此次核查记录；主 Agent 维持 G0.1 不放行的判定。
