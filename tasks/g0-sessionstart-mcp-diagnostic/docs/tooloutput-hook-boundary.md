# 0.154.0：纯 toolOutput 新 turn 不产生 UserPromptSubmit

固定源码条件明确区分用户输入和独立工具输出：

1. `turn/start` 带 toolOutput、`input=[]` 时，[turn_processor.rs:585](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/app-server/src/request_processors/turn_processor.rs#L585) 构造 `ResponseItem::FunctionCallOutput { call_id:None, … }`，没有构造 UserInput。
2. [session/turn_input.rs:628](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn_input.rs#L628) 将这个 standalone 输出转换为内部 `TurnInput::FunctionCallOutput`。
3. [run_hooks_and_record_inputs:687](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/session/turn.rs#L687) 对每个内部 input 调用 inspect_pending_input。[hook_runtime.rs:667](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/hook_runtime.rs#L667) 只有 `TurnInput::UserInput { content, … }` 分支构造并执行 UserPromptSubmit；`:689` 的 ResponseItem/FunctionCallOutput 分支直接返回 `should_stop:false` 和空 additional_contexts。该判断按 input 类型，不按模型文字或是否出现新 turn。
4. [record_pending_input:726](https://github.com/openai/codex/blob/6b9826e3aa83b1a5947db50f4332cb9c65f1b340/codex-rs/core/src/hook_runtime.rs#L726) 将 FunctionCallOutput 持久化，并发出对应 item started/completed，不发 UserPromptSubmit。

因此，sd08 的合法纯 toolOutput 回传不应新增 UserPromptSubmit；出现新 UserPromptSubmit 表示另有 UserInput 进入该 hook 分支，需要按合同检查。初始双 Hook 可继续精确冻结，不能把仅有新 turn 或 functionCallOutput 视为额外用户输入。SessionStart 属于独立 pending-source 机制，本结论不等于它永不再出现。

本项只读复用已保留固定源码，未新增服务、RPC、模型调用或源码下载。
