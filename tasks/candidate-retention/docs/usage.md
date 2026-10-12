# 候选工作区与私有 ref 回收

普通 AutoDirectory producer 和 v2 compose 的完整冻结回执会在原 Host Result/Exited 持久化后封存并登记。后续由独立、固定二进制身份的维护进程执行清理；Coordinator 不直接执行 Git。

成功保留期为 30 天；失败或取消需对应 run 当前 owner 显式 `resolved` 或 `abandoned` 并给出原因，之后保留 7 天。计时不早于资源登记、整个 run 及递归已知 new-base 下游的最后终态、精确消费和解决记录；必须严格超过期限。所有任务、预算、问题、投递和下游读取都结束后才具备资格。

第一次 apply 准备成功会永久禁止源 run 及已有 new-base 下游再发起消费，包括重试、续跑、增加任务和修改任务版本。关闭 GC 不撤销该围栏。回收保留原封存日志、完整回执、操作日志及数据库索引；索引用于审计和继续未完成的清理，不是文件备份，不能保证 Git 对象被后续 GC 后仍可恢复文件。

历史未登记、缺 origin 身份、过滤候选、所有 continuation/rework/new-base producer、UNKNOWN 或不完整 Git 进程历史不自动回收。不会扫描目录并认领遗留现场。中断操作复用同一个操作日志；目录、文件、私有 ref 身份不符时保留现场。

使用当前 run 的 owner-only control file 和请求文件（0600），以下命令直接调用 `codex-orchestrator` 二进制：

```sh
codex-orchestrator candidate-retention-resolve --state-dir STATE --control-file CONTROL --request resolution.json
codex-orchestrator candidate-retention-plan --state-dir STATE --control-file CONTROL --request plan-request.json > saved-plan.json
codex-orchestrator candidate-retention-apply --state-dir STATE --control-file CONTROL --request saved-plan.json
codex-orchestrator candidate-gc-status --state-dir STATE --control-file CONTROL --request gc.json
codex-orchestrator candidate-gc-enable --state-dir STATE --control-file CONTROL --request gc.json
codex-orchestrator candidate-gc-disable --state-dir STATE --control-file CONTROL --request gc.json
```

创建请求/保存计划前设置 `umask 077`。解决请求：

```json
{"version":1,"run_id":"RUN","request_id":"resolve-1","resolution":"resolved","reason":"已确认失败原因及现场处理完成"}
```

计划请求（单页最多 8 项，`after_resource_id` 可接上次返回的 `next_after_resource_id`）：

```json
{"version":1,"run_id":"RUN","request_id":"retention-1","limit":8}
```

apply 直接读取 plan 命令保存的完整 JSON，不手改摘要。返回每项的资源 ID、操作 ID、generation、state 和可选 code。`pending` 保留恢复现场；已退出的旧维护进程经身份与进程组核验后，可用新 request_id 重新规划，继续原操作日志。

GC 请求：

```json
{"version":1,"run_id":"RUN","expected_revision":0}
```

自动回收默认关闭；先查 status，enable/disable 使用其当前 revision。显式启用记录绑定当前 controller、控制凭据摘要和 owner 变更历史；重绑后不会自动继承旧同意。已有合法启用记录可在协调器恢复后继续，未启用时不会离线补造授权。GC 与已有诊断/附件回收共享调度和 idle/inFlight 围栏，每轮最多一项/每 run；不另开后台 Git 执行通道。
