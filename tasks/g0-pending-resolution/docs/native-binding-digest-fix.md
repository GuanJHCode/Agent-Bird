# controller binding 摘要合同修复

sd03 的原 binding 经 `private_json` 输出为 1668 字节，raw SHA 为 `18a053a8…da122`；helper 用同一对象的 canonical JSON 计算 `dab2c091…d15c` 并写入 immutable attempt。原 controller 把 raw SHA 传给 `read_send_attempt`，在实际第 101 行拒绝；[原事实](native-sd-qual-03-result.md)不因修复改变。

新增专用 `canonical_binding_sha(path)`，实际打开受限文件并验证 regular/0600/current uid/nlink=1/非 symlink/≤65536 字节，再调用固定 `delivery_audit.parse` 与 `canonical`。helper 调用点只替换 binding 的摘要算法；通用 `c.sha`、原记录、二进制验 SHA、一次发送和原期限不变。没有修改 helper、nonce 或任何 native 设置，也没有重发 sd03。

[RED/GREEN 收据](native-binding-digest-tdd.json)记录实际 sd03 文件克隆在原 receiver 的 raw 摘要拒绝、两项定点测试 RED→GREEN，以及相关 37 项测试通过。测试覆盖格式与键顺序不影响 canonical hash、内容变更仍不匹配、重复键/非有限值/非对象/超限或不可信文件被拒绝。旧 attempt 时钟只在离线克隆测试中明确替代，不作为原生运行证据。真实 `helper.main → issue_once → controller receiver` 的跨组件回归由独立 writer 负责，本提交不声称已完成该链。
