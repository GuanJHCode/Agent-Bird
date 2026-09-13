# G1–G4 资源验收 harness

本 harness 对冻结的真实 Go `orchestrator` binary 运行无认证 synthetic provider，并把资源曲线与协议完整性分开判定。它不会因为输出被截断后 RSS 稳定而宣称洪峰通过。只有固定时长、固定负载、独立 report 全部 durable、RSS/CPU/趋势门槛同时成立，场景才会输出 `PASS`。

当前 manifest/result 明确标记 `measurement_scope.claim=g1_core_resource_only`。采样包含 coordinator、Source Host、report helper 及可见产品后代，排除 synthetic provider 和原生 Codex backend；它不启动也不测 G0 Python service/bridge child。因此 `PASS` 只证明 G1 核心，不能写成整个 G4 distribution 达标。

## 固定场景

门槛由 `data/resource-acceptance-thresholds.json` 在 binary freeze 前固化：

| 场景 | 完整时长 | 负载与判定 |
|---|---:|---|
| `idle` | 40 秒 | 启动空协调器；30 秒防抖后到结束必须没有本工具进程。 |
| `callback` | 5 分钟 | synthetic worker 产生完成事件后保持 `owner_review/result_ready`，不 accept、不 ACK；Source Host 必须先退出，此后 controller 不再调用产品。总 RSS ≤64 MiB，平均 CPU ≤单核 1%，稳定后不周期产生新产品进程。完成到首次 `collect` 可见 durable result ≤2 秒；窗口末 result 仍为 pending；attached 回传 5 秒必须有产品证据。 |
| `workers` | 10 分钟 | 同一来源 Host 下 2 个真实 synthetic worker；worker 资源排除，本工具进程树总 RSS ≤128 MiB。该场景保留作隔离诊断。 |
| `hosts` | 10 分钟 | 4 个来源 Host 同时存活；第 5 个必须返回 `admission_deferred`，本工具进程树总 RSS ≤128 MiB。全局默认执行槽仍为 2，且必须有 2 个 worker 各自完整运行 10 分钟，因此本场景同时提供正式 `workers` 证据。 |
| `burst-valid` | 20 分钟 | 每 worker 的 stdout/stderr 合计 1 MiB/s，合法 Claude schema 普通消息；每 30 秒一个 4 MiB 普通诊断巨行；每 10 秒通过独立 report 通道一个 ≤4 KiB 关键事件。2 workers。 |
| `burst-unknown` | 20 分钟 | 速率、巨行和 report 与上项相同，普通输出改为 unknown raw。2 workers。 |

callback 首次 durable 时间在轮询中立即锁定，只匹配非 `resource-*` 的 Host terminal result；fixture 在 terminal stdout write 前记录 `terminal_emitted_unix_nano`，随后才发送独立 report 并写 final stats。harness 等 final stats 只为取得冻结时间戳，不会把这段等待算入首次 durable 时间。

洪峰除 128 MiB 上限外，还按最后 10 分钟的一分钟 RSS 中位数冻结两个趋势门槛：Theil–Sen slope ≤0.5 MiB/min，末点相对首点增长 ≤8 MiB。fixture stats 必须证明完整时长、总字节、巨行次数与 report 次数；worker 提前退出一律失败。

普通诊断/unknown raw 巨行应有界丢弃或记录截断，不能挤掉关键 report。关键结构化事件自身超限应可观察拒绝；harness 不要求产品无限容忍坏协议。

## 独立 report 合同

资源验收依赖以下产品合同；sidecar 只证明 fixture 发出了事件，不是 durable 证据：

1. `LaunchCommand` 带稳定 `report_capability_id`。已认证 Source Host 生成 raw token、写入自己的 durable ledger 与 0600 capability，并在 spawn 前通过 Host 协议登记 token hash 和完整绑定；coordinator durable ACK 后才允许 spawn。Launch 重发必须按同一 capability ID 复用 Host ledger/capability，不能在协调器崩溃窗生成不可恢复的 raw token。
2. capability 绑定 `run_id/task_id/attempt_id/segment_id/producer_id/work_revision/execution_epoch`；coordinator DB 只保存 token hash。Host 只向该 segment 注入绝对 `ORCHESTRATOR_REPORT_EXECUTABLE` 与 `ORCHESTRATOR_REPORT_CAPABILITY`。
3. 同一 binary 提供 `report --capability-file ABS --request ABS`。request 是 strict JSON `{version:1,event_id,sequence,kind,payload}`，总帧 ≤64 KiB；不能复用 run control token。资源 progress payload 固定为 `{status:"resource_sample",sequence,elapsed_ms}`。最终 result payload 固定为 `{status:"completed",artifact:{id,path,size,sha256}}`；artifact 是 capability 同目录下的 0600 sentinel，report CLI 必须校验其锚定路径、大小和 hash。
4. worker report 使用独立 segment sequence 和 `report_events` durable 表，ACK 后 helper 才成功退出；`collect` 统一投影。harness 仅通过 `collect` 的 `event_id` 与 sidecar 对账，不读写产品 DB。

每 10 秒启动的短命 `report` helper 可能被 1 秒 `ps` 采样漏掉。fixture 对每次 helper 调用采集子进程 `rusage`：CPU 全量加入场景 CPU，两个 worker 的 helper 最大 RSS 相加后保守加入 peak RSS。持续的 coordinator、Host 及其可见辅助后代通过每秒进程树采样统计；synthetic provider 及其非产品后代排除。Source Host 的 owner monitor 每 200 ms 运行一次 `/bin/ps` 身份探针，因此 callback 在 Host 退出前不开始稳态窗口；Host 未退出会直接失败，不能用每秒采样“没有碰巧抓到”推断无周期 helper。其他场景不为抓一个已知短命探针把采样成本放大五倍。

## 构建、冻结与运行

构建脚本从 shared worktree 根使用 `tasks/g0-macos/data/go/bin/go`，固定 `GOENV=off`、`GOTOOLCHAIN=local`，并使用任务目录中的显式 `GOCACHE/GOMODCACHE/TMPDIR`：

```sh
repo=/Users/guanjunhui/code/ai/codex-cli-orchestration-design
wt="$repo/tasks/g1-g4-delivery/tmp/resource-acceptance"
build=/tmp/resource-build-final
"$wt/tasks/g1-g4-delivery/scripts/resource-build.sh" "$build"
```

主会话确认 G1–G4 最终组合 commit 且构建 worktree 干净后，冻结一次 binary、worker、门槛与基准机。manifest 会记录 `source_clean`；完整验收的 `preflight` 要求它为 `true`：

```sh
harness="$wt/tasks/g1-g4-delivery/scripts/resource-acceptance.py"
manifest=/tmp/resource-g1-g4-freeze.json
"$harness" freeze \
  --orchestrator "$build/bin/orchestrator" \
  --worker "$build/bin/resource-synthetic-worker" \
  --out "$manifest"
"$harness" preflight --manifest "$manifest"
```

每个场景使用新的短绝对路径。macOS Unix socket 的路径必须不超过 103 bytes，harness 会在启动前拒绝过长路径。完整运行示例：

```sh
"$harness" run --manifest "$manifest" --scenario idle --out /tmp/resource-idle-final
"$harness" run --manifest "$manifest" --scenario callback --out /tmp/resource-callback-final
"$harness" run --manifest "$manifest" --scenario hosts --out /tmp/resource-hosts-final
"$harness" run --manifest "$manifest" --scenario burst-valid --out /tmp/resource-burst-valid-final
"$harness" run --manifest "$manifest" --scenario burst-unknown --out /tmp/resource-burst-unknown-final
```

`hosts` 已在同一冻结阈值下覆盖默认 2 workers 的完整 10 分钟运行；完整验收不重复执行 `workers`。只有 `hosts` 的 worker 子检查失败、需要隔离 Host 数量影响时，才单独运行 `workers`。

`--smoke-seconds 3..30` 仅验证接线，结果永远是 `SMOKE_ONLY`，其中完整时长与趋势检查必然不通过，不能作为验收。场景目录保留 `resource-result.json`、`resource-samples.jsonl`、fixture sidecar 和产品 state；state 含未公开 capability，整个场景目录必须保持 0700，不能作为普通日志整体分享。harness 收尾只向该场景 state path 对应的冻结 binary 进程发信号，不删除证据，并在强制信号前重新核对 PID 的 executable 与 state path。

Source Host 使用 harness 提供的白名单环境：只有系统 `PATH`、`LANG/LC_ALL` 和场景 `TMPDIR`。不读取或传递登录、auth、Keychain、模型选择、原生 Codex 或用户全局配置。

## 当前产品缺口核查

核查基线 `bad5b80a209f00cbc927c3a9b6c822ac9167c6e3`，以下项目尚不能通过完整验收：

- Host 尚未把 `adapter.StreamParser` 接入执行路径，只合并 stdout/stderr 并保留前 1 MiB。合法 schema 与 unknown raw 没有运行时区别，末尾关键结果可以被截断；因此洪峰 RSS 即使平稳也不能通过。
- binary 尚无上述 attempt-scoped `report` 入口、capability、IPC 与独立 durable sequence。synthetic worker 的 `product_report_attempts` 会是 0，`all_report_events_durable` 必须失败。
- `collect` 当前只能证明 Host 生命周期 result 入库；问题入库 ≤2 秒没有可运行问题报告入口，attached 回传 ≤5 秒也没有该 binary 的可观察证据。
- 20 MiB attempt 日志、16 MiB run spool、128 MiB 控制事件、低磁盘余量与 cleanup retry 缺少冻结阈值注入接口。洪峰覆盖输出有界性，但不能替代 spool 限额、低磁盘和清理重试的独立故障注入。产品提供接口前这些条目保持未验证，不能从资源曲线推断通过。
- 旧 `measure-resources.sh` 只采样静态 PID，`no-task` 还要求 5 分钟参数；它没有负载证明、进程树、短命 helper、durable report 或趋势判定，不能作为 §8 验收证据。本 harness 不修改该历史脚本。

完整 distribution 资源结论还需在同一场景、同一时间轴纳入 G0 进程，不能把分离运行的两个峰值相加：

- service 身份必须从 `state_dir/<activation_id>/service-start.json` 读取并核验；入口为 pinned Python `-I -B .../projectproxy_launchd_entrypoint.py --manifest <0600 manifest>`。idle 无 frontend 时应在 10 秒内退出，离线 pending callback 也不应保留。
- bridge Python child PID 必须从同 Go binary `native-bridge helper` 的 boot/ready receipt 读取；不能从 argv 猜。owner EOF 后应退出。attached 且 delivery/业务 ACK 在途时 service/helper 保留，必须与 G1 同时采样并计入总 RSS；原生 Codex backend仍排除。

主会话的最终 native distribution case 可提供上述 identity/lifecycle 证据。除非该 case 与 G1 资源窗口并发、使用统一进程清单采样，否则两份证据应分列为“G1核心资源”和“G0生命周期”，不能合并宣称全产品资源通过。

最终组合 binary 尚未冻结，因此目前只允许 `preflight` 和短 smoke。20 分钟场景必须在主会话冻结 SHA 后完整运行；不得缩短时长后改写 `status` 或门槛。
