# 缓存切换样本 03：轨迹已观察，整体 unknown

2026-09-13，固定源码基线 8ede436，单次执行。P 为 `01a094db-aaaf-7dc3-8308-8077d4f778b3`，新 S 为 `01a096b7-d459-72b3-b4f9-d6e77b15cbb3`。

- 首次空 `/side`：fork request 10（seq 143）与 response（144）精确配对，S 的 forkedFromId=P；started（145）、inject request 11（150）与 response（151）成立。
- 四次独立 `/status` 为 P、S、P、S；每次均有独立 ID、时间窗、捕获字节数和摘要。全程模型轮次 0，trace 完整 195 行。
- cached P 段含 thread/read(P) 与后台刷新，分类 refresh_rpc。cached S 段含 command/skills/plugin/account 等流量，分类 other_rpc。两者均非 zero-RPC；cached S 没有带 S 的选择事件，不能建立当前显示的 attachment candidate。
- 最后仍在 side S 时发送 `/quit`，原 10 秒窗口内终止事件为 0→0；随后自有清理终止 TUI（exit -15）。整体保持 unknown，不能称正常退出通过。
- posthash 与 preflight 全部一致；主会话另行重新计算保护摘要并确认 PID 72892、72917 已退出。原始 case 未重跑、延长或改写。

原始证据：`../data/proxy-cached-03/`、同目录 preflight/controller argv/post-run receipt。独立 reviewer 已确认测量阶段的 P/S 路径及选择身份缺口。后继只补单独返回 primary 的清理动作，再验证正常退出；它不计入原三段测量。

回传契约仍待决策，见 `routing-decision.md`。本样本不证明通用 attachment、立即停止、用户级冷启动或完整 G0。
