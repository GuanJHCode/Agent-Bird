# F03 有序候选组合：本地验收

生产快照 `766ddea`（F03 `6a0c500` 与已验收 F02/schema31 `c8a6f3e` 合并）。独立静态最终审查 PASS；原有 integration 尚须由 ROOT 合入，集成回执另存。

- 支持同 repo/common/base 的 2..63 个已接受 producer，显式 v2 保留 `[B,A]` 顺序。无主 ProviderLock，Store 不读取 Git/生成最终 manifest、不新增 schema 或配额桶。
- Host 认证每源 artifact/payload/binding、完整目录身份、base ancestry、private ref。worker 在 common 锁内重验 source pins，使用原始 Handle Wait/tree 和 Git process journal。v1 validate/review/integrate 保留 composition provenance，Git 操作前重验 pin。
- 并行 producer 的 freeze 使用有界、可取消、固定 FD/inode 的候选锁等待。冲突保留工作区并报 `compose_conflict`；journal 竞争/不确定所有权报 UNKNOWN。无自动模型调用、Git 重跑或清理。

## 实际证据

| 范围 | 结果与原始报告 |
|---|---|
| 最终组合公开 CLI + Store capture/decision + F02 删除后的历史 DAG proof | `data/final/combined-schema31.jsonl`，4 top / 6 named PASS，0 fail/skip，exit0。真实 submit/collect/ACK/accept、两个并行受控 producer、compose、v1 后继及最终 Git tree。 |
| Mac race 核心 | `data/final/mac-race.jsonl`，5 top / 15 named PASS，0 fail/skip。包含旧 v1 真实链、Store pending/consumed 缺证明反例、候选锁与 manifest。 |
| 最后 UNKNOWN 修复的 race | `data/final/uncertainty-race.jsonl`，3 top / 3 named PASS，0 fail/skip。实际 worker busy journal、Host UNKNOWN 拒绝后正常 `[B,A]` 链、真实 merge conflict。 |
| 原有 Git / worker 定向边界 | `data/git-boundary/3403609-{gitops,worker}-test.json` 为原始 Go JSON + exit。复用部分覆盖 candidate runner UNKNOWN、扩展拒绝、receipt 身份/private ref、旧单输入 worker 链。不是每项都经新 Host 路由。 |
| Linux arm64 Store（`206e158`） | 2 top / 4 named PASS，0 fail/skip；禁网/非 root/只读根/限额，原 attach exit0、非 OOM，CID `91fd15261c5a7050158365ebeeab03f918434906b0ed5c650b73cf32cb5531f2` 已删除且 ROOT 再 inspect 不存在。见 `data/linux/root-verification.json`。该快照早于 F02/schema31 合并。 |
| 构建与静态检查 | Go1.26.8，TMPDIR=/private/tmp；`766ddea` 全模块 `go build ./...` 与 host/gitops/gitopsworker/store/cmd `go vet` 均 exit0。 |

ROOT 独立解析实际 JSON 的 top/named/fail/skip 与 SHA，记录在 `data/final/{counts,combined-schema31-receipt}.json`。未跑全 Store 或重复旧 F10/F12 套件。

## 保留的失败与修正

`data/host-chain` 保留 completion envelope 夹具缺字段、真实 compose 缺 Git ledger、Host 覆盖 stdout sink、验证命令不在既有允许列表、DAG 不接受 compose source、并行 freeze 锁竞争的失败。`data/final/busy-journal-red.jsonl` 是 journal 忙误报确定失败的真实 RED。Store 初版跳过 decision guard 被独审拒绝，已撤回；新增两条反例证明拒绝不修改 attempt/budget/quota/vector/binding。

## 限制与清理

真实 Provider/model/account 未调用；受控 fixture 不证明模型质量或跨 Provider 真实组合。post-integration new-base 返工、全 crash/power-loss 矩阵、F04 执行扩展仍未完成。新 compose 要求完整目录身份；旧缺字段 receipt 不自动升级为新执行权限。

全部本地缓存 Linux 镜像均缺 Git，Gitops/worker/Host Linux 用例未启动，不以交叉编译代替验收；初始 Redis 能力探针仅保留 stdout，另外两份镜像探针及 Store 运行保留完整 CID lifecycle。Intel Mac / Windows 原生延期。未替换用户安装、push 或 release。

两个失败公开链的本任务测试二进制在确认无对应存活进程后按 SHA 删除；请求、DB、日志和 Git 工作区保留。Linux worker 已按 SHA 将两份自有测试二进制移入系统 Trash，原始证据保留。清理回执 `data/final/owned-cleanup.json`；没有删除原用户文件。
