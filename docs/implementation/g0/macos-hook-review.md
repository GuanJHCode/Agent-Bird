# macOS G0 Hook 工具独立复核

范围：当前G0.1的实验用身份记录工具及专用项目配置，不是生产协调器或原生回调验收。

## 审查来源

用户优先指定的GPT-6 Astra / max审查者本轮返回usage limit，未完成此轮审查。已按用户允许的不可用回退使用 `g0_hook_review_fallback`，工具角色 `code-reviewer`，实际配置为GPT-5.6 Terra / max。该审查者未参与实现；主Agent在原实现者因usage limit退出后接管实现。

## 第一轮：配置和pre-trust证据

审查范围：本轮范围更新、`macos-hooks.json`、正常信任步骤及原生TUI/退出记录。结论：0项有效问题，配置与证据初审通过。

确认点：仅3个项目事件和受控命令；没有trusted_hash、--remote、额外app-server或审批绕过参数；当前YOLO状态准确记为既有状态；未把配置准备、Windows延期或原生pre-trust界面当G0通过。

## 第二轮：整合后实际代码

候选：5435a4b，`tools/g0-probe/cmd/g0-hook/`及README增补。

主Agent最新证据：[race/vet/build](macos-hook-verification.json)、[二进制与文件测试](macos-hook-binary-checks.json)。独立Terra / max审查者完成实际代码复核，结论：**工具可交付PASS；G0放行BLOCKED；有效问题0项**。

审查确认：原G0R01未改；整合后的source commit和实际二进制hash与证据一致；输入有界、仅3类身份白名单，无环境/账号/网络/模型/子进程访问；目录句柄核验、随机O_EXCL/0600写入、同步和失败路径符合当前实验合同；红灯来自原stub，测试覆盖真实并发、symlink/缺目录、输入超时和子进程RLIMIT_FSIZE写失败，没有发现测试失真。

主Agent最终判定：本轮Hook实验工具通过测试和独立复核，正常信任材料可交付；G0.1仍等待原生目录/Hook信任及身份链实测，不进入后续功能。原生Hook执行0，未执行3次基础场景或20次故障屏障；Windows延期，不宣称两平台支持。
