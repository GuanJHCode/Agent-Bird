# T03 独立复审与主签收

实现/root，独审/root/review_t03；r2冻结32文件，基线bde4bacf，patchSHA750d3ea6c58a3eae6d7274b26689197adfdd5bbea68b3bccac77303140af0200，manifest见T03-r2-source-manifest。结论PASS，初审唯一P2闭环。

独审核对新Store/CLI控制ID边界，原65455-byte Submit反例现invalid_dag拒绝，九类控制ID×六种超界/非法输入共54子项拒绝且runs/tasks/attempts/budget_runtime/policy全零。双adapter各精确16384 encoded bytes、最大HTML转义ID及AcceptedInput源event的实际Launch通过。旧row/receipt不改写，Claim/Pending最终launch校验保留。

独立focused148PASS/0fail/0skip、外部反例矩阵57PASS/0fail/0skip、精确adapter1PASS；实际报告与overlay保留外部tmp/review-t03/r2，公开摘要记录其SHA。32项manifest与patch均匹配。无其它未解决问题。

主会话自行读取并实施Store group policy、全部预算门禁、legacy迁移和输入校验，核对真实报告及最后source摘要；集成树在T01上应用32文件后重跑35相关项PASS/0fail/0skip、exit0，r2 vet与build均exit0，diff-check无异常。主签收PASS，批准提交及push本T03功能。

范围：unit/protocol-fixture，无模型、installed-package或原生TUI。修订前4包普通673PASS/4skip、race134PASS不冒充修订后全量；未改变预算部分证据可复用，新ID部分由当前相关回归/独审矩阵证明。最终冻结全量在T12执行。
