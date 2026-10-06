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


ROOT跨Coordinator重启增量（2026-10-07）：

- 有效native RED43366中ticket成功，Attach丢失HostPending后offline、Applied因旧wake重复Ensure消耗slot、Activate因renewal Applied proof未更新失败。14670只暴露hardclose fixture错误，不算产品RED。
- 三处生产变化：signed state按原pending/current签名tuple选择同child ticket或已Attach child token重连；historical replacement permit仅以exact pending request/body/slot与连续predecessor记chain、不启用旧epoch执行权；fresh renewal把permit/Applied proof成对持久更新，保留原StartSlot/request/child。等待当前generation fresh HostPending时忽略旧wake/offline/旧epoch，避免额外Ensure。
- Attach71841、Applied/Activate26088和签名/slot/ledger/reopen纯测试50976已普通通过；稳定五文件生产与新增测试独立审查PASS。最终组合race有效52顶层/219具名PASS，0FAIL/0SKIP：复用v1 session28997中的Host45/201、CLI4/4；v2 session47159覆盖same-tuple1/4；v3 session30866覆盖sameepoch+crossrestart ACK2/10。v1整体FAIL：旧same-tuple fixture固定等两次connect，而同连接历史重放+新epoch renewal只需一次；ROOT定向SIGTERM收束旧测试child，parent/child皆FAIL，无Wait0成功声明。fixture改为一次connect并加5秒读deadline，重验v2通过。vet首次lostcancel告警后补显式context cancel，v2 vet通过；build/diffcheck通过。
- 原生断言保留同owner、replacement直接child、原worker/attempt/segment/workrev/binding；pending request/StartSlot1、Attempts2/noReservation/activated不变。ticket跨restart revision2，已提交Attach/Applied/Activate跨restart revision3。race插桩范围与前述相同，不把另外build的public product binary算race。
- Linux arm64真实Docker纯协议合同4顶层/23具名PASS，原session36588/container exit0；CID3bb170d3f743fd2d1e9711c8bd4da4d6ae84ca4f1915222bab9526347c49b5dc精确删除且inspect确认不存在。非Linux Provider/公开原生整链。完整hash/原session/复用边界在root-pending-epoch-receipt.json；七个RED/fixture现场的已登记21个PID均ESRCH，保留现场，不推断未登记历史。
- B2仍缺ticket兑换ACKloss、连续丢回复与物理共享五次耗尽/13秒清理fault矩阵；B3 deadowner seal/replay仍OPEN。该本地里程碑尚未集成。


凭据兑换回复丢失增量（2026-10-07，b994afc独立审查PASS）：

- 新私有exchange hook只在实际精确child兑换成功后丢reply，不开放生产配置/故障环境变量。原生sameepoch/hardrestart RED69057 exit1，1top3named FAIL，证明旧driver在exchange后offline/未Attach，不是未执行兑换。
- owner等待当前child的HostPending失败后，使用同一持久五次Ensure预算，并按已有signed state为原pending child重签ticket/重建连接；保留原child/Wait/StartSlot/request，不重新Start。Attach ACK repair也统一重建确切连接，支持sameepoch丢失socket；signed owner RPC拒绝仍直返。
- 普通63207 exit0，1top3named；最终race72548 exit0：Host2top7named、Coordinator3top13named（原8个ACK案例+exchange2案例）、CLI4top4named，共9top24named 0fail0skip。build/vet/diffcheck0。已有5f4cf78中未受影响的OwnerExecution/ledger和same-tuple验证可复用，但不把新exchange复验称为整个B2完成。
- 两处RED现场的8个DB/ledger/marker记录PID均ESRCH，未删除必要证据，无原Wait0推断。完整receipt=root-exchange-receipt.json。稳定5f4cf78..b994afc已独立审查PASS，未集成。签名拒绝立即停止仅指owner RPC；private exchange错误回复未签名，child转offline后仍须经有预算上限的重新鉴权，未获得额外权限。physical五次共享耗尽/13秒fault矩阵和B3仍OPEN。


共享五次预算与原生耗尽清理增量（2026-10-07）：

- 仅增加测试，生产仍为 b994afc。连续丢同一 ticket 的前 4 次成功回复后，第 5 次恢复成功；ledger Attempts=5、Unavailable=false、无 reservation，原 pending request/StartSlot=1/child、owner/worker/attempt/segment/binding 保持，原 worker 最终唯一 Exited=0。
- 连续丢 5 次成功回复后，第 6 次操作不得发生。ledger Attempts=5、Unavailable=true、无 reservation，V2 允许保留精确原 OwnerPending。受控 worker 记录 TERM 后继续至少 8 秒；测试验收前不发信号、不 release，产品自身升级清理，在最后一次丢回复后严格 13 秒内使原 owner/child/worker 均 ESRCH，并留下唯一原 PID/birth 非零 Exited。单 attempt/segment 与 O_EXCL marker 证明未再派 worker。
- 首次 session84139 exit1 只因成功场景窗口 15 秒短于第五次 16 秒 backoff，耗尽场景已通过 10.196 秒；属于 fixture 失败，保留日志和现场。修为 30 秒等待，TERM 文件半写继续等待，deadline 先于 allGone 接受。普通 session48200 exit0：1top3named、0fail/skip，耗尽 10.200 秒。
- 最终 race 原 session21982 exit0：4top16named、0fail/skip，包含既有同 epoch/跨重启四类 ACK-loss、exchange 两场景和新增两场景。go vet exit0（d99f9f），每场景实际构建产品 CLI 成功；稳定一文件测试独立审查 PASS。完整 hash/计数/自动清理耗时在 root-native-five-budget-receipt.json。
- race 插桩只覆盖测试、inprocess Coordinator 和复制 worker；另 build 的产品 CLI/owner/transport 非 race。worker 的持久原退出记录与 actor ESRCH 分别记账，后者不称其原 Wait0。此 fixture 无持久 descendant subtree，不代表完整 Provider 进程树验收。
- 所有原 session 终态；成功现场清理，v1 失败现场 4 个已登记 PID 均 ESRCH（root-native-five-v1-actor-audit.json），必要证据保留。未运行 Docker/真实模型。origin-loss/authenticated-terminal 与 blocked admission/publisher/disk 完整 13 秒矩阵、B3 及集成仍 OPEN。


Origin loss 与 authenticated terminal 原生增量（2026-10-07）：

- 两个 tests-only 场景复用 public build/submit 和实际 replacement activated 后的原 worker。origin 使用测试持有原 Cmd 的独立 /bin/sleep，仅 Kill 该 origin 并消费原 Wait（SIGKILL+reap）；terminal 通过现有 sendHostDisposition 发完整签名帧，不关闭 Coordinator 或控制 socket。
- 两场景均只由产品向 stubborn worker 发 TERM，必须在 trigger 后 2 秒内记录；原 worker 忽略 TERM 后存活至少 8 秒，随后 owner/replacement/worker 必须在 trigger+13 秒内 ESRCH。保存唯一原 PID/birth 非零 Exited，marker、attempt/segment 均不替换。Coordinator 仍 ready/not terminating；retained session.terminalSent 在 origin 场景为 false、terminal 场景为 true，排除 shutdown/terminal 混因。
- 独审指出主要字段相等不足以证明完整 recovery authority 未变。补为 activated/noReservation 后、trigger 前读取有效 canonical ledger bytes，actor 全退出后全文相等，覆盖 backoff、phase、proof、scope/authority。独审复核 PASS，生产未改。
- 原普通51309与第一race30137均exit0，前者1top3named，后者Host4top10named+Coordinator2top6named。增强断言后的普通33214、race68259均exit0，各1top3named，0fail/skip；替代此前stop两个case，未受影响的Host与five-budget回归复用30137。最终有效race6top16named。原日志/SHA/各清理时长与替代规则在root-native-stop-triggers-receipt.json。最终vet3414b6/diffcheck通过，每场景实际build成功。
- 所有原session终态，成功现场清理，无本增量failed现场/强制cleanup/Docker。无真实模型/Provider/TUI声明；另build产品非race，原owner/transport只能证明身份消失，不伪造其原Wait0；worker fixture无持久后代树。blocked admission/publisher/disk完整原生13秒矩阵、B3与集成仍OPEN。
