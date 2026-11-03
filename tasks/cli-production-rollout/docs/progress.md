# CLI 多版本生产落地记录

## 当前验收快照（2026-11-03）

- 基线：交接分支 `handoff/cli-version-compatibility-20261102` @ `55741e28c783fc7b97eeba8e0688c31c0118f229`，初始干净，225 项源码摘要匹配，远端回读相同；原研发脏 main 保留原样。
- 已实现：Go Host/installed receipt → 受信只读登录投影 → native guard 唯一注入 → 固定 native 主进程与 Code Mode companion → 原生命名只读 profile → exec/collect。生产身份仅精确 0.154.0、0.166.0 双二进制摘要。
- 已通过：最终包 `0.1.11-cli-production` 的8个端点与Claude2.1.290/Grok1.0.52两个中间版全部真实owner loop成功（marker、产物SHA/size、collect、ACK、accept、final）；无pin bypass。旧包证据仅保留历史。
- 用户网关：Claude/Grok 使用指定 `deepseek-v4.1-flash-1017`；Claude x-api-key 401 后 Bearer 原生闭环通过；Grok 原生 custom OpenAI chat_completions 通过。配置/密钥只在仓库外任务子进程环境，不更改原认证。原生 Grok 账户余额0为用户确认；不再重试该账户。
- 自动化最新：Go 全量441顶层/856含子例 PASS、0FAIL/6SKIP；Python159 PASS/4SKIP；0.166 opt-in合成刷新1 PASS。race同为441/856 PASS、0FAIL/6SKIP，vet/build全部通过。
- 独立复核：Go生产接线、provider controls、模型显式选择、lock错误优先级与解析fixture分层 PASS；双 native pin/auth/tool-policy/真实闭环安全复核 PASS。0.166永久测试已导入并复核 PASS。
- 固定版本无模型：13实测通过，126清单版本未测。新增官方固定下载 Claude2.1.290/Grok1.0.52/Codex0.159.2，摘要核对通过；AGY1.3.4官方release无assets、bucket列举401，未取得可核验artifact。
- 插件入口说明已补齐Codex controls/probe/readonly run与精确native限制；最终11包重建后10项重新通过。Go/Python执行代码与最终full/race快照字节相同。
- 公开产品：基于公开main `2e78f24` 的选择性移植补丁与验收记录已交付本目录；保留controller/dispatch/coding/portable入口，公开Codex仍守原门禁。独立审查、干净base applycheck、full和最终race均401顶层/770含子例 PASS、0FAIL/4SKIP，vet/build通过；未更新main/master，未发布或做公开产品原生安装真实模型验收。

## 已确认的边界

- 用户已批准固定摘要 native 主进程受信，由原生命名只读 profile 约束模型工具；外层Seatbelt会阻止原生工具的二次sandbox，不再重走嵌套路线。用户认证原文只读，native使用0600认证快照并由Host清理；Go不注入refresh override。
- 认证 freshness、来源/快照变化、CoreFoundation preferences、配置层、任何已有override（含空值/相同值）、未知双binary、helper/receipt/interpreter篡改全部拒绝。
- CodeModeOnly模型要求固定companion；不允许in-process fallback，工具delegate回同一native sandbox。模型禁止读取控制目录、DB、兄弟spool、原/投影认证和config，禁止workspace写及工具网络；scratch/artifacts仅允许本次所需写入。
- 只推已授权交接分支，非force，不操作main/master；原始会话、个人路径、认证与私密摘要不进公开仓库。冻结目录和原有锁保持。

## 历史失败与修复（均非新待办）

- 原矩阵三项与process Stop/Wait契约在基线已关闭，不重新立项。430/834和6SKIP仅是历史基线。
- 首轮审查修复reportInvocation绕过、快照替换、preferences、cli-default、resume以及provider control whitelist漏Codex。
- 原包1–7实验中的嵌套Seatbelt、TMPDIR祖先、native缓存只读和缺CodeMode companion均已定位修复；只有后续精确marker闭环才计通过。
- 最终Go首轮发现typed lock优先级和Codex解析fixture误入native门禁两处回归。保留原断言，修复纯校验顺序；解析fixture明确在内部executor测试，新增公共入口缺pin拒绝回归。修复后全量通过。
- 0.166永久合成测试首次在旧5秒JSONL逐帧读取窗口超时；positive已2次exact请求、guarded已拒刷新。只为该release显式有界20秒，旧调用默认5秒不变；严格JSONL/version/identity/计数断言保留，重验1 PASS。
- 公开产品首轮race两个时间窗失败，未改断言或timeout；单例三轮与完整race重验通过，原失败日志保留。

最终结果、包/源码链与剩余项以 [verification](../../../docs/cli-version-verification.json) 为准。
