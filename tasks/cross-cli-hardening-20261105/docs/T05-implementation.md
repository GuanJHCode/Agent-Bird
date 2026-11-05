# T05 r5 主会话接管修订

实现包含此前r3交付全部生产恢复/截止/半frame关闭修复（详见r3-implementation-report.md），以及root闭环的两类竞态；最终完整Python147pass/0fail/0skip，独立复审39pass/0fail/0skip，root集成二进制19个真实进程恢复用例全PASS；经下述签收允许提交。

## owner-ready fixture竞态的因果闭环

原r3独审 BLOCKED：test_local_service_chain ack_success 启动owner-ready超时。原worker10次full tracing未复现，不能当解决；root保留所有原红记录。先以真实UDS拒绝HTTP upgrade确定性证明fixture旧recv EOF不退出，修为EOFError；新deadline测试证明快速失败。light/catch-only路径随后在 `root-startup-fix/socket-cause2/failures/failure-118tahx7/root/backend-error.log` 捕获 production activation_service.py:846 `backend socket identity changed before owner receipt`，此时尚未模型/投递/ACK。

fixture原asyncio.start_unix_server(path)按继承umask创建socket，await返回后chmod600；runtime在两者之间可能记录0700（guard spawn umask077）后见0600而正确拒绝。受控wrapper在server创建后、返回前暂停，旧022 umask固定观测0755红（root-startup-fix/data/socket-privacy-red.xml）。改为raw socket bind期间umask0177、随后restore，在首次可见时已0600，不再post-bind chmod。受控022/077双场景及EOF回归均绿（r4-deadlines.xml 31pass）。未捕获实际失败expected/observed全tuple，不虚构其数值；实际trace与确定性creation-mode反例构成证据。生产socket identity、private mode、peer、manifest pin检查完全未改。临时诊断monkeypatch已从交付树移除，外部私有源和failure现场保留。

## history断线与owner退出竞态

r4全Python146项：145pass/1fail，test_local_product_driver complete-sending_before_io在真实owner退出后一直显示awaiting_history。driver loop先检查owner，然后历史read收到EOF时走异常投影，来不及下一次liveness check；退出后已无driver更新状态。保留r4 XML/log。

crash_fixtures新增私有repinned模式sending_before_io_detach：真实SIGKILL与同owner restart后，history调用位置确定等待attachment失效再抛websocket_eof。未修生产时稳定红（owner-detach-red.xml/log），修复后绿（owner-detach-green.xml/log）。生产异常分支在投影前再次核验既有attachment；失效写owner_detached，仍不resend、不ACK，ledger仍uncertain。fixture进一步断言退出后的turn/start=0、无ACK request、ledger仍delivery_uncertain，不把owner退出当业务完成。

r5冻结source-manifest/implementation.diff；Go文件和先前10pass race/vet0未变，复用其证据。r5新Python全验收及独审终态已确认，source/hash前后相同；对应新报告已纳data/public-evidence。所有场景为protocol-fixture/OS-native-local，不是provider模型、TUI或原thread live验收。
