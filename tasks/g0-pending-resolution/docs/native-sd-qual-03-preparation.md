# sd-qual-03：单次 initial 的后验绑定

本变更对应主会话在 `2bdd04e` 前瞻登记的第三个独立 case。运行常量、目录、marker 与 command ID 只接受 sd03；旧 sd02 与 native-command-input-01 的原字节不修改或重跑。尚未启动 sd03 服务、TUI 或模型。

controller 跳过 `/status`，先以实际进程、出生身份、实读 executable_path、socket 和独占 PTY 封存 `native-origin.json`。在原 TUI 10 秒预算内泵读至启动满 1 秒，仅作为调度，明确 `input_ready_proven=false`。从首次 initial 输入前开始原 RAW 120 秒，只发一次固定 paste 和独立 Enter；未被接受不补发。

`discover_prompt_hook` 只在同 service、原时间窗口寻找唯一真实 UserPromptSubmit。没有合格 root/turn 则继续原窗内观察；多个候选拒绝。Hook 通常先于 job.json，故暂缺 job 仅等待。`read_job_owner` 严格检查固定普通 0600 文件、精确 JSON schema、nonce、revision、controller_thread 与 Hook root 一致；不导出任意 job 字段。只有两者匹配才保存 root 与 owner，候选或 job 记录后来变化会拒绝。

typed READY、真实 pending→精确 READY 重读→pending 屏障和 completed read 保留。SQLite metadata 校验同时执行 `thread_spawn_edges WHERE child_thread_id=root`，查询必须成功且零行；缺表、查询失败或任何父边都不能当作无 parent。首次 helper 前重核 Hook、owner、当前进程/socket，保留观察到的唯一 codex-tui connection；额外、未知或无法分类的 client span 拒绝，不声称日志是完整连接清单。后续 prepare/send 的 task-owned helper 连接明确登记。

helper 在已有原服务 full read 上负责显式 parentThreadId/forkedFromId=null、ephemeral=false 与 source/cwd/version，以及首次原生 sessionId 的持久和后续一致性；caller binding 不自报 sessionId，也不假定它等于 root。没有新增 metadata RPC。

初始 Hook 与 owner 未绑定时记录 `initial_binding_inconclusive`，不从等待时间推定已接收。只有原生当前轮已确认终态才尝试正常 quit：初始 READY 后尚未发送 callback，或 helper 已确认 callback/ACK 终态。其他情况不追加 quit 输入，记录由已授权的 owned 外层收尾处理；安全清理不补算正常退出。

六项新路径先 RED，再覆盖单次直接 initial、sd03 唯一注册、唯一 Hook、先 Hook 后 job、owner/nonce/revision、SQLite 精确子边、额外 client 等条件。原 Hook 过滤已拒绝缺 turn_id，测试保留其不能成为候选的行为。最新 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v` 输出 `Ran 33 tests in 0.756s / OK`。这些是本地实现证据，原生准入仍须独立复核及主会话另行下发。
