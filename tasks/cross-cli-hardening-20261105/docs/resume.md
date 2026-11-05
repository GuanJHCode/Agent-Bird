# 恢复入口

读取TASKS.json、baseline、current-plan、reviews。集成worktree与分支见baseline；root唯一写入，子agent不递归。用户明确每功能独审/主签收后commit并push本任务分支，不merge正式分支、不发布或替换全局安装。

T00 done/pushed=08aa0184；T02独审/集成签收PASS并pushed=428a5877；T10独审/主签收PASS并pushed=bde4bacf；T01独审/集成41PASS已签收并pushed=c29ac2bb。T03预算草案在plans/ADR-003，依赖T02；T05已委派implement_t05在隔离树实施，T03由root推进，文件与测试隔离。所有旧用户现场保留，未跑live。

core_status=NOT_RUN，native_push_status=NOT_RUN，handoff_status=DRAFT。最终包尚未生成，不能宣称整体完成。

当前实施现场：root T03独立树 ../t03（绝对前缀见baseline），budget v2/schema10/encoded limits已实施未审，T03-final-focused运行中（root工具session73297）；源码与ADR003在t03。原始T03-red及其它失败保留外部tmp/t03-evidence。T05 r1独审CHANGES_REQUESTED见reviews/T05-review-r1，原实现agent正在修部分WebSocket frame超时与实际driver SIGKILL恢复测试；不能标done。用户live限额和模型约束见docs/live-budget.md，尚未跑live。T04调查完成：不存在现成artifact registry/pin/GC，必须实现最小正文生命周期；调查结论在/root/investigate_t04消息，后续实施前应落ADR，不能假称已有API。

T03最新：r2独审PASS（148+57+1）、root集成35PASS、vet/build通过，准备本功能提交。当前全部启动/测试会话已收尾。T05仍BLOCKED：半帧P1已关闭，但独审再次ownerready30s超时；root接管，原worker已10次有界诊断全绿无失败栈，未归因，不宣修复。诊断仅fixture改动在tmp/t05，12文件manifest见其startup-diagnosis；后续root分析新路径，不盲目重试。T04调查设计见plans/T04-design-notes.md。
