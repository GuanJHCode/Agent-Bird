# 通用主脑与隔离编码

目标：Grok、AGY 可在托管独立 worktree 编码；Claude、Grok、AGY 可作为主脑，复用已有会话控制、模型、预算、候选验证和审查约束。

基线：Agent-Bird `30b05c3`；本任务不修改已安装运行时、第三方配置或原研究仓现场。用户随后授权推送源码；本次不发布安装包、不替换已安装版本。

## 决策与实现

- 编码共用现有 CandidateWorkspace/Host/freeze 流程；Grok 添加实际文件编辑工具 `search_replace`（同时支持新建文件），使用 `acceptEdits`；AGY 使用 `accept-edits`。不允许 legacy 权限参数选择此模式，不添加 bypass/always-approve 或任意 allow 规则。
- Grok 编码使用原会话目录隔离机制，绑定本次 grant 派生的 session ID，不复用旧意图或额度。
- 新协调器声明 `isolated_provider_coding_v1`；新编码任务及 fallback 遇旧运行时拒绝，不强制重启在途任务。
- 通用主脑先采用显式托管 launcher，以进程生命周期作为归属；不是自动发现第三方原生 conversation ID。同一进程中 `/clear` 或 `/new` 不创建新归属；需另启 launcher 获取独立范围。
- Codex 保持既有原生 thread 绑定。服务端 Unix peer、PID birth、能力文件、worker 防递归校验继续生效。
- 便携包增加 `agent-bird` 入口，无需先安装 Codex。第三方插件自动发现尚未实现，主脑需读取打包 Skill。
- 结构化候选审查仍仅支持 Claude，不把编码能力和审查协议混为一谈。

## 负责人和证据

主会话：adapter/Host 编码、兼容能力、打包、文档、集成验收。
隔离子工作区：通用 owner/launcher 实现与测试。
独立审查：架构审查指出原生 ID 缺失；编码安全审查指出 Grok 工具名误用，已采用实际 `search_replace`。

已观察：新增编码测试先失败于 `implementer_unsupported`；实现后 adapter/Host 原测试通过；两种模拟 CLI 的真实沙箱/候选冻结测试通过。打包无 Codex 独立入口测试先失败、补齐后 3 项通过。

## 最终验证与剩余缺口

- Go 全量：`go test -count=1 -json ./...`，611 pass、3 skip、0 fail（`data/go-final.jsonl`）。跳过为新原生编码验收及两个既有真实 Claude 测试。
- 最后一次 Skill 安全读取修正后：controller/currentOwner 定向 `-race` 复测 12 pass、0 skip、0 fail（`data/controller-final.jsonl`）。之前相关 adapter/Host/store/coordinator 竞态验证 381 pass、1 native skip；controller/process 补充竞态 13 pass。
- Python bridge/auth/packaging/skill/runtime suites：142 pass、2 skip、0 fail（`data/python-final.log`）。`go vet ./...`、Skill validator、`git diff --check` 通过。
- 便携包 `0.1.0-universal.1` 构建通过；6 个清单文件摘要与权限、独立 `agent-bird` 入口模式通过（`data/package-verification.json`）。包保留于 `tmp/package-final`，未安装、未发布。
- 独立安全复审 PASS。修正了 Grok 工具名、默认/不可读 Skill 拒绝、外层 Codex 环境剥离、进程组未知状态保留。真实 PTY 输入和正常退出测试通过；此前失败由测试发送字面反斜杠及未持续读取 PTY 导致，未删除断言。
- 主脑正常关闭仅在进程组退出确认后成功；失去 leader 身份而后代仍存活时返回 `controller_process_tree_unknown`，保留私有 `state/controller-*` 上下文，不向未验证进程组盲发信号。主动脱离进程组的后代不在 PGID 检测保证内。
- 用户随后明确授权各一次、最多 120 秒，均已执行且无重试：Grok 23.00 秒失败（编辑权限取消）；AGY 60.66 秒通过真实编码、冻结和两项行为断言。见 [原生验收](native-acceptance.md)；旧一次性预算未复用，本轮两次额度均已消耗。
- 各 CLI 真实主脑委派闭环及原生插件自动加载未验收/未接入。当前通用主脑是显式 launcher 的进程归属；不能宣称原生会话 ID 自动识别或 `/clear` 后自动隔离。
- 未修改已安装插件、第三方认证/配置、旧运行时或原研究仓 Trial 现场；源码按用户随后授权推送，私有证据和安装包不纳入 Git。下一步为解决 Grok 编辑权限交互、单独验收真实主脑闭环；未经新的明确授权不再调用模型。

清理：本任务创建的临时 Python venv、下载的 Grok 文档目录/tree JSON、早期便携包已清理；最终包、验证证据和隔离 owner 工作区备份保留。

本轮验收后补充：独立证据复核通过；源 Skill/支持矩阵同步 AGY 通过、Grok 编辑权限取消。重打包 `0.1.0-universal.2`（生产二进制未变），6 文件摘要/权限通过，保留于 `tmp/package-native-acceptance`；先前 `tmp/package-final` 为验收前检查点。安装包均未安装或发布；源码按随后授权推送。
