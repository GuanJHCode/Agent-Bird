# 缓存切换样本 04 验收

2026-09-13：主会话和独立 reviewer 验收通过，仅表示受控轨迹和正常清理成立。源码基线 f7c9db2；实际文件、CLI、配置、Hook、种子摘要以本次 preflight 为准。

P=`01a094db-aaaf-7dc3-8308-8077d4f778b3`；新 S=`01a096c3-2bf9-76c3-b3c6-95046dc25648`。

- 空 `/side` 的 fork request 10（seq 145）和 response（146）配对，forkedFromId=P；thread/started(S) 与 inject request 11（152）/response（153）成立。
- 四次独立 fresh status 精确为 P、S、P、S；均保留唯一 query ID、原时间窗和 screen hash。原三段测量分类为 fork_rpc、refresh_rpc、other_rpc。
- cached P 有 thread/read(P) 与后台刷新；cached S 有 command/skills/plugin/account 等 RPC，但没有带 S 的选择事件。不是 zero-RPC，也不是自动识别当前显示成功，`generic_attachment_verified=false`。
- 测量后独立返回 primary 并 fresh status(P)，这段清理导航不计入原测量。随后正常 `/quit` exit 0，terminal 0→3（双向 EOF/close）。
- trace 完整 201 行，model turns 0；已关闭空探活 epoch 与唯一 live anchor 的双 peer 验证成立。主会话重新核对保护摘要、PID 75458/75483 已退出、两个 socket 不存在；旧 01–03 未改写。
- 主树全部相关测试同进程 233 passed；最后一次源码修改后执行，后续只有事实文档与临时文件清理。

原始证据：`../data/proxy-cached-04/`，同目录 preflight/controller argv/post-run receipt。

当前显示自动路由仍按 `routing-decision.md` 暂停等待用户决策；用户级注册未执行，完整 G0 及 G1–G4 不能宣称完成。
