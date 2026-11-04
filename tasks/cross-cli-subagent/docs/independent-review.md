# 独立最终审核记录

## 后续复核：PASS

`review_native_coding` 已独立审核冻结修复 `7cb11e4c`。仅改变原生认证测试 fixture：exact libinfo 查询许可、CFFIXED_USER_HOME 合成目录、按版本精确匹配 refresh 请求。实际 Seatbelt 负面测试证明真实 HOME、其它 OpenDirectory、securityd、cfprefsd 仍不可访问。独审重新运行 exact 0.166 全文件：37 passed、0 skipped；原 baseline 请求存在、guarded 请求不增加、auth 字节/inode 不变断言全部执行，未发现 blocker。

主会话另在集成工作区运行完整相关套件：240 passed、0 failed/errors/skipped。生产代码未改，先前 Go 和真实双向闭环证据继续有效。后续证据见 [auth-unblock.md](auth-unblock.md)。下方为原始失败阶段审核记录。

审核者：`/root/review_native_coding`。主会话汇入其返回结论；审核过程中未修改源码或运行真实模型。

**VERDICT=BLOCKED（核心功能 PASS；整体验收 BLOCKED）**

- 冻结源码 `68e875868215ecb1fd8daf540799fd3d0eee25e3`；原工作区 HEAD 仍为 `5e7443d...`，0 staged。相对原 HEAD 的 175 文件与 source 字节/执行位一致，0 mismatch。
- 独立解析 Go race：1175 pass、8 skip、0 fail；顶层 540 pass。包 smoke 7 pass；当时 delivery 25 pass；native Python 212 项、211 pass、1 fail。随后主会话用最终包 `.7` 补跑 delivery + G1，共 26 pass。
- 最初四缺口及六个核心场景均有适用证据。正向 `.4` 是真实模型候选加人工可信收尾，不能称旧 driver 全自动；反向 `.7` 完整两 revision 自动流程通过。
- 两方向 rev2 均以 rev1 为 parent/base，source SHA、Git blob、review 与 validation 一致；旧 refs 保留。accept 与 ACK 分离；反向 completed、active=0、pending=0，原试验仓 HEAD 未变且干净。
- 早期 submit 前失败、受保护 cwd 失败及终态保留，无旧句柄隐式重试；真实 Codex 总计 3 attempts，未超合同。
- 未发现新增高置信度凭据泄漏、越权、状态机篡改或敏感日志问题。

P1 验收阻断：`tools/real-user-trial/test_native_auth_tripwire.py:1743`，refresh baseline 的 `baseline_count=0`；guarded 阶段未执行，因此无法证明额外 OAuth egress 被阻止及该阶段的 auth 字节/inode 不变。必须在不放宽 sandbox、preferences 或认证权限的前提下恢复 baseline，再重跑原生 oracle。

非阻断边界：当前只有 bounded wait/collect 的可靠回收证据；未验证 MCP Tasks、任意 TUI 唤醒或其他 native 版本。应用前 80 个原脏文件 hash 一致属于保留的历史证据，不能从最终树重新构造。
