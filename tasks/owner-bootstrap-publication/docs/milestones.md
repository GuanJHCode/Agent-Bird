# Server bootstrap publication milestones

- Plan9d1e20c冻结新kind/server发布/schema35独立pending journal；只读架构调查否决status-only，因为Coordinator启动/reconcile/owner activate会覆盖hoststatus。实施须事务内重核所有authority gate，不只Coordinator预检查。
- 生产准备5ee718d→07028a4只共享token-free metadata与FD-relative lease，cmd wrapper保留capability/token/path校验和codeError类型。原lease身份/权限/Sync/lock算法保留，旧CLI仍原流程；没有新RPC/schema/pending实现，尚未合入integration。
- ROOT首shared-lease-01编译FAIL（漏time import），没有运行测试，不算业务RED。07028a4修复后shared-lease-02定向旧lease+token-free reconstruction：3top11namedPASS、0FAIL/SKIP、exit0，原stdout SHA66ff0dda0cd2dbfdbfaa9430e8811b9e9fc9e3d6029e8a6802b6131f68ce4395。fixed07028a4独立reviewer PASS；未重复全仓/原生长链/race，这些等完整协议最后相关修改后再验。
- 无真实Provider/model/account、安装替换或push/release；没有后台actor/test session，Go测试原调用已结束。下一步是schema35journal/quota/事务gate与server新RPC完整串接，不可把当前重构当direct IPC恢复完成。

- 追加只读架构复核确认旧随机stage在mid-write SIGKILL后可累积，不能直接供新server协议重试。冻结稳定journal-derived stage、expected prefix严格补写、单份scratch预付与finalize释放；未知旧stage保留拒绝。完整约束已追加plan；尚未实现或运行新crash检查。
