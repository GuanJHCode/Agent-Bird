# 退出保留与明确续接：独立复核

状态：本轮 Go 实现与原生样本独立复核均 PASS，无未解决问题。完整 G0 未放行。

## 合同与实现首审

`retained_lifecycle_review` 未参与实现，只读审阅合同及固定范围 `66bb9f0..ba4d19e`，并在独立 archive 副本运行真实文件和子进程复现。

- 合同保留在线真正 idle 自动回传的原要求；本轮仅改退出及明确恢复生命周期，没有将局部测试等同完整 G0。
- owner/revision/expected segment 均在同一文件锁内校验，完成态也不绕过 CAS。时间字段验证 RFC3339Nano 的 UTC canonical round-trip，未知字段及未验证文本不直接导出。
- **I1 / Important：健康文件发布被误拒。** reader 捕获 Nlink=2 后，producer 删除已知 stage，reader 再查 stage 得 ENOENT，就将同一已发布文件判断为不可信。真实文件屏障复现；属于读取可靠性问题，未观察到重写产物。
- **I2 / Important：最终诊断阻塞退出。** stdout/stderr 共用满管道时，SIGTERM 已使唯一业务写者停止并保存 interrupted，但 main 的同步 stderr 编码继续阻塞，进程仍活着。真实子进程复现后只清理该测试进程。
- 首审判定 With fixes；需要修复两项并补审直接父退出守卫及最终 test/race/vet/build，尚未放行二进制原生测试。

最终代码 `5b96919` 已通过同一 reviewer 的独立复审：原 I1/I2 复现均转绿，固定 archive 中 retained/review 测试 `17.508s`、exit 0，独立 build/diff 检查通过，无剩余源码问题。父守卫只取消 context，由唯一业务写者提交状态；错误输出仅在 retained 路径设 250ms 上限，不改变旧命令合同。[首审证据](go-review-initial.md)、[最终复审](go-review-final.md)

源码与文档已整合为 `57edba1`、`79c140e`、`748e542`。主树完整 race（包括真实 CLI 子进程 race）`32.527s`、exit 0，vet exit 0；最后源码摘要与复审候选相同，原七份设计文件摘要不变。[整合验证](main-verification.json) 固定二进制 `feb9b9ad…e989a285` 已复制到本任务持久 data，准许进入限定的原生测试；此判定尚不代表原生或完整 G0 通过。

## 原生探针的采集器修正

前置探针的正常 `/quit` 只有有限生命周期事实，本地观察器启动晚于原 10 秒截止，不能计通过。无父守卫的强杀样本实际失败：父被杀后子进程 reparent 并继续到自然完成。

初版观察器把包含 PPID 的整个字典当作创建身份，错误输出“父子均消失”。runner 已保留原件并标记 invalidated，修正为仅比较 PID/出生时间，PPID 只作观测；在受控短窗口内使用秒精度出生时间，不宣称通用无碰撞身份。旧比较 1 项失败、新比较 2 项通过的回归与证据在 `73c5092`。

`native_identity_review` 对该固定提交的 14 个文件独立复核 PASS，无未解决问题：失效原件摘要匹配，强杀后 `85.409546s` 仍完成的 FAIL 在固定模型窗口内，退出采样不足与自然清理均如实限定；未导出任意工具正文。主树已整合为 `3fb1c99`，新鲜运行 `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tasks/g0-retained-resume/scripts -p 'native_*_test.py'` 为 2/2、exit 0。该 PASS 是探针实现与证据审查通过，强杀场景本身仍是 FAIL。

## 正式输出采集器

`75c6971` 的首审发现：输入参数可把未审二进制作为“expected”来源；采集窗口与快照时间缺少明确的时长、未来/倒置校验。`029e88d` 将入口绑定到固定持久二进制路径和审核 SHA，每阶段窗口要求 UTC、已发生且不超过 120 秒，快照时间纳秒级非倒置且不晚于观察时刻。历史 segment 仍允许早于恢复阶段。

`native_identity_review` 对固定修复增量独立复核 PASS、9/9 回归通过，两项阻断关闭。主树整合为 `4b794e5`、`28563c8`，连同之前的进程身份回归，新鲜 Python 测试为 11/11、exit 0。该判定仅覆盖原生输出过滤器，不包含尚未审查的实时进程/锁观察器或实际 retained 原生样本。

## 实时进程与锁观察器

`01234bf` 首审发现 stop 缺少动作前的当前身份链，可能把早已消失、reparent 或复用的进程误记为本次退出成功。`f3e37d` 在 armed 前验证当前 native/worker 的 PID 与出生身份、直接父链、未完成检查点和同一已持有的锁；action 绑定 armed 摘要、控制器动作前复核及实际动作结果，precheck 到动作不超过 2 秒。动作后判断停止，不错误要求进程仍活着。

`native_identity_review` 对固定修复独立复核 PASS，missing、PID reuse、reparent、锁已释放等负例均拒绝。主树整合为 `ca9940c`、`a743181`，新鲜 Python 回归为 14/14、exit 0。该判定允许进入原生样本；最终仍须组合 controller precheck、真实动作结果和 observer 记录，不能只引用 `success` 字段。

## 首个完整原生样本

`f265790` 的 quit01 排除、quit02 全链及仅增加 quit04 注册，已经 `native_identity_review` 独立复核 PASS，无未解决问题。stop 的 armed/action 摘要与执行前身份匹配，原会话恢复静默及明确继续分阶段计时，最终 completion 在 `105.535s` 内，旧前缀保留。主树整合为 `ba03e55`，新鲜 Python 回归 15/15、exit 0。这只计一次正常退出样本，不能凑成三次或替代强杀。

## 剩余原生样本与清理复核

`native_identity_review` 对 `f265790→6277c96→d29741d` 固定增量只读复核 PASS：quit03、quit04、kill01 的退出前身份链、真实动作、停止、同 root 静默恢复、明确继续及最终工具结果完整。三次停止分别为 3.544911、5.731129、0.060753 秒；继续至 ACK 分别为 70.083、70.780、67.672 秒。所有旧 prefix 保留，新 segment 完成，未把 pragma join 摘要当作可信完成来源。

计入此前 quit02 后，本轮三次正常退出及一次带父守卫强杀的基础样本已满足。quit01 仍排除；22 个已登记进程结束，五个锁可取，七个必要运行目录保留。该复核没有阻断项或非阻断项，仅覆盖单进程合成 retained 合同。主树整合为 `908e2e8`、`8490166`。

主 Agent 已独立核对最后三组旧产物摘要、静默时间、明确继续次数、ACK 窗口与 literal inspect 完成结果，见 [主核验](main-native-verification.json)。最终文档、源文件摘要、Python 回归及清理核验见 [收尾检查](final-verification.json) 和 [清理记录](cleanup.json)。完整 G0 的其余条件未因此通过，G1–G4 保持未实施。

最终收尾文档另经同一独立 reviewer 只读一致性复核 PASS，无阻断项：有限 retained 合同、完整 G0 未放行边界、清理保留与链接状态均与已放行原生证据一致。
