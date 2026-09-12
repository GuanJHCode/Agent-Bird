# 部分任务样本 01：停止阶段 unknown

2026-09-13，固定 driver 58a537e（实际 HEAD 后续仅追加既有 cached 事实文档）。单次执行建立新 thread `01a096a1-a611-7373-bf34-681dad9d3fd2`，实际自有 backend 的 config/read 与 requirements 证据确认 Luna/medium。

Go retained worker 64505 的直接父进程为 backend 63768。首次观察到 segment 1、step 1；停止期间又提交 step 2，最终状态 interrupted。driver 的严格 prefix equality 检查拒绝了新增尾部，因此结果为 unknown，没有进入 resume、静默验收或明确继续阶段。

Go 合同允许停止与原子提交竞争，并要求保留已提交效果；后继将核实旧前缀不被改写，采用实际停止后的完整前缀。现有产物没有保存 quit 时间、TUI 身份和退出码，不能反推具体延迟原因或补宣称正常退出通过。

主会话独立核对保护 pre/post 摘要，并以只读 retained-inspect 确认 interrupted、effect_count=completed_steps=2；已知 backend/worker PID 均退出。详见 `proxy-partial-01-root-verification.json`。旧种子文件没有修改，原始 case/job 保留不变，不重跑此 case。

原始证据：`../data/proxy-partial-01/`、`../data/proxy-partial-01-job/`，同目录的 preflight、controller argv 与 post-run receipt。后继需要新 case、新任务以及补齐失败路径时间和双 peer 证据的 driver。
