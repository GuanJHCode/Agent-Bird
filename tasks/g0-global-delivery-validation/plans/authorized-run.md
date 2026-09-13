# 本次注册/撤销验收授权

用户于本轮答复“可以”，批准按 `tasks/g0-proxy-continuation/docs/next-global-validation-plan.md` 再执行一次用户级注册/撤销验收。之前的单次授权已经使用；本轮是新的一次授权，不是永久安装授权。

- 主会话负责编排、真实执行与验收；子 Agent 只准备实现、受控测试及独立审查。
- 只允许一个新的真实 bootstrap 实验；不自动重新注册，不接管原 job、socket、plist 或 lease。撤销和自有资源收尾属于本次实验。
- 目标 label 为 `org.codex.orchestration.proxy`；本轮只读预检得到 `launchctl print` 返回 113，原 plist 和默认 public socket 均不存在。执行前仍须重验。
- 使用全新隔离 HOME/CODEX_HOME/workspace、固定原生二进制、已验证解释器、源文件摘要及本地合成 provider。外部模型调用为零。
- 14 条已登记认证路径只作不透明摘要/元数据比对；不登录、登出、切换账号、复制或恢复认证，不访问 Keychain 凭据，不修改全局账号配置或权限。
- 真实 job 使用 `launch_activate_socket`；离线继承 FD 测试不能计作真实 launchd 通过。服务身份、owner lease、helper admission 与退出记录由真实服务进程发布，controller 不能编造服务状态。
- 完成标准同时包含：同一自有后端、匹配的历史证明、一次 Go ACK、认证未变、owner/helper/backend 退出、私有 socket 清理以及 job/plist/public socket 对称撤销。
- 普通 TUI、持久 native history、真实模型和完整 G0/G1–G4不由本次样本推导。

保留失败样本与任务记录。验收后仅清理本任务创建且不再使用的工作树、构建缓存、凭据路径清单和隔离控制文件；保留用户已有修改与归属不明文件。
