# v2 总预算与输入边界

`task dispatch` 新请求选择 `version:2`，显式提供 `max_attempts`（1–3）、`max_active_ms_per_segment`（1–3600000）、`max_active_ms_total`（1–7200000）。`max_active_ms`在v2必须省略/零，不能混用legacy。可选`budget_group_id`复用同owner不可变策略；省略按原run派生组，拆分若要共用额度必须用原组ID。低层task submit/IPC字段为`budget_policy:{version:2,max_active_ms_per_segment:...,max_active_ms_total:...}`，task max_attempts是同组总attempt数，不是每task新配额。

inspect的budget显示policy、used_ms/reserved_ms/remaining_ms、attempts_used/remaining。unknown持续预占，retry/rework/rebind不补额；下一段min(单段上限,剩余总额)。同组换run/task不能重新获取一份额度，不同owner或策略拒绝。

v1仍使用每段max_active_ms与120分钟组上限，标legacy_policy；未宣告budget_group_policy_v2的旧coordinator拒绝新请求，不自动降级。旧行/回执读取不改，升级schema10后旧二进制拒绝新库。升级仅在协调器安全停止后备份整个state，不能覆盖降级。

新输入adapter及fallback按实际JSON编码UTF8最多16KiB，完整IPC envelope仍64KiB，包含中文/转义/身份信封开销。新控制ID最多128 UTF8 bytes，无NUL/CRLF；dispatch自身request_id仍保持原96字符格式。超限明确拒绝，不截断prompt/acceptance；旧持久正文不为满足新限制而回写。原始文件/owner decision/最终模型输入有各自限制，不将所有层统一成一个上限。
