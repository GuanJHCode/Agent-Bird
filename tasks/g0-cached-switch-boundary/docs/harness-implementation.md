# Cached side probe harness (offline implementation)

该实现只完成离线准入与记录逻辑，未启动 native、RPC 或模型；真实执行仍由主会话预检后单独授权。

目标路径为已知自有 P 的普通 `codex resume P` → 一次空参数 `/side` 建立 S → 第一次 `0x1f` 回缓存 P → 第二次 `0x1f` 回缓存 S。每步独立 `/status` 只做显示核对，不写入 inferer，不新建或恢复 candidate。`status=observed` 仅表示受控轨迹记录完整；`generic_attachment_verified=false` 恒定保留，存活旧 candidate 标记 stale，不能解释为当前 attachment。

复用 `resume_case` 的独占 case/preflight、冻结 binary/source/protected hash、owned runtime 与正常退出校验。配置必须明确为 `gpt-5.6-luna`，本 runner 不改配置、不额外传 model/remote/prompt 参数。继承 `4cec3e9` 的 silent-resume 修复，`rpc_unknown` 中的 server `turn/started` 同样拒绝。

唯一旧 runtime 改动在 `_CaptureBridge._record_trace`：复用已有 `threadId` 标量投影，将 `thread/fork` 与 `thread/inject_items` 加入允许 method。Observer 和 inferer 没有修改；测试经真实 WebSocket parser/Observer/bridge，验证 params/result/items/private canary 不落盘。

首次 `/side` 和每次 cached toggle 前必须 `ui_state=session`、同一自有 live epoch、完整 trace、零模型 turn。首次 fork 必须具有唯一成功 request/response、请求目标 P、响应 S≠P、`forkedFromId=P` 与同 epoch `thread/started(S)`；实际存在的 inject 必须指向 S 且响应成功。首次信息不足时记 unknown，在第一步记录后正常 quit，不进行另两次 toggle。首次 `/side` 在同一个冻结 local deadline 内持续 drain，直到 fork、started 与已经观察到的 inject 配对齐全才发独立 status；首次证据未齐全时不发 status。缓存 toggle 则等到 toggle 后出现新的 PTY 输出并短暂稳定，再查 status；渲染只控制查询时机，目标 ID 仍由 status 独立核对，candidate 不变。S 始终只取 wire 字段。后续真实 RPC 如实分类，zero RPC 不作为强制成功条件。

独立 local window 不超过 10 秒，整体 deadline 不超过原 120 秒。status 使用同一次 fresh capture，收到完整 ID 即结束；不重置 capture，不为等待耗满预算或放宽 deadline。异常或 gap/model gate 失败时仍先保存白名单 trace，结论保持 unknown。

键位来源为固定 Codex `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`、Cargo patch crossterm `45fecb9508105988f42fe6ff0441783ed3717f92`。`data/source/crossterm-source-receipt.json` 引用的 12 个文件已逐项核验 bytes/SHA 并原样纳入；parser `src/event/sys/unix/parse.rs:161-164` 将 0x1f 解为 Ctrl-7，固定 Codex `app/input.rs:485-490` 默认 Ctrl-/ 兼容分支触发 side toggle。当前 PTY 仅回应 cursor-position，无 keyboard-enhancement 回应。其他已归档固定 source 的路径沿用主合同；`reconnect-boundary.md` 是独立边界说明，不改变本 runner 的单 epoch 条件。

验证：初版 13 项 RED→GREEN；失败 trace 落盘两例 RED 后修复；review/UI/lineage/started/local-window 回归 6 failed、18 passed 后变为 24 passed。最后一次组合在单个 pytest 进程执行：

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q tasks/g0-tui-proxy/tests tasks/g0-proxy-continuation/tests tasks/g0-cached-switch-boundary/tests --basetemp <this-task>/tmp/pytest-combined -p no:cacheprovider
```

结果 `186 passed in 1.73s`，其中旧套件 162、新套件 24。具体 RED/GREEN 和组合输出保留在同目录日志。所有 fixture/pytest 临时文件均只在本任务目录；已删除执行后不再需要的临时树。

后继异步等待修复：`async-wait-red.log` 复现 fork 延迟 0.6 秒、缓存渲染延迟 0.6 秒时提前查旧屏幕，以及超出原 10 秒后仍查询的问题（4 failed / 23 passed）。修复后新套件 `27 passed in 0.15s`；每步保留 started/deadline/completed RAW，等待循环不重新计算 deadline。此代码先固定供 review，完整组合测试随后针对该固定代码执行。

固定代码 `214233a7febb2e1b74d62f80fc1c40a584563763` 的最新旧/新同进程组合结果：`189 passed in 1.63s`（旧 162、新 27），命令同上，日志为 `async-combined-tests.log`。代码未在验证后变动。

首次入口更正的固定源码、配置与 termios 证据见 `side-entry-diagnostic.md`。旧 cached01 原案不变。将测试替身改为固定源码的“无 side 时 toggle no-op”后，旧实现出现 `12 failed / 16 passed`；最小入口修复后新套件 `28 passed in 0.14s`。

本次首次入口修复后的新鲜同进程组合：旧 162 + cached 28，`190 passed in 1.61s`，见 `side-entry-combined-tests.log`。随后仅归档来源/文档，未再改源码。

启动探活边界修复：以既有 inferer 的 P candidate 冻结 live anchor，允许同 owned 双 peer、无协议且已在 anchor open 前双 EOF/close 的启动探活前缀。首 status 后不允许新连接/epoch，不复用旧证据。详见 `startup-anchor-boundary.md`；实案 safe projection 与负例 TDD 由 `9 failed / 28 passed` 转为通过，最终旧 162 + cached 38 在同进程 `200 passed in 1.64s`，日志 `startup-anchor-combined-tests.log`。
