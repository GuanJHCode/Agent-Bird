# cached02 启动探活与冻结 anchor

cached02 的失败发生在任何输入之前：同一 owned TUI/backend 双 peer 先建立 epoch1，只留下 open、双 EOF、close；epoch2 才完成 WebSocket、initialize、resume(P) 与 skills。旧 `len(connection_records)==1` 把已关闭的探活也计入，触发 runtime invariant。原 cached02 result/trace/receipt 不修改，不能宣称已经测试过 `/side`。

固定 Codex `6b9826e3aa83b1a5947db50f4332cb9c65f1b340` 证据：

- `tui/src/startup_orchestration.rs:193-199` 在普通 implicit LocalDaemon 入口调用 default socket probe。
- `tui/src/lib.rs:459-485` 仅在 50ms timeout 中执行 `UnixStream::connect`，成功返回 socket path；`_stream` 没有读写即 drop。对应 `:2773-2816` 测试只 bind listener、不 accept，也可成功。
- `tui/src/app_server_connection.rs:17-46` 与 `app-server-client/src/remote.rs:785-810` 是后续正式连接路径，connect 后才建立 WebSocket。

以上 4 个源正文均复用已有固定档案，SHA/bytes 记录在 `data/source/startup-probe-source-receipt.json`。未启动任何 native/RPC 或新模型。

最小规则集中在新 runner 的 `freeze_startup_anchor` 与 `_live`，旧 runtime/Observer/inferer 不修改：

1. current candidate 仍只由既有 inferer 产生，并必须指向原持久 P。此时冻结该 epoch。
2. 所有 connection record 必须分别匹配本次 `OwnedProcess` 的 pid/birth/executable 与 complete 双 peer；epoch 不重复、无 identity error，当前 anchor 唯一 live。
3. 较早 epoch 的事件只能为单 open、双向 EOF、单 close，完整结束在正式 anchor open 之前；禁止 RPC、upgrade、control、gap 或其他事件。关闭状态必须已记录。较早候选或协议证据不允许进入该例外。
4. 冻结记录 metadata SHA、已有 trace 前缀 SHA/count，以及仅允许的 closed probe epochs。首 status 及后续每段都复核；任何新增/替换连接、旧前缀变更、旧 epoch 新事件、anchor EOF，均停止。不会重新选择 anchor。
5. 原测量仍只有空 `/side` 和两个 cached Ctrl-7 输入，所有 10/120 秒窗口不变；测量后的独立清理返回见 `side-cleanup-boundary.md`。unknown 不重发，也不延长窗口。

`tests/fixtures/cached02-startup-projection.json` 只保存原案的双 peer、startup open/EOF/close/下一 open，附原 result/trace SHA；没有 params、result body 或历史内容。离线测试用独立 fake OwnedProcess 匹配这些元数据，并经真实 Observer/bridge 的双 epoch synthetic wire 验证新 epoch 才能创建 candidate。

回归涵盖真实无协议旧连接允许、旧 RPC/upgrade/gap 拒绝、缺 close、任一 peer 不属于本次进程、并发 live 连接、完整首创建/缓存过程、首 status 后即便新连接马上关闭也拒绝。所有数据写入只在独立 worktree；main 和旧 cached02 不写。

验证：`PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q tasks/g0-tui-proxy/tests tasks/g0-proxy-continuation/tests tasks/g0-cached-switch-boundary/tests --basetemp <this-task>/tmp/pytest-anchor-combined -p no:cacheprovider` → `200 passed in 1.64s`。代码在该验证后不再改动。
