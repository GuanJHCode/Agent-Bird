# 修复与验收资料交付

用户于2026-11-09在私有`.8`验收完成后明确要求“推送远仓”。目标为既有`origin/feat/cross-cli-hardening-20261105`，基线`ed3c8060e0276d69a4e88e54694a6b735b056d2d`；提交收录Claude来源环境修复、Codex标准bundle校验、对应回归测试和本轮验收资料。实际提交OID与推送结果以Git和远端ref为准。

源码验收结果见[最终验收](final-acceptance.md)及[独立签收](final-review.md)。本次交付只整理资料；不改变已验收产品代码，不追加模型调用。最终冻结的验收脚本及公开fixture测试位于`revisions/r8/scripts`、`revisions/r8/tests`；根目录`scripts`、`tests`保留初次短路径修订。脚本包含本机固定路径和消费门禁，是验收快照，不应当作可重复运行的通用入口。

- [包身份原文件副本](../data/public-evidence/package-r8-identity.json)、[构建材料清单原文件副本](../data/public-evidence/package-r8-source-materials.json)、[构建时dirty patch原文件副本](../data/public-evidence/package-r8-source.patch)保持原始字节和SHA；构建时source_dirty=true仍为事实。
- [交付核对](../data/public-evidence/publication-check.json)记录与材料清单匹配的产品源码和测试摘要；最终JUnit及Go摘要保留原文件。
- [最终真实结果](../closure/data/final-result.json)、[凭据扫描](../closure/data/credential-scan.json)、[进程核查](../closure/data/process-audit.json)及[独立签收绑定](../closure/data/independent-signoff.json)保持原始字节。

私有二进制、安装目录、归档、认证投影、真实会话原始日志/数据库、消费标记及中间轮次的大体积材料保留在原本机位置，不纳入Git。历史文档或JSON中的绝对路径和未纳入的中间修订路径是本机溯源地址。最终脚本、结果及包身份未因交付而重写；本提交不替换全局插件、不创建release，也不扩展原验收范围。
