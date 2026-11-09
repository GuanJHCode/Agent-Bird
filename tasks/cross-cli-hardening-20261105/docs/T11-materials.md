# 当前 T11 私有交付材料

当前唯一版本 `.5`：source `7eb8c8d434a584fd109d9bed96d8c3a82f3fea98` / tree `2f90c500965cfe9098a9ff203078e5dfd896be36`，包名 `agent-bird-0.1.24-hardening.20261105.5-full-native.tar.gz`。PKG-01～04独立/root PASS，DOC01当前文档独审/root签收PASS。当前参数、安装路径、SHA和准确计数见[root证据](../data/public-evidence/T11-package-r5-root.json)及[交接](final-handoff.md)。

只有一次成功build/一次package、0failedbuild；显式GOMODCACHE使用既有依赖缓存，未沿用.4失败恢复路径。180项原始index、37archive members/25files、普通/空格各24installed files、4entries、MCP8/noTasks、5negative、sameinode整目录move拒绝、2ownedruntime idle/exited、Go7/guard8均通过。当前.5同包Python301/7skip及native metadata各8tools也独立/rootPASS，详见[REG](../data/public-evidence/T12-regression-r5-root.json)。

包内原生runtime仍为macOS arm64、固定Python绝对路径/hash。没有release、签名/公证、正式分支合并或全局安装替换。历史.4及其首次失败build/受限恢复、旧.3真实失败及预算均保留；本包不继承它们为新PASS。真实.5模型LIVE仍NOT_RUN。
