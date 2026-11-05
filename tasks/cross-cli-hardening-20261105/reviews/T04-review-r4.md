# T04 r4 独立复审：CHANGES_REQUESTED

冻结base2347d34ce1e5ca4e40751f53064f3d99001f4832。产品patch SHA2568e5217b8b5eb53b7c5ac56e788c97ba44473f8bf1272773f4da1197a9e848e0b；source manifest SHA256a8d8ef663e6b91d233b8c1d4452fe2874a67603b0b4d34bbe191e8222396fe19。初始逐文件验证零差异；r3→r4仅internal/store/rework_objects.go和rework_refs_test.go变化。依赖helper SHA256d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150与legacy QueueRequestChanges tx hook保持，均不计为T04新产物。详见hash-before.json，末尾有最终核验。

## 原缺陷修复确认

r3额外hardlink导致GC释放仍存在正文的配额已修复。原独立overlay不作修改重新运行（original-probe.json）：GC返回rework_cleanup_identity_unknown，224151字节正文保留、object_rows=1，global charge239579→239579，released=0，1项顶层PASS。

完整.stage/.json集合preflight要求所有已知路径属于记录inode、nlink精确等于已知路径数；held no-follow FD在unlink与directory fsync后要求nlink0，才DELETE registry。永久用例覆盖stage和published-pair额外link、检查后新增link、无已知路径拒绝猜测释放、显式恢复身份后清理；合法publish两link恢复仍通过。非零inode没有已知路径时保留charge是合理保守取舍：包括unlink完成后DB提交前crash，后续无法证明无未知alias，因此不会自动回收。inode0且无路径属于未创建reservation，可安全按单writer合同释放。

## 仍须修复：[P2] 检查后路径替换会删除未证明的inode

位置：tools/orchestrator/internal/store/rework_objects.go:715、:723、:726。preflight和循环Lstat验证的是记录inode，但root.Remove仍按可变路径执行。held-FD的最终nlink检查可防止错误释放quota，却发生在未知替换文件已被unlink之后。

独立确定性本地复现：begin→完整chunk→abort；利用现有gc_before_unlink故障点，在Lstat之后将登记.stage rename到同一root其他名字，再在原.stage创建不同inode文件。GC删除该不同inode的文件；最终因为原登记inode仍有link而返回rework_cleanup_identity_unknown。观察foreign_stat=ENOENT，registered_stat=nil。这违反原始“GC不得删unprovedinode”的明确合同；它与已修复的quota误释放是两个不同后果，当前quota保持安全。

复现需要同UID外部路径变更，不涉及公开RPC扩权；其适用边界与既有symlink/hardlink/replaced-identity故障验收一致。review-r4/probes/overlay.json仅向外部测试副本追加TestReviewGCLatePathReplacementNeverDeletesUnknownInode；源码未变。late-path-replacement.json记录1项顶层FAIL，reproduce-late-replacement.sh直接复现。

应在删除前安全取得待删目录项并验证所取得inode；单纯再加一次Lstat仍保留check/unlink窗口。若使用隔离rename，需要no-overwrite目标及持久化恢复/计费合同，并在隔离后验证inode才unlink；未知替换文件不能被当成已验证原文件删除。根会话已确认此反例属于原合同必须修复的范围；不能以held-FD配额保护冒充未知inode未被删除。

## 其余合同与复用边界

r4仅GC变化，已重新对比r3/r4完整产品patch；之前读审的v1/v2字节/digest兼容、显式v3升级门禁、不可变rev1全prompt、全部历史conditions顺序、body/manifest/ref/chunk/IPC/payload与实际stdin大小限制、Host可信root与同bytes hash/parse、schema11 reservation/metadata/tombstone计费、pin/决定/revision/ready/revoke事务、owner snapshot/replay、Host queue/claim/pending/reconnect/summary门禁与精确旧revision ACK均未改动。新的正常套件重新覆盖Refs、legacy owner、golden、ACK/Delivery；race重跑全部Refs生命周期。

r3全量race1307 pass events/8skip（593顶层PASS/8skip、14测试package PASS+contract无测试skip）仅作为未受GC改变部分的历史基线；不冒充r4全量。r3期间交叉写测试的66normal/21race仅诊断，完全不复用冻结验收。未运行真实provider/model/付费调用、安装包v3端到端、native live-parent/typedresume，不对T07组合运行时或全项目签收。

验证环境：Go1.26.10 darwin/arm64，GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp，独立于实施者的review-r3/gocache缓存，PATH含固定Codex0.166.0。只写外部review证据，不改产品、不派生、不commit/push。实际计数在所有会话结束后追加。

## 全部会话结束后的最终证据

- normal.json：events={'pass': 171}；top-level={'pass': 62}；package结论={'codex-cli-orchestration-design/tools/orchestrator/internal/adapter': 'pass', 'codex-cli-orchestration-design/tools/orchestrator/cmd/orchestrator': 'pass', 'codex-cli-orchestration-design/tools/orchestrator/internal/host': 'pass', 'codex-cli-orchestration-design/tools/orchestrator/internal/store': 'pass'}。
- race.json：events={'pass': 45}；top-level={'pass': 20}；package结论={'codex-cli-orchestration-design/tools/orchestrator/internal/store': 'pass'}。
- original-probe.json：events={'pass': 1}；top-level={'pass': 1}；package结论={'codex-cli-orchestration-design/tools/orchestrator/internal/store': 'pass'}。
- late-path-replacement.json：events={'fail': 1}；top-level={'fail': 1}；package结论={'codex-cli-orchestration-design/tools/orchestrator/internal/store': 'fail'}。

正常/race/原始hardlink反例命令均exit0，0fail/0skip；新路径替换oracle命令exit1、1项FAIL，稳定暴露未知inode误删。所有本审查启动测试会话均已结束，无遗留测试。hash-after.json逐文件复核仍零差异，产品与测试全程稳定；本轮为有效冻结r4证据。构建缓存保留供后续复审，probes/原始JSON/hash/报告均为必要证据，无产品临时文件。

最终结论：CHANGES_REQUESTED。原quota缺陷已修复；路径替换误删未知inode必须修复。新协议需重新冻结，并对隔离/crash/quota恢复及本反例做正常/race验证与独立复审。
