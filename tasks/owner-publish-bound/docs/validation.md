# F10 原 owner 发布阻塞与退出边界

本增量从 integration `19f5a33` 开始，仅本地实现、验证和提交。ROOT 唯一写入；未调用真实模型、替换用户安装、推送或发布。

## 已修复

真实 publisher 在处理已持久提交的 Running 事件后永久不返回，会一直持有原发布锁。原 native 用例 66394 在 13 秒 deadline 等不到 owner 返回，属于有效产品 RED；取消锁等待的两个单测 84392 同样失败。

`PublishAll` 改用可取消的单令牌 gate。取消等待不会增加 goroutine 或遗留 token，事件次序与 durable ACK/cursor 提交规则不变。内部 cleanup 总 deadline 收紧为 12 秒，仍保留 TERM 10 秒、join 11 秒和最多 2 秒 flush，为原 owner 进程退出及原 Cmd.Wait 留出外部 13 秒余量。

## 原生证据

测试父进程持有真实 owner 的唯一原 `exec.Cmd.Wait`。owner 经真实 FD3/FD4、OwnerRPC enroll，并接收实际 Coordinator 签名 launch/terminal。publisher 注入严格核匹配 durable ACK 后永久阻塞；durable-return 注入只在实际 `Spool.Append(Exited)`（文件/目录 sync）成功返回之后永久阻塞。均无验收期间的 signal/kill/release。

worker 与持续存活的同进程组后代均在触发前核 PID/birth/祖先/PGID，产品 TERM 后 8 秒仍保持原身份，最终由产品 KILL 清理。spool 唯一 Exited 保留原 PID/birth 和原 Wait 的 `-SIGKILL`，actor ESRCH 单独核对，不用 PID 消失替代原 Wait。

| 故障 | 普通原 owner Wait | race 原 owner Wait | 结果 |
|---|---:|---:|---|
| 永久 admission | 11.011s | 11.023s | 非 signal、exit1，保留 incomplete |
| 永久 publisher | 12.012s | 12.022s | 非 signal、exit1；唯一本地 Exited，Store 尚未收到 Exited |
| Exited durable-return 永久阻塞 | 11.273s | 11.469s | 非 signal、exit1；完整同一 Exited 已入 Store，spool 已获 ACK |

最后一项证明 cleanup 期间的持久发布结果，未单独鉴别后台 publisher 与 final flush 谁完成提交。该 seam 不模拟物理 fsync 永久阻塞；进程树范围仅为本受控同组后代，不包括 setsid 逃逸。

## 验证与限制

- 最终普通 native：55554 exit0，3 top / 3 named，0 fail / skip。
- 最终 Host race：71375 exit0，40 top / 105 named，0 fail / skip；覆盖相关 owner 控制、生命周期、执行和原并行发布/ACK 回归。此 fixture 的 owner/worker/后代均为同一 race test binary，未另 build 产品子二进制。
- Build `98d536`、vet `9cfa33`、diff check 通过，Go 1.26.8 darwin/arm64。
- Docker Linux arm64：36124 交叉编译 exit0；受限无网络容器执行 3 top / 7 named 纯 gate/cleanup 测试，0 fail / skip，exit0、非 OOM。没有 Linux process.Start/Provider/native owner/race 声明。容器已精确删除，inspect 确认不存在。
- 早期 green 构建遗漏旧测试锁方法、durable fixture 使用不存在的 SQL kind 列，分别为 fixture/编译错误，未算产品 RED；最终已修复。所有失败日志保留。
- 两个失败 native 现场保留；其中登记 owner/worker/descendant 分别 3 个 PID 均 ESRCH。成功现场由 fixture 清理，所有原 session 终态，无本增量遗留 actor/容器。两份一次性编辑脚本已删除，日志/receipt/Linux binary 保留为验证证据。
- 稳定生产与 fixture 两份独立审查均 PASS；之后未修改 Go 源码。

仍未完成：物理磁盘故障矩阵、更广进程树边界、B3 有限签名封存与 dead-owner data-only replay，以及其余 F01–F13 内容。Intel Mac/Windows 原生验证按用户指令延期。
