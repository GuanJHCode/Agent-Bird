# Agent-Bird 本地 Codex 接手包

生成日期：2026-11-05（北京时间）。
审查基线：29691e9f08468b1de56cb0c42977084b58c91975。
目标分支：feat/cross-cli-subagent-20261104。
仓库：https://github.com/GuanJHCode/Agent-Bird

本包提供完整实施方案、可直接复制的执行提示词、独立审查/续接提示词、62 项验收合同和15项任务的状态模板。全部任务初始为 pending/NOT_RUN；方案评审通过不代表产品功能已实施或已验收。

## 使用步骤

1. 将本包解压到本地 Codex 可读取的目录。不要覆盖项目已有文件。
2. 在 Agent-Bird 项目开启 Codex 主会话，或者让它先定位已存在的仓库。
3. 让 Codex 读取 01-implementation-plan.md、03-review-and-resume-prompts.md、04-acceptance-matrix.md 和 TASKS.json。
4. 将 02-local-codex-prompt.md 中的“提示词正文”完整发送给它。
5. 由主会话按依赖推进；每功能由另一 agent 独审，主会话验收后关闭。上下文不足时使用 03 文件的续接提示词并读取仓库现场记录。

不想先复制长正文时，可发送这个启动指令（在会话中指定实际解压目录）：

> 请读取我提供的 Agent-Bird-local-codex-handoff-20261105 目录内全部 Markdown 文件和 TASKS.json。按 02-local-codex-prompt.md 的提示词正文执行，01 是完整实施合同，04 是验收矩阵，03 是审查与续接流程。请从 T00 开始实际实施、测试、独立审查和主会话验收，持续完成所有已授权且无阻塞任务。不要只输出计划；本包未授权 push、公开发布或替换全局插件。

## 文件索引

| 文件 | 用途 |
| --- | --- |
| 01-implementation-plan.md | 架构合同、逐任务改造、依赖、兼容/迁移、测试、交付与源码依据 |
| 02-local-codex-prompt.md | 可直接复制给主 Codex 的完整执行提示词，独立于旧聊天也能使用 |
| 03-review-and-resume-prompts.md | 单功能独审、最终独审、主会话签收、换会话续接、任务委派模板 |
| 04-acceptance-matrix.md | 62 项可观察验收条件和证据记录格式 |
| TASKS.json | 15 项任务、依赖、审查、签收和最终包身份模板 |
| 05-handoff-review.md | 本交接包的独立方案复核记录；不是产品实现验收 |
| SHA256SUMS | 包内交接文件完整性校验 |

## 本轮实施重点

先修正常见开发任务和结果语义：新目录、rejected 重放、参数与预算、长反馈。
再修回执桥：RPC 有界、历史可恢复、未知发送不重发；优化 hash/probe 与 inspect。
随后明确 owner、增加普通 MCP 工具、核对兼容基底。
最后用同一最终包做双向多文件验收，统一文档和安装交付。

MCP Tasks、TUI 主动唤醒按实际父 CLI 的能力单独准入；当前不能核验原生会话的路径继续明确 managed_process。typed worker resume 保持关闭。

## 任务状态

pending → in_progress → awaiting_review → changes_requested 或 awaiting_owner_acceptance → done。

blocked 是未完成，不等于 done。已存在修复可以 completion_basis=verified_existing，但仍需要当轮测试/核验、独审和主会话签收。

交接资料完成与产品验收通过分开：核心必需项 FAIL/BLOCKED/NOT_RUN 时 core_status 不能为 PASS；T13 仍可记录受阻交接、准确列明原因和恢复入口。资料可标 PREPARED，但 T13 产品签收与 task 状态仍为 BLOCKED，最终产品验收通过后才 done。

LIVE-03 自然缺陷返工未触发时可单列 NOT_EXERCISED；确定性返工及其它核心门槛通过可以形成范围受限的核心版本，不能宣称该 live 场景已验收。

T09 是条件性宿主能力任务：其 live gate 不阻塞核心方案交付；若相关代码纳入最终包，须先完成代码冻结与非 live 审查，未验证能力保持关闭。完整“任意目标会话主动回调”不能仅凭核心交付宣称已完成。

## 本地需要核实

macOS 与架构、Go 1.26+、Python/pytest、实际支持的 CLI artifact、用户当前有效的账户/模型授权、现有任务现场和私有安装目录。历史验收文档中的账户、模型和授权不能直接复用为本轮许可。

当前主执行提示词支持任务分支/worktree、实现、私有安装、已授权的限额测试和本地提交。远端发布等动作按本地会话已有授权执行，本包不自动扩大授权。

## 验收诚实性

- unit、fixture、OS-native-local、installed-package、live-parent、live-provider 分别报告。
- ACK、accept、传输确认与代码集成分开。
- 原始合同首次派发前冻结；第二轮新增要求不是首轮未达标。
- 模型首轮全对则该路径通过，缺陷返工场景 NOT_EXERCISED；不得伪造失败。
- 生产材料改变后生成新包身份，按影响重跑；不同包结果不拼成一个最终包全通过。
- 原用户修改、旧 runtime/handle 与失败证据保留。

## 校验

在解压目录运行：

~~~sh
shasum -a 256 -c SHA256SUMS
~~~

本包只包含交接文档和任务模板，源码从上述 Git 仓库读取。文档中的路径以审查基线为准；接手时先对照最新实际代码，避免重复修复。

