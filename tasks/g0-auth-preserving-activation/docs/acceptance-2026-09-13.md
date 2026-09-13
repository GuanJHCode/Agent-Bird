# 认证保护接入实验验收

后续处置：用户明确批准清理残留 socket 后，已复核原 dev/inode、UID/GID、mode、父目录身份以及 job/plist 缺席，仅删除该 socket。删除后 socket、job、plist 均不存在，见 `data/native-launchd-03/authorized-socket-cleanup.json`。本轮残留已清理，没有执行新的注册或认证操作；以下保留原始实验失败与修复记录，不能据此把自动撤回实现追认为已通过真实重测。

本轮真实默认入口已经按需激活并正常完成原生 `/status`、`/quit`，但整体撤回验收未通过：`launchctl bootout` 后默认 public socket 仍存在。认证保护检查通过；不得把本结果表述为完整 G0 或永久安装完成。

## 实际证据

- `data/native-launchd-03/receipt.json`：唯一一次真实 `bootstrap` 和 `bootout` 均返回 0；job、plist、lease 已撤回；原生 child exit 0；backend 原 PID/birth 已消失，private socket 已删除。
- 原生二进制为固定 Codex 0.154.0，普通 CLI 空 argv；使用独立无凭据 HOME/CODEX_HOME、OS sandbox 和 PID grant。`/status` UUID 与 ready/final 一致，最终 protocol 无 turn/start、turn/steer，模型请求为 0，配置为 Luna/medium。
- 14 项认证文件的 opaque hash/元数据前后完全相同；合成 profile 未生成 auth.json。没有执行 login/logout、账号切换、Keychain 凭据操作、真实凭据复制或全局账号配置修改。
- `data/native-launchd-03/post-revoke-inspection.json`：job 查询返回 113，plist 不存在，但默认 socket 为 UID 501、0600、非 symlink；dev=16777234、ino=162584670。注册前检查确认路径不存在，但未在 bootstrap 后冻结该 socket inode，不能排除后续替换，故没有 unlink 或 chmod。
- 原始 receipt 保留当时代码的 transaction `REVOKED` 和 outer `failed`，不覆写历史。后续修复 `2a54596` 在撤回后检查残留，将此情况标为 `UNKNOWN/public_socket_residual`，不删除残留路径；没有为该修复重新注册服务。

## 前置失败与替代方案

| Case | 结果 | 后续 |
| --- | --- | --- |
| native-launchd-01 | `.codex` 为 0755，被错误的 ancestor 0700 检查拒绝；没有 bootstrap | 兼容安全 ancestor，socket leaf 仍要求 0700，不改本机权限 |
| native-launchd-02 | 系统 Python binary 0775，被 artifact gate 拒绝；没有 bootstrap | 不 chmod、不弱化 gate，改选已有安全解释器 |
| native-preflight-03 | 内置 Python 3.12 不支持 `cleanup_socket=False`；私有 service 启动失败 | 离线错误 hash 与真实失败一致，改用 3.13 |
| native-preflight-04 | 私有 UDS/FD、原生 PTY 和完整 probe 通过；没有注册 | 使用同一安全 Python 执行 native-launchd-03 |

实际解释器：`/opt/homebrew/Cellar/python@3.13/3.13.5/Frameworks/Python.framework/Versions/3.13/bin/python3.13`，0755、UID 501；runtime 当前需要 Python 3.13+。所有失败 case 均保留独立记录，认证比对一致。

## 待处理与授权边界

1. 当前默认 socket 的清理需要用户针对该残留对象明确授权。执行前仍需复核同一 inode、parent 身份及 job 缺席；对象改变则停止。没有将此清理许可推定为第二次注册或永久安装许可。
2. 本轮未授权永久安装；未执行第二次真实注册。新注册或正式启用须另行授权。
3. 本轮零模型 cold activation 不证明已有 thread 的持久化 resume。已有恢复、部分任务明确继续、重连样本属于先前独立证据，不能用新建空 thread 代替。
4. controller crash、一般子进程树终止及完整 G0/G1–G4 尚未全部验收，不能声明整体完成。

合成测试 profile 作为退出后的记录保留；私有凭据路径清单及包含该清单的临时 sandbox/manifest 在认证最终比对后清理。原有用户修改、旧 worktree 和归属不明临时文件保持原样。

最终验证见 `data/final-verification.json`：修复后完整相关测试为 **413 passed, 116 subtests passed / 21.07s**；14 项认证元数据/摘要与本轮 9 份前置快照均一致。`data/cleanup.json` 记录已删除的自有 supervisor、version-only 临时目录、私有凭据路径清单及新缓存，并列出保留的合成记录目录。残留默认 socket 未删除。
