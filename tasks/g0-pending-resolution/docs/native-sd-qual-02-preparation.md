# sd-qual-02 前瞻准备

仅注册新的 `sd-qual-02`；不重跑、重发或改写 [sd01](native-sd-qual-01-result.md)。尚未创建新 case/job 或启动原生服务/TUI/模型。新目录固定为主任务 `data/sd-qual-02` 和 trusted repo 内 `tasks/pending-resolution/data/sd-qual-02`，均要求此前不存在并新建 0700。

已分别冻结的输入修复使用完整 `\x1b[200~...\x1b[201~` bracketed-paste 帧，再独立 `initial-submit` Enter；模型窗口从 submit 前开始，仍要求同 root/service 恰好一条实际 UserPromptSubmit。共享服务 metadata 精确期待 `vscode`，这是普通 `codex app-server` 构造时的 SessionSource，不作客户端或当前显示身份证明。Hooks 自身的 source 语义不改。

新运行器/PTY/采集器只用 `time.clock_gettime_ns(time.CLOCK_MONOTONIC_RAW)` 原整数纳秒计时，协议 `clock_impl` 精确为 `clock_gettime_ns(CLOCK_MONOTONIC_RAW)`。本地预算仍 10 秒、模型预算仍 120 秒；send-once 仍从原 send-attempt.mono_ns 加常量得出截止，不转换旧时钟，不重设起点。prepare 在 communicate 和成功判据后再次检查原 deadline；send-once 成功还必须读取同 case 的实际 immutable attempt，并检查原窗口与不变 hash。

墙钟只用于诊断和原生日志的既有 epoch 关联；Hook、日志和 JSONL 检索窗口没有放宽。若墙钟不连续使该关联无法证明，仍判 `epoch_correlation_inconclusive`。失败记录补固定 code/substage、仅本任务源码 file/line/function 和 RAW/wall clock pairs，不保存异常文本、locals 或模型推理。

针对 RAW artifact 被旧时钟代码拒绝、以及缺少固定错误诊断的两条检查先 RED，修后 20 项测试 GREEN。另保留源/输入此前各自 RED→GREEN；清理了本 worktree 的 6 个旧 `native_sd_*.pyc`，后续测试和 helper 调用使用 `-B`。helper clock 需与该协议独立复核通过，再由主会话统一现场 preflight 后下发执行。

主会话另已授权一次仅供本实验的 `/usr/bin/caffeinate -i -t 300 -w <controller_pid>`，用于降低自动 idle sleep 干扰；它属于外层运行准备，未加入 native argv、系统睡眠配置或产品代码，不改变任何期限，也不能阻止手动 sleep。运行后仍须核对其退出。

对冻结版 `5f2eed8` 的服务就绪尾部期限缺口补了定点回归：分别让 `process`、`snapshot_socket` 查询返回时 RAW 达到原 10 秒截止，直接调用控制器完整 `run`（服务/TUI/系统探针均替换为本地测试对象，没有启动原生实例）。原实现两个子例均 RED：`NativePTY` 仍被调用一次。生产修复只在身份与 socket 校验完成后、进入 `start_tui` 前加原 deadline 检查；新实现拒绝启动 TUI，并记录 `continuous_deadline_exceeded` / `service_wait_socket`。命令 `python3 -B -m unittest native_sd_collect_test native_sd_pty_test native_sd_control_test -v` 最新输出 `Ran 21 tests in 0.328s / OK`；未改变预算或注册其他样本。
