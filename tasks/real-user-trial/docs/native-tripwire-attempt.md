# 固定原生刷新保护验证：2026-09-14

状态：`BLOCKED_BEFORE_VERSION`，不能据此声明刷新保护通过或真实试用可用。

主验收对提交 `88f2b351` 的 opt-in fixture 执行了一次测试。原生文件 SHA 在运行前通过 fixture 校验；隔离后的 `--version` 子进程停在 macOS `UE` 状态，未进入 app-server/RPC 或模型阶段。测试只有新生成的 synthetic 凭据，不读取、复制或修改用户认证内容。

命令：

```sh
CODEX_RUN_NATIVE_AUTH_TRIPWIRE=1 CODEX_TRIAL_NATIVE_BINARY=/Users/guanjunhui/.codex/packages/standalone/releases/0.154.0-aarch64-apple-darwin/bin/codex PYTHONDONTWRITEBYTECODE=1 python3 -m pytest -q -p no:cacheprovider tools/real-user-trial/test_native_auth_tripwire.py
```

安全进程记录：

- 原生 child：PID `39659`，uid `501`，birth `Mon Sep 14 07:32:04 2026`，最初 parent `39658` / pgid `39658`，状态 `UE`，RSS `80 KiB`，CPU time `0:00.01`。
- Python 测试 parent：PID `39658`，同一 birth；`subprocess.run(timeout=5)` 超时后等待无法回收的 child，导致外层没有及时结束。核验 parent 身份后仅向 parent 发送 TERM；命令结束码 `143`。
- 超时路径已向 child 发送 KILL，不重复发送，不结束其他任务进程。
- 保留本次两套 `0700` synthetic 根 `/private/tmp/g0-auth-qxskc_u5`、`/private/tmp/g0-auth-g2z7tkep`。原生进程尚未确认退出，不递归删除它可能使用的目录。

后续修正需使 fixture 对不可回收进程有界返回，并区分原生启动阻断与刷新测试失败。在查清系统阻断前不重复原生启动，不放开真实 HOME 或 Keychain 权限来凑通过。

## 只读定位

子 Agent 对同一 PID 的 `sample` / `vmmap` 显示：唯一线程停在 `dyld CacheFinder -> ignite -> boot_boot -> ignition_halt -> abort_with_reason -> __abort_with_payload`，libSystem 尚未初始化，没有 malloc zone。已确认中止发生在 Codex 主程序之前。

定向日志只有 `AMFI: Denying core dump for pid 39659 (codex)`，未提供被拒绝路径；这条日志不能单独说明触发原因。此前成功的 native-product-10 放行系统文件读取后再拒绝指定凭据路径，本次采用严格读取白名单。当前最可能的原因是白名单遗漏动态加载器启动资源，但具体缺项尚无证据，不能随意扩大权限后宣称解决。

旧 PID `47082` 的记录包含明确的 Launch Constraint Violation，本次没有同类证据，不能把两次问题认定为同一个原因。本次 PID `39659` 已由 PID 1 收养且仍为 `UE`，记为 `cleanup_pending`。不重启用户主机，不再启动本机原生验证，不扫描或重建旧凭据路径清单。
