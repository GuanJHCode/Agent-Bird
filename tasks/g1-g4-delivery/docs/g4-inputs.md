# G4 集成输入

生产源码 `tools/orchestrator`；以插件内部命令调用，不替代原生 `codex` 启动命令。交付先在项目内生成，不安装到用户全局配置。Windows 仍延期，不能以编译成功标为支持。

插件按已读取的 `plugin-creator` 技能验证 `.codex-plugin/plugin.json` 和默认 `hooks/hooks.json`，不生成 MCP 或 apps 配置。当前官方兼容说明确认默认 hooks 路径及 `PLUGIN_ROOT` / `PLUGIN_DATA`，可用于引用插件自有二进制；不将主机路径或凭据打包。来源：[官方插件打包](https://developers.openai.com/plugins/build/plugins)。

Hook 只传固定控制元数据，不把执行者结果写成 developer 上下文。SessionStart 恢复时只报告待处理标识，不自动恢复任务；SessionEnd 只能辅助记录，不能作为任意终端崩溃或断连的唯一停止证明。依据：[官方 Hooks](https://learn.chatgpt.com/docs/hooks)。原生 0.154 固定版本仍以本地源码与实际 G0 观察为准，不用最新网页覆盖旧版本事实。

安装/卸载逻辑只拥有本插件的版本目录、manifest 与明确条目；已有文件发生变化则保留并报告，活动任务/待回传/续接仍 pin 版本时不删除。生产状态不放插件cache，按原生用户数据目录处理。暂不公共发布，不自行选择开源许可证。

资源验证只在稳定可用的合成执行链上执行一次：先确认空闲退出、两执行者及输出限额，再运行必要长时资源测量。原始预算见 `docs/runtime-protocols.md` §8；缩短的调试样本不能冒充20分钟洪峰验收。
