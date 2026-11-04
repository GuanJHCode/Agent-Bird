# 跨 CLI subagent 最终复核

**2026-11-04：核心功能 PASS，整体验收 BLOCKED。** 实现已保护性集成到原工作区；两个方向的真实编码、直接返工和业务验收均有证据。认证 refresh-tripwire 仍有 1 项失败，不能宣告整体交付完成。

后续用户已明确授权推送远仓。本地 `main` 与远端 `main/master` 无共同祖先，因此使用独立分支 `feat/cross-cli-subagent-20261104` 保存当前已验证代码及必要报告，保留远端既有分支。下文“HEAD 未变、0 staged、未推送”等内容均描述实施验收时的历史状态；推送授权不改变上述验收结论。此次提交同时保留已验证代码所依赖的原有兼容性基底，未审核的本机目录和缓存继续留在本地。

## 范围与版本

本轮依据最初四项缺口及六个核心场景实施，由主会话编排、各隔离 subagent 落地、独立审核者复核，最后由主会话核对实际代码和原始报告。复用了 [Agent-Bird Skill](/Users/guanjunhui/.codex/plugins/cache/agent-bird-local/agent-bird-local/1.0.0-rc.4/skills/agent-bird/SKILL.md) 的固定运行时与委派约束；真实反向试验加载的是本次私有包生成的 `.7` Skill。

| 项目 | 实际值 |
| --- | --- |
| 最初审核提交 | `4b2b4916232c9d7526f51cf0a73204748c62faf8` |
| 原工作区 HEAD，仍未变 | `5e7443d24cff6deef27ed1c52697a2fb32733ae4` |
| 包含用户原有修改的保护性输入快照 | `eb0d49ce2a06d1268edb0a14f99ce66f3402fe47` |
| 最终冻结生产源码 | `68e875868215ecb1fd8daf540799fd3d0eee25e3` |
| 最终私有包 | `0.1.23-cross-cli.20261104.7` |
| binary SHA256 | `ce05c0fd3c72a5f9f808f3c0eeb28fb0fdd05922ec740d0e6826d1028700d97c` |
| manifest SHA256 | `3a9493d68cb6385efa321aeb3084f79ac4ed113e16f69608bbf18a7576e68eb7` |
| 真正执行的 CLI | Claude Code `2.1.316`；Codex `0.166.0` |
| Claude 认证及模型 | 用户指定的既有 Anthropic 兼容网关、`deepseek-v4.1-flash-1017`；未声称使用 Anthropic 自有模型 |
| Codex 认证及模型 | 当前既有登录；CLI 既定默认模型，无账户或模型回退 |

安装绑定见 [install-result.json](../data/final/package/install-result.json)。没有推送、公开发布或替换全局插件；原工作区没有新增 staged 项。

## 按最初四项缺口复核

| 原缺口 | 本轮结果 | 源码和证据 |
| --- | --- | --- |
| 产品入口与 Go 实现失配 | PASS：controller/routing/dispatch/wait/reconcile 与实际 wrapper、生成 Skill 对齐；真实 Claude 加载并调用 Skill | `cmd/orchestrator/controller.go`、`task_dispatch.go`、`task_reconcile.go`；`build-native-entries.py`；包 smoke、26 项 delivery/G1、真实父会话记录 |
| 当前主会话入口绑定 Codex | PASS：通用 owner 与当前 Codex 识别分离，保留 capability 校验；真实 Claude parent 使用 current_owner 控制 Codex | `current_owner.go`、`local_owner.go`、`current_codex.go`；[反向 parent 摘要](../data/final/claude-to-codex/parent-summary.json) |
| 实际受管编码受限 | PASS，仅限已验证组合：Claude gateway 2.1.316、Codex 0.166.0 产生真实候选；精确文件范围、认证隔离与冻结门禁保留 | `internal/host/claude.go`、`codex.go`、`managed_policy.go`，`lib/trial/codex_exec.py`；两方向 candidate/source/diff 与沙箱验证 |
| 主审直接返工局限于旧四阶段 | PASS：新增 owner request-changes v2，绑定确定事件/revision/candidate 和反馈；下一 revision 基于旧候选，旧 v1 链保持兼容 | `cmd/orchestrator/request_changes.go`、`internal/store/owner_rework.go`、schema 8→9、Host v2 能力门禁；真实 feedback receipt 与回归测试 |

实现没有新建数据库；ACK 仍仅表示回执处理，accept 才是业务验收。技术重试和审查返工分别计数，同时保留总 attempt/active 预算。候选真实性校验独立于剩余返工资格；即使不能继续返工，仍能审查和验收真实候选。

## 按最初六个场景复核

| 场景 | 结果与证据 |
| --- | --- |
| 当前主 CLI 委派另一 CLI 实际改代码 | PASS：两个方向均有真实 CLI、候选 OID 和 Git blob；并非 stub 替代模型 |
| 结果返回原主会话，继续审查 | PASS：主会话调用 bounded wait/collect，实际读取源码和 diff，随后继续决策；未宣称任意 TUI 异步唤醒 |
| 主会话发结构化修改意见 | PASS：revision1 ACK 后公开 request-changes；反馈和验收标准绑定指定候选；正向同 command 重放返回相同 receipt |
| 基于旧候选生成新 revision 并保留历史 | PASS：两方向 rev2 的 parent/base 均为 rev1；旧 private ref 保留，work_revision 从 1 到 2 |
| 检查实际代码和测试后才验收 | PASS：每轮实际 source/diff 审查、OID/SHA 绑定的 Seatbelt 验证；rev1 4 例、rev2 13 例；accept 与 ACK 独立 |
| 重复/旧事件、失败、主会话恢复不误操作 | PASS（自动化回归及实际失败状态）：幂等、冲突、旧 revision、互斥、提交恢复、失联/终止与预算均有回归；实际前两轮反向失败未隐式重试或重复执行 |

### 正向：当前 Codex 主会话 → Claude Code

- 使用原私有安装 `.4` 及原句柄；run `bird-0631629a781a812f6b12ccdf`，2 attempts。
- rev1 `a4b158afe296c286a38f687caf1f6319539e712e`：基础加法；rev2 `c2069762bd53c57406898ad1b6add644c2f8a3f5`：按主审意见拒绝 bool、非数字及 int/float 子类。
- 当时 `.4` summary 在最后 attempt 隐去 candidate OID。主会话核对原 artifact/projection/DB context 摘要、完整绑定和 8 条只读 Git 后，通过原公开 accept/ACK 收尾。**这是实际模型候选加人工可信收尾，不能称旧 driver 自动全流程通过。** 缺陷已修复并独审。
- 最终 completed、active=0、collect 为空。accept/ACK 后才在任务私有试验仓安全 FF 到 rev2；原用户项目未执行该示例合并。
- 证据：[人工复核](../data/final/codex-to-claude/manual-review-2.json)、[rev2 验证](../data/final/codex-to-claude/validation-2.json)、[反馈重放](../data/final/codex-to-claude/feedback-replay-proof.json)、[验收](../data/final/codex-to-claude/manual-accept-result.json)、[ACK](../data/final/codex-to-claude/manual-ack-result.json)、[验收后集成](../data/final/codex-to-claude/integration-proof.json)。

### 反向：Claude Code 主会话 → Codex

- 实际 Claude 加载 `.7 --plugin-dir`，调用 `agent-bird-claude:agent-bird`，再通过公开工具委派 Codex；run `bird-cdf0eee1eede23a414ba11e0`。
- rev1 `50c619a6f1d672584e11bf42227449f778bf29f9`；rev2 `69c473c69337972e5612cf5c80a44a3a302f8b3d`。主会话每轮 Read 实际源码/diff，再显式绑定 OID/SHA validate；rev1 ACK、反馈、rev2 accept 和 ACK 均实际执行。
- 两次 native Codex 运行成功；review_rework_count=1、technical_retry_count=0。最终 completed、active=0、pending=0。独立根会话核对候选/事件/产物摘要及完整 Git lineage；原试验仓仍是基线 HEAD 且干净。
- 自动工作区移到所选 state 同级的 `0700` 专用根；原失败句柄不搬迁或升级。父会话工具仅 Skill/Read/Bash；一次额外多操作 Bash 被拒绝后未提权。
- 证据：[主会话工具序列](../data/final/claude-to-codex/parent-summary.json)、[第二轮代码](../data/final/claude-to-codex/candidate-2.py)、[验证](../data/final/claude-to-codex/validation-2.json)、[最终 inspect](../data/final/claude-to-codex/final-inspect.json)、[根会话复核](../data/final/claude-to-codex/root-final-proof.json)。

早期失败完整保留：`.5` 因 provider control 未支持 Codex 而在 submit 前拒绝，runs/tasks=0；`.6` 因自动 cwd 落入受保护 state，在 native 模型启动前失败，1 failed attempt、segment exited、host released；修复后新合同 `.7` 用 2 attempts 完成。未把失败改为成功，也未重跑旧句柄绕过预算。

## 测试与独立审核

工具链 Go `1.26.10 darwin/arm64`、任务私有 Python venv。Go 命令在 `tools/orchestrator` 执行，显式 `GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp`。

| 实际检查 | 新报告结果 | 适用范围 |
| --- | --- | --- |
| `go test -race -count=1 -json ./...` | 540 顶层通过、8 跳过、0 失败；含子例 1175 通过 | 最终源码 `68e8758`；14 包通过，contract 包无测试 |
| `go vet ./...`；原工作区 `go build ./...` | 均退出 0 | 最终源码/已集成原工作区 |
| 最终安装 wrapper smoke | 7 通过、0 失败/跳过 | 私有包 `.7`，包含真实安装收据绑定 |
| `pytest tasks/cross-cli-subagent/tests/test_delivery_scripts.py tools/native-product-bridge/test_local_product_driver.py` | 26 通过、0 失败/错误/跳过 | 最终包 `.7`；显式提供 BIRD_TEST_INSTALLED_ROOT 与 ORCHESTRATOR_PRODUCT_BINARY |
| `pytest tools/native-product-bridge tools/real-user-trial`，显式 native opt-in | 212 项：211 通过、1 失败、0 错误/跳过 | Python/native 认证代码最终版本；其后只有 Go 工作区分配和安全诊断增量，G1/包入口已另用 `.7` 补验 |
| Claude 2.1.267 / 2.1.316 本地 SSE native 运输 | 每版本 2 项 race 通过 | 工具运输和写入范围；不是额外真实模型编码声明 |
| 独立最终审核 `review_native_coding` | 核心 PASS；整体 BLOCKED | 冻结源码、原工作区字节/执行位、原始计数、两方向真实记录 |

Go 全量的 8 个 skip 是显式诊断/真实 provider opt-in：cause observation、stage timing、两个 Claude native transport、real candidate、real reviewer、两个多版本矩阵。Claude native transport 另有固定版本专项通过记录，真实核心编码由本报告两个流程证明；没有把 skip 算成通过。

计数见 [counts.json](../data/final/tests/counts.json)、[race 原始计数](../data/final/tests/go-race-v7-counts.json)、[失败 JUnit](../data/final/tests/python-native-final.xml)、[最终包 JUnit](../data/final/tests/python-package-v7.xml)。完整 Go JSONL 与模型转录保留在私有任务根，导出证据只含必要数据。

## 唯一整体验收阻断

`tools/real-user-trial/test_native_auth_tripwire.py:1743` 的 `test_fixed_native_refresh_tripwire_blocks_oauth_egress` 失败：预期 baseline 至少一次受控 OAuth refresh，实际 `baseline_count=0`。因该断言失败，随后 guarded 分支及其“无额外 OAuth egress、认证字节/inode 不变”的真实 oracle **未执行**。

只读诊断观察到 exact native 0.166.0 在合成认证 HOME 下返回 preferences 同步和 network-policy 前置错误；受控 loopback 单独验证可达。fixture 已正确绑定所选版本/SHA，并允许合法 RPC error 终结后继续检查真实 HTTP/auth oracle；没有把 RPC 错误当成功。确切 baseline 前置原因仍待解决，连续诊断无新证据后已停止原路径。

未放宽 sandbox、真实 HOME、全局 preferences、认证或网络权限。两个实际模型流程的源认证文件未变，仅能证明这些运行本身，不能代替失败测试尚未执行的 guarded 证明。需要在保持同等隔离约束的 macOS 环境恢复有效 baseline，重跑同一 baseline + guarded 测试及受影响套件，补独审后再改整体验收状态。

## 集成、凭据与保留

- 生产改动相对保护性输入快照共 125 文件；相对原 HEAD 共 175 文件，均与冻结源码逐字节/执行位一致，0 mismatch。原 HEAD 和 index 保持原状，生产增量 diff check 通过。详见 [integration-proof.json](../data/final/integration-proof.json)。
- 第一次集成前的 80 文件保护性检查及 9 个目录保留证据见 [integration-precheck.json](../data/integration-precheck.json)；不以最终树倒推这些历史摘要。
- 已提供网关凭据的扫描结果、源认证文件未变证明见 [final-credential-proof.json](../data/final/tests/final-credential-proof.json)。报告不包含凭据或 owner/control/bootstrap 文件。
- 收尾已清除受控 runner 的凭据内存并确认进程退出，主会话凭据引用已清空；确认失败任务没有活动 segment 后，按精确 image/PID/birth 温和退出任务私有 coordinator。仅清理本任务新建的一份 `.pyc`，记录见 [cleanup.json](../data/final/cleanup.json)。
- 保留 `/private/tmp/agent-bird-cross-cli-5c9jyn8o` 下必要固定运行时、私有安装及证据，及 `/private/tmp/bird-c2c-5c9j`、`/private/tmp/bird-reverse-5c9j*` 的候选 refs/失败状态；它们仍是句柄及后续排障依赖，不执行 prune/GC。原有未知缓存、`.qoder/` 和 `Library/` 不删除。
- 未验证范围：MCP Tasks、任意已有 TUI 主动唤醒、Grok/AGY 编码、更多 CLI 版本、native resume。本次实现的是新受控 CLI 子进程及 bounded wait/collect 回收。
