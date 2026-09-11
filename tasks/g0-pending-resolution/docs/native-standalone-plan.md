# 固定 TUI 的 standalone toolOutput 资格样本

状态：仅完成本地采集器与运行准备；没有启动 listener、TUI、模型或业务 job。仅注册 `sd-qual-01`，不把该准备记为原生通过。

## 范围

在预建的本任务原生 listener 上，以普通 `codex` 打开一个新 root。控制器独占该 PTY，固定显示该 root，直至业务完成与观察结束。验证主控已有 turn 确实 completed 且 idle 后，客户端实际执行的合成结果经一次 `turn/start.toolOutput` 引发新 turn，并以真实 `functionCallOutput` 写入同一原生 thread。

这不是通用当前界面附着证明，也不验证服务自动启动、切换后资格、故障二十次或完整 G0。不开 Goal、不改源码/配置/Hook/认证/权限，不添加 MCP 派发或回调，不通过用户输入发送外部执行结果。[本轮合同](../plans/contract.md)

## 固定输入

| 项目 | 值 |
|---|---|
| case/nonce | `sd-qual-01` |
| 初始 final | `SD_QUAL_01_READY` |
| 自动续轮 final | `SD_QUAL_01_ACK` |
| native CLI | `0.153.4`，SHA `b973d440acac501fd2594a43e7ca9ce41e0a65b9dfb28d0d7a7837c99e1261e3` |
| 业务二进制 | `tasks/g0-retained-resume/data/g0-return-lab-retained-darwin-arm64`，SHA `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285` |
| 原生 cwd | 主仓库 `tasks/g0-macos/data/test-repo`，沿用既有信任 |
| 业务延迟 | `40s`，仅此 job，初始 root 用真实 `$CODEX_THREAD_ID` owned start |
| helper | 独立实现/审查的 `prepare / send-once / observe`；固定接口和 SHA 到达后绑定 |

控制器 case 固定为主任务 `tasks/g0-pending-resolution/data/sd-qual-01`；job 固定为专用测试仓库的 `tasks/pending-resolution/data/sd-qual-01`。两者分开、仅不存在时创建为 0700；case 只放本次白名单证据、binding 和 helper durable intent，不引用将被回收的 worktree 数据目录。

## 身份与连续性

1. 启动前确认默认 socket 不存在；存在则停在准备阶段，不替换、删除或探测启动未知服务。
2. 原生服务 argv 仅 `codex app-server --listen unix://`；TUI argv 仅 `codex`。服务以普通 subprocess 启动、stdio 为 devnull，避免控制器关闭终端时对可能出现的其他客户端产生隐式 SIGHUP；普通 TUI 由 PTY 自动化设置 `TERM` 并调用可执行文件，不是产品替代启动器。控制器直接持有新进程的 PID 与出生身份。
3. 记录服务和 TUI 的 PID、lstart、uid、comm，确认实际 native 可执行文件路径与固定 SHA；记录 socket dev/ino/uid/0600 及服务持有路径。PID 身份不含 PPID，PPID 单独用于观察。
4. `/status` 提供当前受控 root UUID；真实同根 `UserPromptSubmit` Hook 提供 prior turn ID，Hook 的 PPID 必须是同一服务。helper 用精确 root/prior turn 查询，不以 cwd/latest 猜测。服务 `diagnostics.process.id`、原生 `codex-tui` connection 记录和实际 owned job 的 controller ID 交叉核对。
5. 当前显示证据来自本次受控 PTY 的输入动作记录：身份建立后没有导航、新线程、恢复或其他用户输入。日志/Hook/loaded 本身均不冒充自动附着资格。
6. 采集 config、登录 shell 配置、专用 Hook 定义及可信二进制的存在性/SHA，前后比较；不导出原文或认证数据。启动和回调请求都不带模型/审批/权限/配置覆盖。该记录只证明本次连续性，不放行完整 C12/C13。

## 连续执行与窗口

1. 完成身份准备，发送一次已授权的真实合成任务初始输入。以这次实际提交时间为初始模型 120 秒窗口起点。
2. root 实际执行一次 owned `start`，随后 final `SD_QUAL_01_READY`。控制器执行 `Go read pending → 精确 root/Hook turn 的 native task_complete → Go read pending`，记录两个真实命令输出和该有类型事件的时间/身份；不靠 UI 回显的常量推断。helper 另核 native prior turn completed、工具已结束及 idle，单见 final 字符串不足。
3. 控制器保持显示且不输入，等待同 nonce job 的真实结果；helper `prepare` 实际 read/wait/inspect，验证 count=1、owner/revision/event hash、尚未 ACK，写 durable 单事件 intent。
4. helper `send-once` 在验证同服务、remote disabled、native cli source、精确 prior completed/READY/idle 后，先用 O_EXCL 记录 send-attempt，再唯一发送原生 standalone toolOutput。从原 attempt 时间固定新的最多 120 秒模型观察窗口，任何补查仍使用该原窗口。
5. root 实际 inspect → ack，使用原生 `$CODEX_THREAD_ID`、revision=1、inspect 返回的 event hash；`ack-r1.json` 的 decision/effect_count=1 独立证明业务动作。native history 的角色/类型及 body hash 单独证明交付，不把 RPC success 当落盘或业务 ACK。
6. 没有符合条件的 completed/idle、发生审批请求、未知来源、未知客户端或超窗时保留未通过事实；不代答权限、不延长或换起点凑通过、不重派 job、不自动重发 uncertain 事件。
7. helper exit0 已要求历史中的 exact functionCallOutput、匹配新轮 completed/ACK final、真实业务 ACK 且 idle；随后才正常 `/quit`。只根据已采集的白名单 connection 记录作有限收尾判断：出现未识别 client、第二个 TUI connection 或证据不足时保留 headless listener 给主会话核对，不用猜测关闭服务；该判断不是通用客户端管理。失败时先确保单一 job 完成；只在服务归属证据足够时停止本任务服务并退出自己的 TUI。记录所有保留 PID、退出、业务最终状态、socket 与配置比对。

所有本地身份/采集操作窗口至多 10 秒。两个模型阶段各有自己的实际开始时间和 120 秒上限；准备空档不能改变已开始阶段的截止。

## 采集器

[native_sd_collect.py](../scripts/native_sd_collect.py) 只采集固定 case 的本地进程、socket、有限 Hook 与 native 协议字段；不执行 RPC、不读取模型 history、不落 raw UI/log/配置。`/status` 仅支持独立 Session 状态行；[PTY 采集](../scripts/native_sd_pty.py) 在唯一初始提示之前发出 `/status`，清除旧缓冲并记录命令之后的新字节区间，发送初始提示即关闭该解析窗口。嵌在 model transcript 等文本中的标签不能作为状态行。metadata 查询要求 exact root、source=cli、固定专用 cwd、固定 CLI 版本。Hook 要求 exact root、服务 PPID 和原始时窗，其他字段严格丢弃。

9dc 初版的时窗长度检查正确，但过滤时退回浮点秒，独立审查发现会接受截止后 1ns 的 Hook/log。新增两条红例分别通过真实 Hook 筛选及临时真实 SQLite 查询复现；修后窗口、Hook 时间、mtime 粗筛、日志逐行筛选都用整数 epoch 纳秒，SQL 只负责粗筛秒范围。旧结论不据此倒推为正确。

原生 snapshot 现在拒绝缺失的服务/TUI；控制器的发送前守卫额外比对两者的原创建身份和 socket dev/ino/uid/mode/服务持有路径。身份键不含 PPID，reparent 不冒充进程退出。输入/身份/隐私回归共 14 项新鲜通过，包含上述独立审查的 RED→GREEN。当前 CLI preflight 实际只读执行成功，默认 socket absent。原生模型运行仍等待 helper 与完整 controller 冻结复审。

[固定运行器](../scripts/native_sd_control.py) 已补全串行路径；最新本地 suite 为 17 项，均通过，未启动 Codex。运行器只接受固定路径 helper 和 import 的审查 SHA，`send-once` 外层先用 10 秒本地预检期限，读到该 helper 的同次 immutable `send-attempt.mono_ns` 后使用 `mono_ns + 120s` 原模型截止，任何补读不会改起点。命令为 `python3 tasks/g0-pending-resolution/scripts/native_sd_control.py --helper-sha REVIEWED_SHA --audit-sha REVIEWED_IMPORT_SHA`；真实运行必须等固定代码独立审查通过及最终 SHA。
