# 公开 Resume 路由阶段验收

2026-10-08 ROOT 验收当前五个 Go 文件，源码与证据摘要见 data/root-router-receipt.json。此阶段是私有检查点，等待 CLI 接线后合入；旧 supervised CLI 尚无 CommandID，不能独立宣称端到端兼容。

- macOS Coordinator 受影响 race：8 个顶层及具名 PASS，0 FAIL/skip，42.723s。
- 新只读 Store target race：1 顶层及具名 PASS，0 FAIL/skip。此前错误 selector 跑到 0 tests，保留 empty-selector 日志，不作为验收证据。
- Docker Linux Coordinator：相同 8/8 PASS，0 FAIL/skip。CID e8bfc618e43641bcd3b17ddb2149d2d159b0ce83f9c2fbb7a9a233a2ef555a87，exit0 非 OOM，精确删除并 inspect absent；owned binary SHA 匹配 receipt 后移除。
- 最终 Coordinator vet 和整库 build 均 exit0，独立路由 delta 审查 PASS。

真实 Unix socket 回归先出现 owner_registration_deferred RED：旧外层 mutable dispatch gate 抑制已认证的 immutable receipt replay。KindResume 仍经过真实 peer-role/worker ancestry 校验；仅将 ValidateDispatchPeer 移至 authenticated lookup miss 之后，fresh legacy/supervised 都继续受约束。Wrong token 先失败、fresh command 仍被 defer，原 receipt 返回相同字节且不 dispatch。

早期缺 API 编译失败与行为 RED 分开保存。实际签名 RPC child 覆盖 owner Alive/Exited；Owner+Host 都退出后 existing-only lease 成功、新 Host pending admission、CLI journal 接线及 ledger scope handoff 仍未验收。受控 Store 插入和可信 peer-context fixture 不是完整公开 CLI/native 证明。
