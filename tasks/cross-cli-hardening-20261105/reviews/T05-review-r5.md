# T05 r5 独立复审：PASS

范围：T05实现、protocol-fixture与macOS OS-native-local synthetic服务/UDS恢复验收。此结论不代表真实provider模型、父CLI/TUI主动唤醒或整个项目集成验收。

审查者 `/root/review_t05`，实现者为另一agent，r3阻塞由root接管闭环。本轮只读审查，无源码修改、派生、真实模型、提交/push/全局安装。实际冻结worktree `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05`，HEAD `428a5877d63247d0dd0b345b0765aa3c912507c2`。源码12文件在独立测试前后均匹配manifest；SHA256 `ac10bb1dadeec8da9cb7a60a18b6acab60d6a2bf43d1afc520a01f593ac0b7d2`。实际diff SHA256 `272d520293e838cf03e1d48ccbe9dd5e57769af2f5cc206a287c36942c965572`。

## 已关闭问题与依据

1. 原P1部分frame超时后复用失步连接已关闭。再次运行原独审反例得到 `_closed=True`、`helper_closed`；header/payload/fragment×timeout/cancel六场景使用新UDS连接history-only核对，不重发，取消传播。原日志保留，r5新输出见data/original-half-frame.log。
2. 原r3 BLOCKED的ownerready启动失败已通过因果证据闭环，非重跑掩盖。实际catch-only trace保留于 `root-startup-fix/socket-cause2/failures/failure-118tahx7/root/backend-error.log`，明确activation_service.py846因socket identity变化拒绝。旧fixture先start_unix_server(path)、await返回后chmod的窗口可确定性观测0755而非0600；旧umask022受控测试红，修后022/077在server返回前已0600。实际原失败未抓取完整expected/observed tuple，不把推断值冒充采样值。新raw socket在bind期间用umask0177并恢复umask，没有await期间全局umask改变；生产activation/proxy guards均无修改。
3. fixture旧HTTP upgrade EOF recv循环会无限spin；真实UDS拒绝upgrade的红测试TimeoutError与修后快速EOFError闭环。没有延长deadline/skip。
4. owner退出与historyEOF竞态已关闭。私有repinned sending_before_io_detach在真实SIGKILL并同owner恢复后，于history处确定等待attachment失效再抛EOF；旧版红为awaiting_history而非owner_detached，修后异常投影前重核attachment。新独立入口测试通过，owner退出后turn/start仍0、无ACK request、ledger仍delivery_uncertain；不是把退出当业务成功。

## 生产合同复核

- connect/drain/read/send/matching-response/history/close各阶段有命名deadline，通知不无限续命；packet数量/字节、pending RPC ID与fragment限额保留。
- durable sending之后仅原ID/envelope/hash/thread/epoch/owner核史；不能重发、换ID、autoACK；严格完整history proof未放宽。正常busy保留awaiting_history；monotonic deadline不跨进程持久化。
- 同owner恢复经完整attachment（只允许helper grant变化）与原control路径/hash、task绑定核验，真实status授权有效后走delivery_recovered；不Submit、不业务rebind、不变attempt/budget。新owner不能使用旧batch proof；旧ledger状态仍兼容解析。
- kernel flock覆盖整个driver生命周期；私有lockfile权限/属主/nlink检查与真实SIGKILL后释放已测试。生产Go launcher并发竞争明确delivery_driver_busy，无新增业务记录。
- proof/ACK crash分别恢复：ACK已成功但ledger未记录重放原durable command；controller_acked但driver投影未记录只补receipt/status。生产Go launcher/run_driver与helper真实服务/UDS的切点测试，区别于只调用observe_delivery。注入仅限私有重新pin的测试副本。
- Go启动消费者与native_product_case均接受awaiting_history，bootstrap接受delivery_recovered。取消传播和有界cleanup保留。

## 实际验证

独立命令及完整环境：verification.json；已结束exit0，无活动测试会话。

- r5最终Python报告实际解析：147 tests，0 failures/errors/skips，JUnit 66.069秒；进程exit0。XML SHA256 `7b51334d7ab4df790ad40e7474eba8aff381e1464944808be27bf0824636c2f3`，log SHA256 `f2bbe6b1145c796edc6dd92aa9f1bbb8f5c50acdf3be2b955931920096f6ae8d`。
- 本审查独立focused：39 tests，0 failures/errors/skips，38.664秒，exit0。包含完整更新deadline套件、新detach、driver ack_success/controller_acked、helper ack_success/controller_acked，以及三个真实Go入口control路径/hash错绑、并发driver负例。XML SHA256 `b64a9dfca7ee476f55696bb25e338eb412f4604f2a9cec049ee2eabfb1517702`；log SHA256 `b244a824aff2298721231287428253cec7a6c8832c0481ae7cd4177be5478628`。
- 身份负例经生产Go launcher明确拒绝，原worker到awaiting_owner_decision稳定后SQLite runs/attempts/budget快照不变。半frame原反例独立再次核对，无guard放宽。
- 原Go race 10 test pass（含subtest）/0fail/skip、vet exit0有效证据复用：对应Go文件r1之后未变，本轮不机械重复。未执行项目全量集成回归，留给主会话。

data/evidence-index.json含精确hash/计数索引。原r1/r2/r3/r4失败与诊断全部保留；本轮只在review-t05-r5新增必要报告/证据，无无用临时文件需清理。当前没有新增confirmed finding或未闭环T05阻塞。

结论：T05范围独立审查PASS，交由root核验与最终签收；不自行标整个项目完成，不扩展为live/TUI能力声明。

## 主会话集成签收

12个集成文件与独审manifest逐个一致，git diff --check通过。主会话复核EOF/权限竞态根因与生产不变guard、同owner恢复和异常投影代码；用包含T01/T02/T03/T07/T10的当前集成树重新编译orchestrator，运行全部真实driver/helper恢复19用例，0失败/错误/跳过，完整进程退出0。binary SHA与命令/计数见data/public-evidence/T05-integrated.json。T05实现范围签收PASS，允许commit并push；最终同包provider/TUI/live仍待后续，不作项目整体签收。
