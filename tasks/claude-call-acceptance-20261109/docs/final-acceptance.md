# Claude调用修复最终验收

**PASS：私有 `0.1.24-hardening.20261105.8` 双向真实 CLI、父验收与私有 fixture 整合已完成。**

| 方向 | 场景墙钟（含CLI清理） | worker活跃 | attempts | 父accept / ACK / 私有FF |
|---|---:|---:|---:|---|
| codex-to-claude | 87.395s | 19.375s | 1 | PASS / PASS / PASS |
| claude-to-codex | 109.344s | 81.961s | 1 | PASS / PASS / PASS |

最终一轮场景墙钟合计 **196.739s**（约3分17秒）。每方向≤180s，worker累计各≤90s；90s额度可供单段使用，第二attempt只能使用同组剩余额度。Codex仍为gpt-6-luna/low，Claude仍为当前CC默认DeepSeek。历史失败轮另有完整记录，上表不是整个修复过程或账单时长。

修复包括：Claude启动时新增的两项本机IPC上下文与provider认证区分；Codex固定0.166标准bundle布局及四个原生可执行文件的严格SHA/权限校验；验收父调用的可选task_id与ACK字段指令、短socket路径和90秒总额度分配。真实认证/路由变更、未知CLAUDE字段、受篡改native helper仍被拒绝。

验证：Go有效去重1667pass/0fail/8既有skip，相关race146pass；最终产品Python及入口377pass/0fail/error/8既有skip；最终90秒验收脚本277pass；收尾脚本-O负例3pass。非canonical TMPDIR导致的首轮Go失败及复验、独审提出并修复的两轮问题均保留。

两方向真实candidate验证通过，父accept与ACK绑定同一候选；root逐一fresh postflight后，只将准备器拥有的两个fixture仓库FF到已接受的OID/tree，HEAD与tree均匹配且工作区干净。任务completed、segment exited、host offline、预算settled、active/unknown/reserved/actionable pending均0；记录内remaining/unverified/discovery均空，终查无本轮coordinator pidfile或对应runtime进程。321个本轮runtime/preflight文件扫描无当前来源凭据原文/base64/url编码命中，无auth projection残留。

限制：自然返工LIVE-03未触发（两个方向都一次完成）；8项Go与8项Python既有opt-in检查仍跳过，不把离线测试充作native TUI验收。全局`.bird1/1.0.0-rc.4`未替换，未发布release；本次只完成私有包双向CLI范围。T09原线程idle/busy与native push不在本结论内。

用户随后明确要求“推送远仓”；本提交收录对应源码、回归测试和可提交的验收证据。`.8`包仍绑定构建时的`ed3c8060`加dirty patch，未改写包身份或重跑模型。资料范围与本机保留项见[交付说明](publication.md)。

- [原始结果、父accept/ACK、预算与私有FF证据](../closure/data/final-result.json)
- [最终凭据扫描](../closure/data/credential-scan.json)
- [私有.8包身份（原文件逐字副本）](../data/public-evidence/package-r8-identity.json)
- [Go验证摘要](../revisions/r4/data/validation-summary.json)
- [最终产品Python验证](../revisions/r7/data/python-final.xml)
- [最终场景回归](../revisions/r8/data/regression.xml)
- [阶段与失败记录](progress.md)
- [最终独立签收](final-review.md)
