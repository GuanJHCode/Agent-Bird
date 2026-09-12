# 后继工作状态

更新于 2026-09-13。授权边界见 `../plans/contract.md`。

| 工作 | 当前证据与下一步 |
| --- | --- |
| 原持久任务重新打开且不自动执行 | proxy-resume-02 已通过主会话和独立验收，见 `proxy-resume-02-acceptance.md`。 |
| 部分任务退出停止、记录保留、明确继续后续跑 | proxy-partial-02 已通过主会话和独立验收：quiet 0 turn、前缀不变，明确继续后 segment 2 完成 12/12。初次 backend cleanup 约 10.155 秒，不是立即停止。 |
| cached side/primary 切换 | proxy-cached-04 的 P/S/P/S 与正常退出已通过。cached S 有后台 RPC 但没有带 S 的选择事件；不能自动绑定当前显示，用户已批准固定原主会话回传，正在补 home/profile 上下文保护，见 `../../g0-cached-switch-boundary/docs/routing-decision.md`。 |
| 强制断连后重新获得附着资格 | proxy-reconnect-01 已通过：旧 epoch 失效，新 epoch 独立 initialize/resume/skills，status 同 P，零模型正常退出。 |
| 用户级按需冷启动 | 一次注册/撤销实验已获授权；正在补 activation FD 接线、逐客户端隔离及认证保护，未实际注册。 |
| 控制器崩溃后的任务清理 | 当前 runtime 没有 parent-death guard，只拥有直接 TUI/backend；未验证，不沿用旧 Go fixture 的直接父进程保证。 |
| 完整 G0 与 G1–G4 | G0 尚未全部通过；不启动 G1–G4 正式功能。 |

用户已允许项目内实现与受控实验，测试使用 Luna，禁止 Astra。用户于 2026-09-13 批准固定 owner 回传和一次用户级注册/撤销；不得影响任何现有 CLI 账号的 auth。实际账号登出、认证写入、全局账号配置修改和永久安装仍未授权。
