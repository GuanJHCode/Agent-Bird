# F02 显式诊断删除验收

已完成本子阶段；生产快照 `0164436`，基线 `f7db2e8`。它不代表完整 F02 或 F01–F13 完成。

公开 `diagnostics-delete` / `task diagnostics-delete` 使用原控制能力及精确 request ID/revision，仅删除最新已完成 attempt 的详细日志（原 attempt ≤64 exited segments、无未解决执行、诊断 delivery idle）。Schema31 持久保存不可变原始事实、文件计划与完成回执；删除期间全额保留 reservation，文件确认移除后才事务释放。文件操作固定原目录与锁，以身份/内容核对逐文件移除，保留已计费 marker/目录/锁。历史 DAG rework 可验证原 diagnostics grant，实时派发仍拒绝退休 grant。

有效证据（原始结果均在 `../data/`）：

- 原 schema30 旁路基线：1 top /4 named FAIL，确认无 intent 可改变 reservation 状态，原 RED 保留。
- Store 首轮普通和 race 各8 top /28 named PASS。后续受影响部分单独重验；不把这份旧 race 冒充最终全部源码测试。
- 文件层、Store、peer 权限及公开 CLI 组合：17 top /37 named PASS，0 fail/skip。计费修正后增量覆盖全部新增 Store 用例及受影响旧迁移/authority 夹具：16 top /68 named PASS，另2项历史不可擦除 guard PASS。
- 最终 Store race：4 top /6 named PASS，涵盖独立实际字段计费、两阶段真实 deferred-FK COMMIT 失败、read/delete 竞争及 Store inode 替换。最终 FS race：5 top /5 named PASS；FS 源码此后未变。
- Docker Linux arm64：最终 FS5 top/5 named、计费修正后 Store6 top/16 named、公开 CLI1 top/1 named，全部0 fail/skip。公开路由和FS源码在计费修正中未变；Store使用新计费快照重验。缓存镜像、禁网、非root、只读根、cap-drop与限额参数见各 receipt。
- 完整 `go build ./...`、相关4package `go vet` 通过；计费修正后重新完整build及Store vet通过。Go1.26.8、Mac `TMPDIR=/private/tmp`。
- 独立审查首轮发现漏收 phase8字节、4个旧迁移fixture上界和2个故障fixture未到预期状态。独立表列计费检测真实 RED3144/3152与旧迁移实际失败均保留；修复后增量独审 PASS，无剩余 blocker。

公开 CLI 验收复用现有受控夹具：真实 CLI 子进程、IPC/Coordinator及原 Host 身份 helper 的 original Wait；完成事件由 Store 夹具受控提交，不冒称真实 Provider 工作执行。文件恢复验收的子进程在实际 chunk unlink 后 `os.Exit(91)`；父进程验证 marker 存在、manifest 保留、原 chunk 已缺失，确认原 Wait 后重开回放，不是仅回调返回错误，也不是断电实验。

4个本任务 Linux 容器均终态exit0/nonOOM、按精确CID删除且ROOT再次inspect确认不存在。4个owned测试二进制经build/receipt SHA一致核验后删除；原始日志、receipt和必要测试源码保留。见 `data/linux/root-verification.json` 与 `data/final-receipt.json`。

保留失败：旧schema30迁移测试成功重试后仍硬编码version30，修为currentSchemaVersion且原proof数量断言保留；旧3项FS普通证据不能证明实际partial unlink，最终夹具已修。全量Store、自动TTL、其他artifact删除、2GiB/洪峰/最新候选资源资格、物理断电、同UID恶意ABA、Intel Mac/Windows原生均未在本批验收。无真实模型/账号、用户安装替换、推送或发布。

本地集成：merge `7b94272`；14个生产Go SHA逐项匹配，整个 `tools/orchestrator` 源码与已验收 `31bace8` 一致。合入后公开 `TestDiagnosticsPublicCLIDeleteAfterOwnerCompletion` 1 PASS/0F/S，原始 `integrated-public.jsonl` 与 `integrated-receipt.json` 已保留。未重复不受影响的全套测试。
