# sd-qual-08：固定 TUI 的原生独立工具回传资格通过

本次单样本通过受控原生资格：原初始任务真正 final 后，合成作业完成；没有新用户输入，唯一 standalone toolOutput 进入同一 root 的持久历史并启动后续新轮，实际 Go ACK 为 `acknowledged`、`decision_count=1`、`effect_count=1`。TUI 正常 quit 成功。该结果只覆盖这一个固定、自有 TUI 的样本，不计作通用当前会话附着、冷启动自动接入或完整 G0。

本次运行主树 `6c0a805`（代码 `9bd967c`），普通 Codex 0.154.0。前检确认普通入口实际路径/固定 native 与 Go SHA、冻结六脚本、默认 socket 空、sd08 case/job 未存在，再执行 `codex app-server --listen unix://` 与 `codex <固定初始任务>`。初始参数只包含用户任务，不承载结果。见[前检](native-sd-qual-08-preflight.json)、[启动记录](native-sd-qual-08-launch.json)。

| 身份 | 实际值 |
|---|---|
| root | `01a094db-aaaf-7dc3-8308-8077d4f778b3` |
| 初始 turn | `01a094db-ab4b-7ef3-835d-8e157704bd6a` |
| 回传 turn | `01a094dc-770d-7261-a1f7-9bec93a140ae` |
| controller / service / TUI | `67411 / 67418 / 67446` |
| Go owner / ACK root | 与上述 root 一致 |
| 原生稳定 sessionId | 本样本实际等于 root；并未将相等当作一般前提 |

初始 120 秒窗口为 2026-09-12T09:03:38.857154+00:00 至 2026-09-12T09:05:38.857154+00:00，在 TUI spawn 前开始。只登记一次 `initial-argv`，2089 字节，零初始 PTY paste/Enter；post-launch origin 于 2026-09-12T09:03:39.222661+00:00 捕获，明确不是输入前证明。intent→origin→controller evidence 的原字节摘要链已独立核对一致。

| 关口 | 证据 |
|---|---|
| 后验双 Hook | SessionStart 2026-09-12T09:03:41.717289Z；UserPromptSubmit 2026-09-12T09:03:41.728387Z，同 root/原 service；原初始窗口各 1 条 |
| 真正 final 后仍 pending | READY 为 `SD_QUAL_08_READY`；对同一原生 READY 完成事件的复读夹在两次 Go pending 读取之间 |
| 作业完成与 prepare | Go read completed；原服务 prepare 的 root_idle、prior_tools_terminal、fresh_root_family 均为 true |
| 唯一发送 | `send-attempt.json` 一份；send-once 原始独立 120 秒窗口未变，helper 用时 25.610699041 秒 |
| 原历史 | `history_match=true`，匹配 `functionCallOutput`，新轮完成且为最后一轮；ACK final marker 为 true |
| 实际业务 ACK | command `sd-qual-08-ack-1`，revision 1，handled，effect_count 1 |
| 正常退出 | `/quit` 的 paste+Enter 仅用于退出；227.9025 ms 内 exit 0 |

[运输收据](native-sd-qual-08-receipt.json) 仅确认请求返回 inProgress，保留 `history_delivery_proven=false`；交付结论依据[后续原生观察](native-sd-qual-08-send-observation.json)及[真实 Go ACK 文件](native-sd-qual-08-job-ack-r1.json)，不把运输响应当作持久历史。

独立[离线原捕获审计](native-sd-qual-08-offline-audit.json)确认 1 个精确历史工具输出，但原 `audit-input.json` 的 acks 数组为空，且分页链完整性未证明，因此该离线捕获仍显示 business_ack_complete=false、captured_page_chain_complete=false。原捕获未改写；实际 Go ACK 另有原生观察和磁盘文件一致的证据。这一限制仍阻止把单样本扩大为完整 C11/G0 对账通过。

helper 保留 `source_service_verified=false` 与 `current_display_verified=false`，它本身不证明一般当前 UI 关系。本样本依据受控 controller 的自有服务/TUI 创建身份、同 socket、后验双 Hook、真实 Go owner 和固定 root 组合证据；不扩大该受控事实的适用范围。

TUI 正常退出后，外层另行核对 service 67418 的 PID/birth、实际 executable_path/SHA、原 socket dev/inode/owner/mode、唯一 holder 和 Unix peer。对自有服务 SIGINT，155.372125 ms 后 service 与明确自有子进程 68154 均不存在，默认 socket 消失；controller 与 caffeinate 也已退出。见[所有权](native-sd-qual-08-cleanup-ownership.json)、[收尾](native-sd-qual-08-post-window.json)。保护配置与六脚本未变。

原 case/job 的 19 个文件（含原发送意图、收据、历史捕获和锁文件）保留，见[字节清单](native-sd-qual-08-artifacts.json)；最终事实复核见[验证摘要](native-sd-qual-08-final-validation.json)。没有新建临时脚本；本次保留的日志和证据都是交付物，原有 pycache 未动，未清理非本任务进程。

下一可执行动作是复核本次事实后，按合同前瞻登记独立基础重复，重新冻结唯一 nonce 与新鲜前检。sd08 不重试、不复用原 case 或发送意图；G1–G4 继续等待完整 G0。
