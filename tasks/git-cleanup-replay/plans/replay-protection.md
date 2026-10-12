# Git cleanup replay protection

范围：仅为 `gitops.Cleanup` 增加缺失 root 的完成证明和精确回放保护。

- cleanup intent/outcome 绑定 receipt 的 canonical SHA-256；receipt 包含 root、repo/common-dir、inode、文件清单和 private ref/OID。
- 只有 durable journal 的严格、有限读取可证明同一 receipt 已 `removed`；memory/未知 journal 对缺失 root 返回 `cleanup_pending`。
- 已完成回放返回原 outcome；重建 root 或 private ref 使回放拒绝并保留现场。
- quarantine 后中断不尝试猜测或扫描 quarantine，保留 private ref。
