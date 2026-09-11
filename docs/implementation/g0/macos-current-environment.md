# 当前macOS环境的G0续作

日期：2026-09-10；开始基线8f50768。用户在讨论当前环境测试后明确“可以先不做windows”。本轮只推进macOS，Windows记延期未验证；原始两平台发布目标不变。

**2026-09-11最新状态：已修复固定0.153.4官方安装，并实测普通codex隐式复用已建立的原生共享服务；外部Go探针可读取同服务同thread。G0.1仍不放行：TUI退出后元数据保持idle，缺少可靠附着正证据；没有服务时普通codex回到in-process。** 最新证据见 [daemon/result.md](daemon/result.md)。用户不需要重复信任或授权。以下为9月10日实验和准备记录，保留为历史。

使用本任务新建Git测试仓库和原生 `codex` 会话；不把现有个人会话当测试对象。账号/权限/正常Hook信任边界不变，禁止用替代启动器、额外app-server、手动继续或键盘输入代替自动回调。测试中的正常TUI输入只是实验动作，不是产品回调机制。

当前功能仍为G0.1端点、原服务、TUI和thread的身份链及附着正证据；未通过不进入G0.2发送或G1–G4。基础场景3次、故障屏障20次、本地窗口10秒/模型窗口120秒的条件不变。

| 工作 | 负责人 | 状态 |
|---|---|---|
| 正常原生TUI启动及专用仓库实验 | 主Agent | 本轮实验已结束，普通标记和3条Hook实测；G0.1受阻 |
| control socket/普通Unix端点协议、Hook配置核验 | g0_interface_research，只读 | 本轮只读核验完成，无已验证接入候选 |
| 整合后工具代码独立复核 | GPT-5.6 Terra / max（当时Astra因usage limit不可用） | 已完成，工具PASS，G0未放行 |
| 最新原生证据与文档独立复核 | GPT-6 Astra / max（本轮恢复可用） | 复核完成，N01已关闭，未解决问题0；G0.1仍不放行 |

## 前一轮准备记录（历史）

执行调整：原实现Agent在行为红灯后、原Astra独立审查者在启动复核时，均报告服务usage limit而退出。主Agent接管同一独立worktree完成实现；未并发写入。根据用户授权的模型不可用回退规则，实际改用 `code-reviewer`（GPT-5.6 Terra / max）进行独立复核，不声称本轮代码由Astra完成审查。

初查：工作树干净，Codex仍为0.153.4；默认daemon version连接失败，socket不存在。该结果只是启动前观测，不作为G0失败结论。保留的macOS探针可复用，无需先安装Go即可进行已有工具实验。

实际启动记录：在本任务新建空Git仓库内以 `codex`（无CLI附加参数）启动原生TUI。工具PTY的TERM=dumb触发显示兼容性提示，选择继续显示；这不是权限或Hook信任。原生随后进入目录信任界面，尚未确认，模型测试轮次0。TUI显示的已有权限为YOLO mode；本任务未设置或改变该值。OS元数据见 `macos-process-pretrust.json`：本次node/native PID及创建时间已定位，等待信任时默认daemon socket仍不存在。此时尚未证明服务最终启动路径，不判no-go。

当前准备范围：增加G0实验用最小Go Hook身份记录器，放在 `tools/g0-probe/cmd/g0-hook/`；只读取有限stdin白名单字段，stdout为空，记录到本任务数据目录。实现由g0_probe_impl在独立worktree `tasks/g0-macos/tmp/worktrees/hook` 负责；主Agent准备专用仓库 `.codex/hooks.json`，正常信任交由用户完成，不写trusted_hash、不自动点击Trust。

项目定义已准备，见 [macos-hooks.json](macos-hooks.json)，与专用测试仓库 `.codex/hooks.json` 内容一致。仅注册SessionStart/UserPromptSubmit/SessionEnd；本轮未注册Stop，因为官方对其exit 0输出JSON有单独要求，当前身份链不需要该事件。记录器命令使用受控绝对路径，timeout=3、async=false，未新增账号或权限参数。[官方Hook结构](https://learn.chatgpt.com/docs/hooks#config-shape)、[正常信任流程](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks)。

已在原生目录信任提示选择“No, quit”，PTY exit 0；本任务TUI和node进程退出、模型轮次0、Hook事件文件0，见 [macos-pretrust-exit.json](macos-pretrust-exit.json)。这证明正常信任步骤尚未完成，不证明回调不支持。后续用户完成正常目录/Hook审阅后，再由原生启动进行正式身份实验。

上轮临时Go已清理，本轮重新取得经过官方SHA验证的Go 1.26.8，放入 `tasks/g0-macos/data/go/`，供尚未完成的macOS G0工具开发/复核复用；仅下载包和无用构建缓存按临时材料清理，不再每次工具检查后删除仍需使用的编译器。它不修改全局PATH或CLI配置。

本轮所有生成物在 `tasks/g0-macos/` 按 `data/scripts/tmp` 分类；读到的本任务TUI/协议证据只保留必要字段，不保存凭据、完整环境或个人历史。任务自身创建的进程和文件登记后定点清理；原始任务交付物不删除。

最小Hook工具已整合为5435a4b。实现前 `go test -count=1 ./cmd/g0-hook` exit 1（日志 [macos-hook-red.log](macos-hook-red.log)）；首绿3.250s；整合后race exit 0（4.404s）、vet exit 0、macOS构建exit 0，精确命令见 [macos-hook-verification.json](macos-hook-verification.json)。真实二进制合成stdin/文件测试与hash见 [macos-hook-binary-checks.json](macos-hook-binary-checks.json)。这些全部是工具测试，原生Hook执行仍为0，不作为G0通过证据。

独立Terra审查者已完成配置/pre-trust证据初审与整合后代码复核，均无有效问题；详见 [macos-hook-review.md](macos-hook-review.md)。用户所需正常信任操作已整理为 [macos-trust-step.md](macos-trust-step.md)，材料已准备并通过工具复核，本轮不会替用户批准。此版本二进制及定义hash已记录；正常信任后不覆盖该二进制，后续代码修改须使用新候选路径并按正常流程重新审阅定义。

清理结果见 [macos-cleanup.json](macos-cleanup.json)：下载包、构建缓存、工作区红灯副本及实现worktree/ref定点回收；保留尚需用于继续G0的编译器、已审查二进制、专用测试仓库、空Hook事件目录以及源码/证据，不删除待用户正常审阅的材料。

前一轮历史结论：Hook实验工具可交付，当时等待正常信任。最新结论已更新为本页开头及原生结果记录；用户不需要再次执行信任。macOS G0仍未通过，Windows延期未验证。
