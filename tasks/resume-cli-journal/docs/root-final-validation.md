# CLI Resume journal最终底层验收

ROOT于2026-10-08验收f0aee84五个Go文件，独立安全审查PASS。最终hash与原始证据索引见data/root-final-receipt.json。本记录更新worker报告中的待验收状态；CLI/RPC接线仍在独立owner-public-wiring WT继续。

- macOS最终race：13顶层PASS，58含父子具名PASS、0FAIL、4UID子例SKIP，3.496s。最后测试版本的go vet ./cmd/orchestrator重新执行exit0，解决审查指出的旧vet日志时间早于最后test变更问题。
- Docker Linux arm64普通非root全selector：13顶层、58具名PASS，0FAIL，4UID子例因chown权限SKIP；exit0非OOM。
- 独立Linux UID补验容器：root且drop all capabilities后仅加CHOWN，禁网络/只读，仅测试private tmp下的owner拒绝矩阵。1顶层、5含父子具名PASS，0FAIL/skip，四个先前SKIP全部实际执行通过。两批并集为62个具名PASS，不把重复parent叠加为独立用例。
- 两容器精确删除并inspect不存在；共享crosscompiled test binary与两个receipt SHA256一致后已删除。实际create argv/CID/终态在各receipt中，无用户安装或账号/Provider调用。

六个真实子进程故障点覆盖pending和complete各temp_created/temp_synced/原子发布后，恢复与scan外orphan边界正确。此证据是process abrupt exit，不是物理断电/内核崩溃。系统调用和双目录fsync已审查；保持未知orphan，不按prefix删除归属不明文件。

本批完成底层durable journal、安全边界及本机/Linux验收。没有执行public Resume、启动SourceHost或宣称整个F10完成。Store authenticated replay仍是权限依据，本地completed不是授权收据。后续接线必须先严格loadControlCapability，再选择journal；收到可验证Store收据后标completed，失败保留同CommandID。
