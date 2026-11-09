# T08：普通 MCP 短引用

## 目标与边界

历史私有 `.3` 的 Codex → Claude 真实试验中，主模型在后续调用改写了 847 字符 opaque handle；产品正确拒绝，但该方向没有完成验收。本修复缩短传输引用，保留完整签名与每次操作的真实业务授权。离线测试不能证明最低档模型已能正确传递短引用；原试验及其预算、失败证据保持不变。Claude → Codex 原方向仍因 HTTP 401 阻塞，用户确认没有可用本机配置，本修复不尝试新的付费调用或认证方式。

## 合同

- 新 route 为 `m1r_` 加无 padding 的 canonical base64url，共 79 字符：既有 dispatch 两层 24 位小写 hex 目录的 24 bytes 坐标，以及完整 32 bytes HMAC-SHA256。每次从私有 `handle.json` / `reservation.json` 重建完整 route；签名同时绑定 domain、namespace、坐标和 canonical 完整 route。非 canonical 历史目录继续签发旧格式。
- 新 review 为 `m1v_` 加完整 32 bytes HMAC 的 base64url，共 47 字符。签名绑定独立 domain、namespace 和完整冻结 review tuple；原有 owner、run、task、candidate、segment、revision、event/hash、action slot 字段均保留。
- 完整 review tuple 是辅助元数据，存放于既有 request 目录的 `mcp-reviews/`，目录 `0700`，记录与 `.lock` 为当前用户拥有的 `0600` 普通单链接文件。读取和创建通过 FD/openat 锚定、NOFOLLOW 和命名身份复查，拒绝 symlink、hardlink、公开权限、非 canonical 内容及损坏记录。
- 每 dispatch 最多 64 个记录，每个最多 16 KiB，总记录上限 1 MiB；同 tuple 去重且不重写。创建受私有 flock 串行化，锁等待最多一秒；不淘汰、不自动删除或替换。未成功发布的部分记录保留并拒绝使用，防止清理误删。缺失当前 tuple 的记录可由重新 inspect 重建；历史 tuple 缺失不保证恢复。
- inspect 允许产生上述辅助文件；数据库业务行、owner 租约、task 状态、capability、审批和决定保持原合同。测试对 inspect 前后全部数据库表行取摘要，另验证重复 inspect 的记录 inode/mtime 不变。
- 引用只用于定位。所有公开业务入口仍先重新加载配置、私有 handle、control/bootstrap，并通过原 Store owner、完整 tuple 和 action-slot 检查。签名记录不能代替 accept / request-changes 的决定，也不能代替 collect/ACK。
- 新版本继续解析旧 sealed route/review。旧二进制不能解析新短引用，回滚时须保留与引用兼容的安装版本及其私有 request 元数据；不把新包复标成 `.3`。
- 工具列表仍为普通 MCP 的 8 工具，无 MCP Tasks/native chat 续审声明；DB、安装、receipt、configure schema 不变。新生产字节须生成唯一私有 `.4` 包，重新验证包身份及安装门禁。

## 实施与验收

root 是 `t08-short-refs` worktree 唯一写入负责人；独立安全审查只读最终快照，外部 `.4` 包脚本准备与源码测试隔离。

源码位置：`tools/orchestrator/cmd/orchestrator/mcp_references.go`、`mcp_routes.go`；回归位于 `mcp_references_test.go` 与真实目录布局的 `mcp_business_test.go`。没有新的 route 数据库或可变业务缓存。

原红测、Go SDK 缺失导致的失败、首次旧测试解析失败均保留在外部 `t08-short-refs-evidence/data/`。最终 root 证据需分别记录 normal/race 的实际测试数、失败和 opt-in skip，另执行 vet/build。独立审查 PASS、root 验收后提交并立即 push；包验收独立记录。真实 `.4` 双向试验保持 NOT_RUN，整个产品仍受既有 LIVE 门禁约束。
