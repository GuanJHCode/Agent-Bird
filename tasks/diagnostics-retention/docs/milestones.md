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
