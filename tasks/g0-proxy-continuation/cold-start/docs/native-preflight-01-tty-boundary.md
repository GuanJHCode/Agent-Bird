# native-preflight-01 terminal initialization boundary

Read-only review of the original case: child exit 1, 148 PTY bytes, bracketed-paste/keyboard/cursor/color queries followed by `Operation not permitted`; no backend/status/quit/model activity. Its auth comparison remained unchanged. No original artifact or production source was modified, and this review did not run native or launchctl.

The `/dev/tty` RDWR hypothesis is conditional. Fixed crossterm [`file_descriptor.rs:113-140`](https://github.com/openai-oss-forks/crossterm/blob/45fecb9508105988f42fe6ff0441783ed3717f92/src/terminal/sys/file_descriptor.rs#L113) borrows stdin when it is already a terminal and uses `/dev/tty` RDWR only as a fallback. Fixed Codex `tui.rs:422-430` first checks that stdin/stdout are terminals. Its emitted keyboard/color queries also place the observed failure after `enable_raw_mode` in `set_modes`.

Fixed [`tui.rs:505-506`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/tui.rs#L505) constructs the terminal, then installs macOS stderr suppression. [`tui/terminal_stderr.rs:135-168`](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/tui/src/tui/terminal_stderr.rs#L135) enters suppression when stdout/stderr identify the same TTY, duplicates stderr, opens `/dev/null` for writing and uses `dup2`. Errors propagate from installation. The case's cursor-reset sequence before mode restoration is also consistent with the already-created terminal being dropped when this later installation fails.

A new dummy Python process used the real production controlling-PTY spawn helper, actual sandbox-exec, a fresh synthetic profile and two newly owned test PTYs. It tested stdin raw/restore, `/dev/tty` read and RDWR variants, size ioctl, the complete stderr duplicate/open/dup2/restore sequence, anonymous pipes/socketpairs, an unrelated newly owned PTY, and fresh ptmx allocation. The original profile corresponds to production `27c8cee2033650dd9d8600542ee361a6f5bd19f1`. Tests explicitly reconstruct that denied baseline when comparing added grants.

| Operation (0 = success, 1 = EPERM) | Slave-only baseline | Add `/dev/tty` read/write/ioctl | Add only `/dev/null` write |
| --- | ---: | ---: | ---: |
| stdin raw + restore | 0 | 0 | 0 |
| `/dev/tty` read + TIOCGWINSZ | 0 | 0 | 0 |
| `/dev/tty` RDWR + raw/restore | 1 | 0 | 1 |
| `/dev/null` O_WRONLY | 1 | 1 | 0 |
| full stderr suppression + restore | 1 | 1 | 0 |
| other test PTY raw ioctl | 1 | 1 | 1 |
| new `/dev/ptmx` open | 1 | 1 | 1 |
| anonymous pipe / Unix socketpair | 0 / 0 | 0 / 0 | 0 / 0 |

The justified candidate fix is only `(allow file-write* (literal "/dev/null"))`. Adding the tty alias does not fix the reproduced stderr failure. The source and syscall evidence identify a concrete startup blocker matching the native failure order; the next authorized native case must establish the final outcome.

Latest bounded command:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -B -m pytest -q -s -p no:cacheprovider tasks/g0-proxy-continuation/cold-start/tests/test_controlling_tty_boundary.py --tb=short
```

Result: **3 passed in 0.21s**. All newly owned test PTYs/processes/temp roots were closed or removed. No user TTY or real auth target was accessed by the dummy processes.
