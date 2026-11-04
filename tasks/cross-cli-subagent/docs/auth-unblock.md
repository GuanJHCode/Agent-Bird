# 原生认证验收阻塞解除

用户要求解决 `baseline_count=0`。起点为已推送提交 `9d39f127af8d3d52c0ae46bc79254a699b3b541b`，既有验收报告与失败证据保留。

## 负责人和边界

- 主会话：确定验证契约、集成、执行最终验证并更新验收结论。
- `unblock_auth_fixture`：`/private/tmp/agent-bird-auth-unblock-20261104` 唯一写入者，负责最小合成诊断、修复和回归。
- `review_native_coding`：只读检查真实 native 调用路径并独立审核修复。
- 使用 exact pinned Codex 0.166.0、合成凭据、私有 HOME 和本机测试端点；不调用真实模型，不改变真实账户、全局 preferences 或认证权限。

## 验收条件

1. 明确原 baseline 未触发的具体原因，提供可证伪的前后证据。
2. 相同 native 二进制的 baseline 实际向本机端点产生预期 refresh 请求。
3. guarded 运行没有额外 refresh 请求，合成 auth 文件字节与 inode 不变。
4. 未跳过断言、以 stub 替代原生 oracle，或通过放宽真实 HOME/外网访问制造通过。
5. 受影响回归与独审通过后，主会话复核原始报告，再更新整体验收状态。

## 最终结果

**PASS：本轮唯一验收阻塞已解除。** 冻结修复 `7cb11e4c2ac7b6b07b2713f41865921342ba6a7e` 只有一个测试文件改变，生产代码、依赖与账户配置不变。

根因一：沙箱遗漏精确 `com.apple.system.opendirectoryd.libinfo` 查询服务。CoreFoundation 在采用 CFFIXED_USER_HOME 前会通过 UID 查询 home；该操作被拒绝后，Codex config load 失败，app-server 回退 defaults，而 network policy 保持未初始化，refresh 请求在 HTTP 之前即被拒绝。实际失败 PID 的 sandbox 日志与仅增加该服务后的真实 HTTP 恢复构成前后证据。

根因二：Codex 0.166.0 请求新增 `refresh_reason: proactive_expiry`；旧三字段 oracle 将已经发出的合法请求记为异常。现在按 0.154/0.166 精确绑定三/四字段，错 reason、错 token、额外字段和不支持版本仍拒绝。

修复仅放行上述精确服务，CFFIXED_USER_HOME 与 HOME 均绑定新建合成目录；没有私有 CF 环境开关。实际 Seatbelt 负面测试确认真实 HOME、其它 OpenDirectory、securityd、cfprefsd 仍被拒绝。保留原 account/read 与 baseline/guarded 所有断言。

## 验证与独审

- worker exact 0.166 native 全文件：37 passed、0 failed/skipped。
- `review_native_coding` 独立重跑 exact 0.166 native：37 passed、0 skipped；审核 PASS，无 blocker。
- 主会话最终命令：`python -B -m pytest tools/native-product-bridge tools/real-user-trial tasks/cross-cli-subagent/tests/test_delivery_scripts.py -q`，显式提供 native auth/tool-policy opt-in、精确 binary/bundle/pin manifest、最终 `.7` G1 binary 和 installed root。
- 最终 JUnit：[final-r2.xml](../data/auth-unblock/final-r2.xml)：240 tests、240 passed、0 failures/errors/skipped，19.96 秒。native refresh-tripwire 本例通过且未跳过。保留一条已有 `finally return` SyntaxWarning，不影响断言结果。
- 主会话首轮遗漏 `CODEX_TRIAL_PIN_MANIFEST`，得到 239 passed、1 个清单参数错误；认证测试当轮已通过。原 [final.xml](../data/auth-unblock/final.xml) 保留；补齐参数后全套重新执行，没有删除失败记录。
- 原 Go race 540 顶层通过、8 skip，以及两方向真实编码/返工/验收证据未受测试 fixture 修改影响，复用有效证据。

## 范围与清理

测试仅使用合成认证、本机 HTTP 和固定 native，没有新的真实账户或模型请求。worker 的 native 进程已退出，成功 fixture 按原测试逻辑清理；失败现场和必要审查证据保留。当前验证覆盖本机 macOS、exact Codex 0.166；未将结论扩大到其它平台/版本或 MCP Tasks/任意 TUI 主动唤醒。
