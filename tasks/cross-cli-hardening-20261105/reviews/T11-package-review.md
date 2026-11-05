# T11 最终 `.3` 实际包独立审查

VERDICT=PASS

## 检查范围

- 冻结包目录：`/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-package-r3/{docs,data,scripts,artifacts}`；未修改其中任何文件。
- 冻结 source：clean commit `d700356c9ee497952b22ec9220d1fde111f71848`，tree `5b04a4d7fb49c47a5f16ebe26a04ddc3a1ee509a`；审查结束再次核对仍 clean、commit/tree 未变。
- 唯一 archive SHA-256：`dfbbf79b29471a9ec81b92598b74b03ba02af96ad4eac7f4b0e4ca53ee6529f8`；Go binary：`f5c54bde90a72e2e95e130989a53cfc595b33dcb0a86b607832dd3f926ba5c4c`；package manifest：`26f2a2b22eeaa74d4246b797ff251a58667b5b041ab3165a4f63f6fa10979bcc`；固定 Python：`760d13aa43656d9e51bcbc1847088691a035f9dc37030070f1d4993f55d4e689`。测试后全部重算仍一致。
- `docs/evidence-index.json` 的 158/158 path/hash/mode pins 全匹配；`source-materials.json` 绑定 2,873 个 tracked materials，另核对 post-change production binding 的 148/148 文件等于 clean package source。
- archive 共 37 members（25 files、12 directories），无 absolute/traversal/symlink/special member，无 token、capability、credential、control、SQLite/DB 纳包项。package manifest schema1 的 24/24 file hash/mode 与 preserve-permissions 解包结果一致，无未列文件。
- 核对原验收的 normal/space installs、schema2 `installed_root`、两字段 receipt、binary/runtime/wrapper hashes、四 Provider entries、实际 wrapper MCP 8 tools/no Tasks、五个负例、显式 state 的 active/unknown/queued=0、owned coordinator 退出，以及 Codex/Claude metadata-only source projection PASS。
- 独立从同一 archive 在新 review-owned 路径安装，并重演正常 doctor/entries/MCP、7-node wrapper fixture、8-node interpreter guard、whole-destination same-FS rename，以及 fresh missing-file/changed-digest 负例。
- 未读取 token-key、capability、global runtime 或凭据；MCP configure 使用 `providers={}`；未启动 provider/model，未发起 HTTP，未修改全局安装或账号。

## BLOCKERS

无。PKG-01～PKG-04 的包身份、实际安装入口、正常/空格布局证据、位置/文件/摘要/解释器拒绝和材料冻结合同成立；未发现置信度大于 80% 的行为错误、测试失真或安全回归。

## NON_BLOCKING

- LOW — `/Users/guanjunhui/code/ai/agent-bird-hardening-20261105/tmp/t11-package-r3/docs/acceptance-plan.md:1`、`:29`、`:33`：计划仍写 `candidate .2`、`.2 directory` 和 `Preparation only`，而同文件第 3 行及 implementation report 已是执行完成的 `.3`。风险是最终证据读者误判候选代次/状态。建议在最终交接前改为 `.3` 或明确标注“历史 pre-execution plan”，并重建 158-pin evidence index；不要求重建产品包。
- MEDIUM — archive metadata 与 manifest 正确，但在 `umask 077` 下用未带 preserve-permissions 的 tar 解包，会把 archive 中声明的 `0644` runtime Python 文件落为 `0600`，导致 package identity/安装拒绝；`tar -xzpf` 解包后 24/24 modes/hashes 一致且安装通过。建议最终私有包使用说明明确保留归档权限，或提供固定的安全解包入口。该问题未改变 archive 本身的正确元数据。

## 独立实际验证

### 身份与正常安装

主要命令：

```sh
shasum -a 256 artifacts/agent-bird-0.1.24-hardening.20261105.3-full-native.tar.gz artifacts/codex-orchestrator artifacts/package/package-manifest.json <pinned-python>
tar -xzpf artifacts/agent-bird-0.1.24-hardening.20261105.3-full-native.tar.gz -C <review>/tmp/preserved
<extracted>/plugin/bin/codex-orchestrator install --request <review>/data/install-request.json
<installed>/scripts/invoke.sh doctor --request <review>/data/doctor-normal-request.json
<pinned-python> -B <clean-source>/tasks/cross-cli-subagent/scripts/build-native-entries.py --installed-root <installed> --output <review>/tmp/entries
```

结果：1 个 fresh install PASS；doctor healthy；installed manifest schema2，`installed_root` 等于 canonical version root；receipt keys 精确为 `manifest_sha256,version` 且 hash 匹配；24/24 installed file hash/mode 匹配；binary/Python SHA 匹配；生成 `agy/claude/codex/grok` 四个 entries。

MCP 使用实际 installed wrapper：

```sh
<installed>/scripts/invoke.sh mcp configure --request <review>/data/mcp-request.json
<pinned-python> -B <review>/scripts/check_mcp.py --wrapper <installed-wrapper> --config <private-config> --output <review>/data/mcp-protocol.json ...
```

结果：configure `providers={}`；生成 command 精确指向同一 installed binary；initialize/list exit 0；8/8 tools 为 `accept,ack,collect,dispatch,inspect,request_changes,stop,wait`；Tasks capability absent；stderr SHA 为 empty-file SHA。

### Go fixture

```sh
env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=<review-cache> BIRD_TEST_WRAPPER=<installed-wrapper> /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v -run '^(TestCLIOwnerRequestChangesSingleTaskAndReplay|TestManagedDispatchReusesRequestAndRejectsChangedContent|TestCLIInterruptionFeedbackUsesIndependentProgress)$' ./cmd/orchestrator
```

结果：7 passed nodes（3 top-level + 4 subtests），0 failed/error/skipped。

```sh
umask 022
env GOENV=off GOTOOLCHAIN=local GOPROXY=off TMPDIR=/private/tmp GOMAXPROCS=2 GOCACHE=<fresh-review-cache> BIRD_TEST_WRAPPER=<installed-wrapper> /private/tmp/go1.26.10-sdk/bin/go test -count=1 -v -run '^(TestInstallAndDoctorRequirePinnedG0Runtime|TestCodexGuardRuntimeRequiresReceiptAndUnchangedGuardAndInterpreter)$' ./internal/install
```

结果：8 passed nodes（2 top-level + 6 subtests），0 failed/error/skipped。

### 独立 PKG-03 负例

Whole-destination rename 前后：

| 对象 | before dev/inode | after dev/inode |
| --- | --- | --- |
| destination | `16777232/244062500` | `16777232/244062500` |
| version root | `16777232/244062506` | `16777232/244062506` |
| executable | `16777232/244062510` | `16777232/244062510` |

执行同父目录 `mv <review>/tmp/i <review>/tmp/j` 后，moved installed wrapper doctor exit 0 但 `healthy=false`，唯一位置问题为 `install_location_changed_reinstall_required`；entry generation exit 1，`installed_doctor_unhealthy`。未从源码/latest fallback。

另从同一 archive 分别 fresh install：

- missing-file：基线 doctor healthy；将 `skills/orchestrate/SKILL.md` 移到 review-owned quarantine 后 doctor `healthy=false`/`:files`，entry exit 1 `FileNotFoundError`。
- changed-digest：基线 doctor healthy；只改 review negative copy 后 doctor `healthy=false`/`:files`，entry exit 1 `installed_file_changed`。

原 evidence 中其余 wrong-interpreter 与 changed-runtime-manifest 两个独立负例亦为 unhealthy + entry exit 1；五负例总表 pin 已核验。

## 复用但未冒充独立执行的证据

- post-change production Go normal/race：各 1,628 passed（649 top-level + 979 subtests），各 8 opt-in skipped，0 failed/error；vet/build PASS。production source binding 148/148 等于 clean `d700356c`。
- Python actual-installed-Go：301 passed、7 native opt-in skipped。
- Root actual `.3` Codex/Claude metadata-only 报告：两者 status PASS、source projection equal、8 tools、exit 0、remaining PGID members 0；Claude control report 18/18 public sentinels inherited且无 model turn。它们不是 live-provider 证据。

## 剩余风险

- 未运行真实 provider/model/live 双向试验；本结论只覆盖最终 `.3` package/installation/metadata 边界，不把 MCP tools metadata 冒充 live。
- 独立审查未重跑 space-path、wrong-interpreter、runtime-manifest-version 三个场景，但逐项核验了其冻结 raw/indexed evidence；normal path、whole rename、missing 和 digest 已独立重演。
- 公共发布、签名/公证、非 macOS/arm64、任意第三方解包器不在本次范围。
- review 自有 `/private/tmp/abr3rv.b3EA3V` MCP 配置/state/token 临时目录已在未读取 token authority 的前提下删除，不可恢复；missing-file 负例采用 review-owned quarantine 移动，原冻结包未受影响。
