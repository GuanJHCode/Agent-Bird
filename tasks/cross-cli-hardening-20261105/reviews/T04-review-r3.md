# T04 r3 独立审查：CHANGES_REQUESTED

审查基线2347d34ce1e5ca4e40751f53064f3d99001f4832。冻结patch SHA256 1ed0d74272f733c59b0f8955e933b9202bf595e77a54825e7c387abeecb5166c；source-manifest SHA256 eff3409b1302403afd2458a369574a82e238e48c6d7c824426b389d5f709b83e。已逐个验证清单中全部实际文件，零不匹配。只读依赖owner_action_guard.go SHA256 d3704ec2d0d1ace2b2575af7ab53238b9868240ecd593afb27733d12f9649150完全相符，不计T04交付产物。

## 必须修复

[P2] GC在存在未知额外hardlink时释放仍占磁盘的对象计费。

位置：tools/orchestrator/internal/store/rework_objects.go:706（检查循环）、:716（缺少完整link集合验证）、:728（删除registry释放charge）。

gcReworkRefs验证已知.stage/.json的device/inode/uid/mode，却不证明inode的全部hardlink都属于这两个已知路径。因此有未登记link时仍unlink登记路径并DELETE registry，storage_usage触发器释放charge，而同一private rework-objects目录中的完整正文继续占空间。反复upload/abort/gc可留下不再受配额计费的对象；违反REW-04与ADR的“成功unlink并fsync后才释放charge”生命周期合同。

真实本地复现：refsFixture→begin→完整16KiB chunks→os.Link(registered condition.stage, root/unregistered-extra-link)→abort→gc。观察GC status=removed, err=nil；额外link仍为224151 bytes；condition registry rows=0；global charged bytes239579→7122（释放232457，其中含condition及brief）。未知link的condition正文仍在磁盘。

外部probes/rework_refs_test.go仅通过Go -overlay追加独立oracle，源码没有修改；reproduce.sh可复现，gc-hardlink-probe.json记录1顶层FAIL、0PASS、0skip（这是预期暴露缺陷的oracle失败）。

建议先完整Lstat/验证.stage与.json集合，要求两者若存在均为同一已记录inode且st_nlink恰等于已知现存link数，再执行任何unlink；未知link须保留charged deleting row并返回错误。覆盖1个已知link+未知link、合法stage/final双link恢复以及路径替换；不能仅将nlink<=2当成充分证明。

## 已检查的其他合同

- REW-01：rev1全prompt直接封存，无regex剥离；v3最多brief+两次condition，严格历史顺序；正文只在对象文件，DB/launch保持小引用。adapter/rework_refs.go:18、:136；store/rework_objects.go:254、:554。
- REW-02：v1/v2字段顺序及omitempty保持；新增固定v2 golden；旧v1/v2和无context高revision不自动升级。store/rework_objects.go:248；delivery.go:201仅扩充精确旧revision handled ACK允许的kind，未放宽tuple。
- REW-03：Host独立trustedState路径，no-follow/0600/0700/uid/regular/nlink1/device/inode/size/hash及严格body解析；相同有界bytes完成hash/parse/assembly；不授worker读控制目录。host/rework_refs.go:14，adapter/rework_refs.go:87。
- LIM-01：body256KiB、manifest8KiB、refs<=3、chunk16KiB、原64KiBIPC/16KiBpayload保持。commit在决定事务前检查组装以及实际adapter stdin512KiB；Host最终再次检查。现有Codex guard继承stdin、没有新增更大输入guard；本审查未扩大任何transport权限或限制。
- REW-04除上述缺陷：reservation先持久化再写文件；object size+4096与upload4096全状态/tombstone计费；同锁无跨RPC writer；known-inode no-overwrite publish、fsync；pin/decision/revision/ready/revoke在同事务；active/pinned/recovery保护；abort新command复用brief；当前owner snapshot在v3各写事务与replay、legacyQueue入口重核。queue/claim/pending/reconnect/summary能力门禁与已commit replay先于动态availability保持。

## 验证范围

独立正常/race正在结束，最终实际计数追加在下节。所有源码保持冻结r3；未使用凭据、模型或付费调用，不commit/push，不派生Agent。

实施报告的已结束原始JSON经本审查独立计数：normal-final-r3-store.json 130 pass events/47顶层PASS；normal-final-r2-focused.json137 pass events/41顶层PASS；race-final-r3.json1307 pass events/8skip，593顶层PASS/8skip，14测试package PASS；contract无测试package skip。build/vet r3 log为零输出，结合实施者退出0记录使用，不当作本审查独立运行证据。r1正常全量先于最后guards；按报告限定复用，不冒充最后树完整正常全量。

独立首轮normal.json缺失TMPDIR=/private/tmp，macOS长/tmp路径导致unix socket invalid argument及canonical worktree mismatch；保留原始失败证据，使用项目既定canonical TMPDIR重跑，不改变断言。

未验证：安装包/真实provider v3端到端、native live-parent回调、typed native resume；未对项目整体或T07组合授权代码签收。新GC修复需另冻结r4后复审。probes、JSON日志与reproduce.sh是必要复现证据；gocache仅外部可复用构建缓存，不属于产品交付。

## 协调状态更正

根会话告知：实施者结束自己的全race后，在本审查canonical正常/race会话运行期间追加了永久hardlink回归到internal/store/rework_refs_test.go；仅测试源码变动，产品未改。根会话现已再次冻结。编译取用源码的具体时点未作足够证明，因此本审查在途canonical套件即便退出0也仅计诊断，不能作为冻结r3独立验收。最初manifest全文件hash核验及GC overlay独立反例发生在该变动前，保留为有效证据。r4必须在所有写入停止、全部会话结束后重新稳定验收。

## 独立已结束会话实际计数（完成后解析）

- normal.json: events={'pass': 124, 'fail': 63}; top-level={'pass': 35, 'fail': 31}; packages=4.
- normal-canonical.json: events={'pass': 187}; top-level={'pass': 66}; packages=4.
- race-canonical.json: events={'pass': 61}; top-level={'pass': 21}; packages=3.
- gc-hardlink-probe.json: events={'fail': 1}; top-level={'fail': 1}; packages=1.

canonical normal与refs race均exit0、0fail、0skip；因上述并发测试源码改动，仅作为诊断。首轮normal exit1是canonical TMPDIR未设置的环境失败。GC独立oracle exit1稳定暴露登记被删除但额外hardlink正文仍存。所有本审查启动会话均已结束，没有遗留运行测试。最终再核source manifest，唯一差异为实施者追加回归的internal/store/rework_refs_test.go；产品源码仍与r3冻结完全一致。

结论仍CHANGES_REQUESTED：修复并稳定冻结r4后重做受影响GC正常/race及组合验证、独审。
