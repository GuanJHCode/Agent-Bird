# T00 基线与现场

2026-11-05。实际实现基线与 fetch 后 origin/feat/cross-cli-subagent-20261104 均为 `29691e9f08468b1de56cb0c42977084b58c91975`，没有后续差异。对照树 `4b2b4916232c9d7526f51cf0a73204748c62faf8` 仅做内容比较。

原仓库 `/Users/guanjunhui/code/ai/codex-cli-orchestration-design` 保留原分支、全部未跟踪文件和旧 worktree/runtime/refs。原未跟踪范围：`.qoder/`、`Library/`、`docs/capability-index-20260930.md`、`tasks/cli-version-compatibility/`，以及 native_bridge/trial/scripts/runtime/native/native-product-bridge/real-user-trial 的 `__pycache__/`。没有 tracked/index 修改。未执行 reset/clean/stash/prune/GC，fetch 触发 Git 自身后台自动维护警告（旧 gc.log/unreachable objects），未更改维护配置或清理旧对象。

集成 worktree：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration`；分支 `feat/cross-cli-hardening-20261105`。主会话为此 worktree 唯一写入者。实现 worker 使用另外的隔离 worktree，代码在独审 PASS 后由主会话集成、验证、提交、push 本任务分支；不自动合并原分支。

交接目录的 7 项 SHA256SUMS 全部核验 OK。已读取全部交接 Markdown/TASKS、根 AGENTS、原实施合同/验收、runtime/git-cleanup/provider-runtime/support-matrix/testing/README。交接原件保存在 plans/handoff，历史授权描述不覆盖本轮用户授权。

环境：macOS 26.3.1 (25D2128)，arm64。默认 PATH 没有 Go，默认 Python 3.14 没有 pytest；已核验可用的原私有 Go `/private/tmp/go1.26.10-sdk/bin/go` 为 1.26.10，原私有 venv `/private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python` 的 pytest 为 9.2.0。使用显式 PATH、GOENV=off、GOTOOLCHAIN=local、GOPROXY=off、TMPDIR=/private/tmp；各 worker 独立 GOCACHE。无需修改系统工具链。

已核验本机非模型版本探测：Codex 0.166.0 SHA256 `39acf979729dd290f9a24ee38b2914e036c91b65952872e28ae2a6b88a7db7c1`；AGY 1.4.12 SHA256 `34423fc9b0af3841e7646aa72a2d40ffeab8218b22d1cb99eac7355f9eba31e3`；Grok 1.0.70 SHA256 `5c8a688251bf61e35370b828b6a353b0a20fcc3f64000de8a91d426039f29cec`。Claude 不在当前 PATH；旧安装和版本的定位与 receipt 核验待后续，不以旧记录冒充当前可用。未读取认证值、未执行真实模型、未继承历史模型授权；live 前确定可用账号/模型与有限预算。

初始测试：本轮相关基线测试待执行并单独登记；历史 240 测试和 .4/.7 双向结果均只作为历史，不代表本轮通过。实现阶段可在不修改生产代码的 T00 文档阶段执行基线，证据保存到 data/public-evidence。
