# 预授权 probe 重试与预算

现有 `task submit --request <plan.json>` 的 task 对象，以及 `task dispatch` 的业务 JSON，可增加 `execution_policy`。保留原先已授权的 adapter、provider lock、账号、模型与权限。示例是在现有 task 对象上增加这些字段：

```json
{
  "max_attempts": 3,
  "max_active_ms": 3600000,
  "execution_policy": {
    "version": 1,
    "group_max_attempts": 3,
    "group_max_active_ms": 7200000,
    "task_max_active_ms": 7200000,
    "segment_max_active_ms": 3600000,
    "retry": {
      "codes": ["probe_start_eagain", "probe_start_eintr"],
      "backoff_ms": [5000, 20000]
    }
  }
}
```

示例只展示预算字段；完整计划继续使用原来的 task id、dependencies、adapter 和 completion policy。请求文件与 handle 保持原有 owner-only 约束。`status` 返回实际冻结的 `execution_policy`，等待退避时包含 `next_retry_at_unix_ms`。

没有 `execution_policy` 的任务保留历史行为；没有 `retry` 的 policy 也不会自动重试。新 policy 中 `max_attempts` 是 task 次数上限，group 次数是所有成员共同的容量。问答 continuation 使用同一个 attempt，segment 累计消耗 attempt/task/group 活动预算。UNKNOWN 保留完整预占；已证明 not_started 的时间结算为零，但次数与编号不会退回。

v1 policy 提交后不可变；本里程碑没有增额命令。零值表示省略后的默认值，不能表示无限。默认 task/group 次数 3，attempt/segment 60 分钟，task/group 120 分钟；显式 task 次数 1–16，group 1–256，group 时间最多 24 小时，且 segment ≤ attempt ≤ task ≤ group。同 group 成员共享 group 两项，局部 task/attempt/segment 可以不同。policy 与 legacy 不能混组，也不能跨 run 复用已存在 policy group。

`task dispatch` 为 policy 生成的 adapter profile timeout 使用 segment 上限；attempt 可以通过同一 attempt 的 continuation 累计超过 60 分钟。`task submit` 保留显式 adapter profile，Host deadline 使用 profile 与实际预算 grant 的较小值。

退避省略时为 5 秒、20 秒，按 task 剩余可授权次数截短；次数大于 3 也不会自行增加默认自动重试。显式退避为 1–15 项，每项 1 ms–60 分钟，项数不得超过 task 次数减一。到期前既不创建新 attempt，也不启动执行；pending 决策逐 task/binding 预占次数和下一段有限时间，claim 原子消费该承诺并沿用原额度；重启和重复事件不会清零退避或生成第二份 grant。取消、Provider disable 或必需依赖终止会退休待执行承诺、释放未使用的容量，并保留原决定与额度历史。

本阶段只支持生产 `--version`/`--help` probe 在主 worker 尚未启动、probe 本身未启动且 OS 返回 EAGAIN/EINTR 的事实。已启动 probe、退出码、超时/取消、权限错误、任意 launch_failed、UNKNOWN 或 question 都不触发自动重试。候选 validate/review/implement/integrate 与 GitOps 的自动重试目前明确拒绝；这些任务仍可使用没有 `retry` 的预算策略。已退出 readonly 执行和候选阶段安全重试、策略 revision 增额、详细诊断日志仍是后续范围。

合法 retry 任务的公开入口只做静态 profile/binary pin/digest 校验，动态 version/help probe 在持久 Host attempt 内执行。primary 与所有 fallback 均不得包含候选/GitOps 自动重试。最终未排重试的已证明启动拒绝会阻塞该任务必需后代，原事件 ACK 后才释放配置 pin；显式 stop 不会触发再次启动。
