# Host artifact dispatch 授权验收

当前状态：派发授权基础阶段已通过本批验收。基础提交为 `2a167a7`，完整 M3 / F02 和 F01–F13 尚未完成。

## 本阶段行为

- 每个新 segment 原子预占 terminal/prelaunch 1MiB 和 21×64KiB progress，共两行 reservation、2,424,832 raw bytes；resume 新建自己的两笔预占，旧承诺保留。
- grant 固定 run/task/attempt/segment/producer/command/work revision，epoch 重连不改变原授权。schema20 原有表可表达该契约，没有新增版本或 DDL。
- 新 candidate intent 绑定 grant；旧 nil-grant intent 保持原字节契约，仅用于核验历史证据。Live Pending 和 signed owner/Host accept 拒绝缺失、部分、已释放或漂移的授权，不补造权限。
- ClaimReady 提交前验证全部 raw promises 与本事务完整 control 增量，涵盖后续输入快照、diagnostics 和 continuation；失败回滚整个派发事务。
- 历史迁移夹具先核验原命令并重建旧摘要。索引审查夹具在生成历史附件前就采用旧摘要，保留覆盖证明，不重写已接受的证据。

## 有效证据

工具链为 Go1.26.8 darwin/arm64，TMPDIR=/private/tmp。所有测试均 count=1。

| 验证 | 顶层 / 具名通过 | 结果 |
|---|---:|---|
| Mac full Store | 399 / 1132 | PASS，0 fail/skip，78.335s |
| Mac full contract | 39 / 268 | PASS，0 fail/skip |
| Mac affected Store race | 19 / 50 | PASS，0 fail/skip，125.725s |
| Mac affected contract race | 4 / 19 | PASS，0 fail/skip |
| Mac full Host | 401 / 1548 | PASS，0 fail、16 skip，255.131s |
| Mac full Coordinator | 84 / 176 | PASS，0 fail/skip，233.064s |
| Mac full IPC | 9 / 21 | PASS，0 fail/skip |
| Mac affected Host race | 10 / 19 | PASS，0 fail/skip，9.774s |
| Mac 公开 CLI race | 3 / 3 | PASS，0 fail/skip，10.948s |
| Docker Linux arm64 affected Store | 20 / 68 | PASS，0 fail/skip |
| Docker Linux arm64 full contract | 39 / 268 | PASS，0 fail/skip |
| go build ./... / go vet ./... | — | exit0 |

Host 的16个 skip为未启用的原生actor或helper入口，包含 TestNativeCodingHost、TestNativeSchemaContract、managed Codex诊断/sandbox/retained-pipe actors和AGY history sandbox；完整名单在receipt，未将其计为本批原生验收通过。

原始日志、测试名、命令、源码 SHA256 和容器终态见本任务 `data/`；最终汇总由 `data/root-final-receipt.json` 记录。公开CLI覆盖真实submit/原owner/受控worker执行、transport替换后原worker与segment不变，以及report删源后精确ACK重放；原Coordinator Wait=0。worker仅为受控fixture，未调用Provider模型；CLI子orchestrator为普通go build，不扩称子binary经过race插桩。

Linux使用固定本地镜像，禁网、只读根、非root和资源限制；不声称 Linux race、Provider、sandbox、安装或分发验收。

## 保留的失败与修正

- 新 claim 最初没有 grant，raw allowance 不足仍派发；nil live grant 仍可写 Host ledger / owner 签名，行为 RED 已保留。
- 初次 full Store 回归揭露 DAG 历史重建漏恢复 HostID/grant；后续揭露只读 retry 历史证据错误地经过 live binder，均修复并通过完整 Store。
- 额度 fixture 的原有精确边界加入新 reservation metadata，保留负边界、原子回滚、UTF-8字节和重放断言。终止/删除业务 capture 不释放 Host 原始预占。
- combined dispatch disk one-byte-short 在旧实现实际返回成功（`unsafe join admitted`）；补全事务末尾 raw/control 合计校验后定向和完整 Store 通过。
- 首次 Host 全量因旧合成正向 grant 缺授权失败（5top/8named）；第二轮还发现离线恢复fixture遗漏（1top/1named）。修复明确fixture后，末次Host race和第三轮完整Host全部通过，不在生产入口自动补授权，不把早期组合命令记为通过。
- Linux contract 首次漏挂固定 testdata：5 个顶层失败；补只读 testdata 挂载后完整 39/268 通过。本批三个运行容器均已按原 CID 删除，并由ROOT再次inspect确认不存在。

## 独立审查与边界

Host fixture 子任务隔离提交 `1837b84` 的9文件 diff已由ROOT检查并应用；WIP snapshot `80d6cac` 不作为产品交付。该 Agent 随后只读审查 production scope / reserve / replay / history、整事务磁盘复核与历史 fixture，未发现未解决 blocker。ROOT负责最终集成和实际测试计数。

本阶段未完成 descriptor-bound 物理 Host terminal/progress/prelaunch writer、Host refs/retained 提交、legacy_pending 计费、diagnostics retained/delete/tombstone、完整2GiB与原生 flood/RSS/物理故障矩阵。旧直接 Host.Execute/ExecuteLaunch 测试 API 不因此获得物理授权，后续 writer 接管仍须处理。不能将派发授权基础阶段称为完整 M3。

未调用真实模型/账号，未替换安装、推送或发布。Intel Mac/Windows 原生验收按用户确认延期；现有 Mac arm64 和本机 Docker Linux 的后续工作继续。
