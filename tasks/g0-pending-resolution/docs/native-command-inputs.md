# 固定 /status 与 /quit 输入

sd02 的短命令同批输入存在可达的 PasteBurst 风险，但没有采到当时内部输入状态，不能将它认定为实际唯一根因。旧样本仍是 `/quit` 后 10 秒未证退出，后续进程消失与安全收尾另计。

固定 0.153.4 源码有 slash 例外：textarea 首行已经以 `/` 开头，或 Command popup 已活跃时，Enter 不受普通 burst 抑制。但从空 draft 快速输入时，首个 ASCII `/` 可先保留；第二个字符在 8ms 内会启用缓冲，后续字符尚未进入 textarea。popup 同步只看 textarea，因此紧接的 Enter 仍可看到空文本、active burst，加入换行而不提交。[composer 路径](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/bottom_pane/chat_composer.rs#L3183)与[缓冲状态机](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/bottom_pane/paste_burst.rs#L220)支持这一条件路径。若此前已 flush，slash 例外就生效；一次 os.write 并不证明具体调度节奏。

固定 `/status`、`/quit` 均改为完整 bracketed-paste 帧，再独立 Enter。显式 handle_paste 会插入短文本、清除 burst 状态并同步 popup，随后 Enter 可以[分派已选中的本地命令](https://github.com/openai/codex/blob/3d2ee51ca2d5db578f328aa75e20aa22c0197c9a/codex-rs/tui/src/bottom_pane/chat_composer/slash_input.rs#L341)。没有新增任意命令入口、Ctrl-C 或计时 sleep 来猜阈值。

`status-paste` 沿用原先 drain、清旧 buffer、记录 start byte 的因果捕获；`status-submit` 保留同一捕获，后续非 status 输入关闭。status 的独立 RAW 10 秒预算从首动作前开始，包含 drain、两次发送及根身份解析。quit 同样从首动作前开始，只在原窗内发送下一动作、观察退出；记录实际 exit code、观察时间与 `normal_exit_within_window`，超窗或非零退出都不算正常退出。存在尚未提交的 paste 时拒绝拼接退出命令，留待明确归属的外层收尾。

固定 action/原预算用例先 RED，再完成实现。最新 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v` 为 `Ran 25 tests in 0.751s / OK`。本地 Python PTY fixture 核验完整 status/quit 帧、独立 CR 与新鲜 status 捕获；预算用例覆盖发送耗尽期限、读操作跨期限以及期限内退出。它们不是 native 语义通过；无模型 fresh TUI 的 status/exit 资格尚未运行。nonce 仍是 02，旧 case 原字节不改。

三份固定公开源码的实际字节数与 SHA 见[来源清单](native-command-input-source-manifest.json)。临时公开下载只为独立复核保留，没有修改 native 源码或配置。
