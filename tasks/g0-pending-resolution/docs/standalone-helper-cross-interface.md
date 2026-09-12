# e6＋c2 交叉接口归档（离线）

固定 publisher/controller `e6b02bd9a814532031f5c6c586b037b98cfa31c2` 与 consumer/helper `c2b30e0cf98a3c9bb501822bb81f44294fdb6c70` 的 sd03 binding/marker 接口对齐；交叉检查同时发现一个观察错误摘要丢失问题。此处没有运行原生服务或模型，没有覆盖 controller 的 Hook、owner、SQLite 整轮资格审查，也不把离线结果标为原生通过。

[runtime03-interface](standalone-helper-cross-runtime03-interface.json) 与历史02测试分开：不 patch ACTIVE_NONCE，实际 controller 常量和 helper 唯一入口均为 sd-qual-03；CASE 为主任务 data/sd-qual-03，JOB 为 test-repo/tasks/pending-resolution/data/sd-qual-03。fixed_prompt 的 READY/ACK 均与 helper 派生的 SD_QUAL_03_READY/SD_QUAL_03_ACK 一致，native/Go SHA 与 RAW clock identity 也一致。

字段核验实际执行 e6 run 内 L520、521、522 的三条 binding 产出语句（后者跨至 L526），不是另写一份预期 schema。服务和 TUI 经过真实 collect.process → binding_process；ps/libproc/root/boot/证据 hash 是明确的离线输入替身，未启动或探测这些 synthetic PID。产出的14个顶层键、9个 service 键、6个 tui 键直接通过 c2.validate_binding；comm 保留 codex，executable_path 独立存在。无 nonce patch 时旧01/02仍拒绝。这只证明生产字段产出与消费兼容，不证明其来源已经通过现场资格。

[Go 链日志](standalone-helper-cross-go-chain.log) 是同一固定组合的独立历史02回归：1 项，0.145s。它使用 c2 已提交的 native_standalone_binding_test.py，测试内部明确 patch ACTIVE_NONCE=02，并补 synthetic family/session、idle 与内核/RPC替身；原七份输入字节不改，固定 Go 只 read/inspect 私有副本。不能用这条历史测试替代上面的03入口检查。

[四阶段 producer 返回](standalone-helper-cross-rpc-main-outputs.json) 来自 c2 已提交的 test_rpc_error_survives_prepare_send_and_observe_without_authorizing_resend：只 wrap 实际 main 收集返回值，没有替换返回。四个包均已断言不含 CANARY 正文，只保留 helper 已受限字段；scope 明确是 synthetic。它们直接送入 e6 的 helper_rpc_error_fields，得到 [原 RED](standalone-helper-cross-rpc-red.json)：initialize、prepare、turn/start 错误的五字段保留，observe 的 thread/read（id6）被丢弃。原因是 e6 顶层白名单只接受 main 的短错误包，未接受 observe 的完整观察包。它不会放宽成功或重发，却会再次隐藏观察阶段诊断。反例和实际返回已交 runner，由其修改 controller 并提交对应回归；修复 GREEN 为独立后继证据，不改本次原 RED。

复核历史链时，从上述固定 OID 导出 helper/native_standalone.py、delivery_audit.py，以及 controller 的 native_sd_control.py、native_sd_collect.py、native_sd_clock.py、native_sd_pty.py；各文件 SHA 在 [manifest](standalone-helper-cross-manifest.json)。在 c2 的测试目录运行：

```sh
G0_SD_HELPER_SCRIPTS=ABS_C2_HELPER_DIR G0_SD_CONTROLLER_SCRIPTS=ABS_E6_CONTROLLER_DIR python3 -B -m unittest discover -s tasks/g0-pending-resolution/scripts -p native_standalone_binding_test.py
```

诊断 RED 可以直接把已归档四个包喂实际 consumer，不需重新启动 producer 或复制临时 wrapper。下面命令预期在 e6 失败，仅完整 observe 包无法保持五字段：

```sh
python3 -B - ABS_E6_CONTROLLER_DIR tasks/g0-pending-resolution/docs/standalone-helper-cross-rpc-main-outputs.json <<'PY'
import json, sys
sys.path.insert(0, sys.argv[1])
from native_sd_control import helper_rpc_error_fields
outputs = json.load(open(sys.argv[2]))["outputs"]
kept = [helper_rpc_error_fields(json.dumps(value).encode()) == value["rpc_error"] for value in outputs]
print(kept)
assert all(kept)
PY
```

已按归属逐个核验本次 cross-interface 临时目录13文件（154870 bytes）和归档字节/SHA；runner 确认复制后删除该目录。另清理本 agent 先前已完成审查、可由固定06c/08提交恢复的6份源码导出。manifest 保存删除前文件清单、来源提交、SHA、归档去向与删除后不存在状态。未删除其他 agent/reviewer 的 tmp，运行 helper/controller 均未改动。
