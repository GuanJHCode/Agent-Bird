# 原生重连样本 01 验收

2026-09-13：主会话与独立 reviewer 验收通过。运行源码基线为 72b74bc，实际文件摘要以同目录 preflight 为准。

- 普通 `codex resume 01a094db-aaaf-7dc3-8308-8077d4f778b3` 命中默认 UDS；只切断一次 TUI 端 transport，未修改启动命令。
- 旧 epoch 1 在 local sequence 141/142 收到双向 EOF，143 close；断开后旧 epoch RPC 为 0，没有复用旧 candidate。
- 新 epoch 2 独立完成双向升级、initialize（ID `initialize`）、同一 P 的 resume（ID 4）以及后继 skills（ID 7）的请求响应配对。独立 `/status` 精确匹配 P。
- 完整 trace 198 行；客户端和服务端 turn-start 均为 0，没有 prompt 或模型执行。正常 `/quit` 退出码 0，新 epoch 收到 EOF。
- 两个 epoch 的双 peer 身份一致。主会话另行确认 backend 58862、TUI 58888 已退出，两个 socket 不存在；源码、配置、Hook、19 个种子文件等保护摘要与运行前一致。
- 本地重连窗口保持 10 秒，未重跑、未延长。主树整合后的旧代理与恢复组合测试 170 passed。

原始证据：`../data/proxy-reconnect-01/result.json`、`trace.json`，以及同目录 `proxy-reconnect-01-preflight.json`、`proxy-reconnect-01-controller-argv.json`、`proxy-reconnect-01-post-run-receipt.json`。

该结果只覆盖此受控连接的断连、原任务重新附着与正常退出。它不证明部分任务续跑、缓存切换的通用识别、冷启动安装或完整 G0。
