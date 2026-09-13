# 独立 helper 回执与只读历史适配

native-delivery-02（主树 source d9e86d3）当时记录 helper_admitted 两次、同 backend 第二连接、两次 synthetic HTTP 200；helper 的 turn/start id=2 有回应，但只有 thread/status/changed，没有 owner 的 turn/item lifecycle。controller 在 helper-proof 等到原 120 秒才开始 cleanup，现场 snapshot 的 stop/socket 状态为 unproven；root 后继 post-run-cleanup.json 才证明 kernel PID/socket 已消失。保留两份时间层次，不将后验事实回填现场快照，旧 case 不变。

这符合固定官方 0.154 commit `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`：本地归档 `tasks/g0-native-0154-contract/data/source/codex-rs/app-server/src/request_processors/turn_processor.rs:514` 至698 的 turn/start 仅返回 TurnStartResponse，687–698 给出 inProgress receipt；未为该连接 ensure_conversation_listener。`thread_processor.rs:1534` 在 thread/start 自动 attach owner listener，3958 在 resume attach；本夹具不会为 helper 调 resume 或新增订阅。`thread_processor.rs:866` 的 thread/read 与2824/3000–3055 的 loaded/persisted view 路径允许同服务直接读取 includeTurns 历史。故本改动是客户端契约适配，不称 native 通知丢失 bug。

helper 同一 adapter/claim 只调用一次 send_tool_output_once(wait_for_completion=False)。receipt 只提供本次 delivery_turn_id；没有 completed notification 不当失败，也不当完成。接着同连接、同原 threadId、includeTurns=true，以唯一递增请求ID（3..22）最多20次只读查询，共用一个10秒 history deadline，且不超过业务deadline。仅当原初始turn与本deliveryturn按顺序出现、均completed/无error、固定synthetic文本及exact envelope通过原adapter.audit_history，controller 才显式decide handled并交同adapter真实GoACK。若恰收到lifecycle，仍核验其turn/item identity；不要求通知集合完整。所有等待/次数耗尽都UNKNOWN、不重发、不新claim。

总窗口仍为启动前冻结的RAW120秒；业务截止提前到total_deadline-10（默认110秒），配置分开 business 与 cleanup deadline。status/claim/send/read/ACK均使用业务截止；owner close、helper EOF 和 owned stop 最多使用原total_deadline，不延长。finally记录cleanup开始/实际截止。先计算remaining再创建等待coroutine，过期不调用readline/wait/close factory，避免Process.wait未await的副作用。

实际dummy回归使用Python backend仅向owner发送lifecycle；helper收到receipt后，第一次thread/read返回inProgress，第二次返回completed，实际GoACK effect_count=1。另一真实dummy永远inProgress，用仅测试的6秒总窗/3秒reserve复现业务耗尽：无ACK，自有PID/socket在原总窗内结束。production常量保持120/10。另验证过期等待零coroutine/零signal。初始RED3项和GREEN3项分别保存，最终组合验证见后续追加证据。

未运行真实native/launchctl/账号模型；旧01具体admission reason仍UNKNOWN，不由02或dummy反推。所有profile/source pins、auth隔离门禁、Go capability/proof、原120上限与零自动重跑保持。

已串行继承 adapter `cfb3891` 与 `34a6b61`（本 WT 对应 `0f9d58b`、`6b8673e`），仅调用新参数，不修改 adapter。最后源码修改后旧新同进程 **71 passed in 24.53s**（新runner套件13项），无RuntimeWarning；原始RED3 failed、定向GREEN3 passed，日志分别保留。

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -p no:cacheprovider \
  tasks/g0-completion/tests/test_native_delivery_case.py \
  tasks/g0-completion/tests/test_delivery_adapter.py \
  tasks/g0-completion/tests/test_owner_helper.py --tb=short
```
