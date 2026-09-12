# 隔离原生版本检查

2026-09-13，主会话在认证隔离复审和本机沙箱测试通过后执行三个零模型样本；没有注册 LaunchAgent。

| 样本 | 结果 |
| --- | --- |
| native-version-01 | 原生程序初始化异常退出，code -6；14 个已登记认证文件前后比对一致。 |
| native-version-02 | 相同策略复现，捕获 Rust guard-page 初始化错误；认证比对一致。 |
| native-version-03 | 只增加 `hw.pagesize_compat` 的 sysctl 只读权限，exit 0，输出 `codex-cli 0.154.0`，stderr 为空；认证比对一致。 |

本机 Python 对照试验显示：原策略下 `os.sysconf("SC_PAGE_SIZE")` 返回 EPERM；允许 `hw.pagesize` 仍失败，允许 `hw.pagesize_compat` 后返回 16384。这与 [Rust 固定编译器版本的栈保护页初始化](https://github.com/rust-lang/rust/blob/59807616e1fa2540724bfbac14d7976d7e4a3860/library/std/src/sys/pal/unix/stack_overflow.rs) 使用页面大小的实现相符。策略只新增这一项非认证只读权限；本机 Apple profile 的对应规则见 `docs/source/os-sbpl-receipt.md`。

三个样本均使用全新测试 HOME、CODEX_HOME、workspace、固定 Codex binary 和清理后的环境，未复制真实凭据。测试 CODEX_HOME 内始终没有 auth.json。真实 HOME 写入、登记凭据读取、Keychain Mach 服务和 IP 网络限制继续生效。认证文件摘要仅用于前后比对，不保存凭据正文，也不恢复任何凭据备份。

证据保存在 `data/native-version-01/`、`data/native-version-02/` 和 `data/native-version-03/`。固定 binary SHA-256 为 `4f85982624b3898c8991cb80c0981b2aa71070e3537046c9a95950318a95afcc`。

通过范围仅为沙箱下原生版本检查。真实 TUI、private backend、默认入口按需激活和用户级注册/撤销仍需后续独立验收，不能由该结果替代。
