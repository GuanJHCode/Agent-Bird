# 当前验收状态（2026-11-03）

- 生产代码、旧测试夹具、交接文档及旧提示词已整理；最终隔离包0.1.17-cli-series。
- full/race均454顶层/889含子PASS，0FAIL、7显式SKIP；14测试包PASS，vet/build通过。最后PID原子ready及Grok编译metadata夹具保留原业务断言，独立审查PASS。
- 最终构建binary与68个真实接受记录逐字摘要相同；安装24文件/mode核验通过，261源码摘要匹配。Python未变证据按组件复用。
- 当前包真实：Claude27代表版、Grok37全版、Codex2、AGY2；20合同组覆盖Claude405发布版，378版无直接模型验证。全498目录中79实际二进制检查、419未测，68真实闭环、430未直接模型验收。
- 公开候选38文件：full/race419顶层/804含子PASS、0FAIL、5SKIP，vet/build/产品入口与3个无模型SSE通过；最后失败日志单行脱敏变化已独立审查并定向验证。保留公开产品功能和Codex原四项门禁；未发布、未原生安装/模型验收。
- 秘密与个人路径精确扫描、gitleaks通过；任务创建的API凭据文件与publication工作副本已清理，固定二进制/必要失败和验收证据保留。原始认证、原脏main、已有锁和冻结目录保留。
- 提交/远端以交接分支实际Git HEAD核对，沿用非force推送授权；不操作main/master。矩阵和Stop/Wait生产合同已关闭。

## 历史：本轮里程碑

# CLI 连续版本系列验收

- 起点：交接分支2e089338，初始工作区干净；保留上一轮源码/包/10项真实闭环证据。
- 用户纠正：目标是连续版本系列兼容；小版本差异小时允许基于依据分组，而非几个离散版本通过就宣称全段。
- Claude 要求最新版本并向前覆盖几个主要系列，先核实官方registry/发布记录；Grok 先按既定1.0.34–1.0.70范围。
- 负责人：主会话唯一写入；独立研究和最终审查只读或独立私有目录。
- 验收：每一版本映射至有依据的合同组，区分直接实测、代表版覆盖推断和未验证；CLI参数、流协议、工具权限、鉴权/网关/会话变化必须拆组并验关键边界。
- 约束：不跟随runtime latest；本次调研后固定快照；不改全局安装、认证、已有锁或main/master，凭据/原始会话不入Git。

## 历史：首批实施与证据

- 官方snapshot：latest2.1.317/stable2.1.311。Claude发布系列1.0(121)/2.0(70)/2.1(273)；按必要合同将生产floor设为1.0.61（405个已发布版本），0..60缺JSONL/plan/settings等必需能力，不按小差异忽略。Grok稳定1.0.34–70全部37版已下载/复核version/help/SHA。
- 已修：Claude不再强制未消费的partial flag；1.0.61–85使用camel deny、86+使用kebab；旧系列限定typed readonly，其他角色/candidate/untyped不扩窗。
- 认证隔离实现：Claude gateway readonly在scratch内新建HOME/state、严格空MCP/settings、exact env allowlist；拒OAuth/双key/provider冲突和项目/managed配置，OS拒原HOME/auth/Keychain与securityd访问。旧Node解释器逐launch复制同摘要到私有目录，原nvm不改，修复其在原HOME被拒读时的SIGSEGV。
- 首pkg12真实：Claude1.0.61无输出失败（已定位Node HOME沙箱并离线修复）；317启动失败，正用无真实凭据的本地SSE/header fixture定位。两条失败保留，不记PASS。
- Grok37系列首次真测34暴露preamble拼接：真实事件是tool_call/tool_call_update，而旧fixture误称tool_started真实。新增红测已复现，按官方ACP事件修复且保留现有protocol/locks与marker断言。pkg13重验34完整owner loop PASS，正在串行其余36版。
- Native离线：1.0.61解释器复制后version PASS；317合成Bearer/SSE PASS。它们是离线传输/权限证据，不是真实模型闭环。
- 当前修改尚未全量/race/vet/build验收或提交。上一轮2e089338与pkg11记录保持历史有效，不作为当前源码结果。

## 历史：生产边界收束

- 独立审查发现并关闭：gateway在candidate/action前统一拒绝非plain typed readonly；Claude metadata执行前后pin复核、无密钥ExactEnv/fresh HOME/禁网/只写probe scratch；Node必须显式路径且匹配官方24.13.0 darwin-arm64固定摘要，不再ambient PATH决定解释器；legacy deny加WebFetch/WebSearch，policy目录不可写。
- 官方Node archive通过SHASUMS256核验，与本机原Node逐字摘要相同；复制用于私有launch/probe，不改原安装。JS metadata实际copy/start超2s，单次有界8s、production version+help总窗20s，非Claude保持原2s。另新增sourceEnvironment回归保留非capability Node pin配置，其他ORCHESTRATOR控制能力继续剔除。
- pkg14最新Claude失败通过真实state形状native fixture复现：settings安全路径检查需自身scratch祖先metadata。只放行这些literal metadata，兄弟spool/控制数据仍拒读。
- pkg17：Claude1.0.61与2.1.317均已完整真实owner-loop PASS，其他合同代表版串行验收中。pkg15最新通过与pkg12/14/16失败单独保留，不能混成最终结果。
- Native本地SSE fixture已加入真实Read roundtrip、两类唯一认证header断言、高危工具不广告和credential-state扫描。2.0.30的内部无tools非流式辅助请求暴露fixture假设错误，fixture改按stream形状正确响应，Read主链断言保留；这不属于生产解析bug。
- 实际OS检查PASS：source auth拒读、workspace拒写、policy目录拒删/改；2个可达securityd服务在相同权限下均被gateway sandbox拒绝。只做无状态bootstrap lookup，没有创建Keychain或修改全局认证。
- 当前尚未最终全量/race/vet/build；源码review PASS，最终证据和公开产品副本仍需验收。

## 历史：真实闭环收口

- 最终pkg17：Claude27个合同/已知回归代表版、Grok1.0.34–70全部37版、Codex0.154/0.166、AGY1.2.3/1.4.12，共68个版本严格闭环PASS（相同最终production字节）。Grok37早期pkg13结果保留，但本次已在最终包逐版重验。
- AGY1.2.3首次响应末尾多2个LF，33B精确断言拒绝；模型已成功响应，不能误报quota。保留失败，无断言/marker截取调整；随后ceiling和baseline有界重验均完整通过。绝对剩余quota仍未知。
- Claude36个固定版真实CLI的本地SSE/Read/Bearer合同通过，另61/2.0.31/317的API-key模式通过。20个合同组映射405发布版，其余378版无直接模型验证，不能称405全实测；原始低于61两版仅作拒绝依据。
- root normal full已454顶层/889含子PASS、7skip；首次full的6个旧candidate/progress fixture因metadata exactEnv不再带fixture env而失败，常量metadata移到mode门禁前后通过。race还暴露测试binary自身默认exit sleep使version+help过2s；仅这些常量分支改syscall.Exit，不改worker/m.Run、race检测、生产环境/2s窗口或断言。正在最终稳定fixture全量/race/vet/build。
- 公开产品候选同类fixture修正后normal full419/804 PASS、5skip，后续race/产品入口回归仍在验收；不能沿用旧401/770结果。
