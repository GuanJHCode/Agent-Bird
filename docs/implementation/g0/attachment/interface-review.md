# 原生附着接口独立核查

日期：2026-09-11；执行者 `g0_attachment_research`，只读 subagent。二进制为本轮预检记录的 Codex 0.153.4，未修改配置、仓库、服务或个人会话。

结论：未找到能够开始新原生实验的当前附着候选。内部连接状态存在，但没有证实可外部读取的当前 TUI→thread 绑定、刷新和失效契约。此结论只覆盖本轮核查范围，不证明所有原生路径均不可行。

实际复核命令：

```sh
codex --version
codex app-server --help
codex debug app-server --help
nm -nm /Users/guanjunhui/.local/bin/codex |
  rg 'update_has_connections|connection_closed|disconnect_connection|log_listener_attach_result'
task_objdump=/Library/Developer/CommandLineTools/usr/bin/llvm-objdump
"$task_objdump" -d --start-address=0x102aa8a00 --stop-address=0x102aa8ad0 /Users/guanjunhui/.local/bin/codex
"$task_objdump" -d --start-address=0x102ab1884 --stop-address=0x102ab1e90 /Users/guanjunhui/.local/bin/codex
```

局部反汇编显示 `ThreadEntry::update_has_connections` 更新内部布尔状态并通知 Tokio watch；`log_listener_attach_result` 的已核查分支包含 closed connection 跳过和失败日志。没有失败日志不能作为成功附着证明；内部 watch 也不是外部公开通知。

只读复用了主 Agent 生成的 schema；方法数量及定义见 [schema-audit.json](schema-audit.json)。新增核查覆盖远控客户端列表、执行环境连接、Hook 通知以及初始化/诊断/Thread 元数据。Hook 通知外层的 threadId/turnId 可供未来 C09 关联实验，但当前没有 attach/detach/heartbeat Hook。

in-process TUI 能否经受支持配置或 Hook 暴露同进程本地 listener 仍未知。此次帮助与 schema 未给出对应入口；`remoteControl/enable` 只有 ephemeral 参数，不足以推断其会建立本地 listener，没有开启远控试探。

官方 [Hooks](https://learn.chatgpt.com/docs/hooks#sessionend) 与 [App Server](https://learn.chatgpt.com/docs/app-server#unsubscribe-from-a-loaded-thread) 的生命周期说明支持“SessionEnd、loaded 不等于当前界面附着”的判断。
