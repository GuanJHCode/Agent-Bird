# macOS G0.1 原生接入准入结果

日期：2026-09-10。源码基线2da7af8，探针/Hook二进制未修改。本轮由用户正常完成信任后继续实验，Windows延期。

2026-09-11续验已得到新的共享服务证据，见 [最新结果](daemon/result.md)：普通codex可复用预先建立的原生服务，但附着与自动启动缺口仍在。本文保留为9月10日历史结论。

**结论：当前组合G0.1不放行。** 已证实普通原生对话和三种Hook身份记录工作；尚未建立可供外部桥接的原服务端点，以及可刷新、可失效的TUI附着正证据。不能据此推断所有Codex版本或原生daemon路径均不可行。

## 实际操作与证据

本次使用macOS 26.3.1 arm64、现有npm Codex 0.153.4、同一专用测试Git仓库。原生argv为 `codex`，没有 `--remote`、权限覆盖或替代启动器。第二次工具PTY仅设置 `TERM=xterm-256color` 以提供终端显示能力；认证环境、PATH和CLI设置未被本任务覆盖。原生按用户已有配置加载的其他工具不作为本项目接入通道，本次模型标记轮次没有工具调用。

| 操作 | 实际观察 | 证据范围 |
|---|---|---|
| 正常启动原生 `codex` | 越过目录信任提示，显示原生TUI | 当前目录信任已生效 |
| 原生 `/hooks` 逐项查看 | SessionStart/SessionEnd/UserPromptSubmit均Installed=1、Active=1、Enabled、Trusted，来源专用仓库hooks.json | 用户正常信任已完成，未代批或切换状态 |
| 普通用户标记消息 | 返回 `G0_ACK_A`，未调用工具 | 一次普通原生交互；不是toolOutput回调 |
| 原生 `/status` | 本任务会话标识为01a08ac7-adb1-7450-8553-21bc2963b1a8 | 仅保留本任务ID和非凭据模型/权限字段，不保留账号/用量显示 |
| 三种真实Hook | session_id均匹配本任务，PPID均88713；UserPromptSubmit有自己的turn_id | 当前根会话的身份映射样本；不推广到fork或共享daemon |
| 精确本任务日志的公开协议字段 | process_uuid含本任务PID88713；thread/start及hooks/list的transport均为in-process，client_name为codex-tui | 当前原生服务使用进程内连接，不是猜测最近会话 |
| `codex app-server daemon version` | exit 1，默认control socket不存在 | 当前默认候选端点不可用；不等于所有端点/版本绝对不可能 |
| 原生 `/quit` | exit 0；本任务native/node及其三个已有工具子进程均不再可见 | 本任务进程已退出，未强杀共享服务 |

核心原始摘要：[信任和标记](macos-native-trust-and-marker.json)、[实际Hook文件逐字段副本](macos-native-hook-records.json)、[本任务会话元数据](macos-own-session-metadata.json)、[原生协议字段](macos-native-transport-fields.json)、[进程身份](macos-trusted-processes.json)、[正常退出](macos-trusted-session-exit.json)。原生私有数据只读查询限于本任务精确thread及其process_uuid，未复制完整历史或认证配置。

采集更正：最初即时读取为0个Hook文件；随后保存的marker快照已记为2个，主Agent未及时检查该字段，部分进度更新沿用了早期0值。退出后确认为3个。Hook故障假设已排除，没有为该假设修改已信任二进制、放宽输入校验或重复信任。

## 放行条目

| 项目 | 状态 | 判定 |
|---|---|---|
| C01/C12的正常信任与根会话Hook映射子项 | 已实测一个样本 | 三条记录与当前TUI身份一致，但不足以通过完整条目 |
| C01原服务外部接入、C05–C07附着正证据 | 受阻 | 当前in-process路径未得到可用外部端点；没有可靠附着证明 |
| C02/C09多会话、fork/resume/clear等 | 未验证 | 未因单个根会话样本推定通过 |
| C03–C11其余回调、关闭后执行、恢复、对账 | 未验证 | 未发送turn/start.toolOutput；没有提前进入后续功能 |
| C12未信任/禁用/策略/定义变化、C13实际桥接设置连续性 | 未验证 | 正常信任成功不覆盖这些情境 |
| Windows | 延期未验证 | 不阻挡本轮macOS工作，也不支持两平台声明 |

基础场景3次、故障屏障20次均未完成；当前核心接入前提缺失，未为凑次数重复付费标记或进行无意义故障注入。

## 原生daemon候选与边界

只读核验发现0.153.4原生daemon使用PID管理后端，命令包括 `app-server --listen unix://` 和独立的 `app-server daemon pid-update-loop`；帮助与已查官方资料没有给出足以确认的普通codex自动拉起/完全空闲退出条件。没有证据将它写成LaunchAgent或KeepAlive服务，也不能用thread卸载宽限期替代进程退出保证。[官方命令说明](https://learn.chatgpt.com/docs/developer-commands#codex-remote-control)

本机另有**任务前已存在**的managed安装/daemon状态：[有限字段观测](macos-managed-install-observation.json)。`packages/standalone/current`指向标注0.136.0的缺失目标；既有 `remoteControlEnabled=true`，设置时间为2026-06-02。未调用bootstrap/start，没有修复安装、修改这个设置或启动远程控制。不能把这些动作当作对当前已建立原会话的简单连接，也不能混入已审查的一次性Hook配置。

继续准入需要先找到明确、可验证且符合“直接codex入口、原服务、按需启动/空闲退出”的接入候选。当前不静默切换启动方式或接管旧daemon配置，不实施G0.2及G1–G4。

## 独立复核

本轮原GPT-6 Astra / max审查者已恢复可用。其独立检查确认实际3个Hook文件、会话/父进程匹配及已信任定义/二进制摘要不变，支持当前组合不放行；不要求重复marker来掩盖端点缺口。

发现P2记录问题N01：旧计划/当前环境记录仍把“等待用户信任”写成当前状态。已将旧状态标为历史并更新当前缺口；GPT-6 Astra / max对修订后的实际文件复核，确认N01关闭，新增有效问题0项，未解决复核问题为空。

主Agent最终判定：本轮证据记录复核完成，**G0.1受阻，G0未通过**。普通对话与Hook身份样本不满足外部原服务端点及TUI附着正证据的必要条件；不进入下一功能。

最新证据一致性检查见 [macos-native-final-checks.json](macos-native-final-checks.json)：原七文档SHA、可信二进制/定义摘要、精确Hook文件匹配、源码未变、文档链接及Git检查通过。源码本轮未修改，未重复无关工具测试。清理清单见 [macos-native-cleanup.json](macos-native-cleanup.json)，原始Hook事件和可复用工具保留。
