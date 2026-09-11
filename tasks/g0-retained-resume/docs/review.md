# 退出保留与明确续接：独立复核

状态：进行中。代码正确性、原生样本和完整 G0 分开判定。

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

## 待完成

- 实时进程/锁观察器和原生退出/恢复样本复核。
- 最终原生证据整合、文档一致性与清理核对。
