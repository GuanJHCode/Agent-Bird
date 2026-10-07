# Owner recovery handoff 文件层

唯一 writer review_retry_v2_worker，branch feat/owner-recovery-handoff-20261008，base a6ecc08。只新增 internal/host/owner_recovery_handoff.go 与对应测试及本任务 artifacts；不改原 recovery/lease/Store/contract，不派生，不连接 DB/RPC/runtime。Go1.26.8，TMPDIR=/private/tmp，普通 focused/vet；race/container/native 由 ROOT 持有。

1. 缺 API compile RED 独立记录；只读 facts、scope相邻、真实旧scope/普通commit拒绝与 budget 保留的行为 RED。
2. 冻结 Proof/Facts 五个 digest 字段（json:"-"），Proof 另有 Applied。same run/launch/producer、next incarnation=prior+1，通过 existing originalOwnerRecoveryScope 推导两个 scope；不额外冻结 origin/gen。
3. existing-only directory/lock/ledger opens，复用原 FD/named inode/AuthoritySHA/strict canonical record/OwnerPending/reservation guards。nil proof只接受prior；prepared可exact prior/next；applied只next。facts整文件 SHA256 包含 canonical JSON 的终止 newline。
4. CommitPrepared exact facts 后进行只改Scope的专用 publication，不调用原 ownerRecoveryState.commit。ROOT裁定原persistBytes会清理不确定现场，不可直接复用；本新文件保留失败stage/ledger，不恢复旧bytes、不prefixGC。成功接管已验证stage FD并重新核验。
5. 真实 flock/重开/缺文件零creation/security/tamper/故障矩阵。私有 per-lease hook，无公开 env 后门，观测实际filesystem；不声称物理断电。
6. focused普通JSON、vet、gofmt/diff-check，report/source hashes与准确P/F/S，独立commit后交还 ROOT。

ROOT裁定：bytes/mtime sticky 继承 ledger 语义；lock只沿用既有euid/0600/nlink1/named inode/flock/AuthoritySHA，不额外绑定lock内容/mtime。文件API接受Coordinator内部Store proof映射，不自行验证DB intent/applied；本层不等于B3集成完成。

完成：ROOT test-only 解环补丁18f3bcb→7731f21后标准package执行TDD，无fixture排除。最终30top、216named PASS/5UIDSKIP；新handoff15top、116named PASS/5UIDSKIP。源码冻结、vet/gofmt/diff-check通过；报告及hash/counts已生成，ROOT回报独立FS审查PASS，race/Linux/UID/native集成交ROOT。
