# G0 后续落地与验收

基线：`fba2cc7`。用户要求主会话编排和验收，subagent 落地，允许主会话选择子 Agent 模型及推理深度，完成后在本会话汇报。

本轮先补齐 G0 中能够独立实施的三项：控制器崩溃后的自有执行清理、注册事务的安全 public socket 收尾、回传持久意图和逐事件业务 ACK 对账。各实现者使用独立 worktree；主会话负责独立复审、串行整合及最终新鲜验证。现有未提交修改不纳入交付。

- 认证约束持续有效：不操作本机 CLI 登录账号、auth、Keychain 或全局账号配置，不复制凭据。测试使用合成临时身份和本任务自有进程、UDS、文件及数据库。
- 不执行新的真实 LaunchAgent 注册或永久安装；先完成可审查的代码、测试和部署前置方案，有必要时再请求具体授权。
- 不恢复已停用的历史原生 controller，不把离线/合成故障测试表述为原生端到端验收。
- 固定原任务 owner 回传，UI 切换不迁移 owner，账号/profile 改变拒绝跨身份续接。
- public socket 由注册事务管理；private backend 清理器不得删除。身份、所属 job 或父目录不明确时保留资源、报告 UNKNOWN。
- 崩溃恢复不能用父 PID 消失代替整棵执行树停止证明；未证实停止不允许自动重跑。任务记录保留。
- ACK 与运输状态分开，uncertain 不盲重发，不以新的 command UUID 绕过业务幂等；部分 ACK 不阻挡未处理问题。
- G0 未完整验收前不启动 G1–G4 正式功能。测试不使用 Astra；原生模型测试若后续具备准入条件，仍为 Luna/medium。

完成判据：每项具备失败回归、最小实现、新鲜通过证据和独立复审；主会话对真实功能边界逐项汇总，不将项目内实现等同于安装启用或完整 G0 通过。

接线审计补充：activation 与 standalone delivery 目前未接通。同 profile 的普通第二 frontend 会等待原 lease 结束并启动新 backend，不能满足在线回传。因此在上述范围内补充受控 `owner-helper` role：只连同一 active lease 的既有 backend listener，使用独立 WebSocket；grant 固定 profile、owner、thread、epoch、lease 与进程/源码身份，方法及目标 thread 受限。helper 退出不终止 owner，owner 退出关闭 helper；仍需新鲜合成端到端测试，不能冒充真实 native 回传验收。
