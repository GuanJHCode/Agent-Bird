# 当前交接

交接状态：PREPARED（最终独立文档/集成审查与root资料签收通过）；离线实现、包与回归均已完成相应独审/root验收。**产品 core=BLOCKED，native push=BLOCKED；新 `.4` 真实模型试验=NOT_RUN。**

## 冻结源码和私有包

分支 `feat/cross-cli-hardening-20261105`；生产源码 commit `7b7f0421fc9dc553be29d871b16d15f779293b76`，tree `51b0bd73b3efcb607a2ff47476eced440f2b3731`，已推送。后续交接文档提交不改变包源；当前 checkout 的文档 HEAD 用 `git rev-parse HEAD` 核对，最终独审以冻结文件 hash 清单为准。原工作区及原未跟踪文件保留。

私有版本 `0.1.24-hardening.20261105.4`，archive `agent-bird-0.1.24-hardening.20261105.4-full-native.tar.gz`；本机目录 `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-package-r4/artifacts/`。

| 材料 | SHA256 |
|---|---|
| archive | `1817a713d710ac0d6c75539b1c765b5826f991d2ab9b81befd78b47b42d33597` |
| Go binary | `11e573d8d240cafb25d580d0a62862f2f617cf5b6ca38340c16df42222ea9a89` |
| package manifest | `665429b8ea50adfe5a1036665a04121dbaeb3c86a4dcbfa5f0a8334314aebb99` |
| tracked source materials | `95e2585b5d8022d49665c5c74aedc2ff3b3e2584a4833212a9769138987f73ae` |

本包仅为本机 macOS arm64 私有验收材料，绑定既有固定 Python 的绝对路径/hash；公开下载仍是 `v0.1.0-preview.1`。没有 release、签名/公证、正式分支合并或全局安装替换。

## 已完成及证据

- T00/T01/T02/T03/T04/T05/T06A/T06B/T07/T08/T10 的实现与单项独审/root证据见[TASKS](../TASKS.json)。最新短引用 route79/review47字符，完整HMAC、旧sealed解析与原owner/完整tuple授权保留；仍8个普通MCP工具，无Tasks。见[合同](../plans/T08-short-references.md)、[源码独审](../reviews/T08-short-references-review.md)。
- T11 私有 `.4` PKG-01～04：普通/空格安装各24文件、4 entries；archive37 members/25 files、无越界或链接；receipt两字段、installed manifest2 canonical root、runtime1。五负例均拒绝，同文件系统whole-destination移动保留inode仍拒绝。两owned runtime idle、SQLite11、已退出。实际installed-wrapper7pass和guard8pass，0fail/skip。见[root](../data/public-evidence/T11-package-r4-root.json)和[独审](../reviews/T11-package-r4-review.md)。DOC-01当前文档已由[最终独审](../reviews/T13-final-review.md)通过，root资料签收完成。
- REG-01：Go normal/race各1654pass（656顶层/998子测试）、0fail、8既有opt-in skip；vet/全模块build通过。149生产材料逐文件绑定到clean包源，aggregate `54e6b86fcfbc6fd0b50cc808778e6b70a4e249a580bb0822de575981c1475c0f`。见[Go证据](../data/public-evidence/T08-short-references-root.json)、[生产绑定](../data/public-evidence/T12-source-binding-r4.json)。
- REG-02：同 `.4` 已安装Go的Python301pass/0fail/0error/7native opt-in skip；保留JUnit、精确跳过项及1个原有SyntaxWarning。实际Codex0.166.0/Claude2.1.316仅metadata，各8tools/source projection一致；OS拒网络、无模型turn、owned进程组无残留。见[当前回归](../data/public-evidence/T12-regression-r4-root.json)。metadata不是live/原生聊天续审证明。
- REG-03：原失败、预算、日志和恢复身份保留；凭据只进入原授权精确子进程，不写源码/包/argv/报告，本次离线恢复不读取用户密钥。首build因private HOME的modulecache缺失exit1；经独审和root一次受限恢复，build-attempt2 exit0，package仅1次。失败1/成功1/尝试2明确记录，未删除marker或伪称一次尝试。Go/pytest恢复到任务专用目录，旧SDK/旧venv和全局配置未改。

## 未完成与未验证

历史 `.3` 的两次有界真实试验均未完成accept/ACK/FF，详见[原live证据](../data/public-evidence/T12-live-root.json)与[原独审](../reviews/T12-live-review.md)。Codex→Claude成功dispatch和首inspect后，父模型改写847字符引用，被产品正确拒绝；worker已interrupted/exited，active/unknown/reserved=0，已用1attempt/53160ms。Claude→Codex在dispatch前10次HTTP401、watchdog收尾，无task/worker。两个原预算/身份没有重置，上述离线收尾阶段（截至402af7cc）没有追加付费CLI验收调用。

新 `.4` 的短引用模型传递、真实父审查、accept/ACK/整合均NOT_RUN。11月5日用户曾确认没有可用本机Claude配置；最新Key/API复测见下方后续记录。401不证明key失效或模型不存在，不猜认证模式、不切默认模型/账户。后续真实验收需要可用的既有认证路由，以及明确的受控新验收授权；不得借换ID重置旧失败预算。请勿发送密钥。

T09 native原线程idle/busy续审仍BLOCKED，typed resume、AGY/Grok coding不开放。LIVE-03原始条件缺陷返工未自然触发，保持NOT_EXERCISED，不用fixture伪装。T12与T13产品签收保持BLOCKED；当前资料已独审/root签收为PREPARED，产品仍不能标PASS。未实际执行`.4`卸载、非macOS arm64、公开发行/签名/公证。

## 继续工作与保留现场

先读[当前resume](resume.md)、[TASKS](../TASKS.json)及本报告；历史过程移到[resume历史](resume-history-20261105-20261109.md)。核对本机私有包hash和当前分支，再处理可用认证/新的有界验收合同；不要直接重跑原`final_trial.py`或尝试第三次build。原`.1/.2/.3`包及失败记录全部保留，`.4`不重标为旧版本。

所有观察到的本轮owned metadata/PKG coordinator已退出；原有两模型试验退出证明亦保留。私有runtime、authority/request文件、包、原始日志、SDK/venv和审查副本用于恢复，保持原0700/0600约束，不纳入Git或全局清理。没有删除归属不明文件；可确认无用的测试临时对象由各fixture按自身合同清理。

历史可追溯性限制：provider-runtime/support-matrix继承的39处历史链接（36个不同target）在当前checkout缺目标，全部为基线已有；本轮未扩张修复。当前`.4`的新增证据/入口链接已核对，详见最终独审。

## 交接后的用户授权连通性复测

用户随后明确允许使用Claude现有Key/API尝试。使用此前会话提供的同一配置，未换账号或默认模型：首次结果采集异常/API结果未知，补测返回HTTP401、CLI token统计0，已停止重试。详情见[复测记录](claude-connectivity-20261109.md)。这不是同包`.4`双向模型验收；core/T12/T13产品仍BLOCKED。若用户另有已配置路由，待其提供路径或管理工具信息，不要求密钥。旧交接root报告的no_new_paid_cli_trials仅对应402af7cc当时的离线阶段，不扩展到这次新授权的复测。

## 后续CC Switch新配置验证

用户之后配置好CC中的Claude，root确认`~/.claude/settings.json`与当前provider一致。按新AUTH_TOKEN/Bearer及原默认模型进行一次最小CLI调用，报告HTTP403、usage0、无模型回答；详见[当前CC验证](cc-claude-connectivity-20261109.md)。配置确已读取，具体403原因仍不确定；旧聊天Key/API的401是前一次历史，不代表这次配置。没有再次自动试错或切换Key/账户/模型/地址，core及新`.4`真实双向门禁保持不变。

最新网关对齐验证：用户替换CC地址为ZCode实际第三方网关后，Key/实际模型保持一致，单次真实Claude调用成功（2.332s，input69/output24，nonce完全匹配）。见[成功记录](cc-gateway-connectivity-20261109.md)。这解除独立连通性阻塞，旧401/403仍为历史；产品worker另发现cli-default丢来源model env缺口，需先修复并生成新包，当前产品双向LIVE仍NOT_RUN/coreBLOCKED，不用显式换模型绕过。
