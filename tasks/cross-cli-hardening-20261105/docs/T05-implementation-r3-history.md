# T05 r3 实施交接：待同 reviewer 复核与主签收

Worktree `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05`，HEAD `428a5877d63247d0dd0b345b0765aa3c912507c2`。11个改动文件；无 commit/push。完整补丁 `r3-implementation.diff` 包含两个新测试文件；逐文件身份 `r3-source-manifest.json`，验证摘要 `r3-validation-summary.json`，ADR `r2-adr.md`。

## P1 修复

- owner_helper.py:657：完整frame read遭timeout/cancel/error中断后有界关闭原连接，再原样抛出。不能继续解释迟到payload。取消仍传播。
- entry observe_delivery:360 保留原delivery ID并记uncertain；closed连接核史得到明确helper_closed，driver/helper分类为awaiting_history，不把半帧变成新的协议/业务失败。恢复需要新的helper transport，严格history proof保留。
- reviewer原复现脚本未改动重跑：第一次TimeoutError后 `_closed=True`，第二次是helper_closed。`r3-review-repro.log`，exit0。
- 新真实UDS fixture覆盖partial header/payload/fragment × timeout/cancel六种组合；确证旧连接废弃、新连接只thread/read，turn/start总计1，无自动ACK，最终完整proof匹配。

## 完整入口恢复及身份/并发

- entry:338/343：按thread/task持有owner-only文件kernel flock，覆盖Submit到cleanup；同时存在的旧driver/helper不能获得第二写入权。SIGKILL自动释放锁；测试真实子进程持有、另一进程拒绝、kill后重获，未以PID文件/超时猜存活。
- entry:912-932：mode仍为rebind，无wire新mode。只有durable checkpoint原task、control文件path/hash及完整native owner/thread/epoch/lease/service/backend绑定匹配，且真实control status当前授权成功时，允许 `delivery_recovered`，不Submit、不业务owner rebind。helper grant变化允许；native owner变化不得迁移原uncertain delivery。
- checkpoint保存delivery/cursor及owner/control绑定；entry:991恢复controller_acked后的driver ack receipt/status。ACK成功但ledger未记时重放原durable request/command；没有新decision或自动ACK。
- tasks产品脚本接纳awaiting_history及rebind动作delivery_recovered；Go launcher原T05 phase处理继续有效。

## 确定性OS crash证据

`crash_fixtures.py`只给测试私有安装副本注入SIGKILL，并重算pins/manifest；生产源码没有fault flag。实际执行Go launcher→生产run_driver，另一组实际执行entry helper→生产run_helper，经真实本机服务/UDS和真实ledger恢复，不是只调用observe helper。

- driver：pending发送前；sending durable且I/O前；bytes已发送；turn/start响应后；proof生成未落盘；proof已落盘；真实Go ACK成功未落ledger；controller_acked未写driver receipt/status，共8个切点。
- sending但I/O前：保持原ID/uncertain，0 turn/start、0 ACK；owner退出后有界owner_detached。
- 其余driver恢复：turn/start总计1，单delivery ID，Submit总计1，rebind-owner为0，恢复前后SQLite runs/attempts/budget_reservations/budget_runtime逐行相同；真实Go ACK成功切点同原command/body重放2次，其余只一次；最终controller_acked、ack receipt及completed status一致。
- helper：pending、bytes已发送、response、proof未/已落盘、ACK成功未记ledger、controller_acked，共7切点。helper control是明确fake CLI，实际entry/服务/ledger/进程是真实；同delivery与ACK原command/body、次数均核对；owner退出EOF和清理有界。真实Go ACK语义由driver组覆盖。
- 11秒busy history继续跨10秒窗口等待，>20 reads仍只一次turn/start；未把worker业务状态改失败。

## 最终验证

- `r3-final-python.xml`：143 tests，0 failures/errors/skips；55.111s，进程exit0。精确command/cwd/显式env/PATH、报告与log hash见 `r3-verification.json`。
- `r3-focused.xml`：3 pass，0 failure/error/skip，exit0（发送前与controller_acked两入口）。
- Go文件逐SHA与r1已验证材料一致，复用 `go-race.log` 的focused nativebridgecli 10 test/subtest pass、0 fail/skip、exit0，及go-vet exit0；无后续Go源码/配置变化。产物orchestrator SHA保存在evidence manifest；测试私有包复制当前Python源并重算pins，不混称最终发布包。
- git diff --check exit0，当前源逐项SHA仍等于r3 manifest。全部本任务exec会话已结束并读取退出码。

## 保留失败与限制

- 独审原P1报告与复现未改动，r1材料未覆盖。
- r2-driver.xml/log首次7个重启失败来自helper重启误走业务rebind，修复为上述经过核验的delivery-only恢复；保留原失败。
- r2-final-python.xml/log：143项中141pass/2fail，保留。发送前fixture在尚无marker时伪造output空字符串，strict audit正确拒绝；r3仅改fixture为turns=[]，未放松proof。另一项helper在进入controller_acked故障点前owner-ready启动watchdog超时；旧临时现场被原fixture清理，不能确认根因。此后focused及完整143项均未再现，未延长timeout/skip；补了失败前ready/方法/FD诊断。不得将此项描述成已定位修复的产品缺陷。
- 未运行真实provider、真人TUI父模型继续审查、最终安装包验收；本次为protocol-fixture与OS-native-local synthetic。
- 新的独立复核及主签收尚未完成，不能自行宣告任务完成。

没有生成需清理的新临时项目文件；测试私有安装自动清理，原失败和验证证据保留。
