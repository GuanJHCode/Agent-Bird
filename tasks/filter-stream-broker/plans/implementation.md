# F04 bounded stream executor：首个基础里程碑

基准 integration4543635（含已验 F01/F02/F10）。完整 F04 仍按 outer plans/f04-isolated-git-draft.md推进；先把已验 Darwin filter sandbox probe 转为可复用、有界的执行原语，再接 durable broker/Store资源、producer/validate/review及target/compose。此批不改变公共 filter 拒绝，不把基础模块称为用户可用 filter。

## 冻结范围

唯一实现 writer 负责新 internal/gitfilter/*、internal/process/native_executable.go 与 host/validation_tool.go 对严格 native parser 的委托，以及 tasks/filter-stream-broker/{scripts,docs,data}。现解析器的完整格式/entry 检查原样抽取；F01 dispatcher拒绝保持。不得修改 contract/LaunchCommand/Store/CLI、gitops public gate、候选语义或普通 integration。

内部 Go API（不是 wire/grant/退出证明），建议 `Execute(ctx, Request) (Result, error)`：

- `Request` 固定 Operation clean/smudge、ScopeSHA256、tool canonical path/SHA256/size、最多16个固定args合计<=4096 UTF8 bytes、已打开锚定的 input/output regular file FD、私有只读 empty working directory。单tool<=32MiB，input/output各<=16MiB；不接收env/shell/filename expansion。context必须有deadline且实际execution最多60秒；上层接入后不得超过剩余active预算。
- input/output由caller独占，本层不承诺跨Execute使用同FD的并发安全；I/O前拒绝两者同dev+inode。输入/output由未来Host broker预留资源、创建并锚定；本执行原语不根据用户路径打开输入，不创建/删除/退款任何持久文件，也不声称已经认证Store grant。输出必须是当前用户single-link0600的空regular file，FD必须O_RDWR且非O_APPEND；从offset0用WriteAt写入，最终FD size必须等于实际written且<=16MiB，再用FD ReadAt回读核全部输出SHA。input经FD before/after身份、size/mtime和stream SHA复核。通过ReadAt/SectionReader读取，避免共享offset影响。仅核heldFD，不根据File.Name打开未知named路径，named绑定留未来Host broker。输出失败只能标记未接受，caller保留原对象，不能靠error删除未知文件。
- Tool 已由可信调用者 seal 到 Host保护区域，当前原语验证named/held regular inode、UID、nlink、0500、size、hash/native格式及canonical parent0500；封闭worker不得写该root。原source读取/复制/escrow/目录durable journal由下一层完成，本层不接受未经seal的可写工具，不声称close同用户恶意外部写入TOCTOU。source path不是模型输入。
- 为filter创建的empty working directory必须已存在、canonical、current UID、0500、无条目、稳定物理身份，不能等同sealed root。HOME/TMPDIR/XDG_CACHE_HOME固定该目录、PATH固定系统最小值、ExactEnv；本层不创建scratch，不允许filter文件写入（除/dev/null），也不提供caller env。工具无法读取candidate/common.git/Host状态和输入/output路径。OS runtime只复用已验probe允许的系统目录、metadata、根目录loader读取、随机设备；网络/fork/file-link拒绝。process-exec默认deny，仅literal canonical sealed tool allow；真实in-place exec另一system binary必须拒绝。相同tool的self-exec可能允许，仍受samepin/前后identity/hash与deadline约束，不声称禁止selfexec。Darwin以外明确unsupported，不以未隔离fallback运行。
- 同时一个Execute可以运行一个进程；每个Request不复用进程。使用原process.StartDuplex和CloseWrite，input copy与stdout drain有界并行，缓冲<=64KiB，stdout超过上限立即失败并停止原handle；stderr沿原process有界捕获。禁止ReadAll/input[]byte整体复制。CloseWrite/EOF不是退出证明。
- Context取消、超限、I/O错误及任何Start后错误，先Close stream；对非nil Handle无论stream是否nil，都使用独立fresh bounded cleanup context依次Stop→original Wait→ConfirmTreeExited，再join本次全部I/O goroutine，不遗留可写output goroutine。nil Handle才记录未启动观察；它不构成Store/Host重试或退款权限。非nil Handle未证明原Wait/tree则UNKNOWN，不从冷journal重建proof。Result记录实际original identity/exit/wait/tree与输入/输出摘要；Accepted明确要求original Wait返回nil && ExitCode==0 && tree检查nil &&全部I/O/输入/output/工具/目录复核通过。Handle.Wait返回nil不表示exit0；非0exit且原Wait/tree已确认时为已知终态但不Accepted，不能混成UNKNOWN。无删除和重试权限。
- 入口立即冻结Request：复制args，捕获operation/scope/tool及其size/pin等快照，后续执行、前后复核及result仅使用该快照；不重新读取caller可变Request字段。ScopeSHA256仅校验canonical digest并绑定观察result/request fingerprint，是opaque caller binding，不是认证authority；不能声称拒绝另一格式合法scope为foreign授权。tool/emptydir held/named physical identity、mode、size/hash和empty事实前后重核。
- 不定义可反序列化Grant、CleanupReceipt、自动cold recovery。后续durable broker必须先Store durable预约才seal/create，绑定original physical Host和scope；journal不能变成Wait proof，UNKNOWN保留资源。这些公共契约由ROOT在下一批冻结，不能借ValidationToolGrant蒙混。

## 最小充分验收

复用probe的限制/原Wait模式，只添加能证明新production原语的必要测试：真实native可逆clean/smudge与positive control；input/output边界/工具或scope漂移；真实cancel及非0exit原Wait/tree；sandbox write/read/fork/network拒绝、只读emptydir保持。抽取parser沿用现F01相关测试，小范围检查，不跑全Host/Store。先固定test业务RED/实现，再一次最终相关race+build/vet；独立审查源码/边界。所有中间FAIL原始证据保留，测试源在执行前固定hash/commit。对UNKNOWN使用原Handle可控证据，不以fake PID或JSON标志冒充退出；若无法合理注入则明确未验范围，不放松清理。

## 2026-10-09 计划审查闭环

上述exec literal allowlist、FD别名与最终回读、exit0 Accepted条件、失败独立cleanup+goroutine join、entry snapshot五项为ROOT明确冻结要求。测试另覆盖合法opaque scope/深冻结fingerprint，不能把结构scope当Store权限；FD caller-exclusive和nil Handle观察均不授予冷恢复、unlink、退款或自动重试。仅本计划先提交复审，获得PASS后才写production/tests。
