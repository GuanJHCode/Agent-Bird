# CC地址对齐后的真实连通性验证

2026-11-09，用户替换地址后明确继续。**独立Claude模型连通性PASS**，不是产品双向LIVE PASS。

root确认Claude生效settings与CCSwitch当前provider一致，并在内存比对：Key、实际模型ID、BaseURL均与ZCode配置一致。新地址`https://bailian-aicoding.shuxinyc.com/yfb/anthropic`，保持ANTHROPIC_AUTH_TOKEN/Bearer、原默认模型与映射，不使用旧聊天Key来源、不添加额外x-api-key。ZCode用阿里云模板但覆盖了baseURL，先前CC误留官方dashscope地址；此次对齐后无需换Key/模型即可成功。

沿用已审查的最小runner，新的独立目录与exclusive marker，固定Claude2.1.316。禁工具/额外MCP/skills/session保存，私有HOME/config/CWD和空user/project/local设置来源；普通重试0，单turn、最多128输出token、请求期限30秒+清理5秒。一次CLI exit0，2.332秒，JSON成功且随机nonce回答完全匹配；CLI报告input69/output24/cache0，无HTTP错误、超时或清理错误。密钥内容落盘扫描0命中，ownedCWD进程0。原始stdout/stderr不保存；账单未核验，token数仅为CLI报告。

[公开证据](../data/public-evidence/T12-cc-gateway-check-20261109.json)保留精确hash和外部索引；先前旧Key/API401/UNKNOWN、CC官方Ali地址403均按原身份保留，此成功不回写或洗掉旧失败。用户自行替换CC地址，本轮测试没有改全局配置。

**产品前置缺口仍在**：静态核对`.4`的cli-default会变成空profile.Model，但Claude gateway ExactEnv只在显式Model非空时写模型变量，空值时不转发来源ANTHROPIC_MODEL/default映射。因此此次直接CLI保留默认模型成功，不能证明`.4`worker会保持同一默认模型。需要修复默认模型环境继承、独审与新唯一包，后续真实双向仍NOT_RUN，core/T12/T13/nativepush仍BLOCKED。旧预算/账本不重置，不因连通成功自动扩大付费验收范围。
