# Agent-Bird N 系列本地批次交付(remaining-work-handoff-20260930)

最高自述状态:**READY_FOR_ROOT_REVIEW**。
本文件是实施者交付,不是集成完成声明;原仓集成、独立复核与最终验收归主会话。

- 实施者:外部会话(QoderCN 实施代理),仅写隔离工作区 `/private/tmp/bird-nseries-20260930-impl/`
  与原仓 `tasks/remaining-work-handoff-20260930/`(交付目录);原仓其余路径与历史交付只读,零写入。
- 冻结基线:`source archive e7bc444a… / manifest 2def1de4… / receipt ebdf61f5… / index 25a67ca2… / HEAD 9eb5561b`(N00 全部在盘复核一致,N00 快照 `003dc0e2…` receipt 内绑定)。
- 交付摘要链:`data/n06/nseries-delivery-receipt.json`(sha `003dc0e25b4fd0bf…`)与
  `data/n06/nseries-final-index.json`(index 只绑 receipt,无自引用)。
- 真实 Provider/native/发布/断电:**零执行**。产品 guard:零删除零放宽。预算常量(2s/5s/10s):零改动。

## 一、已实现

1. **N00**:隔离工作区解包冻结源码(1829/1829 逐字节一致,tar 原始成员精确);
   三方比对(归档×原仓×清单)登记 receipt 后新漂移 4 条、已登记 2 条(其 1 再动)与冻结后新增 3 文件,全部保留/待决策处置;fresh 离线构建 PASS。
2. **N01**:S4 阶段分解(默认并发再现同签名 5.04s;插桩标记把风暴 4.4–4.55s 归到
   running→子进程 main=新文件首 exec 评估;实现/观察段<100ms;warm 负控制 0.09s、
   其冷首迭代自然命中 interrupted 单一终态);确定性终态族测试(pre/-15、trap/exit0);
   EventCount=4 语义源码级更正(stopped 直挂不计数)。产品码零改动;S4 维持 OPEN。
3. **N02**:三窗互不覆盖为源码事实;`conn-delay-5.2`/`delay-5.5` 定量证明
   合同内成功被 5s 观察误拒;`delay-12`/`never-ready` 证明合同外有界失败
   (failed/CancelledError、子进程 3–6ms 收敛、无孤儿);观察协议两候选方案仅作裁定输入,未把 5 改 10。
4. **N03**:S1/S3 两身份分开 10/10+10/10 风暴复现(跨日跨树)、安静 20/20;
   F6 cause 链机械可用实测(deadline/canceled/exit/exec 四类+三负控制+缺采样守卫);
   新观察"mid-eval 击杀后同路径重探仍冷"登记(机制推断)。
5. **N04**:确认持管道×进度组合此前零覆盖;新增 3 个真实 OS/store 测试身份
   (host 2 + store 1),普通+race 全绿;变异体 M1/M2/M3 实跑杀死并还原(sha 验证)。
   候选源码净增:仅 `progress_pipe_hold_test.go`、`progress_guards_test.go` 两文件。
6. **N05**:当前能力索引草案(五层+SHA/时点、逐 guard 现行行锚、五类历史口径校正);
   未改任何公共历史文档。
7. **N06**:完整门禁电池+新候选/源码包/便携候选/隔离安装(全部数字见"已验证")。
8. **N07–N15**:方案、授权单模板、夹具与审核表就位(含 N11 判据脚本自检已实跑 4/4);
   零真实执行。X01–X06:全部拒绝保持,零扩能。

## 二、已验证(命令、身份、计数、摘要)

工作区(绑定 Go `go1.26.8`,GOPROXY=off/GOTOOLCHAIN=local,任务私有 GOCACHE):

- `validate_batch.sh` 电池(证据 `data/n06/gate-20260930T023614Z/`):
  - `go test -json -count=1 ./...` → **730 pass / 0 fail / 2 skip**,exit 0
    (基线 727+本批 3 新身份;S4 身份本轮通过——间歇性问题不因单轮绿关闭,OPEN 维持)
  - `go test -race -json -count=1 ./...` → **730 / 0 / 2**,exit 0
  - `go build ./...` exit 0;`go vet ./...` exit 0
  - Python 电池:**309 tests = 308 pass + 0 fail + 0 error + 1 skip**
    (唯一 skip 为既定 native-auth opt-in 未授权项)
  - `diff-check` **原始 exit 1、summary `passed=false` 一律保留不删**(`gate-20260930T023614Z/`)。
  - 【R4 纠正】原补充记录的两处表述与原始日志矛盾,现更正:
    (1) "git add 后 `git diff --check` rc=0" **不能证明 staged 通过**: `--check`(不带 `--cached`)
        只看未暂存差异,内容已全部 staged 时它恒为空/0, 是空证据;
    (2) 原逐文件检查把根路径拼成 `tools/orchestrator/tools/orchestrator/…`,两次 `Could not access`、
        `whitespace rc=1`(见 `diff-check.log:5–:8`),而 delivery/receipt 却写"2 新文件 rc=0" —— 该说法错误。
  - 【R4 联合候选的正确判据】在最终联合候选上, 先 `git add -A` 再 `git diff --cached --check`(rc=0),
    新增文件逐路径 `git diff --no-index --check /dev/null <correct-root-path>`, 以"是否出现 whitespace/
    blank-line-at-EOF/ trailing 行"为判定(退出码 1 仅表示与 /dev/null 有差异, 非空白错误), 并补三类负控制
    (缺文件双重前缀→Could not access、传目录→报错、植入真实尾随空格/EOF 空行→必报空白行)。证据: `evidence/R4/`。
  - culprit `grok_protocol_test.go` EOF 空行三方 sha `0224afd2…` 全同、非本批引入的事实不变。
  - 绑定披露:race 轮构建早于 `progress_guards_test.go` 的 gofmt(纯空白);
    最终字节已单独普通+race 复测 PASS(`data/n04/N04-analysis.md`)
- fresh-cache 离线重建:`build_source_archive.py` → 归档 1831 文件(净 +2/−0/改 0),
  原始成员精确、解包 0 差、空 GOCACHE 重建二进制 **42dc58ac…** == 候选;
  新归档 sha `5a8f90e1…`,新清单 sha `f2691dbc…`
- 便携候选 `0.2.0-local.nseries.20260930.agent1`:`verify_portable_candidate.py` **status=pass**
  (包内 binary 42dc58ac、普通 doctor/help 按合同 rc=2、isolated check rc=0、无本地路径/秘密泄漏断言通过)
- 隔离安装:`verify_install.py`(全新 HOME+空目录)→ installed, doctor healthy
- N01–N04 逐实验:命令与原始 JSONL/JSON 全在 `data/n01..n04/`,分析文件同目录
- **生产二进制 == 上批 42dc58ac…**:本批对运行中行为零改动(测试/文档增量),
  新包不引用旧 binary 证明"新行为",而是申报"无行为变更"+新源码绑定。

## 三、未完成/未验证/推断(高估禁止)

- S4 默认并发稳定性 **OPEN**(本批两轮一败两绿);历史实例归因未证(测量面结论仅限本轮)。
- N01 机制内部解释(评估队列键控、子阶段贡献)维持**推断**;N03"击杀-重冷化"为观察面登记,机制未证。
- N02 本轮风暴强度低于批次三(E2c 聚合敏感未再现,负载形状差异登记);S2 历史单段归因仍未证。
- N04 剩余缺口:coordinator `KindHostProgress` IPC 路由负例、`captureWriter` 1MiB 溢出、
  ticker 20 次上界长跑——未做。
- N07–N15 全部真实步骤未执行(授权缺失,按设计);N13 受控崩溃仅设计+骨架,未实现。
- N06 候选未集成:逐路径集成、原仓重跑门禁、独立复核与验收归主会话。
- 独立复核:本批各项一律停在"待独立复核";无任何自审 PASS 声明。
- 远端 CI/发布/备份:零动作;GitHub Actions 仍只有旧 run 事实。
- 真实 Provider/native/Linux/Windows/断电/远程备份:维持上批 unverified 全集。

## 四、需要主会话裁定的偏离/事项

1. **N02 观察协议**:5s 观察与 10s 合同的错位已定量;两候选方案(两段式观察 / 对齐 10s)
   均属预算类变更,请求裁定(裁定前无人可改)。
2. **门禁调度纪律**:是否将"时间敏感/probe 类测试不与全新二进制首 exec 并发"
   写入门禁规范(N01/N03 证据支持;批次三 §4c 同题延续)。
3. **N04 新文件集成**:2 个测试文件为候选源码新增(归档已含);确认集成与原仓重跑。
4. **实时原仓漂移**:N00 登记的 4 条新漂移+1 条再动(全部在历史脚本/计划面)
   需要处置裁定;本批未追改、未合并。
5. **"击杀-重冷化"观察**是否立项。
6. **N06 二进制相等性**:如主会话期望"新候选必含新行为",本批如实申报无产品行为
   变更(全部为测试/证据/文档增量),集成价值在测试覆盖与 N 系列证据链,非二进制差异。
7. 交付目录物化(本报告+数据复制进原仓任务目录)已按 dispatch §7 执行——若与
   "原仓仅 root 可写"纪律冲突,请主会话裁定该交付位置是否合规(本文按任务书指定位置放置)。
