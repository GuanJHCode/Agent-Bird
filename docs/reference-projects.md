# 参考项目与许可核验

核验日期：2026-09-15。固定修订和许可链接列于下表。以下项目用于设计比较，本轮未复制其受限实现，也未接入新的投递系统。

| 项目 / 固定修订 | 核实的许可 | 本项目采用的设计方向 |
|---|---|---|
| [backnotprop/orchestrator · 583acf4](https://github.com/backnotprop/orchestrator/tree/583acf4b469b91131f96ae2136797749c788b4c7) | [BSL 1.1](https://github.com/backnotprop/orchestrator/blob/583acf4b469b91131f96ae2136797749c788b4c7/LICENSE) | 主脑 Skill、短操作契约、紧凑输出 |
| [PAL MCP · 7afc7c1](https://github.com/BeehiveInnovations/pal-mcp-server/tree/7afc7c1cc96e23992c8f105f960132c657883bb1) | [Apache-2.0](https://github.com/BeehiveInnovations/pal-mcp-server/blob/7afc7c1cc96e23992c8f105f960132c657883bb1/LICENSE) | CLI 协议、角色和配置分离；未采用 bypass 审批参数 |
| [Gas Town · 649b832](https://github.com/gastownhall/gastown/tree/649b832b7672bc7a2dbef26f5983aba6198b819b) | [MIT](https://github.com/gastownhall/gastown/blob/649b832b7672bc7a2dbef26f5983aba6198b819b/LICENSE) | 任务模板和持久化上下文恢复；未引入组织层级或第二套状态系统 |
| [MCP Agent Mail · ac4966c](https://github.com/Dicklesworthstone/mcp_agent_mail/tree/ac4966c64d7e39692a4fb9c707448a1718ab29db) | [MIT 文本加 OpenAI/Anthropic 限制条款](https://github.com/Dicklesworthstone/mcp_agent_mail/blob/ac4966c64d7e39692a4fb9c707448a1718ab29db/LICENSE) | 仅比较消息确认/处置语义；不复制实现，不重复接入投递系统 |

## 需要保留的区别

backnotprop 的附加授权允许包括内部业务在内的生产使用，但不允许据此向第三方提供商业托管或代管的 Agent 编排服务。该修订列出的 Change Date 是 2029-07-09，Change License 是 Apache-2.0；这不等于它现在已经使用 Apache-2.0。许可按版本生效，应核对具体修订的条款。

Agent Mail 的 rider 限制特定主体及代表其行事者的权利，不能把仓库标题中的 MIT 当成无附加限制的标准 MIT。这里保留许可识别与设计语义比较，不引入其代码。

这些参考项目的许可不决定本项目的许可。维护者已于2026-09-17选择 [MIT](../LICENSE)；历史 Apache-2.0 候选文本未采用。现有 Go 依赖继续遵守各自许可，未新增第三方运行时依赖。
