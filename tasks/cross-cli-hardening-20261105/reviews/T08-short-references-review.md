# T08 短引用独立安全审查

日期：2026-11-09。审查 Agent：`/root/review_short_refs_security`，已注册 `security-reviewer` 只读角色；本文件由 root 按该 Agent 返回的最终结论记录。

**VERDICT=PASS；阻塞问题：无。**

基线 `48b3a9b25f7428c50c6305abbb1fb8eee14c48f0`，隔离分支 `work/t08-short-mcp-refs-20261105`。审查四个最终源码/测试文件和任务合同，检查 HMAC 域、namespace/scope、完整 route/review tuple、旧格式兼容、真实 control/Store 授权链、symlink/hardlink/rename/TOCTOU、权限、锁、容量、注入及敏感信息。审查未修改文件、联网、构建或调用模型 CLI；执行了只读检查及 `git diff --check`。

| 文件 | SHA256 |
|---|---|
| `tools/orchestrator/cmd/orchestrator/mcp_routes.go` | `a7926614a977f7c6968b99bb6c5586639cb24e5e2f225e0ae0ac3aab07db2f41` |
| `tools/orchestrator/cmd/orchestrator/mcp_references.go` | `ad5042d928bd1668b2be4995f8f68f5b0713c19cab10fbe26c2de483d44cee1e` |
| `tools/orchestrator/cmd/orchestrator/mcp_business_test.go` | `aed3fad1a93579787aa561111cd1aed112768d1cb1514828c5b001ff58fb35c5` |
| `tools/orchestrator/cmd/orchestrator/mcp_references_test.go` | `34d9656a6bcfd3b8bec5c3da17e859d2ec1cde1326475f04a4a4f735e6ea1477` |
| `tasks/cross-cli-hardening-20261105/plans/T08-short-references.md` | `05c95d8c6ed03a7459751f088470962d64e66b67574160e3cdd89214726e8893` |

审查确认：route 和 review 使用独立 domain 与完整 32-byte HMAC，长度分帧绑定 namespace 与完整内容；解析后仍重新读取私有 handle/control/bootstrap，真实 Store 保留 owner、tuple、revision、segment、event/hash、action-slot 检查。记录采用 `0700/0600`、FD-relative openat、NOFOLLOW、单链接及命名 inode 复核，每 dispatch 最多 64 条、每条 16 KiB，flock 等待最多一秒；重复 tuple 不重写，部分发布失败关闭。没有业务 Store、schema、依赖或 8 工具列表变化。

审查核对 root 的 `green3.jsonl`：181 个 pass 事件（含子测试），0 fail/skip，覆盖重启、旧格式、tamper、namespace、权限、symlink/hardlink、缺失/超限、并发去重及 quota。该测试由 root 执行，审查 Agent 未重复运行；完整 normal/race/vet/build 另见 root 验收证据，不能将本报告解释为已执行这些检查。

限制：本 PASS 仅覆盖以上源码安全快照，不证明 `.4` 包身份/安装门禁或真实模型传递效果。旧二进制不能解析新短引用，回滚须保留兼容安装及私有元数据。Claude 401/无可用配置使原真实 live 仍 BLOCKED，本结论不改变产品门禁。
