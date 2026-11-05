# T06A r2 independent re-review — PASS

适用快照：HEAD `a14affc483ae79522197d1adaaed35b7118b8d1c`，r2完整patch SHA256 `8d5653e439738ddba339c0b93533379e07fe285fcc03ea0379ba6c70b1cd0177`。本轮未改产品源码、未commit/push、未运行模型。原r1失败报告保留于`../review/review.md`。

## P2 已关闭

`plugins/codex-orchestrator/scripts/native-product-bridge.py:822-834`新增`wait_decision_with_metrics`；仅包围`replace_private`的局部try捕获`EntryError/OSError`，等待本身及业务强验证不在该捕获内，`CancelledError`未被吞。`:1217-1230`接入wrapper，原决策验证与ACK顺序保持。未知0644 metrics文件仍由原private-file校验拒绝写入，未chmod、替换或删除；观测失败不影响授权动作。

原真实driver反例已被永久正向测试覆盖并由本审查独立重跑：`tools/native-product-bridge/test_local_product_driver.py:436-463`复用完整本机driver流程，在合法decision前创建`{}`/0644 metrics文件，实际完成completed和ACK，并现场断言原文件内容及权限不变。它继续执行原fixture对合法决策、单次turn/start、ACK和最终ledger状态的断言。

`test_wait_performance.py:177-214`的6组真实wait异常×metrics EntryError/OSError测试及writer CancelledError测试全部独立通过：owner_detached、调用者取消、决策文件private_file拒绝均保持原异常；writer的取消也不会被宽泛吞掉。未发现尚存的P2反例或新增需修问题。

## 实际验证与证据

- 新跑`focused.xml/log`：45 tests / 45 passed / 0 failures / 0 errors / 0 skipped，36.18s。范围为`test_wait_performance.py`（27）、`test_entrypoint.py`（5）、`test_local_product_driver.py`（13，包含全部12恢复场景及原反例正向回归）。使用指定venv、TMPDIR=/private/tmp、PYTHONDONTWRITEBYTECODE=1和已核hash的本树product binary。
- 直接解析r2实际JUnit：`r2/data/red.xml`为1 test/1 failure；`r2/data/final.xml`为76 tests/0 failures/0 errors/0 skipped。76项是实现者受影响四suite证据；本审查新跑45项，不混称76项独立复跑。r1全量301pass/8skip仅复用不受本次wrapper改动影响的既有范围，未称新跑全量。
- `check_evidence.py`从HEAD原生产文件及r1原patch独立重建r1文本，先核其完整SHA为`6cc4d716a2854e298c51753f2141d53b50d55a49a8ce576618b2539294f318d6`，再核r1/r2 `file_identity`至`wait_decision`完整连续代码块逐字相等；core SHA为`143b5e528fa2d69b0cd2b1bbd63f06de2cb2dbb9bf21b6898d7984500ab5977a`（不含尾部空行）。因此原benchmark核心计数/分布及r1独审3样本计数可复用；没有声称r2重新测量metrics flush或端到端时延。
- `source-before.json`及`evidence-check.json`：审查前后4份源码/测试SHA全部与r2 manifest相符，patch SHA相符；binary真实存在且SHA仍为`911b570f92d128dd6e378f285dc77353fb8ff383236419aedaa78b7859bbd6b7`。Go源码/二进制无改动，不重复Go build/tests。
- 所有本轮工具会话已结束。仅写外部review-r2证据；本任务pytest basetemp清理，日志/JUnit/检查脚本/JSON与报告保留。原r1证据未覆盖。

## 验收边界

r1关于heldFD/hash、身份与绑定文档watch、退避响应、恢复稳定前置及progress摘要不等同liveness的审查结论继续适用。未运行live parent/TUI、真实超过5分钟native模型、provider模型或8项既有opt-in。本次PASS仅针对冻结r2代码与已列有效证据，不替代后续集成或native验收。

## 主会话集成签收

root核对集成4文件与冻结manifest逐一相同，复核等待FD/hash/policy边界及局部best-effort metrics不影响业务异常。当前集成树新增wait与真实driver metrics恢复28项全PASS，0failure/error/skip，工具进程完整结束。Go源码未变，复用已签收T05集成binary，实际SHA与命令记录见data/public-evidence/T06A-integrated.json。性能benchmark仅核心校验样本，未称metrics flush、模型或端到端收益。T06A范围签收PASS，允许commit并push；最终包/native/live仍后验。
