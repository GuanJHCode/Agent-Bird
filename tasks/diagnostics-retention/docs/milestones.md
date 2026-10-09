# F02 显式retention里程碑

生产Store types/planner/guard/apply及CLI/IPC接线已完成；完整page/partial/public链与最终race待后续，未称整体完成。ROOT独占最终build/vet与固定快照审查。

当前限定terminal vector=该attempt全部runtime_events；host_progress/report_events为不同协议，不纳入这个terminal received保留期。guard含旧facts JSON+完整bounded event identity/bodyhash/receive vector，最多512rows/每body64KiB（SQL length/substr），DBrev=seq允许producer bodyrev0；fresh snapshot/prepare双重算，完整request计费/持久，prepared/removed exact replay。nilguard原请求/原facts格式不变。请求入口copy guard、apply JSON deep-freeze。

plan先run auth，SQL最多64task，输出items0..8，canonical+CLI newline总<=8192；size/limit边界不跨未纳入eligible，cursor允许满64后下一空页。计划不写DB/不拿FS锁。apply逐item旧physical lease+VerifyRemoved/refund，无后台/schema变更。

- `retention-request-red` top {'fail': 1}, named {'pass': 1, 'fail': 2}, exit 1.
- `retention-request-green` top {'pass': 1}, named {'pass': 3}, exit 0.
- `retention-store-01` top {'pass': 1, 'fail': 2}, named {'pass': 22, 'fail': 4}, exit 1.
- `retention-store-green-02` top {'pass': 2}, named {'pass': 23}, exit 0.
- `retention-cli-gate` top {'pass': 1}, named {'pass': 10}, exit 0.

全部原FAIL保留。request-red为真实旧optional字段拒绝业务RED，nil原字节已PASS。store-01中late/max失败由共享pending夹具的无效phase恢复（schema拒绝，未检查Exec错误）造成，改为pending独立fixture；quota夹具误写不存在storage_limits表，改原DB runControlLimit。上述两点非生产业务RED，不删除断言、不覆盖原输出。其余初批具名PASS按未受影响源码复用；最后限64/UTF8/8192newline变更由后续page矩阵/最终race覆盖。


## 独审增量：重连 epoch 与分页 stdout 边界

真实 Store BeginCoordinatorEpoch(2)、同 Host Register/Reconcile 与 CommitHostEvent 的首次 late Exited 接收保留 epoch2；原 segment launch epoch1 不变。retention 现在验证接收 body 的 epoch>0，并保留完整 body/vector 绑定；不机械要求等于旧 claim epoch。

每个 candidate（包括 excluded）均试算实际 cursor、digest 和 CLI newline；超限回退该 candidate 全部计数与 item，不推进 cursor。Validate/apply 同为 canonical JSON + newline <=8192。

原 retention-review-red 中容量夹具尚未达到目标也保留，不称其为分页机制证据；retention-review-red-02 的 exact8192 业务断言失败，retention-page-red-03 的真实64task/长 excluded cursor 触发原 plan_invalid。retention-review-green 2top/4named PASS（0FAIL/0SKIP），覆盖真实重连、excluded cursor 与 canonical8192拒绝。公开 CLI/完整 apply 与最终 race 仍待完成。


## 完整 apply / 公共 CLI 里程碑

新增 whole-plan JSON freeze 回归发现 map 不被现有 bridge 精确字段扫描器支持；retention-apply-bounds-01 与真实 retention-public-red 保留。固定 reason wire DTO 支持同一 JSON 形状，Plan 的 map Marshal/digest 不变，不放宽全局 parser。Wire 自身严格拒绝 null（包含 count:null）；缺失/null Excluded 拒绝，{} 保留；完整字段扫描继续拒绝 unknown/case/duplicate，显式0计数由 Plan.Validate 拒绝。

retention-wire-apply-green 中 second segment synthetic fixture 少 budget row 导致失败，保留原始报告；retention-wire-apply-green-02 的 3top/19named 全部通过，覆盖512个真实认证 late events、513拒绝、65536/65537 bounded body、全部required segment和 max、wholeplan auth/order/hash、部分成功 exact replay、cancel 和调用者 mutation。多segment额外生命周期是显式合成fixture，不声称真实worker启动。

同一个 epoch 用例现在覆盖更常见路径：原segment Running/Result 已入库，重连到epoch2后首次 Exited入库、完成原attempt；原 launch epoch仍1。旧 RED/green记录的late Exited仍只说明对应边界。

retention-public-green：1top/1named PASS；真实独立CLI child调用 plan、保存其原样 stdout(含newline)、apply、exact replay；6个畸形 Excluded 请求经真实Coordinator IPC均拒绝且无delete intent。原Host身份fixture的确切原Wait0在任何数据读取/删除前完成，CLI children真实Wait0；事件是受控Store fixture，无真实provider。Wire presence追加矩阵与最终定向race待完成，ROOT独立build/vet/审查待汇总。


## 最终定向 race

clean61daae6上的retention-final-race exit0：12top64named PASS，0FAIL/0SKIP（不含package pass）。Store10top62named、新CLI1top1named、原physical deletion crash1top1named。stdout SHA256=`62a72acd4398396cda20094ae911098a8b24c11fb333a65af19c391ce48a2ac0`，stderr为空。包括追加Wire presence矩阵、全部F02定向回归和原manual删除/实际FK COMMIT失败恢复。公开计划stdout810bytes（含newline），原样0600保存后真实apply/replay；原Host身份fixture Wait0及每CLIchild原Wait0。原crash helper Run/Wait为明确exit91，原计划物理lease replay/VerifyRemoved通过。

生产及oracle独立审查/ROOT wholebuild+vet为ROOT所有权，当前等待固定61daae6增量结果。未运行全Store/全仓；未触及安装、Hook注册、真实provider/model/account、push/release。
