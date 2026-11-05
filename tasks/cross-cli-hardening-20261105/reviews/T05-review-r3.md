# T05 r3 独立复审：BLOCKED

审查者 `/root/review_t05`，实现者 `/root/implement_t05`。未修改源码、未派生、未运行真实模型、未提交/推送/安装全局插件。主会话于本轮末尾接管启动路径排障。本轮审查已结束，无活动测试进程。

冻结worktree `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05`，HEAD `428a5877d63247d0dd0b345b0765aa3c912507c2`。11文件逐一核对 `r3-source-manifest.json` SHA256，独审测试后再次核对未变。manifest SHA256 `240f8ad726e1a9e9d9fe47d925250a59991156743a6e7804c06863178ffe21b3`；diff SHA256 `612903e10a58efa312c8be1c7c8fda07339300ab3bc62dc06c181dd3c904017a`。

## 已关闭原 P1

`owner_helper.py:657` read_rpc 在 frame读超时/取消后有界关闭连接，保留原异常。独立重跑原review脚本，输出从旧版 `_closed=False / websocket_compression_or_reserved` 变为 `_closed=True / helper_closed`。新增6种header/payload/fragment×timeout/cancel场景通过真实UDS连接重新核查history，发送列表精确为turn/start、thread/read，不重发/autoACK，取消传播。该P1已修复。

## 已复核生产恢复路径

- 主生产入口mode=rebind只在同native owner完整attachment（允许helper grant变化）、原task/control路径与hash匹配且真实control status授权有效时走delivery_recovered；不Submit、不业务rebind、不变attempt/budget。新owner不能通过旧batch owner校验，不迁移uncertain。
- 生命周期kernel flock覆盖submit/watch/send/history/decision/ACK/cleanup；owner-only lockfile有类型/属主/mode/nlink检查，SIGKILL后锁释放；没有持久化monotonic deadline。
- 生产ledger与driver checkpoint分别保存事实。ACK成功但ledger未记录时按原durable request/command重放；ledger已controller_acked但driver receipt/status未记录时只重建投影。
- 新故障注入只写测试私有、重新pin的安装副本；真实Go launcher/run_driver与helper两入口均有SIGKILL切点，不是只测observe_delivery镜像。driver使用真实Go Submit/status/ACK/Store，helper control明确fake。不得将其称为真实模型/TUI验收。
- 消费者包括Go StartDriver与native_product_case.py已识别awaiting_history，bootstrap接受delivery_recovered。

## 本轮阻塞：ownerready启动失败独立再现，根因未闭环

精确失败：`test_local_service_chain.py::test_real_local_service_admission_bridges_g1_event_and_ack[ack_success]`。场景在ownerready启动观察阶段30秒deadline到期，尚未到delivery/SIGKILL/ACK切点；不是ACK断言失败。诊断：

```text
fixture_readiness {"elapsed": 30.002220417023636, "owner_returncode": null, "owner_leases": 0, "ready_receipts": 0, "backend_methods": [], "open_fds": 37}
```

`data/focused.xml`：32 tests，31 pass、1 failure、0 error/skip，36.762秒；进程exit1。该失败与实现者r2曾出现的启动watchdog失败属相同阶段，目前不能确定是环境、fixture还是activation产品启动竞态。后续隔离重试成功只能证明间歇性，不能抹除本次失败。

失败命令（cwd为上述冻结worktree）：

```sh
PATH=/private/tmp/go1.26.10-sdk/bin:/usr/bin:/bin:/usr/sbin:/sbin GOENV=off GOTOOLCHAIN=local GOPROXY=off G0_GO_EXECUTABLE=/private/tmp/go1.26.10-sdk/bin/go GOCACHE=/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/go-cache-review-t05 ORCHESTRATOR_PRODUCT_BINARY=/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05-evidence/orchestrator PYTHONDONTWRITEBYTECODE=1 /private/tmp/agent-bird-cross-cli-5c9jyn8o/python-venv/bin/python -m pytest tools/native-product-bridge/test_delivery_deadlines.py 'tools/native-product-bridge/test_local_product_driver.py::test_owner_bound_driver_waits_then_delivers_without_manual_collect[complete-ack_success]' 'tools/native-product-bridge/test_local_product_driver.py::test_owner_bound_driver_waits_then_delivers_without_manual_collect[complete-controller_acked]' 'tools/native-product-bridge/test_local_service_chain.py::test_real_local_service_admission_bridges_g1_event_and_ack[ack_success]' 'tools/native-product-bridge/test_local_service_chain.py::test_real_local_service_admission_bridges_g1_event_and_ack[controller_acked]' -p no:cacheprovider -q -rs --junitxml=/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t05-evidence/review-t05-r3/data/focused.xml
```

接管需保留失败并定位启动链事实（service admission/owner-connected/backend启动/guardian与failure receipts）；不延长deadline、不skip、不因重复转绿签收。定位修正后只复验受影响部分与相关独审。

## 有效证据与限制

- 实现者r3最终套件exit0：143 tests，0 failure/error/skip；XML时间55.111秒，进程55.308秒。实际解析XML与核对日志hash；`r3-final-python.xml` SHA256 `6b309fa81a6414cce0aa59fa6b6b411a51298a3b67a636a7e1c5e56caf5351ed`，log SHA256 `34de1a6f7ef8feb2446501bc3c5e98ac8248943b71bcabf5787e4bd935b0ea55`。
- 独审focused的31个通过项包括deadline/原ID重启/严格proof/半frame/取消/锁，以及真实driver ack_success、controller_acked、helper controller_acked。失败helper ack_success后来单例重试通过。
- 独审另外3个生产入口负例全部通过（negative-final.xml）：恢复替换control路径、同路径篡改control hash均delivery_control_changed；竞争driver明确delivery_driver_busy。均以真实Go launcher拒绝，等待原worker状态稳定后核对runs/attempts/budget完整SQLite快照不变。
- `negative-and-retry.xml`的1失败为独审外部测试初版oracle错误：在原worker仍running时比较快照，把其自然result_ready/预算settled当成竞争driver副作用。只修外部测试，改为先awaiting_owner_decision稳定快照；negative-final 3/3通过，原记录保留。
- 在root接管指令到达前已启动的诊断外部脚本（不改源码/deadline）随后自然结束：3个helper ack_success再现尝试均PASS，readiness.xml 3tests/0failure/error/skip，5.978秒；未触发失败快照。没有继续重复执行。
- Go生产文件r1后未变，已核对旧Go race 10 test pass（含subtest）/0fail/skip与vet exit0证据可复用；本轮未重复Go测试。

所有独审日志/XML SHA256及计数见 `data/evidence-index.json`；原r1、r2失败与证据未覆盖。外部脚本位于scripts/，均为必要复现证据保留。临时服务/安装由测试正常清理；没有删除归属不明文件。

结论：原P1与身份负例、关键ACK恢复复验有效，T05整体当前BLOCKED于重复出现而未归因的ownerready启动失败。DEL01～05不能整体PASS，macOS真实模型/TUI/live仍未执行。待主会话接管闭环后复审，当前不签收、不commit。
