# Schema22 历史附件计费验收

本阶段把有效历史 raw 声明与 control quota 分开，建立 immutable、data-only legacy ledger；不产生文件系统权限、managed reservation/ref/intake，不读取/写入历史附件。无歧义有效记录仅退款原有raw一次；无法分类的原body/hash完整保留并阻止新增raw授权。历史结果已经写出时，不将其当作新预占拒绝；原始ACK重放不重复计费。

有效声明保留原artifact JSON字节、未知字段及hash。重复JSON键、错误整数等保守记为malformed；escaped exited引用不另计raw。有效managed report/progress只在完整durable scope/descriptor/proof验证后排除，未填满progress pool不再被误判。所有claim source的identity/artifact保持绑定，malformed source不可删除退款。迁移检查scope计费、原body及pending question的4096预留，失败完整回滚DDL/version/counters。补入可信session保留artifact扩展字段和原ledger declaration。

## 最终快照证据

原始压缩JSON、命令结果与源码摘要见 `../data/root-receipt.json`；最终生产差异已由独立 reliability reviewer 判定 PASS。

| 验证 | 结果 |
|---|---|
| macOS arm64 full Store `go test -timeout=12m -count=1 -json ./internal/store` | 428 top / 1210 named PASS，0 FAIL/SKIP，124.955s |
| macOS相关 Store race，`^(TestLegacy.*\|TestReport.*\|TestHostArtifact.*\|TestQuota.*)$` | 66 top / 185 named PASS，0 FAIL/SKIP，416.455s |
| `go vet ./internal/store` | 退出0 |
| Docker Linux arm64 legacy / question / quota migration | 28 top / 68 named PASS，0 FAIL/SKIP；实际执行交叉构建后的test binary |

Linux容器固定本地镜像，无网络、只读根、nobody、no caps、独立tmpfs。CID `a0be1d6226550043a494b09d31624b250c5646fdfb4104c8151ee312c8b3a7df` exit0、非OOM，精确删除后inspect确认不存在。见 `../data/linux/legacy-root-final-receipt.json`。

## 有效失败和纠正

- ROOT `root-contract-red` 复现部分retained managed progress误分类、重复JSON漏记及旧授权terminal结果被新额度拒绝。
- `root-expanded-red` 复现capability used不足、run storage不足和escaped exited错误计raw。其中三项managed report corruption fixture先被原immutable/FK guard挡住，不当作产品RED；后在fixture内保留并恢复原guard，使用合法第二run完成跨scope验证。
- `root-session-reserve-red` 真实复现缺4096 reserve升级成功、真实EventSession被source guard拒绝；修复后包含原有question续接路径的28 top /68 named聚焦测试通过。
- `root-full-store-v2` 是最后两处question修复之前的427 top /1208 named PASS。最终凭据使用后续428/1210结果，不复用旧快照替代最后验证。
- 名含green的中间JSON有实际FAIL（编译、快照比较、fixture错误），均不作为最终通过；原worker中断/失败日志与早期receipt由本记录取代，不隐藏全量曾失败的事实。

## 未关闭范围

仍未实现/验证 F02 全部保留与删除策略、永久tombstone、真实2GiB/flood/RSS及完整磁盘满/截断矩阵。Admission当前仍含线性扫描，未作吞吐保证。该WT没有真实Provider/native TUI或公开CLI验收；合入integration后继续受影响候选和公开路径验证。Intel Mac/Windows原生按用户可用环境延期。

## 集成后的公开路径

integration `f771ee3` 同时包含 Linux portable check、F12安全state目录、F04原Git进程跟踪及schema22。原18项legacy和7项Linux源码hash逐项相同。`go build ./...` 退出0；parent race的公开CLI选择9 top /15 named PASS，0 FAIL/SKIP，25.218s，包含原owner签名worker、普通候选DAG、indexed候选DAG、state路径和portable policy。CLI子进程使用普通构建，不声称子进程race覆盖。精确列表见 `../data/root-integrated-receipt.json`。现有两项F10死owner resume失败仍留在后续B3范围，没有用本次选择替代其验收。
