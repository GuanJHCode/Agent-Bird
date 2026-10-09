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
