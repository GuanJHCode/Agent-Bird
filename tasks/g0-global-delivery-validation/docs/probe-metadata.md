# 0RPC唤醒探测元数据只作诊断

已只读核对原 `launchd-native-01/result.json`：ungranted-activation 的失败 SHA `cfba1877dbc0e9bb304a33a5b8402e823ffac23b2c99c32d1c877f03b60e5994` 对应 `ungranted probe UID differs`；EOF后peer UID=0，PID/birth/executable均缺失。原结果UNKNOWN、交易REVOKED、0HTTP、auth unchanged，原case不改。

生产仅删除0RPC probe元数据的两处授权断言（UID必须当前用户、完整probe peer必须等于service）。probe无grant、不发RPC，仍必须EOF且无backend lease目录；原元数据照常保留。真正服务仍由新state/manifest/service-start、launchctl job PID和kernel身份一致冻结。owner/helper持久在线WS完整peer校验没有修改，shared client SHA仍为 `d0114f0c0c0c16fb4eb53ff2093144a387d035cb6422ec6f8a8b9f1caeb2eea0`。本修复不推断这些WS在真实launchd下将返回何种UID，更未放行其UID0。

受控TDD只替换controller的post-EOF诊断返回值：UID0、全部缺失、完整但不同的peer三例。服务/owner/helper为真实独立Python进程，保留实际OS校验与真实Go。RED三例均复现原UID断言失败；GREEN完成dummy HTTP2、真实Go ACK(effect_count=1)及对称revoke，持久在线peer仍为实际UID。

最后源码修改后，本task **38 passed in 26.71s**；完整日志保留一条 `Loop ... handles pid 72980 is closed`。只读核对该PID当前identity为 `(None,None)`，未宣称该提示根因已解决，也未扩改其他代码。随后仅重验三项新回归：**3 passed in 8.58s**，没有该提示。RED、全套GREEN、定向GREEN日志均保留。命令沿用 runner.md 的固定safePython/显式只读pytest依赖/禁自动插件，测试选择本task tests或 `-k probe_metadata_is_diagnostic`。

没有执行新native、真实launchctl或读取真实auth。先前唯一真实注册授权已经使用；任何真实重测由root在新的用户授权后编排，本交付不自动重测。
