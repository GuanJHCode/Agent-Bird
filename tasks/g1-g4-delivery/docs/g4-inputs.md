# G4 集成输入

生产源码 `tools/orchestrator`；以插件内部命令调用，不替代原生 `codex` 启动命令。交付先在项目内生成，不安装到用户全局配置。Windows 仍延期，不能以编译成功标为支持。

插件按已读取的 `plugin-creator` 技能验证 `.codex-plugin/plugin.json`。当前包包含 skill、受限 `invoke.sh`、native bridge、同一 orchestrator binary 和固定的 `runtime/g0`；不包含 hooks、MCP 或 apps 配置。插件通过 `PLUGIN_ROOT` 只引用本包文件，不把凭据打包。来源：[官方插件打包](https://developers.openai.com/plugins/build/plugins)。

会话恢复和原生回传由 owner capability、持久状态、`wait-events` 与 native bridge driver 处理，不依赖 SessionStart/SessionEnd hook。启动保持静默，只有显式 resume、answer 或 retry 才会继续任务。原生 0.154 固定版本仍以本地源码与实际 G0 观察为准，不用最新网页覆盖旧版本事实。

安装/卸载逻辑只拥有本插件的版本目录、manifest 与明确条目；已有文件发生变化则保留并报告，活动任务/待回传/续接仍 pin 版本时不删除。生产状态不放插件cache，按原生用户数据目录处理。暂不公共发布，不自行选择开源许可证。

资源验证使用 [`resource-acceptance.py`](../scripts/resource-acceptance.py)：先确认空闲退出、两执行者、durable report 和输出限额，再运行必要长时场景。原始预算见 [`runtime-protocols.md`](../../../docs/runtime-protocols.md) §8；缩短的调试样本不能冒充 5/10/20 分钟验收。
