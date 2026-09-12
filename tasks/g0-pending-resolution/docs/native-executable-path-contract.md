# 原生进程字段：comm 与 executable_path

本变更修复 sd02 的本地字段合同不一致，不重跑旧样本。运行 nonce 仍为 `sd-qual-02`；旧 case、binding、controller JSON 原字节不改，也没有注册或启动新模型样本。

collector 的 `process(pid)` 保留真实 `ps comm`，仅接受本次固定入口的 `codex`、入口绝对路径或固定实际二进制路径；不把 basename 转写成所谓原始值。新增必填 `executable_path` 由 `native_executable_path(pid)` 实际调用 `libproc.proc_pidpath` 读取。它严格检查读取成功、绝对路径与固定 CLI 的实际路径一致，返回读取到的字符串；`CLI.resolve()` 只作为比对值，不填充 observed 字段。

service/tui 的 collector 身份与 guard 都要求该新字段。缺失、非字符串、非固定实际路径会拒绝；PID、uid、出生身份、socket inode/owner/mode 和既有进程匹配仍保持。PPID 仍是观测字段，reparent 不被误当作新进程。

controller 的 `binding_process(identity)` 再调用真实 `c.process(pid)`，核同 PID 与出生身份，再将当前的 `comm`、`executable_path` 分开写入 service/tui binding。helper 后继应分别用实际 ps 字段比较 `comm`、用 libproc 路径比较并哈希 `executable_path`；不能再把 comm 兼作文件路径。

TDD 使用冻结 sd02 身份记录的内存克隆，ps/libproc 明确为本地 fixture 替身，直接执行实际 `c.process` → `control.binding_process`。新路径由替身 libproc 返回，经实际采集器写出，不手写到旧记录或 builder 返回值。原实现 RED：collector/builder 没有 executable_path，且两种角色缺字段时 guard 未拒绝。修后同时覆盖真实裸 `comm=codex`、已知绝对 comm 保留、libproc 空结果/错误路径拒绝、guard 缺字段/None/错误路径及原身份回归。

最新命令 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v`：`Ran 24 tests in 0.336s / OK`。这些是本地字段与回归证据，完整 helper prepare 集成由其拥有者在独立树补充，不当作原生 RPC 阳性。
