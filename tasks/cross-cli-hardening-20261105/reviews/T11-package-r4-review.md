# T11 私有 .4 安装包独立审查

日期：2026-11-09。审查 Agent：`/root/review_package_r4_scripts`（code-reviewer，只读）；root 按返回的最终报告记录。

**VERDICT=PASS；BLOCKERS：无。** 仅对应本报告的冻结包和分层证据，产品 core 仍 BLOCKED。未重建、新安装、联网、读取凭据或运行模型 CLI；重算实际 archive/manifests/receipts/evidence，并重新运行两套只读 doctor。

| 身份 | 值 |
|---|---|
| version | `0.1.24-hardening.20261105.4` |
| source commit | `7b7f0421fc9dc553be29d871b16d15f779293b76` |
| source tree | `51b0bd73b3efcb607a2ff47476eced440f2b3731` |
| source-materials SHA256 | `95e2585b5d8022d49665c5c74aedc2ff3b3e2584a4833212a9769138987f73ae` |
| binary SHA256 | `11e573d8d240cafb25d580d0a62862f2f617cf5b6ca38340c16df42222ea9a89` |
| package manifest SHA256 | `665429b8ea50adfe5a1036665a04121dbaeb3c86a4dcbfa5f0a8334314aebb99` |
| archive SHA256 | `1817a713d710ac0d6c75539b1c765b5826f991d2ab9b81befd78b47b42d33597` |
| identity JSON SHA256 | `1edf94b8322f485f5b5ee0e09483f1ee2dabfa011a9b181ac763c5253294530c` |
| evidence index SHA256 | `09328a9c79f62eef59ce5ace1297d5fc869a7b6df802aa9f71db9bfaeb546ebd` |

核验结果：2,891/2,891 tracked source materials hash/mode 与 clean/pushed7b 一致；252/252 indexed evidence 匹配；archive37 members（25 files/12 directories），无 duplicate/traversal/link/special member，24/24 package files hash/mode 及 preserve-permissions 解包一致，无DB/token authority/capability文件。两套 normal/space 安装各24/24文件、device/inode/size/mtime、schema2 canonical installed_root、两字段 receipt、11/11 runtime files/version1和固定解释器全部匹配；fresh doctor healthy，各生成4个native entries。

两套 wrapper MCP providers={}，initialize/tools/list 恰好8工具，无Tasks。实际Go JSON：installed-wrapper7pass、interpreter/install guard8pass，均0fail/skip。五负例doctor unhealthy、entries exit1；whole-destination rename前后三对象device/inode全部一致，仍拒绝位置变化。两套explicit owned runtime均observation_only、active/unknown/queued全0、SQLite11；0700 state/0600 DB，已记录两个PID均不存在。

构建记录61命令、118个raw stdout/stderr引用全部hash匹配。首次build因private HOME下module cache缺失exit1；同源受限恢复build-attempt2 exit0，package恰好1次。原失败marker/logs/identity/materials和root恢复授权全部protected SHA保留且匹配，不隐去失败、不重置模型预算。脚本准备中的3项有效问题修复并复审通过；普通raise AssertionError被误判为-O绕过的一项已明确收回。

独立核读外部root REG02：同installedbinary Python301pass/7skip/0fail/0error；Codex/Claude metadata各8工具、source projection一致、OS拒网络、无model turn、剩余owned进程组为空。上述不是package runner执行结果，不冒充live。

限制：未做真实provider/model/live/认证/付费试验；7个native opt-in跳过不算PASS；未重跑Go fixtures或重建/重装。uninstall-intent schema1仅静态确认，未实际卸载；公开发布、签名/公证、非macOS arm64不在本轮范围。历史.3真实失败及新.4真实未运行保持分开。完整root证据见[T11-package-r4-root.json](../data/public-evidence/T11-package-r4-root.json)。
