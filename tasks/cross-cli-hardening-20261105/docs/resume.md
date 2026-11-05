# 恢复入口

读取TASKS.json、baseline、current-plan、reviews。集成worktree与分支见baseline；root唯一写入，子agent不递归。用户明确每功能独审/主签收后commit并push本任务分支，不merge正式分支、不发布或替换全局安装。

T00 done/pushed=08aa0184；T02独审/集成签收PASS并pushed=428a5877；T10独审及主签收PASS，本轮提交；T01隔离worktree由implement_t01冻结验证中。T03预算草案在plans/ADR-003，依赖T02；T05已委派implement_t05在隔离树实施，T03由root推进，文件与测试隔离。所有旧用户现场保留，未跑live。

core_status=NOT_RUN，native_push_status=NOT_RUN，handoff_status=DRAFT。最终包尚未生成，不能宣称整体完成。
