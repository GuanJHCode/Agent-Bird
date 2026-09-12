# sd08：一次原生 argv 初始任务与后验双 Hook

按主合同 `a39d7df` 实现。首个固定任务经 `['codex', fixed_prompt]` 提供一次；InputSequence 领取后只允许必要的 quit，拒绝第二次 argv、initial paste/Enter 和状态导航。服务入口、0.154.0 版本/SHA、原配置、后续唯一 toolOutput 及 helper binding 顶层字段保持原合同；本提交没有启动原生样本、修改 helper 或覆盖用户配置。

初始 120 秒从携 prompt 的 TUI spawn 前开始，包含持久化意图、启动、后验 Hook、READY/pending 屏障和作业完成。原 TUI 10 秒启动上限也从该起点计算。写意图或启动观察跨期均中止；NativePTY 在实际 fork 前再次核对同一原启动截止点。post-launch origin 不重置初始窗口，也不作为输入就绪证明。

摘要链只追加：

1. `launch-intent.json` 在 spawn 前以 0600/O_EXCL/fsync 写入，含 nonce/case/job/boot、本 service PID/birth/actual executable、固定 native SHA、socket 身份、初始任务 bytes/SHA 与原窗口。仅存摘要，不导出任务正文，不声明 native 已接收。
2. `native-origin.json` 在 native TUI 进程身份可观察后写入，含独立实际 service/TUI/socket、捕获时间/时钟、PTY 元数据和原 intent 字节 SHA；scope 明确它可能晚于原生自动提交。
3. `controller-evidence.json` 同时引用两份原始文件 SHA，并绑定后验 root、双 Hook、Go owner、READY 屏障与完成读数。每次 helper 前按原窗口复查双 Hook、原生身份与两份受限原始文件；不回写 launch-intent。

原窗口内的本服务唯一 startup SessionStart 与唯一 UserPromptSubmit 必须同 session/root，且顺序成立。Hook 可早于 post-launch origin；缺失、重复、异 root、超期或发送前变化均不准入 helper。原生 source/cwd/version、显式 family/session、SQLite parent edge、Go owner 与发送一次/uncertain 不重发判据延续。

TDD 证据：

- [初始 RED](native-sd-qual-08-initial-red.log)：原实现没有 argv 初始任务，且没有意图持久化跨期门禁；旧 PTY 初始输入仍被允许。
- [初始 GREEN](native-sd-qual-08-initial-green.log)：argv、早到 Hook、原窗口、身份/摘要变化等 12 项相关检查通过。
- [Hook 复查 RED](native-sd-qual-08-hook-recheck-red.log)：本地证据读取后 SessionStart 改变仍到达 prepare；已把原双 Hook 复查放入每次 helper 前的身份检查。
- [最终 native 检查](native-sd-qual-08-native-suite.log)：50 tests PASS，1.166 秒。`git diff --check` 退出 0。源码 SHA 见 [冻结清单](native-sd-qual-08-native-freeze.json)。

sd07 的前置 Hook/paste 测试已按新合同替换为 sd08 后验路径；已归档 sd01–sd07 文件及 sd03 canonical digest fixture 未改。历史 `/status` 的低层 PTY 测试使用显式 test-only 输入序列，不给当前运行入口恢复旧输入能力。临时测试目录由测试释放，未新建待清理脚本；原有 untracked pycache 保留。

仍需独立 native review、固定 helper 的跨组件回归、主树整合与临近 RUN 的新鲜前检。该实现或本地通过不计作原生资格通过。
