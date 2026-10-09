# F01 里程碑记录

contract/process/Host 已完成；Store/公开完整链尚未实现，未声明 F01 完成。

- typed proof 固定原 cmd.Start 边界、Context/完整 Command，单次消费；已启动 Handle 保留原 Wait/Stop。
- Host 发布独立 validation_start 事实，执行 private sealed native 入口；冷恢复不重建 live proof。
- 最终本里程碑 `milestone-host-green`：8 top /49 named PASS，exit 0。以下保留所有此前失败，不以中间批冒充 final。

- `contract-process-green`: exit 0; top {'pass': 3, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `contract-process-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 4, 'fail': 2, 'skip': 0}.
- `host-green-02`: exit 0; top {'pass': 5, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `host-green`: exit 1; top {'pass': 4, 'fail': 1, 'skip': 0}; named {'pass': 21, 'fail': 2, 'skip': 0}.
- `host-red`: exit 1; top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `milestone-host-green`: exit 0; top {'pass': 8, 'fail': 0, 'skip': 0}; named {'pass': 49, 'fail': 0, 'skip': 0}.
- `sealed-security-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 0, 'fail': 3, 'skip': 0}.

`contract-process-red`/`host-red` 包含新 API 编译 RED；`host-green` 失败由本任务 sealed 0500 目录的测试清理权限引起，已按 owned path 显式清理修复。`sealed-security-red` 为 malformed native 与 writable sealed 的业务拒绝回归 RED。
实际 cmd.Start EAGAIN/EINTR 使用 per-call 受控 seam 注入，未声明自然内核资源失败。真实 Host+Git+Store 新 attempt 链待后续里程碑；无真实 model/provider 或安装替换。

## Store 与有额度的 Host 流式里程碑

ROOT schema34/API生产 `3c8a55b` 与DTO/hash、迁移证据已按固定提交cherry-pick；本worker只改独占Host/Store接线。

- Store 冻结完整source字节、原adapter/pin、input/vector、plan/work revision、原physical Host；新kind缺失/孤儿/retired binding拒绝，legacy不重建。full grant历史读取允许released工具身份，live仍需新reserved grant。
- Host 32MiB上限、64KiB流式copy/hash；完整binary不ReadAll。固定小native header/load/program command验证；private `0500`目录与tool。每版journal至多4096bytes（两版8192 escrow），精确inode/hash/目录身份删除并durable removed后才带cleanup发布LaunchRejected/Exited。UNKNOWN保留copy/预约；已执行Handle只由原Stop/Wait/tree证据清理。
- 原cmd.Start前的普通refusal也先absent/removed journal+cleanup。sandbox profile末尾保护整个`.validation-tools`树，实际profile self/peer/ancestor actor尚待下一批。
- 真实Host+Git+Store新attempt完整链尚未验证，本里程碑不称最终完成。

- `store-bound-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 2, 'fail': 2, 'skip': 0}.
- `store-chain-01`: exit 1; top {'pass': 1, 'fail': 1, 'skip': 0}; named {'pass': 9, 'fail': 2, 'skip': 0}.
- `store-chain-02`: exit 0; top {'pass': 3, 'fail': 0, 'skip': 0}; named {'pass': 19, 'fail': 0, 'skip': 0}.
- `store-chain-diagnostic-01`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 0, 'fail': 2, 'skip': 0}.
- `store-facts-green-03`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 4, 'fail': 0, 'skip': 0}.
- `store-guard-green-02`: exit 1; top {'pass': 2, 'fail': 1, 'skip': 0}; named {'pass': 19, 'fail': 2, 'skip': 0}.
- `store-milestone-01`: exit 1; top {'pass': 3, 'fail': 2, 'skip': 0}; named {'pass': 25, 'fail': 4, 'skip': 0}.
- `store-policy-green-02`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 8, 'fail': 0, 'skip': 0}.
- `store-policy-green`: exit 1; top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `store-policy-red-02`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 6, 'fail': 2, 'skip': 0}.
- `store-policy-red`: exit missing (launcher未执行Go); top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `integrated-store-host-01`: exit 1; top {'pass': 7, 'fail': 1, 'skip': 0}; named {'pass': 47, 'fail': 8, 'skip': 0}.
- `host-boundary-green-03`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 8, 'fail': 0, 'skip': 0}.

所有FAIL保留：`store-policy-red` launcher误传`--`，Go未执行，仅有command/空输出，没有原exit文件，不补造；`store-policy-green`为validOID未定义编译失败；`store-chain-01`/diagnostic01为strict JSON walker拒绝source合法null，已用完整[]byte/base64冻结修复；`store-bound-red`为bound missing/emptydecision真实业务RED；`store-milestone-01`包含测试未核consumed退休CHECK及公开artifact wrapper转换底层COMMIT错误的问题，修正为精确schema断言和原intake回调的真实FK COMMIT；`integrated-store-host-01`只有Boundary夹具缺HostArtifact grant，Boundary后续1top8named PASS。`store-guard-green-02`的missing_tool被schema retained trigger先拒绝，已显式断言该防线，再于隔离测试DB模拟缺行检查独立intake gate；事实guard后续1top4named PASS。其余该FAIL batch具名PASS可据实际delta复用，未宣称整个batch通过。

跨physical限制：原event已入Store则exact ACK replay；仅旧spool已durable但Store未记录，在新PID/birth物理Host重绑后保守拒绝释放/自动retry。可写spool JSON不能证明原publisher；冷journal不是original Wait。实际Start错误为per-call受控注入，不称自然内核资源EAGAIN。

## source 真实形状与 sealed prepare 回归

`source-artifact-red` 为真实 gitops.CandidateArtifact producer shape，1 top / 2 named FAIL；修复只按实际内层 Candidate.Binding 与原 source adapter/provider lock 认证，不要求 producer 不存在的 outer binding/pin 字段。compose 保留原 outer binding 与 receipt 摘要认证。
`sealed-prepare-red` 为 seal 成功后真实 sandbox/profile 准备失败，1 top / 3 named FAIL。新工具 grant 作用域普通 pre-Start prepareErr 统一进入 finalizePrelaunchRejected；cleanup/persist 失败发布 UNKNOWN 并保留额度，不扩大旧无工具 Provider 行为。
最终增量 `source-prepare-green`：2 top / 14 named PASS，exit 0，无 FAIL/SKIP。此前两批业务 RED 原始 stdout/stderr/exit 全保留。
recordLaunchFailed 的 exact terminal replay 在扫描到已有 LaunchRejected 时直接返回，发生在 cleanup 之前；source runner 后续同 tuple 调用不会再删除或重复退款。

private Go-only per-call validationStarter 只接受有完整工具grant的受限validate invocation；public ExecuteLaunch 使用零control，无request/env/global选择器。ordinary invocation负例 `host-control-green` 1top1named PASS；API编译RED `host-control-red`保留。真正fault→新attempt链仍待native测试，不以此单门禁冒称完成。

## 真实 native/Git/公开 Store 链与 profile 操作 oracle

受控native actor由项目Go1.26.8构建，不调用provider/model/network。source实际Host profile+Managed Git freeze，validate实际materialization+sealed copy，原sandbox wrapper cmd.Start per-call注入EAGAIN（不称自然内核EAGAIN）。Store原事件ACK/replay与原tool raw释放、独立candidate_validation_retry队列、公开CollectPending/AckDecision处理旧拒绝、新attempt新grant/新workspace成功、ValidationEvidence原命令/pin、原Handle Wait/tree Exited cleanup、owner accept全部闭环。旧拒绝runtime_events仍留审计行、delivery_status=acked；两份removed journals各8192metadata escrow仍保留。exact重复Host recordLaunchFailed不会重复cleanup/publication。

实际Host profile actor以StartDuplex原Handle/Wait/tree闭环：无sandbox同UID正对照self chmod/unlink、parent/ancestor chmod、peer chmod/write/unlink七操作errno均0；actual profile七操作均EPERM=1。只声明这七种原生操作实证；不以self overwrite的ETXTBSY或签名限制作为profile证明。peer/本profile夹具使用测试专用完整grant，无Store额度准入宣称；Store真实额度在前述完整链验。

跨physical冷spool反例：新PID/birth后原未入Storeterminal拒绝、不记录event、不退款、不排队，reserved保留；只有Store已durable的exact event replay许可。

- `native-chain-01` exit 1; top {}; named {}.
- `native-chain-02` exit 1; top {}; named {}.
- `native-chain-03` exit 1; top {}; named {}.
- `native-chain-04` exit 1; top {'fail': 1, 'pass': 1}; named {'fail': 1, 'pass': 3}.
- `native-chain-green-05` exit 0; top {'pass': 1}; named {'pass': 1}.
- `native-final-focused` exit 1; top {'fail': 1}; named {'fail': 1}.
- `native-chain-green-06` exit 1; top {'fail': 1}; named {'fail': 1}.
- `native-chain-green-07` exit 0; top {'pass': 1}; named {'pass': 1}.
- `store-physical-cold` exit 0; top {'pass': 1}; named {'pass': 2}.

所有失败保留。native-chain-01/02/03为新增测试对既有process API/事件常量拼写误用编译失败；04完整新attempt执行已达Result/Exited，但末尾测试错误假设CollectPending仅1event，实际旧拒绝审计事件仍pending。05修为精确当前attempt Result后PASS。后加公开旧拒绝ACK/audit/metadata断言的native-final-focused误用不存在kind SQL列且混合subtest regex未选Store，06修为JSON后字段大小写错误；07改用实际snake kind后完整链PASS。Store反例独立精确subtest跑2named PASS。上述测试错误不算生产业务RED，不隐藏、不反复称final。最终race待固定test快照一批验。

## 最终race与native测试清理收紧

clean d663785 的 `final-runtime-race` exit0：Host7top33named PASS、Store8top48named PASS，合计15top81named，0FAIL/0SKIP（不含package pass）。stdout SHA256 `ad273f45cfaee6627b2e999ec4a2a23d0a207cff35bf3636ac34f4c766eb8c42`。该batch未选择TestValidationStartFactsRequirePinnedActionAuthority，其此前store-facts-green-03的1top4named PASS不受后续production delta影响可复用；schema相邻历史migration也复用ROOT证据，不称全Store。

独审发现测试cleanup风险：t.TempDir在UNKNOWN/Wait失败后会自动删除actor现场。仅test-only改为owned os.MkdirTemp+目录inode身份guard；任何实际Start/Host call登记pending，只有原Wait/tree或原live typed never-started确证才解除。setup Go build也使用原process.Handle Wait/tree；测试失败或任何pending均保留并在raw记录精确root，不chmod/delete未知copy。只在所有原进程确证且测试成功后改变0500目录权限并RemoveAll。

`native-cleanup-01`定向race 3top5named PASS，exit0，0FAIL/SKIP：完整公开native链、七操作profile正对照、一个真正活original actor未收到stdin/尚未Wait时cleanup拒绝并保留copy；之后原Stop/Wait/tree确证，模拟failed仍保留，再于已确证成功路径删除。新cleanup源须独审，不重复未受影响Store race。生产仍b5899d2。

cleanup增量复审删除unsandboxed子例旧无条件t.Cleanup chmod一行，避免failed/pending时绕过父guard；所有chmod/RemoveAll统一由confirmed安全分支执行。`native-profile-cleanup-02`仅重跑受影响profile test race，1top3named PASS exit0；完整chain/Store证据按未受影响源码复用，不机械重跑。
