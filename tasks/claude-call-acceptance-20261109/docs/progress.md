# 私有 .5 Claude 调用验收

**最新：已修复至私有.8，双向真实CLI、父accept/ACK、私有FF与清理PASS，详见[最终验收](final-acceptance.md)。以下按时间保留失败和修复过程。**

后续用户已明确要求“推送远仓”，本提交收录对应修复、测试与验收资料，见[交付说明](publication.md)。下文的“未提交/未授权推送”等表述是各阶段当时状态。

## 目标与授权

用户于 2026-11-09 明确选择“继续私有 .5 包的 Claude 调用验收”。沿用已提出的冻结合同：codex-to-claude 在先，两方向严格串行，合计最多 6 分钟 scenario wall；每方向 180 秒含清理；worker 每段 60 秒、总活跃 90 秒、最多 2 attempts（仅自然必要返工）。Codex gpt-6-luna/low，Claude 当前默认 DeepSeek。第一方向失败即停止；不追加真实探针、不升级模型。

## 身份与归属

- 主会话唯一写入负责人；独立 Agent 只读审查，不派生。
- 当前源码 worktree：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/integration`，HEAD `ed3c8060e0276d69a4e88e54694a6b735b056d2d`。
- 包版本 `0.1.24-hardening.20261105.5`，source `7eb8c8d434a584fd109d9bed96d8c3a82f3fea98`，不重建或改动安装。
- 既有已审 launcher：外部 `t12-r5-live-prep/scripts/final_trial.py`；既有 trial：`t12-live-r5-20261109`。不删除消费标记、不创建替代试验身份。
- 原 cwd 旧分支与已安装 `.bird1/1.0.0-rc.4` 不在本次修改范围。

## 里程碑

1. 已核对 51 个 setup pins 全部匹配；两方向未消费；原合同预算/模型一致。
2. 第一方向已执行一次：scenario wall 5.155622583 秒，controller exit 2，`coordinator_start_timeout`。launcher exit 1；scenario 包装进程 exit 0 不代表验收通过。第二方向保持未启动，原始永久消费标记完整保留。
3. 已完成只读归因：`coordinator.log` 明确报告 `listen unix .../coordinator.sock: bind: invalid argument`。实际 socket 路径 119 字节；本机 Go1.26.10 的 Darwin `RawSockaddrUnix.Path` 容量 104，名字长度 >=104 直接 EINVAL。因此是验收目录过长导致协调器启动失败，尚未运行父 CLI 或 Claude worker。
4. 只读 SQLite 核验 runs/tasks/attempts/segments/budget_reservations/host_instances/host_launches/runtime_hosts/owner_grants 均 0；scenario 记录一个已退出的 controller，remaining-owned/unverified/discovery 均空、输出关闭；OS 精确 state-dir 进程检查未发现残留。worker 活跃 0，未观察到模型 turn，账单未核验。
5. 原始结果和日志哈希、清理/预算状态见 [result.json](../data/result.json)。独立只读证据审查 PASS，见 [审查记录](independent-review.md)；root 已核验六个原始证据哈希及新增链接，`git diff --check` 通过。

## 验证与剩余事项

既有默认模型六字段修复已存在；本轮未修改源码、包、全局安装或当前账户/模型。`.5` 真实验收状态为 BLOCKED_BEFORE_MODEL_CALL，不能据此宣称 Claude 成功或模型修复失败。实际验收测试未运行；首方向失败后按用户合同停止，没有重试、额外探针或升级模型。

后续必须先使用足够短的 canonical、owner-only runtime 路径，并在启动前检查 socket 长度。不能修改/重置本轮 frozen pins、消费标记或失败记录；后续真实重试需另获具体范围与额度。当前不创建新试验身份或借未用时间重跑。

## 路径修复（用户后续指令“处理”）

已在本任务 [scripts/final_trial.py](../scripts/final_trial.py) 提供修订入口；7个原准备脚本的来源与SHA见 [preparation-source.json](../data/preparation-source.json)。6个依赖保持逐字一致，仅final_trial修改。旧外部脚本、.5包、已消费trial完全保留。

- 使用显式短canonical根目录（例如 `/private/tmp/ab5-NAME`）；不自动创建替代身份或重新消费旧预算。新入口setup/launch均先按两方向完整socket路径的文件系统编码字节数校验，超过103字节立即拒绝；相对路径、符号链接路径拒绝。
- launch在消费marker之前确认绑定合同的state_dir确实为该根目录下的原方向state，防止只检查短root却实际使用其他长路径。
- 凭据、包、Provider、模型、原预算、串行锁、失败即停及原始消费标记合同均保留。修订入口显式保留原基础设施TASK路径，避免移动脚本后找错proof helper。
- 5项初始回归均RED；首次综合回归37fail/36pass，原因是历史模拟合同缺少真实合同已有的state_dir。只补齐fixture字段，未删除断言。最终74pass/0fail/0error/0skip，JUnit见 [paths-reviewed-final.xml](../data/paths-reviewed-final.xml)，汇总见 [path-fix.json](../data/path-fix.json)。包括真实本地socket双方向绑定、103/104字节边界、多字节路径、无凭据访问/无提前消费，以及原串行/预算/closure门禁。
- 默认Python无pytest；实际使用既有 `tmp/test-runtime-restore/venv/bin/python -B -m pytest -p no:cacheprovider tasks/claude-call-acceptance-20261109/tests -q`。未安装依赖。测试临时目录由fixture自动清理，保留RED/中间失败/最终报告。

本轮修正准备入口并完成离线验证，没有执行新setup或任何真实模型调用；生产源码与私有包不需要重建。真实验收仍受上一轮失败即停约束，必须在新的明确范围与预算下重新准备，不能重置旧trial。当前修订尚未提交或推送。

路径修复独立审查PASS，LOW测试布局建议已修正并fresh复验74项全部通过，见[修复审查](path-fix-review.md)。

## 持续修复与真实复验授权

用户后续明确要求“继续处理，如果还是失败，修复后确定没有问题才给我回复”，授权失败后定位、修复并复测。此授权替代上一轮失败即结束整个工作的限制；每个试验内部仍先第一方向、通过后第二方向。沿用每方向180秒含清理、worker60秒/总90秒/最多2attempt及原模型；每轮失败保持不可重置，修复后建立单独有记录的新轮次，不隐匿旧预算。禁止未知进程残留时重开。

下一轮使用短根 `/private/tmp/ab5-1109-r2`，由主会话负责；运行前冻结修订入口与.5包，先无模型setup，再有界真实launch。新真实成功需同时具备父决策、候选验证、accept/ACK、预算台账与owned cleanup；最终另做私有fixture整合核验。暂不授权推送或替换全局安装。

### r2 真实复验与 ACK 修复

短根 `/private/tmp/ab5-1109-r2` setup成功（51 pins），两方向socket均63字节。第1方向真实Claude完成候选，worker 29,753ms，父fixture验证通过并accept；parent共111.693秒，无timeout。父ACK错误包含无action_slot的progress，receipt只绑定result，两次返回conflict，第二方向未执行。任务completed、segment exited、预算settled、host offline，记录内remaining/unknown为空，原host/worker PID60552/60726均已退出。见 `data/r2-result.json`；不更改已失败状态或手工代替父ACK。

源码只读调查确认store/collection.go只绑定非progress且非空action_slot事件，先accept后ACK本身合法。独立ACK command_id需每事件唯一，重放数组须不变。下一修订在 `revisions/r3`，仅改父验收指令明确顺序/事件筛选/字段映射/命令ID与原样重放；final_trial更新scenario SHA，其余依赖与产品包不变。不削弱验收，不自动生成父决定或ACK。r2原pins、markers及失败原始证据不变。

r3修订276项Python回归全部通过，Go两项ACK/accept合同测试通过，独立源码入口审查PASS（`docs/r3-entry-review.md`）。新短根 `/private/tmp/ab5-1109-r3` 无模型setup成功，51pins重核匹配；已启动第1方向真实复验。此处仅记录开始，不能当作行为通过。

### r3 真实结果与产品根因

r3 Codex→Claude SCENARIO_PASS，wall112.020秒，worker17,243ms，父accept与ACK、候选验证、预算归零及清理均通过。Claude→Codex在派发前两次mcp_source_environment_changed，wall20.644秒，未创建worker。原记录/消费标记完整。

公开假凭据、固定Claude2.1.316、SDK metadata 0 model turns复现同一错误：只新增CLAUDE_CODE_MESSAGING_SOCKET/TOKEN。此前禁止网络的sandbox连本机UDS也禁止，所以隐藏了该行为；最终仅metadata且无用户turn的公开凭据诊断确认。静态二进制复核这两项是本机消息UDS及childToken，不是provider认证或路由；token仍为敏感本地capability。

产品仅精确排除两名；未知CLAUDE字段、provider认证/路由仍严格校验，两worker ExactEnv均不传播IPC能力。TDD预期RED后GREEN144/0/0，独审PASS；需新私有.6（不覆盖.5），按base commit+源码材料hash+patchhash明确绑定未提交变更。全量测试初轮使用宿主非canonical TMPDIR导致store候选路径负例，正按原回归环境/private/tmp复核，失败日志保留。

### r4 (.6) 进入有界真实验收

源码与私有包/入口两次独立审查PASS。Go初轮环境失败已通过canonical TMPDIR复验，最新去重1667 pass/0 fail/8既有skip；相关race146/0/0，Python276/0/0，vet通过。原生Claude无模型派发校验从mcp_source_environment_changed变为正常参数校验mcp_model_required，owner proof相等。

私有版本0.1.24-hardening.20261105.6，binary cb5a1a05fd53e6673d345f0884e6ee4f022156a7328463034c8c240906cfded8。首次包装记录文件名碰撞发生在build/package/首安装均成功之后；只补产物检查及第二安装，没有重建或覆盖旧产物。

新的显式trial `/private/tmp/ab6-1109-r4`，53pins核对、双socket63字节、原预算/模型/顺序不变；r2/r3各51旧pins仍完整。用户持续修复复验授权有效。主会话开始正向，通过后再反向，失败保留证据不手工补ACK。

### r4首次真实与r5输入修订

r4首方向wall66.005秒失败：父模型wait的可选task_id抄错一个hex字符，handle正确，产品正确拒绝scope mismatch；父提前结束，worker活跃34,225ms后interrupted/exited，budget_runtime settled，hostoffline，无remaining/unverified/discovery；第二方向未启动。原53pins完整。

r5仅父指令明确单任务省略可选task_id，由产品mcpTask基于同handle唯一解析；错误读调用可省略错误参数在同预算内纠正，不允许新dispatch。产品/.6包/权限/预算/验收均不变，原验收脚本逐字保留除scenario prompt及其SHA/共用proof helper路径。独立窄审PASS，相关回归运行中，新root `/private/tmp/ab6-1109-r5`。

### r5反向新阻塞与Codex标准bundle修复

r5正向SCENARIO_PASS：wall104.684秒，worker23,019ms，1attempt，真实validation/accept/ACK及清理全通过。反向source环境准入已通过，Codex worker169ms以native_bundle_untrusted失败，wall26.023秒，父如实ACK失败事件，无候选/accept，cleanup全闭合。

根因：旧Pythonguard只识别人工私有bundle（0700目录、0600且仅version的manifest、resources下companion），而固定官方0.166.0为0755目录、0644完整manifest及bin下companion。只增加已固定0.166标准分支：完整八字段/类型/固定路径、owner/canonical/不可组他写、主binary与code-mode-host固定SHA，auth读取默认0600不放宽，旧private合同不放宽。

第一修订.7只有离线准备、从未LIVE；独立审查发现exec-server/rg也必须固定SHA，HIGH阻断已采纳并新增8负例，先RED后GREEN。新.8加入两固定helper SHA与同等verify_executable校验，独立复审PASS；test_codex_exec66pass，真实本机文件hash核验通过。原.7与失败证据保留，新包仍相同Go binary cb5a1a05，Go/race已通过证据可复用。

.8位于revisions/r7/tmp/package-r8；两个私有安装及archive/manifest验证通过。完整production prepare_launch（包含禁止网络的本地login status，停在模型exec前）执行中，完整Python及入口回归进行中；未启动新的真实trial。

.8完整production prepare_launch PASS、auth副本已删除；完整Python及入口回归377pass/0fail/error/8原opt-in skip。最终产物/入口独审PASS。新root `/private/tmp/ab8-1109-r7` setup53pins匹配，原模型/预算/串行门禁不变，开始正向真实验收；通过后方可反向。

### r7真实结果与90秒总预算分配

最终.8正向SCENARIO_PASS，Claude worker17,650ms，真实accept/ACK/清理全闭合。反向Codex实际运行模型，目标三文件已写入独立worktree；单段60秒到期触发task_deadline_exceeded，used59,304ms，1attempt，stop/exit闭合、父ACK停止事件但无candidate/accept。wall116.570秒，无剩余/未知进程；原r7原始结果保留于data/r7-result.json。

用户原始上限为“worker总活跃各≤90秒”，每方向3分钟、合计6分钟；60秒单段是历史验收脚本内部划分，任务在无candidate时不能resume，剩余额度无法用于收尾。r8只将per_segment_ms分配为90,000，total_ms仍90,000、max_attempts仍2（只自然必要返工且共享总额度），parent180秒与串行360秒不变，用户已获同步说明。不升级模型，不改变accept/ACK/候选验证/清理门禁，不覆盖任何旧trial。

r8纯fixture新增70,000ms单段能够在90,000ms总额内闭合的正例，先RED命中旧policy_guard；回归与独立上限语义审查进行中。产品源码/.8包不变，可复用源码、包、Go与Python安全边界证据。下一显式新root `/private/tmp/ab8-1109-r8`。

r8上限语义独审PASS，277回归全部通过，setup51pins匹配；开始codex-to-claude，通过才claude-to-codex。

### 最终r8完成

两方向SCENARIO_PASS，wall87.395/109.344秒、worker19,375/81,961ms，均1attempt；真实accept/ACK完成。root收尾脚本独审指出assert优化及空host证明问题，已改显式require、精确host数量和非空预算；-O三负例通过、复审PASS后实际双private FF通过。最终独立证据签收PASS，见final-review.md；本任务范围完成。临时auth副本均删除，321文件凭据扫描0命中；无本轮runtime进程/pidfile。
