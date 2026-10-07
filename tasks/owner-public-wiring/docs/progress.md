# Public Resume wiring progress

Basefe2a598 includes accepted Store immutable live Resume and durable CLI journal. ROOT only writer. Five WIP Go files: Store owner_resume_target.go/test, Coordinator owner_resume.go/test, server.go Resume route. No CLI glue/new Host admission/scope migration yet.

Initial private target1topPASS. Coordinator real signedRPC ownerchild with kernelAlive/Exited plus synthetic interrupted task/trusted peer context: first3topPASS1.583s, authrotation+legacy4topPASS1.997s. Cannot claim fullpublicpeer acceptance from those direct control tests. Original owner children finishnaturalEOF/Wait0.

Independent review found real outerroute blocker: handleControl generic ValidateDispatchPeer ran before immutable lookup. It defers unmatched peers during unidentified active segments (but existingorigin ancestry can pass). New real Unix socket regression with synthetic Storebound/receipt and unrelated genuine StoreClaim has clean RED owner_registration_deferred. Fixed onlyKindResume moving generic gate afterauthenticated lookup miss, preservingallfresh modes; kernelauthorizePeer/workerrole remainsouter. Six-top green0fail/skip2.507s includes old unidentified-launch gate. IndependentdeltaPASS.

Coordinator affectedrace +vet/build +Linuxcrossbuild currentlyrunning; sourcefrozen. NeedLinuxactual, private targetrace, finalreceipt beforedependent wiring. JournalfinalcapCHOWN accepted separately: Linux4UIDpreviousskip actuallypass; journal securityPASS,macrace58P/4SKIP andLinuxnonroot58P/4SKIP+UID5P0SKIP. NoProvider/account/install/push/release.

Open: deadBoth+existinglease actualroute, separatepending successorHostsession+StorePreview/Preparepromotion, signedrecovery-scope localCAS (ordinarycommit forbidsScopechange), publicCLIjournalordering/startupselector. Crashgapboundinc2beforeledgerinc1→2persistrequiresdurablemigrationreceipt/two-phase reconcile; preBind originalchildhandleloss andpendingauth/originrebind remainOPEN. No unsafe cleanup/reset or fakeWait.

2026-10-08 06:21 router阶段最终检查点：Coordinator macrace8/8、Linux8/8；Store targetrace1/1，0fail/skip；vet/build0，独立deltaPASS。数据/边界见router-validation.md。Linux容器与owned binary按receipt清理。新Host admission/CLI/scope仍后续WIP。

共享DTO冻结：HostHello.ResumeCommandID可选字段是stale-start selector；HostIdentitySHA256清除selector保持v1，recovery scope不含selector。HostPresenceResponse.OwnerPhase/SuccessorCommandID由服务端填充。真实behaviorRED是selector改变identity/disposition，修后完整contract race {'pass': 291, 'fail': 0, 'skip': 0}。独立小delta审查PASS。新增Store OriginalOwnerResumeRequestSHA256纯wrapper供CLI核对回执，原hash校验/编码不变。
后续状态约定：OwnerPhase=succession_pending仅无current且唯一未消费授权；intended/bound为current owner phase，SuccessorCommandID是其incoming授权命令（初代为空）。CLI successormode只有command精确一致+succession_pending+old_host_exited才一次coldstart，将同一command放Hello；intended/bound只等待、不创建另一signer。旧receipt与当前command不匹配只读返回原receipt。Coordinator新Host admission仍必须重新从durable launch解析并exact匹配，Presence本身不构成授权。

Successor admission最终验收见admission-validation.md/root-admission-receipt.json。ROOT所有本批测试/容器terminal，无owned binary残留；Source冻结，等待合并CLI后的公开integration。文件handoff worker在独立tmp/owner-recovery-handoff实现，不在本批closure内。
