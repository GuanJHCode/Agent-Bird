# ROOT Store recovery handoff 验收

源码检查点5e5cbb4，production与最终test-delta独立审查均PASS；17 Go及Linux runner共18个hash固定在root-final-receipt.json。

| 验证 | top PASS | named PASS | FAIL / SKIP | 用时 |
| --- | ---: | ---: | --- | --- |
| 新handoff普通 | 17 | 129 | 0 / 0 | 13.697s |
| 完整Store | 547 | 1813 | 0 / 0 | 258.556s |
| 受影响Store race | 33 | 164 | 0 / 0 | 763.152s |
| Linux arm64 offline | 33 | 164 | 0 / 0 | 容器exit0 |
| unchanged v2 hash race | 1 | 1 | 0 / 0 | 1.397s |

Go1.26.8，最终完整Store使用TMPDIR=/private/tmp。第一次完整运行误继承 /var/folders/... 的symlink祖先，引发artifact/diagnostics安全路径拒绝（1253PASS/532FAIL）；原日志保留为root-full-store-noncanonical-temp，不算通过。代表性用例在canonical tmp验证后完整重跑获得上表结果。早期API undefined、empty selector设置错误、schema表缺失、真实Applied/Attach绕过与epoch错误均各自保留，不混称行为RED。

四个旧Store fixture显式补Coordinator-observed handoff facts，原游标/预算/COMMIT语义断言保留；真实COMMIT现在明确要求FOREIGN KEY错误，并检查外部与池连接零残留。没有把这些Store fixtures声称为FS CAS。schema27拒绝无FS凭据的旧applied/control2/retired successor，不回填。

Linux CID10e0fd0c366a7058c278d152a69cdd20987b8f758e9ab5c62dcac4c8119d777d，network none/read-only/nonroot，exit0非OOM，精确rm/inspect absent；binary按receipt hash核验后删除。vet Store/contract与whole Go build均exit0。

DB阶段至此验收；RPC/Host runtime接线仍在独立owner-recovery-rpc工作区，接线及其原生验收前不称B3/F10闭环。Bound未Applied死亡、preBind lost original Wait、pendingauth后origin rebind仍另有开放契约。
