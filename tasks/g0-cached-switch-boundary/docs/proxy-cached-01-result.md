# 缓存切换样本 01：unknown

2026-09-13，源码基线 b5c1665（实际启动 HEAD b130df5 仅追加重连验收文档）。一次固定字节 `1f` 输入后，原本地 10 秒窗口内没有出现 `thread/fork`、side `thread/started` 或 `thread/inject_items`。因此结果保留 `unknown / first_side_local_deadline`，没有发后两个 toggle，也未获得 cached P↔S 反例。

初始 resume 与独立 `/status` 匹配持久 P；完整 trace、模型轮次 0。正常 `/quit` 退出码 0 后出现 EOF，worker 留存 socket 清理与保护 posthash；主会话重新核对实现/配置/Hook/种子摘要及 PID 62075、62100 已退出。样本没有重跑，没有延长窗口。

证据为 `../data/proxy-cached-01/result.json`、`trace.json` 与同目录 preflight、controller argv、post-run receipt。该负结果只表示这次输入没有产生首次 fork 证据，不能解释成缓存切换零 RPC，也不能据此断言快捷键、feature gate 或源码机制的根因。

后继先核查固定版本的实际按键入口条件；若采用空 `/side` 替代首次创建，必须另建样本，后续 cached toggle 仍需单独证据。不会覆盖或修改本次结果。
