# F01 里程碑记录

contract/process/Host 已完成；Store/公开完整链尚未实现，未声明 F01 完成。

- typed proof 固定原 cmd.Start 边界、Context/完整 Command，单次消费；已启动 Handle 保留原 Wait/Stop。
- Host 发布独立 validation_start 事实，执行 private sealed native 入口；冷恢复不重建 live proof。
- 最终本里程碑 `milestone-host-green`：8 top /49 named PASS，exit 0。以下保留所有此前失败，不以中间批冒充 final。

- `contract-process-green`: exit 0; top {'pass': 3, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `contract-process-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 4, 'fail': 2, 'skip': 0}.
- `host-green-02`: exit 0; top {'pass': 5, 'fail': 0, 'skip': 0}; named {'pass': 23, 'fail': 0, 'skip': 0}.
- `host-green`: exit 1; top {'pass': 4, 'fail': 1, 'skip': 0}; named {'pass': 21, 'fail': 2, 'skip': 0}.
- `host-red`: exit 1; top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `milestone-host-green`: exit 0; top {'pass': 8, 'fail': 0, 'skip': 0}; named {'pass': 49, 'fail': 0, 'skip': 0}.
- `sealed-security-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 0, 'fail': 3, 'skip': 0}.

`contract-process-red`/`host-red` 包含新 API 编译 RED；`host-green` 失败由本任务 sealed 0500 目录的测试清理权限引起，已按 owned path 显式清理修复。`sealed-security-red` 为 malformed native 与 writable sealed 的业务拒绝回归 RED。
实际 cmd.Start EAGAIN/EINTR 使用 per-call 受控 seam 注入，未声明自然内核资源失败。真实 Host+Git+Store 新 attempt 链待后续里程碑；无真实 model/provider 或安装替换。

## Store 与有额度的 Host 流式里程碑

ROOT schema34/API生产 `3c8a55b` 与DTO/hash、迁移证据已按固定提交cherry-pick；本worker只改独占Host/Store接线。

- Store 冻结完整source字节、原adapter/pin、input/vector、plan/work revision、原physical Host；新kind缺失/孤儿/retired binding拒绝，legacy不重建。full grant历史读取允许released工具身份，live仍需新reserved grant。
- Host 32MiB上限、64KiB流式copy/hash；完整binary不ReadAll。固定小native header/load/program command验证；private `0500`目录与tool。每版journal至多4096bytes（两版8192 escrow），精确inode/hash/目录身份删除并durable removed后才带cleanup发布LaunchRejected/Exited。UNKNOWN保留copy/预约；已执行Handle只由原Stop/Wait/tree证据清理。
- 原cmd.Start前的普通refusal也先absent/removed journal+cleanup。sandbox profile末尾保护整个`.validation-tools`树，实际profile self/peer/ancestor actor尚待下一批。
- 真实Host+Git+Store新attempt完整链尚未验证，本里程碑不称最终完成。

- `store-bound-red`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 2, 'fail': 2, 'skip': 0}.
- `store-chain-01`: exit 1; top {'pass': 1, 'fail': 1, 'skip': 0}; named {'pass': 9, 'fail': 2, 'skip': 0}.
- `store-chain-02`: exit 0; top {'pass': 3, 'fail': 0, 'skip': 0}; named {'pass': 19, 'fail': 0, 'skip': 0}.
- `store-chain-diagnostic-01`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 0, 'fail': 2, 'skip': 0}.
- `store-facts-green-03`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 4, 'fail': 0, 'skip': 0}.
- `store-guard-green-02`: exit 1; top {'pass': 2, 'fail': 1, 'skip': 0}; named {'pass': 19, 'fail': 2, 'skip': 0}.
- `store-milestone-01`: exit 1; top {'pass': 3, 'fail': 2, 'skip': 0}; named {'pass': 25, 'fail': 4, 'skip': 0}.
- `store-policy-green-02`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 8, 'fail': 0, 'skip': 0}.
- `store-policy-green`: exit 1; top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `store-policy-red-02`: exit 1; top {'pass': 0, 'fail': 1, 'skip': 0}; named {'pass': 6, 'fail': 2, 'skip': 0}.
- `store-policy-red`: exit missing (launcher未执行Go); top {'pass': 0, 'fail': 0, 'skip': 0}; named {'pass': 0, 'fail': 0, 'skip': 0}.
- `integrated-store-host-01`: exit 1; top {'pass': 7, 'fail': 1, 'skip': 0}; named {'pass': 47, 'fail': 8, 'skip': 0}.
- `host-boundary-green-03`: exit 0; top {'pass': 1, 'fail': 0, 'skip': 0}; named {'pass': 8, 'fail': 0, 'skip': 0}.

所有FAIL保留：`store-policy-red` launcher误传`--`，Go未执行，仅有command/空输出，没有原exit文件，不补造；`store-policy-green`为validOID未定义编译失败；`store-chain-01`/diagnostic01为strict JSON walker拒绝source合法null，已用完整[]byte/base64冻结修复；`store-bound-red`为bound missing/emptydecision真实业务RED；`store-milestone-01`包含测试未核consumed退休CHECK及公开artifact wrapper转换底层COMMIT错误的问题，修正为精确schema断言和原intake回调的真实FK COMMIT；`integrated-store-host-01`只有Boundary夹具缺HostArtifact grant，Boundary后续1top8named PASS。`store-guard-green-02`的missing_tool被schema retained trigger先拒绝，已显式断言该防线，再于隔离测试DB模拟缺行检查独立intake gate；事实guard后续1top4named PASS。其余该FAIL batch具名PASS可据实际delta复用，未宣称整个batch通过。

跨physical限制：原event已入Store则exact ACK replay；仅旧spool已durable但Store未记录，在新PID/birth物理Host重绑后保守拒绝释放/自动retry。可写spool JSON不能证明原publisher；冷journal不是original Wait。实际Start错误为per-call受控注入，不称自然内核资源EAGAIN。
