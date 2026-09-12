# 原生恢复样本 02 验收

2026-09-13：主会话与独立 reviewer 验收通过，仅覆盖受控恢复、静默与正常退出。

- 源码基线 c3cf984；实际源码（含保留的既有 export 差异）、CLI、配置与 Hook 摘要以 `proxy-resume-02-preflight.json` 为准。
- 普通入口执行 `codex resume 01a094db-aaaf-7dc3-8308-8077d4f778b3`。新的 resume request/response ID 5 与后继 skills ID 9 配对；观察候选与独立 `/status` 一致。
- 完整 trace 152 个事件；客户端 `turn/start`、服务端 `turn/started` 和模型轮次均为 0。没有发送 prompt 或 continue。
- 实际 `/quit` 前没有终止事件，之后收到双向 EOF 与 close；TUI 退出码 0。主会话另行核实自有 PID 46760、46775 已退出，两个 socket 均不存在。
- 前后保护摘要一致；主会话重新计算了 25 个保护文件、4 个实现文件与 CLI 摘要。19 个种子任务文件包含在保护范围内，原失败样本 01 保留不变。

原始结果：`../data/proxy-resume-02/result.json`、`trace.json`；执行参数与清理收据：同目录 `proxy-resume-02-controller-argv.json`、`proxy-resume-02-post-run-receipt.json`。

最终代码已在旧代理和新增恢复测试同一进程验证：161 passed。该样本未证明部分任务在明确继续后续跑、缓存 side/primary 切换识别、用户级冷启动或完整 G0；这些仍是待处理项。用户级启动注册尚未授权，不执行安装。
