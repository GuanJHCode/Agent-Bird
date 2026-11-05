# 恢复入口

读取 TASKS.json、docs/baseline.md、plans/current-plan.md 与 reviews。实际 worktree 及分支见 baseline。仅主会话写集成树，禁止递归 agent。每功能独审通过、主会话签收后才提交，并 push 本任务分支。T00 当前待独审；后续先 T01/T02 并行，T10 主会话准备。不重置用户工作区，不读取/公开旧 capability 或认证。

当前 core_status=NOT_RUN，native_push_status=NOT_RUN，handoff_status=DRAFT；没有最终包或 live 通过声明。
