# T06A implementation and evidence

状态：实现与本地验收完成；独立审查、root签收、commit/push由主会话负责。

## Scope

- 仅production native-product-bridge.py:685–819、driver调用处；不改Store/owner/Go入口/MCP/provider执行合同。
- 单次wait最多3个unique executable heldFD。首次直接hash heldFD并核前后fstat/lstat；每轮核pid/birth/path、socket及完整attachment tuple。path/fd identity包含dev/ino/uid/mode/nlink/size/mtime_ns/ctime_ns，变化fail closed。
- driver另监视已验证request、service manifest、package manifest、g0 manifest（最多4份）identity，初次与decision边界重hash。更换policy/profile/install receipt不能在等待中刷新授权。只缓存artifact事实，无owner/auth授权缓存，无secret内容日志。
- decision出现时force再次完整hash三个role的实际artifact，再读取private decision；其他send/history/ACK已有强核验路径未改。轮询50/100/200ms有界退避；cancel finally关闭所有FD。
- 最新decision-wait-metrics.json记录artifact/binding hash次数、字节/时长、metadata次数/时长、poll/wait时长，单文件覆盖有界。
- provider help/version缓存为可选优化，本次未新增；已有VerifyExecutable/ProbePinnedOutput执行前校验保留。

## Validation

- Python final.xml：309 tests = 301 passed / 0 failed / 0 errors / 8 skipped；58.805s。8 skips为既有native auth/policy/prepare/package opt-in，本次未执行live parent/TUI/provider模型。
- Go 1.26.10构建本树product binary退出0，仅用于真实进程恢复fixture；Go源码未改，未机械重复Go tests。Python 3.14.3。
- 新增20个wait测试：25轮延迟决策hash计数、replace/inplace/恢复mtime/mode/symlink/profile/receipt/owner/socket变化、4种policy/install变化、force重hash拒绝、取消关闭FD、真实轮询decision/detach/cancel响应<350ms、真实/bin/sleep退出。
- 既有bootstrap-window测试通过虚拟301秒且无progress仍可等待；wait_actionable_events不以progress摘要计数设失联时限。
- 原广回归regression.xml保留295pass/1fail/8skip，失败是原worker running/reserved→result_ready/settled与before_recovery快照竞争。test_local_product_driver新增5s有限前置：result_ready/0active/budget全settled后抓快照再rebind；仍比较完整业务快照，仍断言uncertain/no resend/no ACK。
- 额外gated真实worker样本明确观测running/active/reserved旧快照，释放后证明其自然变化，再验证稳定前置与恢复不变。该样本不声称精确重放原marker窗口。最初将gate等到marker后释放导致自锁的recovery-green.xml红已保留；修正后recovery-green2.xml 12pass、最终广回归绿。
- red.xml是最初缺TMPDIR导致UDS路径超长的setup错误；red-api.xml是新增hash-reuse/force API的真实2fail TDD红。所有红报告保留，不作为产品失败修复证据冒充。

## Benchmark

data/benchmark.json由scripts/benchmark.py复算；同机20样本，每样本20次liveness+1次decision force；真实当前Python进程在三个角色、真实UDS；changed还监视4份绑定文档；不含轮询sleep/模型推理。p95 nearest-rank。

| Metric | Baseline | Changed |
| --- | ---: | ---: |
| wait local work p50 ms | 149.223 | 136.196 |
| wait local work p95 ms | 191.050 | 178.566 |
| executable hash calls/sample | 63 | 4 |
| executable bytes/sample | 3249792 | 206336 |
| binding document hashes/sample | 0 | 8 |
| probe_version p50/p95 ms (n=5) | 9.202/10.000 | 8.636/9.296 |
| probe_help p50/p95 ms (n=5) | 8.978/9.179 | 8.833/8.905 |
| spawn p50/p95 ms (n=10) | 1.223/1.315 | 1.343/1.488 |
| first_event p50/p95 ms (n=10) | 15.085/15.208 | 15.571/16.880 |
| history p50/p95 ms (n=20) | 0.095/0.164 | 0.096/0.142 |

CPU self usage across all wait samples: {"baseline": {"user_seconds": 0.250922, "sys_seconds": 0.9832669999999999, "input_blocks": 0, "output_blocks": 0}, "changed": {"user_seconds": 0.22919199999999995, "sys_seconds": 0.95675, "input_blocks": 0, "output_blocks": 0}}.

口径限制：OS metadata仍调用ps，占该小Python executable样本主要成本；三角色共用1文件，因此changed为1次初始hash+3次force=4，独立3文件测试为3+3=6。physical I/O block计数均0，页缓存下只报告代码读取字节，不推断磁盘收益。probe仅固定Codex --version/--help；spawn/first_event为本地Python fixture；history为真实UDS thread/read fixture。以上阶段未优化，其噪声不算收益，无模型推理时长。早期artifact-only样本保留benchmark-artifact-only.json，仅作历史。

## verified_existing

- host.go:463–488：progress 15s最多20份后只等待progressDone；worker生命周期由后续p.Wait(ctx)决定，摘要耗尽不终止worker也不写owner lost。无Go改动；此项为源码合同+既有virtual>300s wait回归，不声称真实等待5分钟的native模型测试。
- probe.go:71ff / claude_probe.go:46ff / lock.go:29ff：实际binary与权限校验保留；本次不引probe缓存。

## Frozen delivery

- HEAD a14affc483ae79522197d1adaaed35b7118b8d1c
- implementation.diff SHA256 98cf053956ca471a9e392130c8321dc87c5dbb4d3a3804941f10a9a113fc1135
- source-manifest.json列出全部4个源码/测试文件SHA；已在验收后再次校验一致。
- validation-summary.json含命令、报告时间/计数/SHA、binary SHA。
- 未commit/push，未修改项目task metadata，未运行模型或付费操作。
