# 主脑结果展示修复

目标：普通委派由原主脑跟踪到结果/失败、核验并展示，授权范围内自行完成
owner 决策；不要求用户再发“查询进度”。用户明确只要后台派发或要求暂停时
尊重其范围。保留原会话、任务句柄、预算、权限、配置和已有修改。

问题复核：主脑可能在返回 running 后结束回合，遗漏 task wait，使持久化
结果需要后续主动收集。受控进程回归覆盖成功、无正文失败和超大事件失败。
具体业务会话、任务状态及原始日志只在本机保留，不纳入公开提交。

根因：当前 portable 包是 collect-only。coordinator 只能唤醒已连接的
wait-events 请求，不能唤醒已结束的原生主脑回合。原生桥不包含在此包中，
也不能手动附着任意原会话。现有 Skill 有等待描述，但简短入口和派发回执
未明确要求主脑保持回合并完成收尾；queued/running 被误当成交付终点。

用户已明确选择“主脑保持当前任务，自动等待、核验并展示结果”，无需新增
MCP 或修改宿主配置。此选择确认上述前台收尾范围，不要求后台唤醒已结束回合。

本轮范围（root 唯一写入，使用既有隔离 worktree）：

1. 在高层派发回执提供 collect-only、没有自动回调及下一步等待/核验提示。
2. 四个主脑的生成 Skill 与完整协议明确默认持续等待、处理结果并展示；
   30 秒超时继续等，多个句柄公平轮转；明确 ACK 与业务 accept 的区别。
3. 测试延迟结果、失败、等待超时与幂等重用；Skill 情景检查确认不会在
   running 时结束或把主脑验收默认推给用户。
4. 验证和安装新不可变包，核对实际入口，不覆盖旧任务运行时或重启协调器。

边界：这修复 active owner 的收尾流程，不宣称可原生唤醒已结束/断开的 CLI。
没有新增真实模型调用；不放宽大事件 guard，不重试业务任务，不处理业务仓
源码。超限需独立证据后修复，不能简单调大上限或接受截断报告。

## 已完成与证据

- `task_continuation.go` 为 submit/run/dispatch 回执增加 collect-only、
  `automatic_callback=false` 及 wait/inspect/reconcile/report_blocker 指引。
  命令采用 argv 数组，保留原句柄；不输出 capability，不执行语义决策。
- 完整协议和四个生成 Skill 已明确前台等待、逐任务 cursor、超时继续、
  不丢下其他运行任务、核验产物、主脑自行决定、独立 ACK 和展示失败。
- TDD 先复现旧派发回执缺少 continuation；随后定向 16 项全部通过。
  失败测试区分“失败但没有正文”和“超大关键事件的 bounded incomplete
  artifact”。前者不保证报告产物，已按既有 Host 契约修正测试预期；
  没有修改生产失败处理或 guard。
- 完整 `go test -race -count=1 ./cmd/orchestrator`：188 pass、2 skip、
  0 fail。跳过项是 opt-in `TestRealClaudeManagedCandidate` 和
  `TestRealClaudeReviewerProfile`，本轮没有执行真实 Provider 模型。
  既有原句柄 collect/accept/ACK 与候选流程包含在该 CLI 回归中。
- `go vet ./cmd/orchestrator` 通过；入口 unittest 3 项、安装/发行
  unittest 7 项通过，0 failure/error/skip。四份短 Skill、完整协议和
  Codex 插件结构校验通过。
- 独立只读审查 PASS，无阻塞；六个情景覆盖部分拒绝、超时、失败、
  只读验收、用户暂停和宿主结束。此项是静态审查，不代表真实模型验收。
- 证据位于私有且 Git 忽略的 `data/`：`focused-race.jsonl`、
  `cmd-race.jsonl`、`cmd-vet.log`、`package-test-summary.json`、
  `test-summary.json`。未提交原业务提示词、会话日志或控制能力。

## 安装与生效范围

已构建并并存安装 `0.2.0-preview.5-result.1`，binary SHA-256：
`5a1df31824f049c55d5158c1086513bbe0e35319efd3172365e33531a6c0795b`。
构建使用 `-trimpath`；发行包 57 件 manifest 文件摘要验证通过，扫描未发现
本机 home 绝对路径或私钥标识。源码修改未提交或推送。

安装前后快照确认：9 个旧运行时、pins、旧插件缓存、Marketplace 来源、
Codex 配置和原任务证据保持一致；协调器 PID/birth/socket/epoch 均未变。
新入口实际 probe Grok 1.0.40 与 AGY 1.2.7 通过，probe
不等于模型执行或新增指纹授权。证据：`data/install-verification.json`。

**默认原生插件尚未切换。** 当前同名 Marketplace 更换版本目录会冲突，
remove/re-add/install 还会清除旧缓存；不满足保留原会话及无需修改宿主
配置的范围。因此交付已安装的新 binary 和协议入口，具体业务交接仅在
本机保留。旧会话先读取新协议后可继续原句柄；不能宣称旧
Skill 自动刷新、新会话自动获得修复或已经唤醒原主脑。

未完成：全局默认插件的安全更新、Grok 超大关键事件根因修复。
未验证：四个主脑真实模型执行时遵循新收尾流程的效果；本轮无新增模型
预算，未重派旧任务；原 owner 仍负责已有结果的核验与收尾。

临时文件已清理：独立 CODEX_HOME 的 catalog fixture、重复构建 binary、
一次性安装快照脚本与空 scripts/tmp 目录。保留 docs、私有测试证据和不可变
交付包；未删除任何原有文件，见 `data/cleanup.json`。
