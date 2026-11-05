# ADR-002：恢复冻结的 Claude 只读版本目录

采用。调查由 /root/investigate_t10 只读完成，主会话按实际对象比对复核：4b2b4916与当前树无共同祖先；当前46项为旧405项严格子集，未找到主动收窄ADR。恢复旧Claude对象与快照日期，不合并无关历史、不在线追latest。其它三个provider对象和21个Codex native pins保持。

范围：目录准入与兼容测试，不代表逐版本native/live，不增加写权限。低版本现有typed-readonly cohort继续，所有typed Claude仍要求--tools；旧CLI缺能力继续拒绝。原handle/runtime路由不变，相关生产admission/profile/Host代码未修改。新旧coding exact gates独立于目录。

证据：data/public-evidence/T10-catalog-comparison.json；T10-red复现旧目录拒绝，T10-green为补丁未成功应用时保留失败（catalog provider键名使用错误，未改动产品），r2/r3为负向测试早期校验错误码预期修正，r4最终通过103项、1项probe观察opt-in跳过，Host相关7项通过。未删除断言或跳过失败；负向越权仍要求拒绝，workspace错误按既有validateWorkspace先行校验契约断言。所有失败原始日志保留外部tmp/t10-evidence，不覆盖。
