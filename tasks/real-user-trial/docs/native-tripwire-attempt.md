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
