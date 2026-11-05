# T06A r2 metrics failure correction

状态：P2修复完成，受影响范围验收通过；等待另一agent复审和root签收，未commit/push。

## Change

`native-product-bridge.py`新增`wait_decision_with_metrics`，封装既有等待与finally的metrics落盘。仅`replace_private`观测调用的`EntryError/OSError`被忽略；正常decision、原owner_detached、原决策文件权限错误、CancelledError均保留。原私有文件核验与写入权限没有放宽；未知0644指标文件不覆盖、不chmod、不删除。观测文件仍仅最新单份、有界。

`AttachmentWait`、`wait_decision`及artifact hash/FD/force/policywatch/退避全部逐字未变。`benchmark-reuse.json`通过重建r1 patch并核对其完整生产文件SHA，证明上述核心代码相同SHA `143b5e528fa2d69b0cd2b1bbd63f06de2cb2dbb9bf21b6898d7984500ab5977a`。原benchmark只复用这些核心本地工作的计数/分布及未优化阶段的历史观测，不宣称重新测了r2 wrapper时延。

## Regression and evidence

- 将独审原反例永久加入`test_local_product_driver.py::test_metrics_failure_preserves_valid_decision_and_ack`。真实完整driver到合法decision前，仅创建0644 metrics文件；未修时`red.xml`:1 failed/12 deselected，1.64s，实际driver failed。修复后同fixture完成completed与ACK，并在现场检查原metrics内容`{}`及0644权限不变。
- `test_wait_performance.py`新增6场景：真实等待owner_detached、cancel、决策文件private_file拒绝，分别遇观测EntryError与OSError时保留原异常；另1场景确保writer产生CancelledError也不会被吞。
- 初轮`green.xml`为6pass/32 deselected（尚未加最后2个决策权限错误场景）；最终受影响4suite `final.xml`：76 passed / 0 failures / 0 errors / 0 skipped，37.685s，含27 wait、5 entrypoint、31 deadline、13真实driver（全部12恢复/门控样本+新增独审真实反例）。
- 原独审诊断脚本断言“发生缺陷”故其passed代表重现红；本次采用同场景永久正向oracle完成ACK，不把旧脚本的预期缺陷作为新产品通过。
- Go代码/二进制未改；复核binary SHA与r1 build一致，复用其构建证据。r1 broad 301pass/8skip未冒充r2新跑；新跑范围如上。未运行Go suites、live parent/TUI、付费或provider模型。

## Frozen delivery

- HEAD `a14affc483ae79522197d1adaaed35b7118b8d1c`。
- 完整含新增文件patch：`data/implementation.diff`，SHA256 `8d5653e439738ddba339c0b93533379e07fe285fcc03ea0379ba6c70b1cd0177`。
- `data/source-manifest.json`列全4文件SHA；`r2-vs-r1.diff`只展示r2生产修订；`validation-summary.json`列实际报告计数、时间、SHA与命令。
- 所有测试会话结束，最终源码未再编辑。仅清理本次3个pytest basetemp，红绿证据完整保留。未改项目task metadata。
