# sd02 持久历史：新私有服务只读诊断

`thread/read(includeTurns:true)` 对已真实执行并持久化的 sd02 root 返回完整首次轮次。本诊断使用另一新建私有服务，不证明原 TUI 的原服务可达、当前显示资格或 idle 自动回传。

2026-09-12T01:23:06Z，按主会话单独授权，在新建 0700 `/tmp/g0-sd02-history-80lmh58p` 内启动唯一普通 `codex app-server --listen unix:///tmp/g0-sd02-history-80lmh58p/s.sock`。native SHA 与 sd02 相同；PID 79293 的出生身份、可执行文件匹配和 0600 socket inode 独立核对，RPC `server/diagnostics.process.id` 也为 79293，remote control disabled。没有新 TUI、thread/start、turn/start、resume 或模型请求。

同一连接使用 helper 原 `g0-native-standalone` / `experimentalApi:true` initialize 设置，然后只读精确 root `01a0932d-1998-7221-8897-5822328be877`。首次 full read 即成功，整个 RPC 阶段 RAW 30,998,792 ns，小于原 10 秒预算，未继续 metadata-only 或 turns/list。

返回 thread 为 source `vscode`、historyMode `paginated`、ephemeral=false、status=`notLoaded`。唯一 turn 为原 `01a0932d-1b16-71c3-b4ef-5982a8510db8`，status=`completed`、itemsView=`full`。受限 item 投影为：

- 一条 userMessage。
- 一条 commentary agentMessage。
- 一条 commandExecution，status=`completed`、exitCode=0。
- 一条 final_answer agentMessage，正文与固定 `SD_QUAL_02_READY` 精确匹配，仅保存匹配布尔值。

这排除了“该固定二进制对所有持久 thread 都不支持 full read”的推断。sd01 的早期 `-32601` 仍保留为该次具体上下文的事实；未据此断言其唯一原因，也未绕过 sd02 的本地 binding 拒绝。当前 `notLoaded` 说明本诊断未把旧 root 恢复成活动主控。

[受限原始投影](native-sd-qual-02-history-diagnostic.json)仅包含已知类型、ID、状态、phase、固定 READY 布尔及请求参数，不保存任意历史正文。关闭自己的唯一探针后，复核同一服务出生身份/socket，向 79293 发 SIGINT；55,100,666 ns 内 exit 0，私有 socket 与空诊断目录已移除。未改 sd02 原 case/job 或 binding。
