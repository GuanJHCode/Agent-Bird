# T06A independent review — CHANGES_REQUESTED

审查快照：HEAD `a14affc483ae79522197d1adaaed35b7118b8d1c`；patch SHA256 `98cf053956ca471a9e392130c8321dc87c5dbb4d3a3804941f10a9a113fc1135`。4份源码/测试文件与提交的source-manifest一致，独立验证后再次核对一致。未修改产品源码、未commit/push、未运行provider模型。

## 必须修复 P2：观测写失败阻断合法决策和 ACK

位置：`plugins/codex-orchestrator/scripts/native-product-bridge.py:1214-1216`。`wait_decision`外层finally无条件`replace_private(decision-wait-metrics.json, ...)`。这项观测写入抛出`EntryError`或`OSError`会覆盖等待结果/原异常，并使已取得合法decision在执行`client.ack`前失败。

真实最小反例：复用完整本机owner-bound driver fixture，正常到达owner decision；提交合法decision前在私有driver_root创建`decision-wait-metrics.json`，内容`{}`、权限`0644`；其余业务输入保持原样。driver实际报告`status=failed,error=driver_status`且ACK文件数0。无需改产品源码、mock产品逻辑或模型。外部复现测试`test_metrics_failure.py`，实际执行结果`metrics-failure.xml/log`：1 passed，1.59s；这个passed表示成功断言缺陷发生，并非产品通过。`metrics-failure.json`只保留必要结果字段。报告后仅将复现记录字段收窄以避免无关能力路径，未再次执行此纯记录调整。

建议最小修复：仅使metrics持久化的`EntryError/OSError`不影响主流程；不得吞`CancelledError`或业务核验错误，不得降低决策/文件权限/owner强校验。补充观测写失败不阻断合法decision、且不覆盖原等待失败/取消的回归，然后复验相关范围。

## 其余关键结论

- `native-product-bridge.py:685-819`：每wait至多3份unique executable heldFD；直接FD hash；身份包含dev/inode/uid/mode/nlink/size/mtime/ctime，初次核fstat/pathstat、hash后再次核身份。高频保留每role真实PID对应birth/exepath元数据、完整persisted attachment和socket身份检查；决策边界force重新hash。相同路径替换、原地写、恢复mtime、mode/symlink变化均fail closed；等待终结finally关闭heldFD。该UID逻辑与既有process_inspector一致，未宣称新增kernel UID读取。
- `:774-786`与`:1204-1212`：4份已验证绑定文档检查初始与强边界digest，中间核完整metadata；不静默刷新授权或跨等待持久化cache。send/history/ACK/no-resend产品证明路径未改。
- 50/100/200ms退避仍有轮询fallback；取消、owner/socket变化以及真实/bin/sleep退出覆盖有效。
- `test_local_product_driver.py:258-300`：修订等待原worker进入attempt=result_ready、active segment=0、budget全settled，再抓取业务快照及重启，符合“恢复不得改变既有稳定业务状态”的前置条件。gated真实worker先观测running/active/reserved，释放gate后证明自然状态推进；这证明旧运行中快照不适合作为稳定不变oracle，没有声称重放相同marker窗口。`:319-335`与`:404-418`仍核稳定snapshot、no resend/no ACK、仅一次submit、无业务rebind等原断言。独立运行12恢复场景全部通过，未见此修订掩盖产品缺陷的证据；仍未覆盖running态与恢复真正同时竞争的新产品合同。
- `tools/orchestrator/internal/.../host.go:463-488`：20次progress摘要后只等待progressDone，实际worker生命周期由p.Wait决定。既有虚拟超过300秒无progress等待测试通过；不当作真实5分钟模型运行验收。

## 独立验证证据

- `focused.xml/log`：37 tests / 37 passed / 0 failure / 0 error / 0 skip，34.94s。范围为test_wait_performance.py、test_entrypoint.py、test_local_product_driver.py，包含20个wait测试及全部12个真实恢复场景。使用指定venv、TMPDIR=/private/tmp、PYTHONDONTWRITEBYTECODE=1、已核hash的本树product binary。
- 直接解析实际`data/final.xml`：309 tests / 301 passed / 0 failure / 0 error / 8 skip；已与validation-summary及binary SHA核对，不将其当本审查新跑全量。8 skips为原native auth/policy/prepare/package opt-in。
- `evidence-check.json`：独立3组真实当前Python kernel metadata与UDS对照，每组20cheap checks+1force，baseline hash=63，changed executable hash=4、bytes=206336、metadata=63、binding hash=8。独立复验计数未声称重新验证原20样本p50/p95分布。
- 原benchmark脚本/数据已读：20同机样本p50 149.223→136.196ms、p95 191.050→178.566ms仅本地验证工作，不含退避或模型；三role共用一个exe。其CPU为self user/system，physical I/O block为0；代码hash bytes不能解释成实际磁盘I/O收益。probe/help、spawn/first_event/history阶段未优化，不将差值算收益。
- `source-after.json`：审查后4份源码hash全部一致。所有本审查工具会话已结束。

## 未验证范围

未运行付费/真实provider模型、live parent/TUI、真实超过5分钟native模型任务或8项opt-in；未重跑Go tests/build（Go源码未改、binary SHA与提交证据一致）。metrics修复尚未实现，本报告不能作为修订后的通过依据。

外部review目录保留复现脚本、日志、JUnit、JSON和本报告，均为交付/复核证据，无待清理的本任务临时文件；测试创建的本机临时目录由fixture清理。
