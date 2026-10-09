# 显式保留期的公开使用

在owner私有目录准备0600请求；run_id须匹配run-control capability。`terminal_received_before`是canonical UTC RFC3339Nano（Z、非未来），用于严格筛选Store已接收的全部required Exited，不是任务completed时间。

```json
{"version":1,"run_id":"run-id","request_id":"retention-batch-1","terminal_received_before":"2026-10-09T00:00:00Z","limit":8}
```

使用绝对路径保存请求与计划，并检查plan命令成功退出后再apply：

```sh
umask 077
orchestrator diagnostics-retention-plan --state-dir /private/state --control-file /private/run-control.json --request /private/task/selection.json > /private/task/plan.json
orchestrator diagnostics-retention-apply --state-dir /private/state --control-file /private/run-control.json --request /private/task/plan.json
```

先检查`items`、`excluded`和`next_after_task_id`。继续分页时把cursor作为下一请求`after_task_id`，保留其余选择字段；空items是正常结果。公开stdout含newline仍≤8192，直接保存为0600 regular file即可，不需要编辑/hash重算。

apply前若source证据、原diagnostics revision或保留期向量漂移，fresh项会拒绝。某项成功后其他项失败/断连可保留原plan并原样重试；成功项重放原receipt，失败/未执行项重新走各自认证和既有删除协议。prepared/removed intent按同一request继续，不可修改cutoff/ID/vector试图重选。退款只在真实Remove+VerifyRemoved和最终Store COMMIT完成；charged marker/locks仍保留，返回的是logical bytes与reserved capacity，不是filesystem blocks。

五秒请求窗口可能不给最终aggregate；没有durable batch report或全局rollback。只支持run-control，task handle不能发起这两个命令；失败/UNKNOWN、尚有pending read、缺Exited或坏/过大runtime证据均不自动删除。host_progress/report_events及其他artifact类别不纳入本保留期，无后台GC。
