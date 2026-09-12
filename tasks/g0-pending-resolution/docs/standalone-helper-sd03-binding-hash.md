# sd03 真实准备文件的摘要交叉回归

实际 sd03 的 prepare 与 send-attempt 保存 canonical binding SHA `dab2c091901e473b5783ea1a43c5a11600f9541a2d308d95dedc18deeb73d15c`，controller 旧 helper callsite 却读取原文件 SHA `18a053a895178db5472b3ea49cdf71da00f60213cc60bc26788a6fb8875da122`。后者包含缩进和末尾换行，两者不能混用。helper c2 的 main/load_prepared/issue_once/load_attempt 一直使用 canonical JSON 的 UTF-8 SHA；本轮 helper 运行代码未改。controller 的专用 canonical_binding_sha 修复由 runner 单独提交，通用 raw c.sha 保留。

[五文件 fixture](standalone-helper-sd03-prepared-fixture.json) 保存原 binding、controller-evidence、producer、intent、prepare-meta 的精确字节及 SHA。测试不含原 send-attempt。真实 controller.private_json 将 binding 再序列化到全新 0700 测试 Case，逐字节确认仍是原1668字节和原raw SHA；其余四份实际准备字节直接克隆。

随后真实 helper.main 读取、校验 Case，自己计算 canonical digest 并经真实 issue_once 执行 O_EXCL/fsync 发布新测试 attempt。只有 wire、旧机器身份和 Go prepare 快照为明确替身；测试 wire 在发布后返回固定错误结束观察，没有真实服务或 ACK。使用当前连续时钟的新测试窗口，不沿用原 attempt 或原模型期限。第二次 main 调用返回 send_intent_exists，测试 attempt 字节保持不变，wire 只看到一次 turn/start。

接收端执行 controller.helper 内实际 `binding_sha` 赋值表达式，再调用真实 read_send_attempt。因此即使新增 canonical 函数却漏改真实 callsite，这条测试仍会失败；没有把自编的 a/b 哈希传给双方。它不启动整个外层 controller，也不把测试机器替身当原生身份证据。

同一条 [native_standalone_hash_test.py](../scripts/native_standalone_hash_test.py) 在固定旧 controller `39e1d36d1ae65d29cc0589d5821ded3170bfdd0e` 上得到明确 assertion [RED](standalone-helper-sd03-binding-hash-red.log)：真实 receiver 拒绝 raw/canonical 错配（1项，0.096s）。只替换加载的 controller 为固定 `59a2d67f94f4998985e9ca4da788a7628650685a`，测试无需修改即 [GREEN](standalone-helper-sd03-binding-hash-green.log)（1项，0.023s），并核原 test attempt mono_ns + 120秒未重置。

```sh
G0_SD_CONTROLLER_SCRIPTS=ABS_FIXED_CONTROLLER_SCRIPTS python3 -B -m unittest discover -s tasks/g0-pending-resolution/scripts -p native_standalone_hash_test.py
```

同目录需既有 native_standalone.py 与 delivery_audit.py；固定 controller 导出目录需 control/collect/clock/pty 四个模块。源码 SHA、原件复核及版本见 [validation](standalone-helper-sd03-binding-hash-validation.json)。测试后原五个准备文件及原 send-attempt 均与冻结事实提交 `6f3054a` 逐字节一致；原 send-attempt 仅作只读核对，没有复制为测试输入。未修改旧 case、Go job、原始预算或 native/controller 运行代码，未补 ACK、重发或启动模型。临时测试 Case 自动清理；固定源码导出待独立复核后清理。
