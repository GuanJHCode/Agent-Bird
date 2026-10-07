# Host managed writers：阶段验收记录

本阶段实现六类 Host terminal/prelaunch/progress 写入的固定物理槽位、Store 原对象引用和 retained 计费，以及候选/审查/诊断消费核验。不是 F02 或 F01–F13 整体完成。

## 行为

- schema21 预占22,528字节 manifest；两笔原始数据预约仍为2,424,832字节。新 event 固定写入时的 verified 计费类别，后到 ref 不改变历史退款。
- Host 要求原 grant，固定 terminal/index0 或 progress/index0..20。冷恢复只读原对象且仍为 UNKNOWN。旧直接 writer API 已移除。
- Store 先鉴权、锁外核验、持对象锁二次鉴权，同事务落 event/ref/retained；已提交重放不再读物理源。
- 受管消费从受信 state/producer 推导 root，拒绝相同内容的替换 inode；Store TX 内快照无 writer lock。
- CLI 先解析已存在父目录的别名，保留 state leaf 私有目录检查。不存在父目录创建时的既有 MkdirAll 竞争窗口仍属于 F12，未冒称全面解决。

## 有效证据

原始 JSON、SHA 和逐项 skip 列表见 `../data/milestone-receipt.json`。

| 验证 | 结果（顶层 / 所有命名测试） |
|---|---|
| artifacts 完整 race | 19 / 65 PASS |
| contract 完整 race | 42 / 282 PASS |
| events 完整 | 6 / 13 PASS |
| Store 完整 | 413 / 1167 PASS |
| Host 完整 | 404 / 1551 PASS；另16 SKIP |
| Store intake/accounting 定向 race | 27 / 95 PASS |
| Store final consumers 定向 race | 30 / 88 PASS |
| CLI 候选完整链、路径、accept replay 最后 race | 5 / 35 PASS |
| CLI 路径最后 race | 3 / 3 PASS |
| Docker Linux artifacts | 19 / 65 PASS |
| Docker Linux Store managed artifacts | 19 / 46 PASS |

Store/Host 全包证据之后仅做 gofmt、CLI 修复/夹具与文档修改；其对应生产语义未再变化。CLI 子进程由普通 go build 生成，不宣称所有子进程也被 race 插桩。Linux 容器均 exit0、非 OOM，已按精确 CID 删除并确认不存在；未声称 Linux Provider/sandbox/install 验收。

## 未通过与未完成

第一次组合 full CLI 为204 PASS、7 FAIL、5 SKIP。修复 /tmp 别名与真实 managed candidate fixture 后，针对原失败组为7 PASS、2 FAIL；最终新增精确 coverage 错误断言后，候选/路径/accept replay race5/35通过。

仍失败且未弱化：`TestCLIStartsServiceSourceHostAndCollectsDurableResult`、`TestCLIOwnerExitRequiresVerifiedRebindBeforeResume` 在 resume 时收到 `initial_owner_transport_rejected`。单独复现保留现场：旧 execution_owner 仍 bound/current，但其 owner/transport 已退出；首次 transport 不能重新登记旧 supervised launch。该证据与未完成的 F10 B3 seal/data-only replay/retire/new incarnation 相关，不以此伪称恢复完成。失败现场128个条目已校验归档，原临时目录已清理；归档不能承诺换路径后可执行回放。

M3 仍欠 legacy_pending、diagnostics retained/显式删除/永久 tombstone、完整2GiB/flood/RSS/物理故障验收。Intel Mac/Windows 原生环境按用户指示延期；未调用真实模型/账号，未替换安装、推送或发布。

独立审查覆盖 schema/intake/accounting/receipt/各writer/consumer 与 CLI root 传播。最新 review 指出的 state leaf Eval 竞争已改为仅解析 parent；indexed negative 继续明确验证 data-only 历史体的共同语义校验，并要求精确 coverage 错误，不能由授权错误空过。
