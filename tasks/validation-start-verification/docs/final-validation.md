# F01 validate 未启动重试：ROOT 验收

限定范围已完成，可本地集成。最终交付4ba68b6，生产b5899d2，最终测试6b17be9；独立生产和测试oracle审查均PASS。范围/用法见 `tasks/validation-start-retry/docs/{final-report,usage-and-limits}.md`。F01其他有副作用阶段不在本次完成声明内。

ROOT独立读取原始go test JSON/exit及源码差异：clean d663785 runtime race为Host7top33named、Store8top48named，合计15top81named PASS，0FAIL/SKIP。stdout SHA256 ad273f45cfaee6627b2e999ec4a2a23d0a207cff35bf3636ac34f4c766eb8c42。测试安全清理增量race3top5named及最后profile1top3named分别通过；这两批实际在提交前dirty测试源码执行，其固定commit差异和未变生产源码经复核，不冒称clean执行。计数包含顶层和子用例，批次之间有重叠，不求和成独立覆盖数。

ROOT另外执行并核实际exit0/空stdout/stderr：84e82a8 core Store/contract/adapter/process vet；b5899d2整模块build及Host/Coordinator/CLI vet；d663785 Host/Store/native actor测试vet；40123af清理增量Host/actor vet。最后一行删除旧无条件chmod仅影响测试清理，定向race已过，复用构建/vet。b5899d2后tools/orchestrator仅test/testdata变化，全部Go文件SHA和tree见data/final-source-receipt.json。未跑全Store或无关长测。

真实链完成原Host Git freeze/materialize、受控wrapper Start EAGAIN、同事务认证终态/工具清理/额度释放/策略入队、公开旧拒绝ACK、新attempt真实native成功、原Wait/tree、owner accept。Schema34每份raw最多32MiB、8192 metadata永久保留；UNKNOWN和新physical Host的旧未入Store spool均不退款。源字段形状与sealed后prepare拒绝两项真实业务RED已修，原FAIL均保留。

原生profile只有七项文件操作证据：无sandbox同UID全成功，actual Host profile均EPERM；不把self-overwrite、签名或动态依赖hermeticity算作实证。进程错误为private per-call注入，不是自然内核耗尽。受控actor不是实际模型。

ROOT核旧boundary exact tool/root以及活cleanup反例root均absent，`/private/tmp`无该native fixture前缀残留；未执行额外删除。测试遇UNKNOWN/pending/失败保留精确根，只有原Wait/tree或live never-started证明且测试成功才清理。记录在data/root-cleanup-observation.json。

当前未覆盖自然内核EAGAIN、真实provider/model/account、Linux Git链、Intel/Windows、断电、用户安装和新源码分发包；旧F13包不覆盖本次生产变化。未替换用户安装、推送或发布。所有早期失败、37批索引和局限随原任务文档保留。
