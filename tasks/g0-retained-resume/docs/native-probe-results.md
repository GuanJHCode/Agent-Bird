# 普通 native exec 前台退出探针

**无守卫的前台进程不能满足强杀后停止。** 普通 `/quit` 样本只提供有限生命周期事实，未取得合格的 10 秒停止采样。以下是 Go retained 实验前的独立前置探针，不是正式 retained 验收，也不代表完整 G0 通过。

两个样本均通过普通 `codex` 0.153.4 在已信任 test-repo 创建新 root，只增加终端 `TERM`，未改配置、Hook、权限或源码。没有 Goal、子 Agent、MCP 调用或自动恢复。原生 exec 启动单个前台 Python 进程，不 detach、不启动后代；进程记录真实 CODEX_THREAD_ID 与自身 PID/PPID/PGID，安装 TERM/HUP/INT 记录处理器，原定睡眠 90 秒。准备目录已预建为 0700。

| 样本 | 原生身份与固定模型窗口 | 观察与判定 |
|---|---|---|
| quit01 | root `01a08f04-99db-72b2-a0e4-31fce854ccff`；TUI PID 5026、出生 13:50:37 +0800；05:53:33–05:55:33 UTC | worker PID 6010 的直接父为 5026、自有 PGID 6010。05:55:01 正常 `/quit`，TUI 最终 exit 0；05:55:30 检查父与 worker 已消失，未留下 completed 或 TERM/HUP/INT 记录。停止观察器晚于 05:55:11 截止启动并拒绝，**不能计为 10 秒窗口通过**，也不能据缺少信号记录断言实际收到 SIGKILL。 |
| kill01 | root `01a08f0b-5109-7c82-85ea-1d9bbce890dd`；TUI PID 7238、出生 13:57:58 +0800；05:59:08–06:01:08 UTC | worker PID 8198、出生 13:59:16 +0800，直接父 7238、自有 PGID 8198。先启动观察器，复核 TUI 出生身份后 05:59:21.026297 仅向 PID 7238 发 SIGKILL，TUI exit 1。05:59:49 直接 ps 仍见原 worker，PPID 变为 1。它于 06:00:46.435843 再写 completed，即父死亡后约 **85.41 秒**仍执行；**FAIL**。 |

kill01 的初版观察器错误地把包含 PPID 的整个字典当作创建身份，因此 worker reparent 时曾输出 `both_original_processes_gone=true`。该记录已保留为 [失效原始记录](native-probe-kill-01-stopped-invalidated.json)，不得引用为通过。其原始字节 hash 与失效说明见 [invalidation](native-probe-kill-01-invalidation.json)。修复后身份比较只采用 PID 与 `ps` 的出生时间，父进程、进程组与可执行文件名属于可变化的观测字段；这个受控短窗口中的时间精度为秒，不声称通用无碰撞身份。

回归先以旧的整个字典比较复现 reparent 红灯，再改为创建身份比较，两项测试通过。没有重跑模型将该强杀失败改成成功。父 Agent 要求安全清理残留时，核对执行发生在 06:01:39，此时 worker 已自然完成并退出，实际未发送 SIGTERM；[清理记录](native-probe-kill-01-cleanup.json) 保留 `sigterm_sent=false`。两个本任务 TUI 与 worker 均已结束。

原生角色/工具操作元数据见 [quit01 thread](native-probe-quit-01-thread.json)、[kill01 thread](native-probe-kill-01-thread.json)，未保存推理或任意工具正文；PID/出生身份与原生环境绑定见相应 ready 文件。正常退出的时序失配见 [quit01 lifecycle](native-probe-quit-01-lifecycle.json)。

后续只在审查通过的 Go retained 工具上验证直接父生命周期守卫、持久步骤与明确用户续接。前台并不自动保证父进程强杀时子进程终止；该工具的单进程守卫也不能外推为任意第三方执行树保证。
