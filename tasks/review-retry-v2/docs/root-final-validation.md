# F01 readonly review failure v2 最终集成验收

ROOT于2026-10-08核对完整原始日志、容器终态及4个Go文件hash。当前实现为a3b7b39；本记录补充并替代旧报告中“等待ROOT验收”的状态，旧RED、夹具失败和中间结果保留。

- 生产spec/quality、正交反例测试增量、portable worker夹具增量均独立审查PASS，无阻塞。
- macOS Store最终race：24顶层、140含父子具名PASS，0FAIL/skip，588.666s（root-final-race.jsonl）。此后只改Host测试夹具，Store文件hash不变，复用此证据。
- macOS最终portable Host race：6顶层、8具名PASS，0FAIL/skip，18.323s（root-portable-host-final-race.jsonl），替代旧Host race。worker普通最终Store24/140与portable实际worker1/3均PASS；生产整体build0，Host/Store vet0，最后Host夹具修改后Host vet0。
- Docker Linux arm64离线、非root、只读、禁网络、固定镜像运行：Store24顶层/140具名PASS；portable Host6顶层/8具名PASS，均0FAIL/skip、exit0非OOM。两容器精确删除并inspect确认不存在；测试binary SHA256与receipt一致后删除。
- 首次Linux Host失败保留：/usr/bin/true不存在导致测试夹具启动前失败，5具名PASS、3FAIL。已使用当前Go test executable固定silent/session helper修复，真实Start/Wait、artifact/event、Store→QueueRetry→ClaimReady和冻结输入断言保持。

最终hash、逐package统计、容器ID/receipt hash见data/root-final-receipt.json；执行器见scripts/run-linux.py。全部证据为本地受控worker，无真实Provider/model/account调用。Host测试从内部h.execute进入，上游edit/validate业务body受控，不声称ExecuteLaunch/Profile资格、Git materialization、安装Hook或native TUI完成。两个无关旧Host测试仍用/usr/bin/true，不在本次Linux selector内。

本项完成限定的显式只读审查失败v2重试契约；未扩展自动重试、可写阶段重试或后续所有F01边界。其他F01–F13由集成任务继续推进。无用户安装、push或发布。任务owned测试binary已清理，交付源码、脚本和证据保留。
