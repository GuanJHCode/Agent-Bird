# 本机0.153.4静态核验

日期2026-09-11；执行者g0_interface_research，只读。以下为实际使用的精确命令；静态分析不替代原生实测。

```sh
codex --version
codex --help
codex features list
task_codex_bin='/Users/guanjunhui/.nvm/versions/node/v24.13.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex'
task_objdump='/Library/Developer/CommandLineTools/usr/bin/llvm-objdump'
"$task_objdump" -d --start-address=0x107fad594 --stop-address=0x107fad630 "$task_codex_bin"
"$task_objdump" -d --start-address=0x1012ee788 --stop-address=0x1012eefc4 "$task_codex_bin"
"$task_objdump" -d --start-address=0x107faa3a0 --stop-address=0x107faa574 "$task_codex_bin"
"$task_objdump" -d --start-address=0x1033d63a8 --stop-address=0x1033d647c "$task_codex_bin"
"$task_objdump" -d --start-address=0x102d31c58 --stop-address=0x102d31d60 "$task_codex_bin"
codex app-server generate-json-schema --experimental --out tasks/g0-daemon/tmp/attachment-research/schema
```

| 区段/命令 | 结果与边界 |
|---|---|
| features list | tui_app_server=removed/true；remote_control=removed/false；不是可推荐的新开关 |
| 0x107fad594 | 具名can_reuse_implicit_local_daemon；参数覆盖可拒绝复用，全部ABI偏移未映射，不编造完整兼容表 |
| 0x1012ee788 | 默认socket probe，由调用侧、socket常量和专属日志识别；50ms超时；错误/超时返回空候选 |
| 0x107faa3a0 | 具名app_server_target_for_launch；已读probe/选择片段未见启动新进程 |
| 0x1033d63a8 | desired_state_from_persisted_enrollment；存在独立持久远控偏好路径，不等于daemon/settings.json |
| 0x102d31c58 | read_server_diagnostics调用codex_diagnostics::snapshot再映射；schema为process及数值gauges，无连接/订阅维度 |
| schema生成及枚举 | 416个JSON；请求155、客户端通知1、服务请求11、服务通知81；未找到明确附着映射接口；临时schema已由该Agent清理 |

官方公开入口仍说明显式listener/remote连接及daemon启停，不承诺普通codex自动拉起。[Developer commands](https://learn.chatgpt.com/docs/developer-commands#codex-remote-control)、[App Server](https://learn.chatgpt.com/docs/app-server)

旧PID管理命令是否完整检查processStartTime及bootstrap缺省flag处理均未证实。本轮没有依赖这些未证实结论操作旧管理器；选择直接拥有的原生listener，实际远控状态只读观察为disabled。
