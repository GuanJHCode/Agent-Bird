# sd02 完整 prepare 输入链离线回归

固定 helper `3763a8c1fda44b7a19530b54daf8548894bd6cf5` 与 controller/collector `08bdd56be86055bd842507bb21ac1c9e016fe91c` 的完整输入链通过：1 项，0.125s。真实执行 `collect.process → control.binding_process → helper.main(prepare)`，继续经过 helper 的机器/schema/原生响应过滤、真实固定 Go `read` 和 `inspect`，最终创建 producer、intent 和 prepare-meta。未创建 send-attempt，没有 ACK、服务或模型调用。此结果仅证明离线输入链兼容，不是原生运输、当前附着或完整 G0 阳性。

[fixture](standalone-helper-sd02-fixture.json) 保存原 sd02 binding 与 controller-evidence 的精确 UTF-8 字节，以及 claim.json、control.json、control.lock、job.json、result.json 五份 Go 文件字节和 SHA。原 binding 的 service/tui comm 均保留 `codex`；新的 executable_path 由真实 collector/builder 调用经过明确的测试内核替身生成，不能把这个路径当作新恢复的历史测量。原 PID 已退出，测试替换 ps、proc_pidpath、boot 和 socket 观察；既有 controller-evidence 的 bytes/SHA 不变。

原生 RPC 是内存响应替身，仅允许 initialize、initialized、server/diagnostics、remoteControl/status/read 和一次精确 root 的 thread/read。历史字段来自 runner `761fbf228ebf644cb845c7fe714bda039334125e` 的受限新服务读取投影：source=vscode、paginated、full、completed、已知 READY。该原始投影是 `notLoaded`；测试显式改成 `idle` 才覆盖成功准备分支，省略的正文不复原。这不是旧服务活跃状态的证据，也没有把未知字段升级成原生观察。

Go 二进制仍为原始固定 SHA `feb9b9ad969abca5ff35db28c0f0d8d920dc0778d7aac68a90952900e989a285`，只接收测试任务 tmp 内独立 0700 副本目录，文件为 0600。真实 read 输出与原 controller 的 completed_read 精确一致；inspect 核 owner/revision/result/hash/ack=null。测试同时断言五份克隆文件字节未变、无额外 ACK 文件、无 send-attempt；未对原 Go 目录执行工具。测试进程内使用空环境替身，避免 collector 复制用户进程环境；没有改变系统环境配置。

中间失败保留在 [initial-failure](standalone-helper-sd02-chain-initial-failure.log) 与 [go-diagnostic](standalone-helper-sd02-chain-go-diagnostic.log)：read 已成功，inspect 因副本漏复制稳定 control.lock 返回 exit 2 / control_missing。补复制原文件的 14 字节到新副本 inode 后成功；Go 与运行 helper 没有额外修改。旧 helper `06c0cfe8ab0f974702df30fb5df92333e9c0f811` 对真实新版 builder 的 executable_path schema 产生 assertion RED，见 [red](standalone-helper-sd02-chain-red.log)。原 sd02 当时的 invalid_data/comm 拒绝仍以此前 original-input 记录为准；这两个失败阶段不混同。

[validation](standalone-helper-sd02-chain-validation.json) 固定本轮依赖 SHA、命令范围和最后 [GREEN](standalone-helper-sd02-chain-green.log)，另核原 binding/evidence/五份 Go 文件仍与克隆来源一致。测试不永久硬锁整个 controller 文件 SHA，避免无关输入/quit 修复破坏这条行为回归；当前模块默认取脚本同目录，历史重放可分别用 `G0_SD_HELPER_SCRIPTS` 和 `G0_SD_CONTROLLER_SCRIPTS` 指向从上述固定 OID 导出的模块目录。这两个变量只属于测试导入路径，运行 helper 没有 bypass。

```sh
G0_SD_CONTROLLER_SCRIPTS=ABS_EXPORTED_CONTROLLER08_DIR python3 -B -m unittest discover -s tasks/g0-pending-resolution/scripts -p native_standalone_binding_test.py
```

controller 导出目录需要 native_sd_collect.py、native_sd_control.py、native_sd_clock.py、native_sd_pty.py；旧 helper 导出目录需要 native_standalone.py 与 delivery_audit.py。本轮临时源码缓存是任务 tmp/standalone-fixture-src 下的这六份 git 导出文件，供独立复核后删除；测试自身的 job/case 临时目录已由 TemporaryDirectory 清理。原 sd02 输入不迁移、不重跑；新 case 的准入另行登记。
