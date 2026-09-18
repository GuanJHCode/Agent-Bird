# Grok headless 编辑许可修复

## 范围与基线

基于 Agent-Bird `62c47bb`，独立 worktree 分支 `fix/grok-edit-permissions`。仅修复已声明候选文件的 Grok 编辑许可；不修改第三方配置、安装缓存或已有会话。此前 Grok/AGY 各一次的原生验收额度均已消耗，用户随后另行授权 Grok 一次、最多 120 秒的隔离验收，本次已执行且没有重试。

## 根因证据

此前 Grok 1.0.34 原生试验中 `read_file` 成功，`search_replace(calc.py)` 的权限请求被取消，文件没有变化。已有 `--permission-mode acceptEdits` 未使 headless 编辑成功。

官方当前源码的 [PermissionMode 定义](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-agent/src/config.rs) 和 [headless 权限请求处理](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/src/headless.rs) 支持这一解释，但公开仓库不能解析本机构建号，不能将 main 当作安装版本的精确源码。

## 修复契约

- Host 在成功 materialize 后，以 `PreparedWorkspace.Receipt.Worktree` 和 `Paths` 为唯一依据，生成每个候选文件的精确 `--allow Edit(absolute-file)` argv。每次尝试重新生成，不复用旧目录。
- 拒绝越界、Git 元数据、规则或 glob 字符、控制字符、符号链接、非普通目标和已有硬链接；全量检查成功后才追加规则。
- 保留原有 deny/ask、MCP deny、工具限制、OS sandbox、候选冻结和 legacy Permission.Allow 拒绝。未声明文件不获许可；不修改任何持久化第三方配置。
- Grok implementation capability probe 增加 `--allow` 要求，read-only profile 保持不变。

## 位置与验收

实现：`tools/orchestrator/internal/host/grok_edit.go`、`host.go`、`candidate.go`；capability：`internal/adapter/profile.go`；对应测试及仓库 Skill 同步。

红灯：增加唯一 `Edit($PWD/calc.py)` 要求后，旧 Host fixture 以退出码 72 失败。绿灯：修复后 Grok、AGY Host fixture 通过，包括禁止向工作区外及 Git 元数据写入的检查。路径/授权边界测试通过。完整 Go 初轮 652 pass / 1 fail / 3 skip（含子测试）；唯一失败为新 capability 测试沿用 reviewer 的 plan 模式，已改为 implementation 的 default 输入。此后 adapter + host 的 `go test -race -count=1 -json` 为 237 pass / 0 fail / 1 native skip；新增真实 Host 双 attempt 测试验证新路径、MCP deny 和失败 worktree 保留。合并有效快照证据为 654 pass / 0 unresolved fail / 3 native skip。`go build ./...`、`go vet ./...`、Skill quick_validate 和 diff whitespace 检查通过。独立实现审查及最终文档/测试复核均无阻断项。

## 原生复验（2026-09-18）

Grok 1.0.34 单次原生验收通过，测试用时 25.85 秒（120 秒上限）。原生事件确认 `read_file` 和 `search_replace` 均获 allow 并成功；`calc.py` 从 `a - b` 改为 `a + b`。Host 正常退出并冻结候选 `55dbb81bc33ab9a4a3a9eb3a997ea2f69f7d3164`，候选内容通过 `add(2,3)==5`、`add(-2,3)==1`。原始测试仓库 HEAD、文件和干净状态不变。

私有证据保留在本任务 `data/native-test.jsonl`、`data/native-acceptance-summary.json`、`data/native-grok-01/`，既有失败会话与额度记录均保留。本次一次性额度已消耗，未自动重试。没有修改第三方持久配置、扩大 sandbox 或放宽 legacy guard。

本次证明固定版本在逐文件许可下可完成真实隔离编辑、候选冻结及行为检查；不代表原生主脑委派、结构化审查、集成全链路均已验收。本记录对应源码修复；已安装插件未更新。
