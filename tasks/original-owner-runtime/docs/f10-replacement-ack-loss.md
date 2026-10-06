# F10 replacement ACK-loss native fixture

目标：通过真实 public nonlocal `submit`、source host、original owner 和 replacement transport，验证首 source 被杀后，对 replacement 的 `ticket`、`attach`、`applied` 或 `activate` 中任一已提交 RPC 回复丢失一次，owner 与唯一 replacement 仍完成同一 worker。

实现位置：`tools/orchestrator/internal/coordinator/owner_replacement_ack_loss_native_test.go`。

约束：测试进程只承载 Coordinator `NewCLIServer(state, false)`，使其在 `Serve` 前设置私有 reply-loss hook；生产 public CLI 仍实际发起 submit。hook 仅在首 worker 启动后武装，只匹配目标kind；ticket/attach首次replacement请求revision=1，不能以>=2过滤。source 信号前核验任务 SQLite 记录的 PID、birth 和 exact executable。fixture worker 只写一次 marker 并等待 release；各自启动的 CLI 均以 `CombinedOutput` 等待，Coordinator `Serve` 也有 done receipt。

当前状态：测试环境实测为 darwin/arm64、Go 1.26.8；未取得 macOS native token。最初两次无 `TestMain` 分流的试运行各在约 30 秒返回且没有捕获 stdout/stderr，留下 `/private/tmp/replacement-ack-loss-*` 现场；后续只读核验16个现场的已登记Host/owner/segment Event.Process精确PID均已不存在；未记录的子进程历史无法倒推，现场保留。该运行没有按要求保存 `-json`，不得用作验证或 RED 证据。夹具现已补上 earliest `TestMain` basename 分流；后续真实运行必须先获 token，并用 `-json` 定向保存。预期 HEAD `28c3017` 会在 replacement 的首 transient RPC reply-loss 后返回，令本夹具 RED。


ROOT接管（2026-10-07）：worker两次未按token/session/JSON约束运行，无有效验收。ROOT唯一writer及Mac/Docker token；所有后续native由ROOT保存原session至terminal。独审揭示ticket/attach永不命中错误revision filter、hook设置晚于Serve、hook连错误reply也丢、清理未登记pending child。已修为Serve前atomic armed hook，生产私有test seam仅success调用；补ready、source退出轮询、O_EXCL worker marker以及本run DB/ledger/marker身份精确cleanup。独立只读复核认为可用于ticket有效RED，尚未实跑，不称PASS。


ROOT实现和验收（same-epoch单次reply-loss）：

- 有效原始RED：ticket session56147 exit1；attach/applied/activate session26527 exit1。均明确命中成功reply loss，恢复未ready或原worker interrupted，不能把partialACK视为失败未提交。
- 生产最小增量：ticket/attach以相同revision/kind/request/body做有界2秒RPC，只有零reply瞬态网络失败才调用共用五次ledger的Ensure流程后重放；signed refusal/auth failure不重试。Applied/Activate恢复交已有current-transport路径；typed reconcile recovery继续共用预算循环。原child/Wait/StartSlot保留；未改public wire/schema。
- 普通4场景session27475 exit0，1顶层/5具名PASS。最终race第一批63448有两项fixture错误：纯耗尽用例模拟时钟回退；Activate已提交ready但owner在2秒backoff，1秒等待太短。生产未改，修为递增模拟时间及5秒等待同一严格ledger谓词，独审增量PASS。
- 最终race29656 exit0：Host pending RPC 1顶层/6具名PASS；Coordinator replacement四ACK与原same-tuple EOF/hardrestart/lostAttachACKrestart 2顶层/9具名PASS。first race63448中未受fixture变化影响的Host28顶层/145具名、公开CLI4顶层/4具名PASS复用。合计有效35顶层/164具名0fail0skip；不将63448原失败file称整体PASS。
- native最终严格核验原owner/worker/attempt/segment/workrev/binding不变，replacement为原owner直接child；丢回复前后的pending tuple/request/StartSlot=1不变，Attempts=2、无Reservation、phase activated、controlRevision=2；唯一worker EventExited原PID/birth/ExitCode0+ESRCH；正常Coordinator结束后owner/transport退出。后两者不是伪造的原Wait0。
- `go build ./...`、`go vet ./...` exit0（session19019）；生产稳定快照独立审查PASS，测试两处更正独审PASS。CLI另build的产品binary未race插桩；test/inprocess Coordinator与复制worker由race编译。
- 未闭合：pending ticket兑换/ACKloss加Coordinator hardrestart与session renewal、连续多次reply loss、原生五次预算耗尽和完整13秒cleanup故障矩阵、dead-owner seal/replay。此增量不作为B2/B3完整交付，不集成整个owner branch。

Linux arm64受控Docker：原session47794 exit0，1顶层/6具名PASS；只读/无网络/nobody/无cap，未跑race/Provider/物理owner RPC链。CID2fd2940babc2c31b8d0adf45aadbf6de22bed34a58b6827c431a788ec3a8d6a2已按原ID删除且列表确认不存在。完整日志、binary哈希、原session与有效证据复用规则见 `../data/root-replacement-ack-receipt.json`。必要失败现场保留；ROOT精确核对新失败现场已登记actors均不存在，所有本批session终态。
