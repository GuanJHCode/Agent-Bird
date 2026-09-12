# 缓存切换样本 02：启动连接门禁 unknown

2026-09-13，实际基线 c177ac4，单次执行。首个 transport epoch 没有 RPC 即 EOF/close；随后同一双 peer 的第二个 epoch 成功 initialize、resume(P) 和 skills。driver 的“总连接记录必须恰好一条”门禁拒绝该启动序列。

本次 PTY inputs 为空，尚未执行 `/status`、`/side` 或切换键。因此它没有验证修正后的 side 创建入口，也没有得到 cached 反例。完整 trace 148 行、模型轮次 0；清理终止 TUI（exit -15），不能宣称正常 quit 通过。

worker 留存 posthash、双 peer 和端点收据，主会话重新核对实现/配置/Hook/种子摘要及 PID 67218、67244 已退出。原始证据位于 `../data/proxy-cached-02/` 和同目录 preflight/controller argv/post-run receipt；样本没有重跑、延长或修改。

后继将严格区分已关闭且无 RPC/upgrade 的前置连接与唯一当前活动 epoch；实验开始后的新连接、并发 live epoch、旧候选复用仍应拒绝。
