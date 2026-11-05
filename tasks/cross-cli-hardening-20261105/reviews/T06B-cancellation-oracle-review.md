# T06B cancellation oracle 独立复核及签收

PASS，仅测试修复。实现 root；独立审查 /root/review_t08（2026-11-05）；root集成签收通过。无产品源码变化。

r2完整race原报告1465 pass/1 fail/8 skip，唯一失败在8个取消summary调用已返回后立即断言SQL InUse=0，实测仍为1。原case聚焦20次通过不能覆盖该失败。Go1.26.10 database/sql 的 awaitDone/rollback 先CAS标记事务done，再异步归还连接；调用方defer Rollback可先获得ErrTxDone。受控stdlib driver确定性复现回滚返回时InUse=1，释放回滚后有界pool barrier再归零，源码及JSON保留本任务scripts/public-evidence。未以盲重跑、删除断言或忽略失败制造通过。

唯一test变更：8调用全部done后，用新3秒context获取并关闭fixture单连接池的Conn，再保留原并发最大2、active=0、slot=0、InUse=0及只读数据库断言。独立审查确认真正连接泄漏会占住唯一slot使barrier超时，因此此同步不掩盖泄漏。原产品外部验证/锁/事务/权限完全未改。

root race count20：20/20，0skip。独立race count5：5/5，0skip。集成新报告覆盖cancel、writer/connection释放、实际Git取消三项：3 pass/0fail/0skip；命令、源码摘要及原日志SHA见data/public-evidence/T06B-cancel-*.json。T08新SDK组合仍须最后稳定树重新完整回归，本项不冒充T08/最终包通过。
