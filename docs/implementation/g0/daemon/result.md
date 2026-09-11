# G0.1 原生共享服务续验结果

日期：2026-09-11。起始代码基线3e42c0d。用户已授权自主执行后续流程；Windows延期。当前功能仍为G0.1，未实施G0.2或G1–G4。

**当前结论：原生共享服务的外部连接与单个根会话身份已实测成立；G0.1仍不放行。** TUI当前附着正证据尚未建立，普通`codex`在候选服务停止后回到in-process，没有自动拉起服务。不能据此宣称所有版本或所有集成方式均不可行。

## 环境修复

官方安装器固定`--release 0.153.4`执行成功，修复了缺失的managed standalone安装；npm版本保留。managed和npm的原生二进制SHA均为`b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3`。新login shell的普通`codex`解析到`~/.local/bin/codex`。已有`.zprofile`、`config.toml`和daemon设置摘要均未改变。[安装意图](install-intent.json)、[实际输出](install-output.log)、[安装后验证](install-result.json)

没有运行旧daemon的bootstrap/start/stop/restart/disable命令。旧PID文件中的33996已被系统进程复用，未向它发送信号。本轮选用本任务直接创建和管理的原生listener；其实际远控状态通过只读RPC观察为`disabled`，不把缺省参数或本地socket本身当作远控关闭保证。[预检](preflight.json)、[运行状态](listener-runtime-audit.json)

修复后的官方安装作为可复用环境保留，不是本项目替代启动器。没有修改Codex二进制、账号、审批权限或全局AGENTS.md，没有复制认证配置，没有推送或发布。

## 实际实验

| 操作 | 实际结果 | 证据范围 |
|---|---|---|
| `codex app-server --listen unix://` | 本任务服务PID35791创建默认socket，权限0600 | 人工准备的原生共享服务候选；不代表产品已按需启动 |
| 专用仓库直接`codex` | TUI PID35918；仅TERM为PTY显示设置，未传`--remote`或模型/权限覆盖 | 普通入口隐式复用已存在服务 |
| 一次普通唯一标记 | 原生返回`G0_DAEMON_ACK_A`，主标记轮次未观察到模型工具调用 | 普通用户输入，不是toolOutput回调；原生另有内部辅助任务，不声称总模型调用仅一次 |
| 原生`/status`和真实Hook | 线程01a08e0b-bd0d-7400-b069-d50978939cdd；Hook PPID35791 | 同一次原生TUI根会话映射 |
| Go探针读取默认socket/明确thread | exit0；process.id=35791，thread.id与原生/status和Hook一致 | 原服务外部接入及元数据，不证明在线 |
| 精确本任务日志 | transport=unix_socket、client_name=codex-tui、connection_id=0 | 原生TUI使用该服务的关联证据，非猜测最近历史 |
| TUI `/quit` | exit0，35918退出；随后probe仍为idle、canAcceptDirectInput=true；尚无SessionEnd | loaded/idle/input能力不能作为界面在线证明 |
| 停止本任务listener | 核对创建身份后向该PTY发送Ctrl-C，exit0；五个测试进程均消失、socket删除，此时才收到根会话SessionEnd | 正常结束测试资源；不代替执行者持续运行或故障恢复验收 |
| 服务停止后再次直接`codex` | 新TUI36854；超过10秒默认socket仍不存在；原生日志transport=in-process | 此安装和启动组合没有自动拉起共享服务；未发送普通模型提示 |
| 第二个TUI `/quit` | exit0 | 本轮原生测试已结束 |

证据：[listener归属](listener-start.json)、[连接协议字段](native-protocol-fields.json)、[TUI退出前](before-tui-exit.json)、[TUI退出后](after-tui-exit.json)、[listener退出](listener-stopped.json)、[没有listener时的启动](no-listener-start.json)。主标记在约13秒内已观察到回复，未超过120秒模型观察窗口；Go元数据检查约13–23ms，未超过10秒本地窗口。

退出前的只读审计使用当前环境已有Python websockets 16.0，并非产品代码：固定Unix端点、10秒总窗口、32帧、64KiB/帧，仅initialize、diagnostics、loaded/list、明确thread/read及远控status/read；不发轮次、不改配置、不保存原始消息。默认压缩握手首次被关闭，关闭压缩后握手/初始化成功。正式Go探针始终正常，源码和已信任Hook二进制未改。

审计发现一个额外loaded的ephemeral线程：相同测试目录，原生日志来自`codex_skills_extension::shadow_selection_experiment`且时间落在本次标记轮次内。这些证据支持其为原生内部辅助任务；该归属判断明确属于推断，不当作TUI附着正证据，也未读取完整历史。[额外线程审计](extra-loaded-thread-audit.json)

原生共享服务自动出现的`codex_tui`及用户既有工具属于原生运行行为，本项目未通过MCP进行派发或回调。listener退出时原生日志有一次内部MCP会话删除失败；其exit0、相关子进程和socket均已清理，但不能将日志写成无错误。

## 核验与缺口

独立接口核验发现入口链`can_reuse_implicit_local_daemon → 默认socket probe → app_server_target_for_launch`，probe含50ms连接预算，已核对片段没有自动启动服务步骤。这是静态证据；本轮两次原生运行分别验证“已有服务可复用”和“无服务转为in-process”。

新鲜experimental schema含155个ClientRequest、1个ClientNotification、11个ServerRequest、81个ServerNotification。`server/diagnostics`只有进程字段和无标签的数值gauges；initialize、Thread、resume响应没有TUI/client/connection到thread映射；unsubscribe只反馈调用方订阅状态。没有找到可用于刷新和失效的当前TUI附着查询或通知。[静态复现命令](static-checks.md)

官方说明最后一个订阅者离开后thread可继续保留，SessionEnd也不等于TUI断连事件。这与本次真实前后对照一致。[订阅生命周期](https://learn.chatgpt.com/docs/app-server#unsubscribe-from-a-loaded-thread)、[SessionEnd](https://learn.chatgpt.com/docs/hooks#sessionend)

| 验收部分 | 状态 |
|---|---|
| C01原服务端点与单个根thread映射 | 当前预先启动服务条件下，实测一个样本 |
| C01完整直接入口、C02/C09身份变化 | 未通过完整验收；仅有上述有限样本 |
| C05–C07所需当前TUI附着正证据 | 受阻；现有状态在TUI退出后保持相同 |
| C03–C11回调、执行者持续运行、恢复、对账 | 未验证；未发送toolOutput或进入下一功能 |
| C12完整信任/策略失败情境，C13实际桥接设置连续性 | 未验证；普通读取和未改配置摘要不足以替代完整条目 |
| P02/P03候选生命周期 | 本轮没有自动拉起；实际空闲退出保证未建立。手动listener保持运行的观察不等于已完成完全空闲条件验收 |
| Windows | 延期未验证 |

基础场景3次和故障屏障20次未完成。缺少当前功能必要的附着证据时，不为凑次数重复模型轮次，也不将常驻服务、手动继续、键盘模拟或替代启动器当作通过。

继续需要一个能可靠确认当前TUI所附着thread并有明确失效/切换语义的原生信号，以及符合直接入口要求的按需服务启动候选；新增操作授权已经使用，当前缺口不是等待用户再次授权。

## 独立复核及主Agent判定

GPT-6 Astra / max已完成安装/实验边界初审和最新实际运行证据的最终独立复核：有效问题0项，未解决复核问题为空。复核确认单样本关联成立、退出前后元数据相同、归属推断及内部错误未被掩盖，临时材料/原始文件/可信工具保护记录一致；未发现具体越界证据。

主Agent最终判定：本轮续验与复核已完成，**G0.1仍受阻，G0未通过**。接入原服务的候选路径已有真实证据，当前TUI附着正证据和按需启动条件仍未建立。不进入下一功能，不用额外操作授权替代缺失的原生能力证明。

新鲜证据一致性检查见 [final-checks.json](final-checks.json)，清理及保留材料见 [cleanup.json](cleanup.json)。这些检查不替代未通过的G0准入条件。
