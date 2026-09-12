# cached01 首次入口更正（固定 0.154）

cached01 无 fork 的直接源码解释是：新 TUI 没有 side 状态时，Ctrl-/ 与 Ctrl-7 的 toggle 分支只返回，不会首次创建 S。此前把第一次 toggle 当成 fork 入口是本任务方案错误。cached01 的原 result/trace/receipt 保持 unknown，不能改称 cached zero-RPC 反例，也不能证明通用 attachment。

固定 Codex commit 为 `6b9826e3aa83b1a5947db50f4332cb9c65f1b340`。本次必要源码正文及逐文件 bytes/SHA 收据位于 `data/source/side-entry-source-receipt.json`；已有正文复用并对照固定 Git tree 的 blob SHA-1 校验，缺失文件仅下载固定官方 URL。

## 创建与切换是不同入口

- `codex-rs/tui/src/app/startup.rs:715` 将新 App 的 `side_threads` 初始化为空。
- `app/input.rs:485-495` 在有效 side bindings 包含 Ctrl-/ 时才把 Ctrl-7 作为兼容输入，随后调用 `toggle_side_conversation`。调用前还要求没有 overlay/modal/popup（`:399-406,582-583`）。
- `app/side.rs:391-412`：没有当前显示 thread、没有 side，或当前既非 parent 也非 side 时直接返回；存在 side 时才选择 parent/side。尤其 `:399-400` 的空 map 路径不会 fork。
- 空参数 `/side` 走 `chatwidget/slash_dispatch.rs:119-128,336-338`，明确传 `user_message=None` 给 `StartSide`。`app/side.rs:712-715` 发 `fork_side_thread`；`:734-736` 向 S 注入 side boundary item；`:746-748` 选择 S。
- `app/side.rs:769-773` 只有 `Some(user_message)` 才调用 `submit_user_message_as_plain_user_turn`。因此后继样本只允许精确 `/side`，不带任何文本或图片。

首次 `/side` 仍可能因 primary 未建立、当前已处于 side（`:612-618`）、permissions 正在更新（`:687-692`）或 fork/inject 失败而无法完成。这些应保留 unknown，不能通过补发 prompt 或改窗修复。既有 P 已有受控持久历史；不新增 seed 模型回合。

## 配置与键位

`keymap.rs:1498` 默认 side 绑定 Ctrl-/。`:605-610,652-660` 只在用户未显式设 side 且其他 global/list/approval 动作占用了 Ctrl-/ 或 Ctrl-7 时自动清空默认 side binding。显式 side alias 另经过冲突校验。

本次只读配置投影：

| 层 | SHA-256 | keymap / side feature |
|---|---|---|
| `/Users/guanjunhui/.codex/config.toml` | `53b1aa3016356f244170c6b5bc84773767672c852b59f9594f9ae694ebb8eca3` | 无 `tui.keymap`，无 side feature |
| `test-repo/.codex/config.toml` | `298a250c45c9585c695e427c830c20100ecadc8a612445703b2a8b5878a3aa2b` | 无 `tui.keymap`，无 features；model 为 Luna |

读取 test-repo 到根的实际祖先 `.codex/config.toml` 候选只发现以上两份，`CODEX_HOME` 环境变量未设置，也没有指定 profile/CLI keymap override。固定 config 文档顺序是具体 context → global → defaults（已归档 `core/src/config/mod.rs:809-814`）。这次没有读取个人历史/DB，也没有用 RPC 查看服务端 effective config。

`bottom_pane/slash_commands.rs:70-83` 的 feature 过滤项没有 side 开关，空 `/side` 的相关 dispatch 与 app 入口也没有 side feature flag。因此此次入口修正无需增加项目 featureflag，更无需改 global 配置；如果真实创建仍 unknown，应针对当次具体事实另查。

## PTY / termios 边界

仓库 `PTYDriver.spawn` 设置普通 PTY 与 `TERM=xterm-256color`，`write` 原样写 bytes；`_capture` 只回复 cursor query，没有过滤 0x1f 或 keyboard-enhancement 响应。固定 TUI `tui.rs:227-232,422-432` 在初始化时调用 `enable_raw_mode`。固定 crossterm `45fecb9508105988f42fe6ff0441783ed3717f92` 的 `terminal/sys/unix.rs:108-139,299-316` 将 tty 切换为 raw（libc `cfmakeraw` 或 rustix `make_raw`）。既有 parser `event/sys/unix/parse.rs:161-164` 把 0x1f 解为 Ctrl-7。

因此没有源码依据把此次 no-fork 归因于普通 termios 吞掉 0x1f；直接 no-side return 已足以解释结果。cached01 没保存当时 termios 快照，不能宣称已经在运行期逐字节证明 key dispatch。此次调试没有新启动 TUI/RPC/模型，也没有改配置。

## 下一最小样本

独立新 case 预检后：普通 resume P → fresh status(P) → 精确空 `/side` 一次 → 在原 local 10 秒 deadline 内等待同 epoch fork(P→S)、started(S)、已知 inject 配对 → fresh status(S) → 第一次 0x1f 回 P → fresh status(P) → 第二次 0x1f 回 S → fresh status(S) → 测量结束。随后独立 cleanup-return-primary（一次 0x1f + fresh status(P)，共享原 10 秒）→ 正常 quit/owned cleanup；退出边界更正见 `side-cleanup-boundary.md`。

整体原 120 秒和每段 10 秒上限不变；trace/model=0、peer、hash、unknown 早停及 candidate/stale 边界不变。首次创建与后续两个 cached toggle 分开记录。只修改独立 worktree 的 runner/测试/合同/来源文档，main 与旧 case 不写；真实 cached02 仍待主会话的独立预检和准入。
