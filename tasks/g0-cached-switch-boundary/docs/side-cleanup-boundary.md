# Side 测量结束后的退出边界

固定 Codex `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 的 `slash_command.rs:191-206` 中，`available_in_side_conversation()` 不包含 Quit/Exit。`chatwidget/slash_dispatch.rs:149-168` 先调用 side allow 检查；拒绝路径 `:1264-1274` 直接返回，因此不会走到 `:425-427` 的 `request_quit_without_confirmation()`。必要源正文与 bytes/SHA/git-blob 收据在 `data/source/side-quit-source-receipt.json`。

cached03 真实 P/S/P/S 与四次 status 已完成，但最后仍在 S，`/quit` 未产生 EOF，原 10 秒后 owned cleanup TERM。因此原整体 unknown、cached-primary=refresh_rpc、cached-side=other_rpc 均保持不变。该事实不授权重跑03。

后继 runner 只在原三段测量和四次 status 完整匹配后，增加一个单独的 `cleanup_return_primary`：

- 原 `switches` 长度仍为 3，测量 `status_checks` 长度仍为 4。
- 一次额外 `0x1f` 的 action 为 `cleanup-return-primary`；使用同一 frozen anchor/完整 trace/model=0 门禁。
- 返回渲染与 fresh status(P) 共用一个启动时固定的 10 秒 local deadline，status 不重置预算。未确认原 P 时不能发 `/quit`。
- 此导航的 RPC、trace 起止、status/hash 与 `parent_confirmed` 独立记录，`part_of_measurement=false`。即便它产生 refresh RPC，也不改写原三段观测；完整 wire 仍保留该事实。
- 成功回 P 后才发原 `/quit`，继续要求原退出窗内新增 EOF、exit 0 与 owned cleanup。整体原 120 秒不扩大。

测试替身现在真实模拟 side 内 Quit/Exit 被拒绝。旧代码出现 `8 failed / 33 passed`；修复后 cached 41 项通过，包含额外清理 RPC 不污染测量、错 P 或超窗禁止 quit，以及原 model/EOF/非零退出回归。只在独立 worktree 改新 runner/测试/合同/来源资料；main 与 cached01/02/03 不写。cached04 须待主会话完成固定提交 review 后创建新的 preflight，尚未运行。

最新验证：`203 passed in 1.65s`（旧 162 + cached 41，单个 pytest 进程，`python3 -B`/no cache）。验证后源码未再变动，own pytest 临时目录已清理。
